package handlers

import (
	"encoding/json"
	"strings"

	"github.com/dockmesh/dockmesh/internal/backup"
	"github.com/dockmesh/dockmesh/internal/backup/targets"
	"github.com/dockmesh/dockmesh/internal/notify"
	"github.com/dockmesh/dockmesh/internal/redact"
)

// Credential fields in backup target configs (also embedded in jobs).
var backupTargetSecrets = []string{"password", "secret_key", "private_key"}

// notifySecrets lists the fields of the given configs that are
// credentials for a channel type: every field that isn't a known public
// one, so a field the client invented can't be read back either. A
// boolean "<field>_set" is a redaction marker, not a field of its own.
func notifySecrets(channelType string, cfgs ...map[string]any) []string {
	seen := map[string]bool{}
	var out []string
	for _, cfg := range cfgs {
		for k, v := range cfg {
			if _, isBool := v.(bool); isBool && strings.HasSuffix(k, redact.SetSuffix) {
				continue
			}
			if !seen[k] && !notify.PublicConfigField(channelType, k) {
				seen[k] = true
				out = append(out, k)
			}
		}
	}
	return out
}

// redactConfig blanks secrets in a JSON-object config; other shapes have
// no named fields and pass through.
func redactConfig(cfg any, secrets []string) any {
	if m := redact.ToMap(cfg); m != nil {
		return redact.Map(m, secrets)
	}
	return cfg
}

// keepConfig fills secrets the client left empty from the stored config.
func keepConfig(next, stored any, secrets []string) (any, error) {
	n := redact.ToMap(next)
	if n == nil {
		return next, nil
	}
	return redact.Keep(n, redact.ToMap(stored), secrets)
}

func redactJob(j *backup.Job) *backup.Job {
	out := *j
	out.TargetConfig = redactConfig(j.TargetConfig, backupTargetSecrets)
	return &out
}

func redactTarget(t *targets.StoredTarget) *targets.StoredTarget {
	out := *t
	out.Config = redactConfig(t.Config, backupTargetSecrets)
	return &out
}

// redactChannel answers a config that isn't a JSON object with an empty
// one: it has no named fields to tell secrets from the rest.
func redactChannel(c *notify.Channel) *notify.Channel {
	out := *c
	out.Config = json.RawMessage("{}")
	if m := redact.ToMap(c.Config); m != nil {
		if b, err := json.Marshal(redact.Map(m, notifySecrets(c.Type, m))); err == nil {
			out.Config = b
		}
	}
	return &out
}
