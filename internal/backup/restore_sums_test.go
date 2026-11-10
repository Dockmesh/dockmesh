package backup

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"path/filepath"
	"testing"

	"github.com/dockmesh/dockmesh/internal/db"
	"github.com/dockmesh/dockmesh/internal/stacks"
)

func TestRestoreRecordsRestoredStackFiles(t *testing.T) {
	database, err := db.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })
	if err := db.Migrate(database); err != nil {
		t.Fatal(err)
	}
	m, err := stacks.NewManager(t.TempDir(), nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { m.Close() })
	m.SetSumStore(stacks.NewSumStore(database))

	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for name, body := range map[string]string{
		"stack/compose.yaml":        "services: {}\n",
		"stack/" + stacks.MetaFileName:  `{"active_environment":"prod"}`,
		"stack/compose.prod.yaml":   "services: {}\n",
		"stack/data/compose.x.yaml": "x",
	} {
		if err := tw.WriteHeader(&tar.Header{Name: name, Mode: 0o644, Size: int64(len(body)), Typeflag: tar.TypeReg}); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write([]byte(body)); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}

	s := &Service{stacks: m}
	if err := s.restoreStackInto(context.Background(), "app", &buf, nil, nil, nil, false, &RestoreReport{}); err != nil {
		t.Fatal(err)
	}
	if changed, err := m.ModifiedOutside("app"); err != nil || len(changed) != 0 {
		t.Errorf("restored files reported as changed outside: %v, %v", changed, err)
	}
}
