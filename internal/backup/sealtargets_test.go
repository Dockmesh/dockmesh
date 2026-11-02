package backup

import (
	"context"
	"strings"
	"testing"

	"github.com/dockmesh/dockmesh/internal/backup/targets"
)

// Configs as a 0.4.x install stored them, one per target type that has
// credentials.
var legacyTargetConfigs = []struct {
	typ, raw, secret string
}{
	{"smb", `{"host":"nas","share":"bk","username":"u","password":"smb-pass"}`, "smb-pass"},
	{"sftp", `{"host":"h","username":"u","password":"sftp-pass","private_key":"-----BEGIN KEY-----","host_key":"ssh-ed25519 AAAA"}`, "sftp-pass"},
	{"s3", `{"endpoint":"s3.example","bucket":"b","access_key":"AK","secret_key":"s3-secret"}`, "s3-secret"},
	{"webdav", `{"url":"https://dav.example/x","username":"u","password":"dav-pass"}`, "dav-pass"},
	{"local", `{"path":"/srv/backups"}`, ""},
}

func rawConfigs(t *testing.T, svc *Service) []string {
	t.Helper()
	rows, err := svc.store.db.Query(`SELECT config_json FROM backup_targets UNION ALL SELECT target_config FROM backup_jobs`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			t.Fatal(err)
		}
		out = append(out, s)
	}
	return out
}

func TestEncryptStoredTargetSecrets(t *testing.T) {
	svc, _ := newTestService(t, true)
	ctx := context.Background()
	for _, c := range legacyTargetConfigs {
		if _, err := svc.store.db.Exec(`INSERT INTO backup_targets (name, type, config_json) VALUES (?, ?, ?)`, c.typ, c.typ, c.raw); err != nil {
			t.Fatal(err)
		}
		if _, err := svc.store.db.Exec(`INSERT INTO backup_jobs (name, encrypt, target_type, target_config, sources)
			VALUES (?, 0, ?, ?, '[]')`, "job-"+c.typ, c.typ, c.raw); err != nil {
			t.Fatal(err)
		}
	}

	n, err := svc.EncryptStoredTargetSecrets(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if n != 8 {
		t.Errorf("rewrote %d rows, want 8 (4 targets with credentials, 4 jobs)", n)
	}
	for _, raw := range rawConfigs(t, svc) {
		for _, c := range legacyTargetConfigs {
			if c.secret != "" && strings.Contains(raw, c.secret) {
				t.Errorf("plaintext %q left in %s", c.secret, raw)
			}
		}
		if strings.Contains(raw, "BEGIN KEY") {
			t.Errorf("private key left in %s", raw)
		}
	}

	if again, err := svc.EncryptStoredTargetSecrets(ctx); err != nil || again != 0 {
		t.Errorf("second pass = %d, %v; want 0, nil", again, err)
	}

	list, err := svc.Targets().List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, tg := range list {
		cfg := tg.Config.(map[string]any)
		for _, c := range legacyTargetConfigs {
			if c.typ != tg.Type || c.secret == "" {
				continue
			}
			found := false
			for _, v := range cfg {
				if v == c.secret {
					found = true
				}
			}
			if !found {
				t.Errorf("%s target doesn't read back its secret: %v", tg.Type, cfg)
			}
		}
		if tg.Type == "sftp" && (cfg["host_key"] != "ssh-ed25519 AAAA" || cfg["private_key"] != "-----BEGIN KEY-----") {
			t.Errorf("sftp config changed: %v", cfg)
		}
	}
	jobs, err := svc.ListJobs(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, j := range jobs {
		if j.TargetType == "s3" && j.TargetConfig.(map[string]any)["secret_key"] != "s3-secret" {
			t.Errorf("job config doesn't read back: %v", j.TargetConfig)
		}
	}
}

func TestEncryptStoredTargetSecretsWithoutEncryption(t *testing.T) {
	svc, _ := newTestService(t, false)
	raw := legacyTargetConfigs[0].raw
	if _, err := svc.store.db.Exec(`INSERT INTO backup_targets (name, type, config_json) VALUES ('nas', 'smb', ?)`, raw); err != nil {
		t.Fatal(err)
	}
	if n, err := svc.EncryptStoredTargetSecrets(context.Background()); err != nil || n != 0 {
		t.Fatalf("= %d, %v; want 0, nil", n, err)
	}
	if got := rawConfigs(t, svc); len(got) != 1 || got[0] != raw {
		t.Errorf("config changed with encryption off: %v", got)
	}
}

// Saved targets and inline job targets are written encrypted, and the
// SQL that records SFTP host keys keeps working on them.
func TestTargetSecretsStoredEncrypted(t *testing.T) {
	svc, _ := newTestService(t, true)
	ctx := context.Background()
	in := sftpJob()
	cfg := in.TargetConfig.(map[string]any)
	cfg["password"] = "wire-pass"
	job, err := svc.CreateJob(ctx, in)
	if err != nil {
		t.Fatal(err)
	}
	tgt, err := svc.Targets().Create(ctx, targets.TargetInput{Name: "s3", Type: "s3",
		Config: map[string]any{"endpoint": "e", "bucket": "b", "access_key": "a", "secret_key": "saved-secret"}})
	if err != nil {
		t.Fatal(err)
	}
	for _, raw := range rawConfigs(t, svc) {
		if strings.Contains(raw, "wire-pass") || strings.Contains(raw, "saved-secret") {
			t.Errorf("plaintext stored: %s", raw)
		}
	}
	if _, _, err := targets.TargetKeyRecorder(svc.store.db, job.TargetID).RecordHostKey(ctx, "ssh-ed25519 KEY"); err != nil {
		t.Fatal(err)
	}
	got, err := svc.Targets().Get(ctx, job.TargetID)
	if err != nil {
		t.Fatal(err)
	}
	m := got.Config.(map[string]any)
	if m["password"] != "wire-pass" || m["host_key"] != "ssh-ed25519 KEY" {
		t.Errorf("target config = %v", m)
	}
	s3, err := svc.Targets().Get(ctx, tgt.ID)
	if err != nil {
		t.Fatal(err)
	}
	if s3.Config.(map[string]any)["secret_key"] != "saved-secret" {
		t.Errorf("s3 config = %v", s3.Config)
	}
}
