package saml

import (
	"context"
	"crypto/rsa"
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strings"
	"syscall"
	"time"
	"unicode"
)

const (
	metadataRefreshInterval = 24 * time.Hour
	metadataFetchTimeout    = 20 * time.Second
	maxMetadataBytes        = 5 << 20
	maxSPEntityIDLen        = 1024 // SAML core limits entityID to 1024 characters
)

// providerOptions is what a save stores besides the metadata itself, after
// merging the request with the row.
type providerOptions struct {
	metadataURL string
	fetchedAt   any
	fetchError  string
	spEntityID  string
	signingCert string
	encCert     string
	encKey      string
}

// resolveOptions validates the optional settings and, for a metadata URL,
// fetches the metadata into in.IdPMetadataXML. cur is nil on create.
func (s *Service) resolveOptions(ctx context.Context, in *ProviderInput, cur *Provider) (*providerOptions, error) {
	o := &providerOptions{}
	if cur != nil {
		o.metadataURL, o.fetchError = cur.IdPMetadataURL, cur.MetadataError
		o.spEntityID, o.signingCert, o.encCert, o.encKey = cur.SPEntityID, cur.SigningCertPEM, cur.EncryptionCertPEM, cur.encryptionKey
		if cur.MetadataFetchedAt != nil {
			o.fetchedAt = *cur.MetadataFetchedAt
		}
	}
	var err error
	if in.SPEntityID != nil {
		if o.spEntityID, err = checkSPEntityID(*in.SPEntityID); err != nil {
			return nil, err
		}
	}
	if in.SigningCertPEM != nil {
		if o.signingCert, err = normalizeCertPEM(*in.SigningCertPEM); err != nil {
			return nil, fmt.Errorf("signing certificate: %w", err)
		}
	}
	if in.EncryptionCertPEM != nil {
		if o.encCert, o.encKey, err = s.encryptionPair(*in.EncryptionCertPEM, o.encKey); err != nil {
			return nil, fmt.Errorf("encryption certificate: %w", err)
		}
	}
	pinned := o.signingCert != ""
	switch {
	case in.IdPMetadataURL != "":
		xml, err := s.fetchMetadata(ctx, in.IdPMetadataURL, pinned)
		if err != nil {
			return nil, err
		}
		in.IdPMetadataXML = xml
		o.metadataURL, o.fetchedAt, o.fetchError = in.IdPMetadataURL, time.Now().UTC(), ""
	case in.IdPMetadataXML != "":
		o.metadataURL, o.fetchedAt, o.fetchError = "", nil, ""
	case o.metadataURL != "":
		if _, err := checkMetadataURL(o.metadataURL, pinned); err != nil {
			return nil, err
		}
	}
	if cur != nil && in.IdPMetadataXML == "" && !pinned && cur.IdPCertPEM == "" && in.SigningCertPEM != nil {
		return nil, errNoSigningCert
	}
	return o, nil
}

func checkSPEntityID(raw string) (string, error) {
	v := strings.TrimSpace(raw)
	if len(v) > maxSPEntityIDLen {
		return "", fmt.Errorf("sp_entity_id is longer than %d characters", maxSPEntityIDLen)
	}
	if strings.IndexFunc(v, func(r rune) bool { return unicode.IsSpace(r) || unicode.IsControl(r) }) >= 0 {
		return "", errors.New("sp_entity_id must not contain spaces or control characters")
	}
	return v, nil
}

// normalizeCertPEM accepts one PEM certificate, or empty to clear it. An
// expired one is refused here, since every login would fail on it.
func normalizeCertPEM(raw string) (string, error) {
	if strings.TrimSpace(raw) == "" {
		return "", nil
	}
	block, _ := pem.Decode([]byte(strings.TrimSpace(raw)))
	if block == nil || block.Type != "CERTIFICATE" {
		return "", errors.New("not a PEM certificate")
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return "", fmt.Errorf("parse: %w", err)
	}
	if time.Now().After(cert.NotAfter) {
		return "", fmt.Errorf("expired on %s", cert.NotAfter.Format(time.DateOnly))
	}
	return string(pem.EncodeToMemory(block)), nil
}

