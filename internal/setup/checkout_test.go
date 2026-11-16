package setup

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dockmesh/dockmesh/internal/config"
	"github.com/dockmesh/dockmesh/internal/db/dbtest"
	"github.com/dockmesh/dockmesh/internal/layout"
)

// checkout is the working directory of a source run up to 0.5.x.
const checkout = "/opt/dockmesh-src"

// startPaths resolves the server's paths at start: layout.CheckoutProbe
// as main runs it, whose directory main then sets as DOCKMESH_DATA_DIR.
// The rooted paths cannot pass the ownership check, which has its own
// tests in layout; trust stands in for its answer.
func startPaths(root string, env map[string]string, extra []string, trust error) (string, config.Paths) {
	getenv := func(k string) string { return env[k] }
	dir, _ := layout.CheckoutProbe{
		WorkDir:  checkout,
		Getenv:   getenv,
		EnvFiles: extra,
		Lstat:    rootedStat(root),
		Trust:    func(string) error { return trust },
	}.Dir()
	return dir, config.ResolvePaths(func(k string) string {
		if k == "DOCKMESH_DATA_DIR" && dir != "" {
			return dir
		}
		return getenv(k)
	})
}

func TestCheckoutDataResolution(t *testing.T) {
	checkoutDB := filepath.Join(checkout, "data", "dockmesh.db")
	tests := []struct {
		name string
		// db is created and seeded with an admin; files are touched.
		db, files []string
		env       map[string]string
		extra     []string
		// distrust is the ownership check's answer; an untrusted checkout
		// is not used as data but still counts for the setup decision.
		distrust error
		wantDir  string
		wantDB   string
		want     Mode
	}{
		{name: "no checkout data", wantDB: "/var/lib/dockmesh/data/dockmesh.db", want: NeverSetUp},
		{name: "checkout data with database", db: []string{checkoutDB}, wantDir: checkout, wantDB: checkoutDB, want: SetUp},
		{
			name:   "checkout data dir without database",
			files:  []string{checkout + "/data/secrets.env", checkout + "/data/agents-ca.crt", checkout + "/stacks/web/compose.yaml"},
			wantDB: "/var/lib/dockmesh/data/dockmesh.db", want: NeverSetUp,
		},
		{name: "database path is a directory", files: []string{checkoutDB + "/x"}, wantDB: "/var/lib/dockmesh/data/dockmesh.db", want: NeverSetUp},
		{
			name: "config file wins", db: []string{checkoutDB}, files: []string{layout.ConfigFile},
			wantDB: "/var/lib/dockmesh/data/dockmesh.db", want: SetUp,
		},
		{
			name: "legacy env file wins", db: []string{checkoutDB}, files: []string{layout.LegacyEnvFiles[0]},
			wantDB: "/var/lib/dockmesh/data/dockmesh.db", want: SetUp,
		},
		{
			name: "unit env file wins", db: []string{checkoutDB}, files: []string{"/srv/unit/dockmesh.env"}, extra: []string{"/srv/unit/dockmesh.env"},
			wantDB: "/var/lib/dockmesh/data/dockmesh.db", want: SetUp,
		},
		{
			name: "DOCKMESH_DATA_DIR wins", db: []string{checkoutDB}, env: map[string]string{"DOCKMESH_DATA_DIR": "/srv/dm"},
			wantDB: "/srv/dm/data/dockmesh.db", want: SetUp,
		},
		{
			name: "untrusted checkout still keeps setup closed", db: []string{checkoutDB}, distrust: errors.New("writable by others"),
			wantDB: "/var/lib/dockmesh/data/dockmesh.db", want: SetUp,
		},
		{
			name: "DOCKMESH_DB_PATH wins", db: []string{checkoutDB}, env: map[string]string{"DOCKMESH_DB_PATH": "/srv/db/dockmesh.db"},
			wantDB: "/srv/db/dockmesh.db", want: SetUp,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := t.TempDir()
			for _, db := range tt.db {
				openAt(t, root, db)
				seedAccounts(t, root, db, true, true)
			}
			for _, f := range tt.files {
				touch(t, root, f)
			}
			dir, paths := startPaths(root, tt.env, tt.extra, tt.distrust)
			if dir != tt.wantDir || paths.DBPath != tt.wantDB {
				t.Fatalf("checkout dir %q, db %q; want %q, %q", dir, paths.DBPath, tt.wantDir, tt.wantDB)
			}
			p := Probe{
				ConfigFiles: tt.extra,
				DBPathSet:   tt.env["DOCKMESH_DB_PATH"] != "",
				Paths:       paths,
			}
			if layout.HasCheckoutDB(checkout, rootedStat(root)) {
				p.WorkDir = checkout
			}
			if got := boot(t, root, p); got != tt.want {
				t.Fatalf("mode = %v, want %v", got, tt.want)
			}
		})
	}
}

