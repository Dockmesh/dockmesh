package setup

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"testing"

	"github.com/dockmesh/dockmesh/internal/db/dbtest"
	"github.com/dockmesh/dockmesh/internal/layout"
)

// rootedStat resolves absolute paths below root, so a test can lay out
// /etc/dockmesh, /var/lib/dockmesh and friends in a temp dir.
func rootedStat(root string) func(string) (fs.FileInfo, error) {
	return func(p string) (fs.FileInfo, error) {
		if p == "" {
			return nil, fs.ErrNotExist
		}
		return os.Stat(filepath.Join(root, p))
	}
}

func touch(t *testing.T, root, p string) {
	t.Helper()
	full := filepath.Join(root, p)
	if err := os.MkdirAll(filepath.Dir(full), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(full, []byte("DOCKMESH_HTTP_ADDR=:8080\n"), 0o600); err != nil {
		t.Fatal(err)
	}
}

// openAt creates a migrated database at p below root, the way the server
// does on its first start.
func openAt(t *testing.T, root, p string) {
	t.Helper()
	full := filepath.Join(root, p)
	if err := os.MkdirAll(filepath.Dir(full), 0o750); err != nil {
		t.Fatal(err)
	}
	conn, err := dbtest.Open(full)
	if err != nil {
		t.Fatal(err)
	}
	conn.Close()
}

func seedAccounts(t *testing.T, root, dbPath string, users, admin bool) {
	t.Helper()
	conn, err := dbtest.Open(filepath.Join(root, dbPath))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if users {
		if _, err := conn.Exec(`INSERT INTO users (id, username, password) VALUES ('u1', 'u1', 'x'), ('u2', 'u2', 'x')`); err != nil {
			t.Fatal(err)
		}
		role := "viewer"
		if admin {
			role = "admin"
		}
		if _, err := conn.Exec(`INSERT INTO role_assignments (user_id, role_name, scope_type) VALUES ('u1', ?, 'global')`, role); err != nil {
			t.Fatal(err)
		}
	}
}

// boot runs what the server does at start: scan, open (and so create) the
// database, count accounts, decide.
func boot(t *testing.T, root string, p Probe) Mode {
	t.Helper()
	p.Stat = rootedStat(root)
	facts := p.Scan()
	full := filepath.Join(root, p.DBPath)
	if err := os.MkdirAll(filepath.Dir(full), 0o750); err != nil {
		t.Fatal(err)
	}
	conn, err := dbtest.Open(full)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	st, mode, _, err := NewForBoot(context.Background(), conn, facts)
	if err != nil {
		t.Fatal(err)
	}
	if st.Active() != (mode != SetUp) {
		t.Fatalf("Active() = %v for mode %v", st.Active(), mode)
	}
	return mode
}

type configLocation struct {
	name string
	// file is created below the root; empty for none.
	file string
	// extra is passed as Probe.ConfigFiles (--env-file, unit EnvironmentFile).
	extra     string
	dbPathSet bool
}

func configLocations() []configLocation {
	locs := []configLocation{
		{name: "no config"},
		{name: "config " + layout.ConfigFile, file: layout.ConfigFile},
		{name: "--env-file", file: "/opt/dm/custom.env", extra: "/opt/dm/custom.env"},
		{name: "unit EnvironmentFile", file: "/srv/unit/dockmesh.env", extra: "/srv/unit/dockmesh.env"},
		{name: "DOCKMESH_DB_PATH", dbPathSet: true},
	}
	for _, l := range layout.LegacyEnvFiles {
		locs = append(locs, configLocation{name: "legacy " + l, file: l})
	}
	return locs
}

// wantMode is the rule set of the decision, written out per input rather
// than derived from Decide.
func wantMode(config, db, users, force bool) Mode {
	if users {
		return SetUp
	}
	if !config && !db {
		return NeverSetUp
	}
	if config && force {
		return LegacyUnfinished
	}
	return SetUp
}

func TestDecisionTable(t *testing.T) {
	const activeDB = "/var/lib/dockmesh/data/dockmesh.db"
	accounts := []struct {
		name         string
		users, admin bool
	}{
		{name: "0 users"},
		{name: "users, 0 admins", users: true},
		{name: "users, admins", users: true, admin: true},
	}
	for _, loc := range configLocations() {
		for _, db := range []bool{false, true} {
			for _, acc := range accounts {
				if acc.users && !db {
					continue
				}
				for _, force := range []bool{false, true} {
					name := fmt.Sprintf("%s/db=%v/%s/force=%v", loc.name, db, acc.name, force)
					t.Run(name, func(t *testing.T) {
						t.Parallel()
						root := t.TempDir()
						if loc.file != "" {
							touch(t, root, loc.file)
						}
						if db {
							openAt(t, root, activeDB)
							seedAccounts(t, root, activeDB, acc.users, acc.admin)
						}
						p := Probe{DBPath: activeDB, DBPathSet: loc.dbPathSet, Force: force}
						if loc.extra != "" {
							p.ConfigFiles = []string{loc.extra}
						}
						config := loc.file != "" || loc.dbPathSet
						// The second start sees the database the first one created.
						for i, dbBefore := range []bool{db, true} {
							want := wantMode(config, dbBefore, acc.users, force)
							if got := boot(t, root, p); got != want {
								t.Fatalf("start %d: mode = %v, want %v", i+1, got, want)
							}
						}
					})
				}
			}
		}
	}
}

func TestUsersWithoutAdminNeverOpenSetup(t *testing.T) {
	const activeDB = "/var/lib/dockmesh/data/dockmesh.db"
	for _, loc := range configLocations() {
		for _, force := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/force=%v", loc.name, force), func(t *testing.T) {
				root := t.TempDir()
				if loc.file != "" {
					touch(t, root, loc.file)
				}
				openAt(t, root, activeDB)
				seedAccounts(t, root, activeDB, true, false)
				p := Probe{DBPath: activeDB, DBPathSet: loc.dbPathSet, Force: force, ConfigFiles: []string{loc.extra}}
				if got := boot(t, root, p); got != SetUp {
					t.Fatalf("mode = %v, want set up", got)
				}
			})
		}
	}
}

