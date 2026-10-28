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

// seedLocalBackupRoots creates the allowed-roots setting on the first start
// that has it: the defaults plus every path existing local targets already
// use, so no configured backup stops working with the upgrade.
func seedLocalBackupRoots(ctx context.Context, st *settings.Store, db *sql.DB, svc *backup.Service, dataDir string) error {
	if st.Get(targets.LocalRootsSetting, "") != "" {
		return nil
	}
	roots := []string{"/srv/backups", filepath.Join(dataDir, "backups")}
	add := func(cfg any) {
		var lc targets.LocalConfig
		b, err := json.Marshal(cfg)
		if err != nil || json.Unmarshal(b, &lc) != nil {
			return
		}
		if lc.Path == "" {
			lc.Path = "./data/backups"
		}
		if abs, err := filepath.Abs(lc.Path); err == nil {
			roots = append(roots, abs)
		}
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
	seen := map[string]bool{}
	var uniq []string
	for _, r := range roots {
		if abs, err := filepath.Abs(r); err == nil && !seen[abs] {
			seen[abs] = true
			uniq = append(uniq, abs)
		}
	}
	return st.Set(ctx, targets.LocalRootsSetting, strings.Join(uniq, "\n"))
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
