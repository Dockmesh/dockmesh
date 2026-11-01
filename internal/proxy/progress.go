package proxy

import (
	"context"
	"net/url"
	"strings"
	"time"

	"github.com/dockmesh/dockmesh/internal/operations"
)

// Steps of creating a site, in order, as reported to its operation.
const (
	StepSave        = "save"
	StepApply       = "apply"
	StepCertificate = "certificate"
)

// NewSiteSteps lists the steps of creating a site.
var NewSiteSteps = []operations.StepSpec{
	{Key: StepSave, Label: "Save the site"},
	{Key: StepApply, Label: "Apply the Caddy configuration"},
	{Key: StepCertificate, Label: "Get a certificate"},
}

// SecretValues returns the values in the options that can carry
// credentials: header values, basic-auth passwords and hashes and the
// forward-auth URL's query.
func (o SiteOptions) SecretValues() []string {
	var out []string
	for _, h := range append(append([]Header{}, o.Headers.RequestSet...), o.Headers.ResponseSet...) {
		out = append(out, h.Value)
	}
	for _, u := range o.Access.BasicAuth {
		out = append(out, u.Password, u.Hash)
	}
	if f := o.Access.ForwardAuth; f != nil {
		if u, err := url.Parse(f.URL); err == nil && u.RawQuery != "" {
			out = append(out, u.RawQuery)
			for _, vals := range u.Query() {
				out = append(out, vals...)
			}
		}
	}
	return out
}

// certWait bounds how long a new site's operation watches for its
// certificate. Caddy keeps trying afterwards; the step then ends as a
// warning, not a failure.
const certWait = 3 * time.Minute

// WatchCertificate ends the certificate step of op once Caddy reports a
// certificate for host (or a failure to get one), or once the served
// certificate for host can be read; issuers backdate NotBefore by up to an
// hour, so a certificate from before the site existed is not taken for a
// new one. It only observes; it never asks Caddy to do anything.
func (s *Service) WatchCertificate(ctx context.Context, op *operations.Op, host, tlsMode string, since time.Time) {
	if op == nil {
		return
	}
	defer op.Finish(nil)
	if tlsMode == "none" {
		op.StepSkip(StepCertificate, "HTTPS is off for this site")
		return
	}
	if !s.Enabled() {
		op.StepSkip(StepCertificate, "the proxy is off; Caddy gets the certificate once it runs")
		return
	}
	issuer := "Let's Encrypt or ZeroSSL"
	if tlsMode == "internal" {
		issuer = "the internal CA"
	}
	op.StepStart(StepCertificate, "")
	op.Logf("info", "waiting for a certificate for %s from %s", host, issuer)
	ctx, cancel := context.WithTimeout(ctx, certWait)
	defer cancel()
	tick := time.NewTicker(5 * time.Second)
	defer tick.Stop()
	logged := map[ACMEEvent]bool{}
	for {
		for _, ev := range s.ACMEEvents() {
			if ev.TS.Before(since.Add(-time.Second)) || !strings.EqualFold(ev.Host, host) || logged[ev] {
				continue
			}
			logged[ev] = true
			op.Logf(levelOf(ev), "%s: %s", ev.Kind, ev.Message)
			if ev.Kind == "failure" {
				op.StepWarn(StepCertificate, "Caddy could not get a certificate yet and keeps retrying: "+ev.Message)
				return
			}
			if ev.Successful {
				op.StepDone(StepCertificate, certDetail(ev.Issuer, issuer))
				return
			}
		}
		if cert, err := probeCert(ctx, host); err == nil && cert.NotBefore.After(since.Add(-2*time.Hour)) {
			op.StepDone(StepCertificate, "served, issued by "+certIssuer(cert))
			return
		}
		select {
		case <-ctx.Done():
			op.StepWarn(StepCertificate, "no certificate seen within 3 minutes; Caddy keeps trying, see the Certificates tab")
			return
		case <-tick.C:
		}
	}
}

func levelOf(ev ACMEEvent) string {
	if ev.Kind == "failure" {
		return "error"
	}
	return "info"
}

func certDetail(got, fallback string) string {
	if got != "" {
		return "issued by " + got
	}
	return "issued by " + fallback
}