// The checkout's database is a known location: a server that does not use
// it never opens setup over it.
func TestCheckoutDatabaseIsKnown(t *testing.T) {
	root := t.TempDir()
	openAt(t, root, filepath.Join(checkout, "data", "dockmesh.db"))
	p := Probe{Paths: config.ResolvePaths(func(string) string { return "" }), WorkDir: checkout}
	if got := boot(t, root, p); got != SetUp {
		t.Fatalf("mode = %v, want set up", got)
	}
}

// TestCheckoutTwoStarts starts on the data of a 0.5.x source run twice
// without changes: both starts use the same database and paths, setup mode
// stays closed and nothing appears outside the checkout.
func TestCheckoutTwoStarts(t *testing.T) {
	root := t.TempDir()
	db := filepath.Join(checkout, "data", "dockmesh.db")
	openAt(t, root, db)
	seedAccounts(t, root, db, true, true)
	for _, f := range []string{"data/secrets.env", "data/secrets.age-key", "data/agents-ca.key", "data/agents-ca.crt", "data/audit-genesis.sha256", "stacks/web/compose.yaml"} {
		touch(t, root, filepath.Join(checkout, f))
	}
	before := tree(t, root)
	want := config.Paths{
		DataDir:          checkout,
		DBPath:           db,
		StacksRoot:       checkout + "/stacks",
		SecretsPath:      checkout + "/data/secrets.env",
		AuditGenesisPath: checkout + "/data/audit-genesis.sha256",
		SecretsKeyPath:   checkout + "/data/secrets.age-key",
		UpdateDir:        checkout + "/update",
	}
	var firstDB fs.FileInfo
	for start := 1; start <= 2; start++ {
		dir, paths := startPaths(root, nil, nil, nil)
		if dir != checkout || paths != want {
			t.Fatalf("start %d: checkout dir %q, paths %+v; want %+v", start, dir, paths, want)
		}
		p := Probe{Paths: paths, WorkDir: checkout, Lstat: rootedStat(root), Stat: rootedFollow(root), ReadDir: rootedReadDir(root)}
		if f := p.Scan(); Fresh(f) || !f.DB || f.ForeignDB {
			t.Fatalf("start %d: facts %+v, want the checkout's database as the active one", start, f)
		}
		if got := boot(t, root, p); got != SetUp {
			t.Fatalf("start %d: mode = %v, want set up", start, got)
		}
		fi, err := os.Stat(filepath.Join(root, db))
		if err != nil {
			t.Fatal(err)
		}
		if firstDB == nil {
			firstDB = fi
		} else if !os.SameFile(firstDB, fi) {
			t.Fatalf("start %d opened another database file", start)
		}
		if got := countUsers(t, filepath.Join(root, db)); got != 2 {
			t.Fatalf("start %d: %d users, want 2", start, got)
		}
		if after := tree(t, root); after != before {
			t.Fatalf("start %d changed the files:\nbefore %s\nafter  %s", start, before, after)
		}
	}
}

// tree lists the files below root; SQLite's side files come and go with
// every open and are left out.
func tree(t *testing.T, root string) string {
	t.Helper()
	var out []string
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if strings.HasSuffix(p, "-wal") || strings.HasSuffix(p, "-shm") || strings.HasSuffix(p, "-journal") {
			return nil
		}
		rel, _ := filepath.Rel(root, p)
		out = append(out, rel)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return strings.Join(out, " ")
}

func countUsers(t *testing.T, path string) int {
	t.Helper()
	conn, err := dbtest.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	var n int
	if err := conn.QueryRow(`SELECT COUNT(*) FROM users`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}
