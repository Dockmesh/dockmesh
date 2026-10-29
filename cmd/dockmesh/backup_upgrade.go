package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"path/filepath"
	"strings"

	"github.com/dockmesh/dockmesh/internal/backup"
	"github.com/dockmesh/dockmesh/internal/backup/targets"
	"github.com/dockmesh/dockmesh/internal/notices"
	"github.com/dockmesh/dockmesh/internal/settings"
)

// seedLocalBackupRoots creates the allowed-roots setting once: the
// defaults plus every path existing local targets already use, so no
// configured backup stops working with the upgrade. The extra paths are
// remembered so admins are asked to review them.
func seedLocalBackupRoots(ctx context.Context, st *settings.Store, db *sql.DB, svc *backup.Service, dataDir string) error {
	if st.Get(targets.LocalRootsSeededSetting, "") != "" {
		return nil
	}
	defaults := []string{"/srv/backups", filepath.Join(dataDir, "backups")}
	var used []string
	add := func(cfg any) {
		var lc targets.LocalConfig
		b, err := json.Marshal(cfg)
		if err != nil || json.Unmarshal(b, &lc) != nil {
			return
		}
		if lc.Path == "" {
			lc.Path = targets.DefaultLocalPath
		}
		used = append(used, lc.Path)
	}
	jobs, err := svc.ListJobs(ctx)
	if err != nil {
		return err
	}
	for _, j := range jobs {
		if j.TargetType == "local" {
			add(j.TargetConfig)
		}
	}
	stored, err := targets.NewTargetStore(db).List(ctx)
	if err != nil {
		return err
	}
	for _, t := range stored {
		if t.Type == "local" {
			add(t.Config)
		}
	}
	roots := uniqueAbs(defaults)
	var extras []string
	for _, p := range uniqueAbs(used) {
		covered := false
		for _, r := range roots {
			if targets.Within(r, p) {
				covered = true
			}
		}
		if !covered {
			extras = append(extras, p)
		}
	}
	if err := st.Set(ctx, targets.LocalRootsSetting, strings.Join(append(roots, extras...), "\n")); err != nil {
		return err
	}
	return st.Set(ctx, targets.LocalRootsSeededSetting, strings.Join(append([]string{"seeded"}, extras...), "\n"))
}

func uniqueAbs(paths []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, p := range paths {
		if abs, err := filepath.Abs(p); err == nil && !seen[abs] {
			seen[abs] = true
			out = append(out, abs)
		}
	}
	return out
}

// syncSeededRootsNotice asks admins to review backup directories the
// upgrade allowed only because existing jobs used them, until the setting
// is edited.
func syncSeededRootsNotice(ctx context.Context, n *notices.Service, st *settings.Store) error {
	lines := strings.Split(st.Get(targets.LocalRootsSeededSetting, ""), "\n")
	extras := []string{}
	if len(lines) > 1 && lines[0] == "seeded" {
		extras = lines[1:]
	}
	return n.Set(ctx, len(extras) > 0, notices.RaiseInput{
		Key:      "backups.local-roots-review",
		Audience: notices.Admins(),
		Severity: notices.SevInfo,
		Title:    "Review allowed local backup directories",
		Body: "Local backup targets may now only write below allowed directories. These were " +
			"added because existing jobs use them: " + strings.Join(extras, ", ") + ". Keep or " +
			"remove them in the " + targets.LocalRootsSetting + " setting.",
		Link: "/settings",
	})
}

// syncUnencryptableNotice tells admins about jobs set to encrypt while
// secrets encryption is off: their runs now fail instead of quietly
// writing unencrypted archives.
func syncUnencryptableNotice(ctx context.Context, n *notices.Service, svc *backup.Service) error {
	jobs, err := svc.UnencryptableJobs(ctx)
	if err != nil {
		return err
	}
	return n.Set(ctx, len(jobs) > 0, notices.RaiseInput{
		Key:      "backups.encrypt-unavailable",
		Audience: notices.Admins(),
		Severity: notices.SevCritical,
		Title:    "Encrypted backup jobs can't run",
		Body: "Secrets encryption is turned off on this server, so these jobs, which are set to " +
			"encrypt, fail instead of writing unencrypted archives: " + strings.Join(jobs, ", ") +
			". Turn secrets encryption back on (DOCKMESH_SECRETS_ENCRYPT_ENV) or switch off " +
			"encryption for each job.",
		Link: "/backups",
	})
}