// A database at another known location is an install with data the
// server does not look at; setup stays closed even with no config.
func TestForeignDatabaseKeepsSetupClosed(t *testing.T) {
	const activeDB = "/opt/elsewhere/data/dockmesh.db"
	for _, known := range layout.KnownDBPaths() {
		t.Run(known, func(t *testing.T) {
			root := t.TempDir()
			openAt(t, root, known)
			if got := boot(t, root, Probe{DBPath: activeDB, Force: true}); got != SetUp {
				t.Fatalf("mode = %v, want set up", got)
			}
		})
	}
}

func TestSameDatabaseThroughSymlinkIsNotForeign(t *testing.T) {
	root := t.TempDir()
	const activeDB = "/var/lib/dockmesh/data/dockmesh.db"
	if err := os.MkdirAll(filepath.Join(root, "/var/lib"), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "/data"), 0o750); err != nil {
		t.Fatal(err)
	}
	// /data/data -> /var/lib/dockmesh/data, as on a host with /data bound
	// onto the data dir.
	if err := os.MkdirAll(filepath.Join(root, "/var/lib/dockmesh/data"), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(root, "/var/lib/dockmesh/data"), filepath.Join(root, "/data/data")); err != nil {
		t.Fatal(err)
	}
	touch(t, root, layout.LegacyEnvFiles[0])
	f := Probe{DBPath: activeDB, Force: true, Stat: rootedStat(root)}.Scan()
	if f.ForeignDB {
		t.Fatal("the active database seen through a symlink counts as foreign")
	}
	openAt(t, root, activeDB)
	if got := boot(t, root, Probe{DBPath: activeDB, Force: true}); got != LegacyUnfinished {
		t.Fatalf("mode = %v, want legacy unfinished", got)
	}
}

func TestDecideDoubtIsSetUp(t *testing.T) {
	tests := []struct {
		name string
		f    Facts
		want Mode
	}{
		{name: "zero facts, users unknown", f: Facts{Users: -1}, want: SetUp},
		{name: "nothing, users unknown", f: Facts{Users: -1, Force: true}, want: SetUp},
		{name: "nothing at all", f: Facts{}, want: NeverSetUp},
		{name: "foreign database", f: Facts{ForeignDB: true}, want: SetUp},
		{name: "force without config", f: Facts{Force: true, DB: true}, want: SetUp},
		{name: "force without config or db", f: Facts{Force: true}, want: NeverSetUp},
		{name: "config and force, no db yet", f: Facts{Config: true, Force: true}, want: LegacyUnfinished},
		{name: "config and force, empty db", f: Facts{Config: true, Force: true, DB: true}, want: LegacyUnfinished},
		{name: "config, no force", f: Facts{Config: true, DB: true}, want: SetUp},
		{name: "db only", f: Facts{DB: true}, want: SetUp},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := Decide(tt.f); got != tt.want {
				t.Fatalf("Decide = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestScanCountsUnreadableAsPresent(t *testing.T) {
	denied := func(p string) (fs.FileInfo, error) {
		if p == layout.ConfigFile {
			return nil, fs.ErrPermission
		}
		return nil, fs.ErrNotExist
	}
	f := Probe{DBPath: "/x/data/dockmesh.db", Stat: denied}.Scan()
	if !f.Config {
		t.Fatal("a config the service may not stat counts as missing")
	}
	if f.Users >= 0 {
		t.Fatal("Scan claims to know the accounts")
	}
}
