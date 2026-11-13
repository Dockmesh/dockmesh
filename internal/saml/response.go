package saml

import (
	"bytes"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"

	"github.com/beevik/etree"
	crewjam "github.com/crewjam/saml"
	"github.com/crewjam/saml/xmlenc"
)

const assertionNS = "urn:oasis:names:tc:SAML:2.0:assertion"

// maxArtifactResponse bounds what is kept of an artifact resolution answer
// for the NameID check; a larger one is refused.
const maxArtifactResponse = 1 << 20

var errForeignNameID = errors.New("assertion subject has a NameID outside the SAML assertion namespace")

// parseResponse validates the IdP's answer with crewjam and adds two checks
// it lacks: a signed assertion missing an element crewjam dereferences makes
// it panic, and it fills Subject.NameID from a NameID element of any XML
// namespace, so a foreign one could name the account.
func parseResponse(sp *crewjam.ServiceProvider, r *http.Request, requestIDs []string) (*crewjam.Assertion, error) {
	var raw []byte
	var artifact *capturedBody
	if r.Form.Get("SAMLart") != "" {
		artifact = &capturedBody{}
		sp.HTTPClient = capturingClient(sp.HTTPClient, artifact)
	} else {
		var err error
		if raw, err = base64.StdEncoding.DecodeString(r.PostForm.Get("SAMLResponse")); err != nil {
			return nil, fmt.Errorf("%w: response is not base64", ErrInvalidAssertion)
		}
	}
	a, err := parseRecovering(sp, r, requestIDs)
	if errors.Is(err, ErrLoginFailed) {
		return nil, err
	}
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrInvalidAssertion, err)
	}
	if artifact != nil {
		if artifact.tooLarge {
			return nil, fmt.Errorf("%w: artifact response too large", ErrInvalidAssertion)
		}
		raw = artifact.buf.Bytes()
	}
	if err := checkSubjectNameIDs(raw, sp); err != nil {
		return nil, fmt.Errorf("%w: %w", ErrInvalidAssertion, err)
	}
	return a, nil
}

func parseRecovering(sp *crewjam.ServiceProvider, r *http.Request, requestIDs []string) (a *crewjam.Assertion, err error) {
	defer func() {
		if v := recover(); v != nil {
			slog.Error("saml: response parser panicked", "panic", fmt.Sprint(v))
			a, err = nil, ErrLoginFailed
		}
	}()
	return sp.ParseResponse(r, requestIDs)
}

// checkSubjectNameIDs refuses any SAML Subject in the response, including
// those in assertions encrypted to sp, with a NameID child from another
// namespace. Assertions that don't decrypt are skipped: crewjam can't
// accept them either.
func checkSubjectNameIDs(raw []byte, sp *crewjam.ServiceProvider) error {
	doc := etree.NewDocument()
	if err := doc.ReadFromBytes(raw); err != nil {
		return err
	}
	if doc.Root() == nil {
		return errors.New("empty response")
	}
	return walkSubjects(doc.Root(), sp)
}

func walkSubjects(el *etree.Element, sp *crewjam.ServiceProvider) error {
	if el.NamespaceURI() == assertionNS {
		switch el.Tag {
		case "Subject":
			for _, c := range el.ChildElements() {
				if c.Tag == "NameID" && c.NamespaceURI() != assertionNS {
					return errForeignNameID
				}
			}
		case "EncryptedAssertion":
			if plain, err := decryptAssertion(el, sp); err == nil {
				if err := walkSubjects(plain, sp); err != nil {
					return err
				}
			}
		}
	}
	for _, c := range el.ChildElements() {
		if err := walkSubjects(c, sp); err != nil {
			return err
		}
	}
	return nil
}

// decryptAssertion mirrors crewjam's decryption of an EncryptedAssertion.
func decryptAssertion(el *etree.Element, sp *crewjam.ServiceProvider) (*etree.Element, error) {
	var data *etree.Element
	for _, c := range el.ChildElements() {
		if c.Tag == "EncryptedData" && c.NamespaceURI() == "http://www.w3.org/2001/04/xmlenc#" {
			data = c
			break
		}
	}
	if data == nil {
		return nil, errors.New("no EncryptedData")
	}
	var key interface{} = sp.Key
	if keyEl := el.FindElement("./EncryptedKey"); keyEl != nil {
		k, err := xmlenc.Decrypt(sp.Key, keyEl)
		if err != nil {
			return nil, err
		}
		key = k
	}
	plain, err := xmlenc.Decrypt(key, data)
	if err != nil {
		return nil, err
	}
	doc := etree.NewDocument()
	if err := doc.ReadFromBytes(plain); err != nil {
		return nil, err
	}
	if doc.Root() == nil {
		return nil, errors.New("empty assertion")
	}
	return doc.Root(), nil
}

// capturedBody keeps the artifact resolution answer crewjam reads, so the
// NameID check sees the same XML.
type capturedBody struct {
	buf      bytes.Buffer
	tooLarge bool
}

func (c *capturedBody) Write(p []byte) (int, error) {
	if c.buf.Len()+len(p) > maxArtifactResponse {
		c.tooLarge = true
		return len(p), nil
	}
	return c.buf.Write(p)
}

func capturingClient(base *http.Client, into *capturedBody) *http.Client {
	if base == nil {
		base = http.DefaultClient
	}
	c := *base
	next := c.Transport
	if next == nil {
		next = http.DefaultTransport
	}
	c.Transport = roundTripFunc(func(req *http.Request) (*http.Response, error) {
		resp, err := next.RoundTrip(req)
		if err != nil {
			return nil, err
		}
		resp.Body = struct {
			io.Reader
			io.Closer
		}{io.TeeReader(resp.Body, into), resp.Body}
		return resp, nil
	})
	return &c
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
