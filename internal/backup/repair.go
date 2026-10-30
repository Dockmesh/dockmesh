package backup

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"sort"

	"github.com/dockmesh/dockmesh/internal/backup/targets"
	"github.com/dockmesh/dockmesh/internal/notices"
)

const (
	repairedNoticeKey = "backups.job-secrets-repaired"
	missingNoticeKey  = "backups.job-secret-missing"

	// RepairNoticeKey names the startup check that runs RepairJobSecrets.
	RepairNoticeKey = "backups.job-repair"
)

// targetIdentity names the config fields that say which server and
// location a target points at, per target type.
var targetIdentity = map[string][]string{
	"smb":    {"host", "port", "username", "share", "path"},
	"sftp":   {"host", "port", "username", "path"},
	"s3":     {"endpoint", "region", "use_ssl", "bucket", "prefix", "access_key"},
	"webdav": {"url", "username", "path"},
}

// targetSecrets lists the credential fields of a target type; the type
// needs at least one of them to be set.
var targetSecrets = map[string][]string{
	"smb":    {"password"},
	"sftp":   {"password", "private_key"},
	"s3":     {"secret_key"},
	"webdav": {"password"},
}

// RepairJobSecrets fixes jobs that were saved with a blank target secret
// (a job made from a saved target in the wizard on 0.4.0). A job whose
// type and server/location match exactly one saved target takes that
// target's secret; onRepair is called for each. Jobs without such a match
// raise a notice asking for the secret to be re-entered.
func (s *Service) RepairJobSecrets(ctx context.Context, ts *targets.TargetStore, n *notices.Service, onRepair func(job Job, target string)) error {
	jobs, err := s.store.listJobs(ctx)
	if err != nil {
		return err
	}
	stored, err := ts.List(ctx)
	if err != nil {
		return err
	}
	var repaired, missing []string
	for _, j := range jobs {
		cfg, err := configToMap(j.TargetConfig)
		if err != nil || !lacksSecret(j.TargetType, cfg) {
			continue
		}
		match := matchingTargets(stored, j.TargetType, cfg)
		if len(match) != 1 {
			missing = append(missing, fmt.Sprintf("%q", j.Name))
			continue
		}
		tcfg, err := configToMap(match[0].Config)
		if err != nil || lacksSecret(j.TargetType, tcfg) {
			missing = append(missing, fmt.Sprintf("%q", j.Name))
			continue
		}
		for _, k := range targetSecrets[j.TargetType] {
			if v, _ := cfg[k].(string); v == "" {
				if tv, _ := tcfg[k].(string); tv != "" {
					cfg[k] = tv
				}
			}
		}
		if err := s.store.setJobTargetConfig(ctx, j.ID, cfg); err != nil {
			slog.Warn("restore job secret", "job", j.Name, "err", err)
			missing = append(missing, fmt.Sprintf("%q", j.Name))
			continue
		}
		if onRepair != nil {
			onRepair(j, match[0].Name)
		}
		repaired = append(repaired, fmt.Sprintf("%q (target %q)", j.Name, match[0].Name))
	}
	if n == nil {
		return nil
	}
	if len(repaired) > 0 {
		if err := n.Raise(ctx, notices.RaiseInput{
			Key:      repairedNoticeKey,
			Audience: notices.Admins(),
			Severity: notices.SevInfo,
			Title:    "Backup job passwords restored",
			Body: "These jobs were saved without a password or key (a 0.4.0 problem with jobs made " +
				"from a saved target). They took it from the matching saved target:" +
				notices.Bullets(repaired),
			Link: "/backups",
		}); err != nil {
			return err
		}
	}
	sort.Strings(missing)
	return setRefreshedNotice(ctx, n, len(missing) > 0, notices.RaiseInput{
		Key:      missingNoticeKey,
		Audience: notices.Admins(),
		Severity: notices.SevWarning,
		Title:    "Re-enter the password in these backup jobs",
		Body: "These jobs have no password or key stored and no single saved target to take it " +
			"from, so their runs fail to sign in:" + notices.Bullets(missing) +
			"Open each job and enter the password again.",
		Link: "/backups",
	})
}

// setRefreshedNotice is notices.Set for a notice whose body lists items:
// an open notice keeps its text on Raise, so it is reopened when the
// list changed.
func setRefreshedNotice(ctx context.Context, n *notices.Service, active bool, in notices.RaiseInput) error {
	if !active {
		return n.Resolve(ctx, in.Key)
	}
	open, err := n.Open(ctx)
	if err != nil {
		return err
	}
	for _, o := range open {
		if o.Key == in.Key && o.Body != in.Body {
			if err := n.Resolve(ctx, in.Key); err != nil {
				return err
			}
		}
	}
	return n.Raise(ctx, in)
}

// lacksSecret reports whether a config of a type that needs credentials
// has all of them blank.
func lacksSecret(typ string, cfg map[string]any) bool {
	keys := targetSecrets[typ]
	if len(keys) == 0 {
		return false
	}
	for _, k := range keys {
		if v, _ := cfg[k].(string); v != "" {
			return false
		}
	}
	return true
}

// matchingTargets returns the saved targets of the type that point at the
// same server and location as cfg.
func matchingTargets(stored []targets.StoredTarget, typ string, cfg map[string]any) []targets.StoredTarget {
	var out []targets.StoredTarget
	for _, t := range stored {
		tcfg, err := configToMap(t.Config)
		if err != nil || t.Type != typ {
			continue
		}
		same := true
		for _, k := range targetIdentity[typ] {
			if identityValue(cfg[k]) != identityValue(tcfg[k]) {
				same = false
				break
			}
		}
		if same {
			out = append(out, t)
		}
	}
	return out
}

// identityValue treats a missing field, "" and 0 alike (an unset port).
func identityValue(v any) string {
	switch x := v.(type) {
	case nil:
		return ""
	case float64:
		if x == 0 {
			return ""
		}
	}
	return fmt.Sprint(v)
}

func configToMap(cfg any) (map[string]any, error) {
	b, err := json.Marshal(cfg)
	if err != nil {
		return nil, err
	}
	m := map[string]any{}
	if string(b) != "null" {
		if err := json.Unmarshal(b, &m); err != nil {
			return nil, err
		}
	}
	return m, nil
}