// encryptionPair takes the certificate and private key assertions are
// encrypted to, returning them as stored. A certificate alone is accepted
// when it matches the stored key, so a form that shows only the certificate
// can be saved back; an IdP encrypting to a key Dockmesh doesn't hold would
// lock every user out.
func (s *Service) encryptionPair(raw, storedKey string) (certPEM, keyEnc string, err error) {
	if strings.TrimSpace(raw) == "" {
		return "", "", nil
	}
	var cert *x509.Certificate
	var key *rsa.PrivateKey
	var certBlock, keyBlock *pem.Block
	rest := []byte(strings.TrimSpace(raw))
	for {
		var block *pem.Block
		if block, rest = pem.Decode(rest); block == nil {
			break
		}
		switch {
		case block.Type == "CERTIFICATE" && cert == nil:
			if cert, err = x509.ParseCertificate(block.Bytes); err != nil {
				return "", "", fmt.Errorf("parse: %w", err)
			}
			certBlock = block
		case strings.HasSuffix(block.Type, "PRIVATE KEY"):
			if key, err = parseRSAKey(block); err != nil {
				return "", "", err
			}
			keyBlock = block
		}
	}
	if cert == nil {
		return "", "", errors.New("no PEM certificate found")
	}
	if time.Now().After(cert.NotAfter) {
		return "", "", fmt.Errorf("expired on %s", cert.NotAfter.Format(time.DateOnly))
	}
	if key == nil {
		if storedKey == "" {
			return "", "", errors.New("include the private key: Dockmesh decrypts assertions with it")
		}
		if key, err = s.decryptKey(storedKey); err != nil {
			return "", "", err
		}
	}
	pub, ok := cert.PublicKey.(*rsa.PublicKey)
	if !ok || !key.PublicKey.Equal(pub) {
		return "", "", errors.New("the certificate does not belong to the private key")
	}
	keyEnc = storedKey
	if keyBlock != nil {
		if keyEnc, err = s.encryptKey(string(pem.EncodeToMemory(keyBlock))); err != nil {
			return "", "", err
		}
	}
	return string(pem.EncodeToMemory(certBlock)), keyEnc, nil
}

func parseRSAKey(block *pem.Block) (*rsa.PrivateKey, error) {
	if block.Type == "RSA PRIVATE KEY" {
		return x509.ParsePKCS1PrivateKey(block.Bytes)
	}
	if block.Type != "PRIVATE KEY" {
		return nil, fmt.Errorf("%s is not supported, use an unencrypted RSA key", block.Type)
	}
	k, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("parse private key: %w", err)
	}
	rk, ok := k.(*rsa.PrivateKey)
	if !ok {
		return nil, errors.New("the private key must be RSA")
	}
	return rk, nil
}

func (s *Service) encryptKey(plain string) (string, error) {
	if s.secrets == nil || !s.secrets.Enabled() {
		return plain, nil
	}
	ct, err := s.secrets.Encrypt([]byte(plain))
	if err != nil {
		return "", err
	}
	return base64.StdEncoding.EncodeToString(ct), nil
}

func (s *Service) decryptKey(stored string) (*rsa.PrivateKey, error) {
	plain := []byte(stored)
	if s.secrets != nil && s.secrets.Enabled() {
		ct, err := base64.StdEncoding.DecodeString(stored)
		if err != nil {
			return nil, fmt.Errorf("decode encryption key: %w", err)
		}
		if plain, err = s.secrets.Decrypt(ct); err != nil {
			return nil, fmt.Errorf("decrypt encryption key: %w", err)
		}
	}
	block, _ := pem.Decode(plain)
	if block == nil {
		return nil, errors.New("stored encryption key is not PEM")
	}
	return parseRSAKey(block)
}

// certBody is a PEM certificate as the bare base64 crewjam expects.
func certBody(certPEM string) (string, error) {
	block, _ := pem.Decode([]byte(certPEM))
	if block == nil {
		return "", errors.New("stored certificate is not PEM")
	}
	return base64.StdEncoding.EncodeToString(block.Bytes), nil
}

// checkMetadataURL allows plain http only next to a pinned signing
// certificate: the metadata carries the keys that verify logins, so anyone
// able to alter it unprotected could otherwise forge a sign-in as any user.
func checkMetadataURL(raw string, pinned bool) (*url.URL, error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Host == "" || u.User != nil || (u.Scheme != "https" && u.Scheme != "http") {
		return nil, errors.New("the metadata URL must be an http(s) URL without credentials")
	}
	if u.Scheme == "http" && !pinned {
		return nil, errors.New("a metadata URL needs https, or a signing certificate set on the provider")
	}
	return u, nil
}

// refuseMetadataAddress keeps the server's own and link-local addresses
// (cloud instance metadata) out of reach of a URL an admin typed. Private
// ranges stay allowed: a self-hosted IdP usually lives there.
func refuseMetadataAddress(address string) error {
	ap, err := netip.ParseAddrPort(address)
	if err != nil {
		return fmt.Errorf("bad address %q", address)
	}
	ip := ap.Addr().Unmap()
	if ip.IsLoopback() || ip.IsLinkLocalUnicast() || ip.IsMulticast() || ip.IsUnspecified() {
		return fmt.Errorf("the metadata URL resolves to %s, which is not allowed", ip)
	}
	return nil
}

