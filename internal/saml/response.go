package saml

import (
	"bytes"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"slices"

	"github.com/beevik/etree"
	crewjam "github.com/crewjam/saml"
	"github.com/crewjam/saml/xmlenc"
)

const (
	assertionNS = "urn:oasis:names:tc:SAML:2.0:assertion"
	protocolNS  = "urn:oasis:names:tc:SAML:2.0:protocol"
	soapNS      = "http://schemas.xmlsoap.org/soap/envelope/"
	xmlencNS    = "http://www.w3.org/2001/04/xmlenc#"
)

// maxArtifactResponse bounds an artifact resolution answer; a larger one
// is refused.
const maxArtifactResponse = 1 << 20

// maxACSBody bounds the posted form; signed and encrypted responses stay
// far below it, and each EncryptedAssertion in it costs an RSA decryption.
const maxACSBody = 1 << 20

var errForeignElement = errors.New("assertion element outside the SAML assertion namespace")

// parseResponse validates the IdP's answer with crewjam and covers two
// gaps: crewjam panics on a signed assertion missing an element it
// dereferences, and it fills Subject.NameID and the attributes from elements of
// any XML namespace, so a foreign one could name the account.
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
	if err := checkNamespaces(raw, sp); err != nil {
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

// unqualified lists, per assertion element, the children crewjam reads by
// local name alone; the identity and the roles come from them. Subject,
// Issuer and Conditions are matched with their namespace but refused here
// as well, so a foreign one never reaches crewjam either.
var unqualified = map[string][]string{
	"Assertion":          {"Subject", "Issuer", "Conditions", "AttributeStatement", "AuthnStatement"},
	"Subject":            {"NameID", "SubjectConfirmation"},
	"AttributeStatement": {"Attribute"},
	"Attribute":          {"AttributeValue"},
}

// checkNamespaces refuses a response in which one of those children is
// from another namespace, also inside assertions encrypted to sp.
// Assertions that don't decrypt are skipped: crewjam can't accept them
// either.
func checkNamespaces(raw []byte, sp *crewjam.ServiceProvider) (err error) {
	defer func() {
		if v := recover(); v != nil {
			slog.Error("saml: namespace check panicked", "panic", fmt.Sprint(v))
			err = ErrLoginFailed
		}
	}()
	doc := etree.NewDocument()
	if err := doc.ReadFromBytes(raw); err != nil {
		return err
	}
	if len(doc.ChildElements()) != 1 {
		return errors.New("response must have exactly one root element")
	}
	return checkTree(doc.Root(), processedResponse(doc.Root()), sp)
}

// processedResponse is the Response whose direct EncryptedAssertions
// crewjam decrypts: the root, or the one inside an artifact answer.
func processedResponse(root *etree.Element) *etree.Element {
	if is(root, protocolNS, "Response") {
		return root
	}
	el := root
	for _, step := range []struct{ ns, tag string }{
		{soapNS, "Body"}, {protocolNS, "ArtifactResponse"}, {protocolNS, "Response"},
	} {
		var next *etree.Element
		for _, c := range el.ChildElements() {
			if is(c, step.ns, step.tag) {
				next = c
				break
			}
		}
		if next == nil {
			return nil
		}
		el = next
	}
	return el
}

func is(el *etree.Element, ns, tag string) bool {
	return el.Tag == tag && el.NamespaceURI() == ns
}

func checkTree(root, response *etree.Element, sp *crewjam.ServiceProvider) error {
	if err := checkPrefixes(root); err != nil {
		return err
	}
	return checkElement(root, response, sp)
}

// checkPrefixes refuses a document that binds one prefix to two
// namespaces. crewjam re-serialises an assertion with every prefix its
// elements use declared once on the root, last binding winning, so the
// XML it decodes could otherwise resolve a prefix differently from the
// signed one checked here.
func checkPrefixes(root *etree.Element) error {
	seen := map[string]string{}
	for _, el := range append([]*etree.Element{root}, root.FindElements("//*")...) {
		if el.Space == "" {
			continue
		}
		ns := el.NamespaceURI()
		if prev, ok := seen[el.Space]; ok && prev != ns {
			return fmt.Errorf("%w: prefix %q bound to two namespaces", errForeignElement, el.Space)
		}
		seen[el.Space] = ns
	}
	return nil
}

func checkElement(el, response *etree.Element, sp *crewjam.ServiceProvider) error {
	if el.NamespaceURI() == assertionNS {
		for _, c := range el.ChildElements() {
			if slices.Contains(unqualified[el.Tag], c.Tag) && c.NamespaceURI() != assertionNS {
				return fmt.Errorf("%w: %s in %s", errForeignElement, c.Tag, el.Tag)
			}
		}
	}
	for _, c := range el.ChildElements() {
		if el == response && is(c, assertionNS, "EncryptedAssertion") {
			if plain, err := decryptAssertion(c, sp); err == nil {
				if err := checkTree(plain, nil, sp); err != nil {
					return err
				}
			}
			continue
		}
		if err := checkElement(c, response, sp); err != nil {
			return err
		}
	}
	return nil
}

// decryptAssertion mirrors crewjam's decryption of an EncryptedAssertion.
func decryptAssertion(el *etree.Element, sp *crewjam.ServiceProvider) (*etree.Element, error) {
	var data *etree.Element
	for _, c := range el.ChildElements() {
		if c.Tag == "EncryptedData" && c.NamespaceURI() == xmlencNS {
			data = c
			break
		}
	}
	if data == nil {
		return nil, errors.New("no EncryptedData")
	}
	var key any = sp.Key
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
// namespace check sees the same XML.
type capturedBody struct {
	buf      bytes.Buffer
	tooLarge bool
}

var errArtifactTooLarge = errors.New("artifact response too large")

// Write fails past the limit, so crewjam stops reading the answer too.
func (c *capturedBody) Write(p []byte) (int, error) {
	if c.tooLarge || c.buf.Len()+len(p) > maxArtifactResponse {
		c.tooLarge = true
		return 0, errArtifactTooLarge
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
		// After a redirect only the answer crewjam goes on to read counts.
		into.buf.Reset()
		into.tooLarge = false
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
