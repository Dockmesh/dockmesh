package proxy

import (
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
	"net"
	"strings"
)

const maxCAPEMBytes = 64 << 10

var (
	errPEMPrivateKey = &OptionError{Msg: "this is a private key; paste the CA certificate instead"}
	errPEMNotCert    = &OptionError{Msg: "paste one or more PEM certificates (-----BEGIN CERTIFICATE-----)"}
)

// isHostname accepts a DNS name as an SNI value: letters, digits, hyphens and
// dots, no scheme, port or wildcard.
func isHostname(s string) bool {
	if s == "" || len(s) > 253 || net.ParseIP(s) != nil {
		return false
	}
	for _, label := range strings.Split(strings.TrimSuffix(s, "."), ".") {
		if label == "" || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return false
		}
		for _, c := range label {
			alnum := c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9'
			if !alnum && c != '-' {
				return false
			}
		}
	}
	return true
}

func (t *UpstreamTLS) validate(field string) error {
	switch t.HostHeader {
	case "", "keep", "upstream":
	default:
		return optErr(field+".host_header", "must be keep or upstream")
	}
	if !t.TLS {
		if t.ServerName != "" || t.CAPEM != "" || t.SkipVerify {
			return optErr(field+".tls", "turn on HTTPS to the backend to use a server name or certificate check")
		}
		return nil
	}
	if t.ServerName != "" && !isHostname(t.ServerName) {
		return optErr(field+".server_name", "use a host name, without scheme or port")
	}
	if t.CAPEM != "" && t.SkipVerify {
		return optErr(field+".skip_verify", "pick either a CA certificate or no certificate check")
	}
	if t.CAPEM != "" {
		if _, err := parseCAPEM(t.CAPEM); err != nil {
			return optErr(field+".ca_pem", "%s", err.Msg)
		}
	}
	return nil
}

// parseCAPEM returns the DER bytes of every certificate in s. Anything
// but CERTIFICATE blocks is refused, a private key with its own message
// because pasting the wrong file is the likely mistake.
func parseCAPEM(s string) ([][]byte, *OptionError) {
	if len(s) > maxCAPEMBytes {
		return nil, &OptionError{Msg: "the certificate is larger than 64 KB"}
	}
	rest := []byte(s)
	var out [][]byte
	for {
		var b *pem.Block
		b, rest = pem.Decode(rest)
		if b == nil {
			break
		}
		if strings.Contains(b.Type, "PRIVATE KEY") {
			return nil, errPEMPrivateKey
		}
		if b.Type != "CERTIFICATE" {
			return nil, errPEMNotCert
		}
		if _, err := x509.ParseCertificate(b.Bytes); err != nil {
			return nil, &OptionError{Msg: "a certificate in the PEM can't be read"}
		}
		out = append(out, b.Bytes)
	}
	if len(out) == 0 || strings.TrimSpace(string(rest)) != "" {
		if strings.Contains(string(rest), "PRIVATE KEY") {
			return nil, errPEMPrivateKey
		}
		return nil, errPEMNotCert
	}
	return out, nil
}

// caddyTLS is the transport TLS block for t, nil for plain HTTP.
func (t *UpstreamTLS) caddyTLS() *caddyTransportTLS {
	if !t.TLS {
		return nil
	}
	out := &caddyTransportTLS{ServerName: t.ServerName, InsecureSkipVerify: t.SkipVerify}
	if t.CAPEM != "" {
		ders, _ := parseCAPEM(t.CAPEM)
		ca := &caddyCAPool{Provider: "inline"}
		for _, d := range ders {
			ca.TrustedCACerts = append(ca.TrustedCACerts, base64.StdEncoding.EncodeToString(d))
		}
		out.CA = ca
	}
	return out
}
