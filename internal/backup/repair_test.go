package backup

import (
	"context"
	"strings"
	"testing"

	"github.com/dockmesh/dockmesh/internal/backup/targets"
)

func smbJob(name string, cfg map[string]any) JobInput {
	return JobInput{
		Name:         name,
		TargetType:   "smb",
		TargetConfig: cfg,
		Sources:      []Source{{Type: "volume", Name: "pgdata"}},
	}
}

func TestRepairJobSecrets(t *testing.T) {
	svc, n := newTestService(t, true)
	ctx := context.Background()
	ts := targets.NewTargetStore(svc.store.db)

	nas := map[string]any{"host": "nas", "port": 445, "share": "bk", "username": "u", "path": "backups"}
	withPass := func(c map[string]any, p string) map[string]any {
		out := map[string]any{"password": p}
		for k, v := range c {
			out[k] = v
		}
		return out
	}
	for _, in := range []targets.TargetInput{
		{Name: "nas", Type: "smb", Config: withPass(nas, "s3cret")},
		{Name: "twin-a", Type: "smb", Config: withPass(map[string]any{"host": "dup", "share": "x", "username": "u"}, "a")},
		{Name: "twin-b", Type: "smb", Config: withPass(map[string]any{"host": "dup", "share": "x", "username": "u"}, "b")},
	} {
		if _, err := ts.Create(ctx, in); err != nil {
			t.Fatal(err)
		}
	}

	fix, err := svc.CreateJob(ctx, smbJob("from-target", withPass(nas, "")))
	if err != nil {
		t.Fatal(err)
	}
	typed, err := svc.CreateJob(ctx, smbJob("typed", withPass(nas, "mine")))
	if err != nil {
		t.Fatal(err)
	}
	other, err := svc.CreateJob(ctx, smbJob("other-share", withPass(map[string]any{"host": "nas", "share": "elsewhere", "username": "u"}, "")))
	if err != nil {
		t.Fatal(err)
	}
	ambiguous, err := svc.CreateJob(ctx, smbJob("ambiguous", withPass(map[string]any{"host": "dup", "share": "x", "username": "u"}, "")))
	if err != nil {
		t.Fatal(err)
	}

	var audited []string
	onRepair := func(j Job, target string) { audited = append(audited, j.Name+"<-"+target) }
	if err := svc.RepairJobSecrets(ctx, ts, n, onRepair); err != nil {
		t.Fatal(err)
	}

	password := func(id int64) string {
		j, err := svc.GetJob(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		cfg, _ := configToMap(j.TargetConfig)
		p, _ := cfg["password"].(string)
		return p
	}
	if got := password(fix.ID); got != "s3cret" {
		t.Errorf("matching job password = %q, want s3cret", got)
	}
	if got := password(typed.ID); got != "mine" {
		t.Errorf("job with its own password = %q, want mine", got)
	}
	for name, id := range map[string]int64{"other-share": other.ID, "ambiguous": ambiguous.ID} {
		if got := password(id); got != "" {
			t.Errorf("%s password = %q, want it left blank", name, got)
		}
	}
	if len(audited) != 1 || audited[0] != "from-target<-nas" {
		t.Errorf("audited = %v", audited)
	}

	open, err := n.Open(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var repaired, missing string
	for _, o := range open {
		switch o.Key {
		case repairedNoticeKey:
			repaired = o.Body
		case missingNoticeKey:
			missing = o.Body
		}
	}
	if !strings.Contains(repaired, `"from-target"`) || strings.Contains(repaired, `"typed"`) {
		t.Errorf("repaired notice body: %q", repaired)
	}
	if !strings.Contains(missing, `"other-share"`) || !strings.Contains(missing, `"ambiguous"`) || strings.Contains(missing, `"from-target"`) {
		t.Errorf("missing notice body: %q", missing)
	}

	// A second start repairs nothing again.
	audited = nil
	if err := svc.RepairJobSecrets(ctx, ts, n, onRepair); err != nil {
		t.Fatal(err)
	}
	if len(audited) != 0 {
		t.Errorf("second run repaired again: %v", audited)
	}
}

func TestRepairJobSecretsResolvesNotice(t *testing.T) {
	svc, n := newTestService(t, true)
	ctx := context.Background()
	ts := targets.NewTargetStore(svc.store.db)

	job, err := svc.CreateJob(ctx, smbJob("lonely", map[string]any{"host": "gone", "share": "x", "username": "u", "password": ""}))
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.RepairJobSecrets(ctx, ts, n, nil); err != nil {
		t.Fatal(err)
	}
	open, _ := n.Open(ctx)
	if !hasKey(open, missingNoticeKey) {
		t.Fatal("expected the re-enter notice for an unmatched job")
	}

	if _, err := svc.store.db.ExecContext(ctx, `DELETE FROM backup_jobs WHERE id = ?`, job.ID); err != nil {
		t.Fatal(err)
	}
	if err := svc.RepairJobSecrets(ctx, ts, n, nil); err != nil {
		t.Fatal(err)
	}
	open, _ = n.Open(ctx)
	if hasKey(open, missingNoticeKey) {
		t.Error("notice stayed open after the job was gone")
	}
}