func metadataHTTPClient(pinned bool) *http.Client {
	dialer := &net.Dialer{
		Timeout: metadataFetchTimeout,
		Control: func(_, address string, _ syscall.RawConn) error { return refuseMetadataAddress(address) },
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.DialContext = dialer.DialContext
	return &http.Client{
		Timeout:   metadataFetchTimeout,
		Transport: transport,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 3 {
				return errors.New("too many redirects")
			}
			_, err := checkMetadataURL(req.URL.String(), pinned)
			return err
		},
	}
}

func (s *Service) fetchMetadata(ctx context.Context, raw string, pinned bool) (string, error) {
	u, err := checkMetadataURL(raw, pinned)
	if err != nil {
		return "", err
	}
	client := s.metadataClient
	if client == nil {
		client = metadataHTTPClient(pinned)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return "", fmt.Errorf("fetch metadata: %w", err)
	}
	req.Header.Set("Accept", "application/samlmetadata+xml, application/xml, text/xml")
	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("fetch metadata: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("fetch metadata: server answered %s", resp.Status)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxMetadataBytes+1))
	if err != nil {
		return "", fmt.Errorf("fetch metadata: %w", err)
	}
	if len(body) > maxMetadataBytes {
		return "", fmt.Errorf("fetch metadata: larger than %d bytes", maxMetadataBytes)
	}
	return string(body), nil
}

// StartMetadataRefresh refetches the metadata of URL-based providers at
// start and once a day, until ctx ends.
func (s *Service) StartMetadataRefresh(ctx context.Context) {
	go func() {
		t := time.NewTicker(metadataRefreshInterval)
		defer t.Stop()
		for {
			s.refreshMetadata(ctx)
			select {
			case <-ctx.Done():
				return
			case <-t.C:
			}
		}
	}()
}

type refreshTarget struct {
	id                     int64
	slug, url, entity, pin string
}

func (s *Service) refreshMetadata(ctx context.Context) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, slug, idp_metadata_url, entity_id, signing_cert_pem FROM saml_providers WHERE idp_metadata_url <> ''`)
	if err != nil {
		slog.Warn("saml: list providers for metadata refresh", "err", err)
		return
	}
	var targets []refreshTarget
	for rows.Next() {
		var t refreshTarget
		if err := rows.Scan(&t.id, &t.slug, &t.url, &t.entity, &t.pin); err != nil {
			rows.Close()
			slog.Warn("saml: read provider for metadata refresh", "err", err)
			return
		}
		targets = append(targets, t)
	}
	rows.Close()
	for _, t := range targets {
		if err := s.refreshProvider(ctx, t); err != nil {
			slog.Warn("saml: metadata refresh failed, keeping the stored metadata", "provider", t.slug, "err", err)
			if _, derr := s.db.ExecContext(ctx, `UPDATE saml_providers SET metadata_error = ? WHERE id = ?`, err.Error(), t.id); derr != nil {
				slog.Warn("saml: record metadata refresh error", "provider", t.slug, "err", derr)
			}
		}
	}
}

// refreshProvider refuses metadata of another IdP entity: stored subjects
// would then name other users, and only an admin's save may unlink them.
func (s *Service) refreshProvider(ctx context.Context, t refreshTarget) error {
	xml, err := s.fetchMetadata(ctx, t.url, t.pin != "")
	if err != nil {
		return err
	}
	meta, err := parseIdPMetadata(xml)
	if err != nil {
		return fmt.Errorf("%w: %w", ErrInvalidMetadata, err)
	}
	if meta.CertPEM == "" && t.pin == "" {
		return errNoSigningCert
	}
	if meta.EntityID != t.entity {
		return fmt.Errorf("the URL now serves entity %q instead of %q; save the provider to accept the change", meta.EntityID, t.entity)
	}
	_, err = s.db.ExecContext(ctx, `
		UPDATE saml_providers SET sso_url = ?, slo_url = ?, idp_metadata_xml = ?, idp_cert_pem = ?,
		       metadata_fetched_at = ?, metadata_error = '', updated_at = CURRENT_TIMESTAMP
		 WHERE id = ?`, meta.SSOURL, nullable(meta.SLOURL), xml, meta.CertPEM, time.Now().UTC(), t.id)
	return err
}
