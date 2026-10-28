package handlers

import (
	"encoding/json"

	"github.com/dockmesh/dockmesh/internal/backup"
	"github.com/dockmesh/dockmesh/internal/backup/targets"
	"github.com/dockmesh/dockmesh/internal/notify"
	"github.com/dockmesh/dockmesh/internal/redact"
)

// Credential fields in backup target configs (also embedded in jobs).
var backupTargetSecrets = []string{"password", "secret_key", "private_key"}

// notifySecrets lists the credential fields of a channel type. For chat
// webhooks the URL itself is the credential.
func notifySecrets(channelType string) []string {
	s := []string{"password", "token", "integration_key", "app_token", "user_key"}
	switch channelType {
	case "discord", "slack", "teams":
		s = append(s, "url")
	}
	return s
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
func keepConfig(next, stored any, secrets []string) any {
	n := redact.ToMap(next)
	if n == nil {
		return next
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

func redactChannel(c *notify.Channel) *notify.Channel {
	out := *c
	if m := redact.ToMap(c.Config); m != nil {
		if b, err := json.Marshal(redact.Map(m, notifySecrets(c.Type))); err == nil {
			out.Config = b
		}
	}
	return &out
}
