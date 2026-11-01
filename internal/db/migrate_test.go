package db

import (
	"database/sql"
	"io/fs"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"testing/fstest"
)

// v042Migrations reports whether name shipped in v0.4.2, the last release
// before 072 and 079+ were merged.
func v042Migrations(name string) bool {
	return name <= "076" && !strings.HasPrefix(name, "072_")
}

// shippedFS returns the embedded migrations accepted by keep (nil keeps
// all) plus extra, as a test file system.
func shippedFS(t *testing.T, keep func(string) bool, extra map[string]string) fstest.MapFS {
	t.Helper()
	src := migrationFS()
	entries, err := fs.ReadDir(src, ".")
	if err != nil {
		t.Fatal(err)
	}
	out := fstest.MapFS{}
	for _, e := range entries {
		if keep != nil && !keep(e.Name()) {
			continue
		}
		b, err := fs.ReadFile(src, e.Name())
		if err != nil {
			t.Fatal(err)
		}
		out[e.Name()] = &fstest.MapFile{Data: b}
	}
	for name, body := range extra {
		out[name] = &fstest.MapFile{Data: []byte(body)}
	}
	return out
}

func openFileDB(t *testing.T) (*sql.DB, string) {
	t.Helper()
	dir := t.TempDir()
	database, err := Open(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })
	return database, dir
}

func appliedInOrder(t *testing.T, database *sql.DB) []string {
	t.Helper()
	rows, err := database.Query(`SELECT version FROM schema_migrations ORDER BY rowid`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var v string
		if err := rows.Scan(&v); err != nil {
			t.Fatal(err)
		}
		out = append(out, v)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}

func tableExists(t *testing.T, database *sql.DB, name string) bool {
	t.Helper()
	var n int
	if err := database.QueryRow(`SELECT COUNT(1) FROM sqlite_master WHERE type = 'table' AND name = ?`, name).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n == 1
}

func TestShippedMigrationNames(t *testing.T) {
	all, err := loadMigrations(migrationFS())
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range all {
		if numberedName.MatchString(m.name) && !slices.Contains(numberedMigrations, m.name) {
			t.Errorf("%s: numbered migration names are frozen, name it YYYYMMDDHHMMSS_<slug>.sql (UTC)", m.name)
		}
	}
	for _, name := range numberedMigrations {
		if !slices.ContainsFunc(all, func(m migration) bool { return m.name == name }) {
			t.Errorf("%s is missing: an applied migration must never be renamed or removed", name)
		}
	}
}

func TestParseMigrationName(t *testing.T) {
	tests := []struct {
		name    string
		version string
		wantErr bool
	}{
		{"001_initial.sql", "001", false},
		{"20261101120000_user_preferences.sql", "20261101120000", false},
		{"20261301120000_bad_month.sql", "", true},
		{"20261101120000_Upper.sql", "", true},
		{"20261101120000-dash.sql", "", true},
		{"2026110112000_short.sql", "", true},
		{"086.sql", "", true},
		{"20261101120000_slug.txt", "", true},
	}
	for _, tt := range tests {
		m, err := parseMigrationName(tt.name)
		if (err != nil) != tt.wantErr {
			t.Errorf("parseMigrationName(%q) error = %v, wantErr %v", tt.name, err, tt.wantErr)
			continue
		}
		if m.version != tt.version {
			t.Errorf("parseMigrationName(%q) version = %q, want %q", tt.name, m.version, tt.version)
		}
	}
}

func TestLoadMigrations(t *testing.T) {
	tests := []struct {
		name    string
		files   []string
		want    []string
		wantErr string
	}{
		{
			name:  "numbered before timestamped, each by version",
			files: []string{"20261102000000_c.sql", "085_b.sql", "20261101000000_d.sql", "001_a.sql"},
			want:  []string{"001_a.sql", "085_b.sql", "20261101000000_d.sql", "20261102000000_c.sql"},
		},
		{
			name:    "duplicate timestamp",
			files:   []string{"20261101000000_a.sql", "20261101000000_b.sql"},
			wantErr: "share version 20261101000000",
		},
		{
			name:    "duplicate number",
			files:   []string{"012_a.sql", "012_b.sql"},
			wantErr: "share version 012",
		},
		{
			name:    "bad name",
			files:   []string{"001_a.sql", "notes.md"},
			wantErr: "notes.md",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fsys := fstest.MapFS{}
			for _, f := range tt.files {
				fsys[f] = &fstest.MapFile{Data: []byte("SELECT 1;")}
			}
			got, err := loadMigrations(fsys)
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("error = %v, want one containing %q", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			var names []string
			for _, m := range got {
				names = append(names, m.name)
			}
			if !slices.Equal(names, tt.want) {
				t.Errorf("order = %v, want %v", names, tt.want)
			}
		})
	}
}

func TestMigrateFreshInstall(t *testing.T) {
	database, dir := openFileDB(t)
	if err := Migrate(database); err != nil {
		t.Fatal(err)
	}
	all, err := loadMigrations(migrationFS())
	if err != nil {
		t.Fatal(err)
	}
	if got := countApplied(t, database); got != len(all) {
		t.Errorf("applied %d migrations, want %d", got, len(all))
	}
	if got := snapshotFiles(t, dir); len(got) != 0 {
		t.Errorf("fresh install wrote snapshots: %v", got)
	}
	if err := Migrate(database); err != nil {
		t.Fatalf("second Migrate: %v", err)
	}
}

const tsMigration = "20261101120000_upgrade_probe.sql"

func TestMigrateUpgrade(t *testing.T) {
	tests := []struct {
		name string
		seed func(string) bool
		// wantNew are migrations the upgrade must apply, in this order.
		wantNew []string
	}{
		{
			name:    "from v0.4.2",
			seed:    v042Migrations,
			wantNew: []string{"072_image_update_checks.sql", "079_alert_engine.sql", tsMigration},
		},
		{
			name:    "from all numbered migrations",
			seed:    func(name string) bool { return numberedName.MatchString(name) },
			wantNew: []string{tsMigration},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			database, dir := openFileDB(t)
			if err := migrate(database, shippedFS(t, tt.seed, nil), ""); err != nil {
				t.Fatal(err)
			}
			if _, err := database.Exec(`INSERT INTO users (id, username, password) VALUES ('u1', 'admin', 'x')`); err != nil {
				t.Fatal(err)
			}
			before := appliedInOrder(t, database)

			full := shippedFS(t, nil, map[string]string{tsMigration: `CREATE TABLE upgrade_probe (id INTEGER PRIMARY KEY);`})
			if err := migrate(database, full, ""); err != nil {
				t.Fatal(err)
			}

			after := appliedInOrder(t, database)
			if !slices.Equal(after[:len(before)], before) {
				t.Fatal("upgrade changed the record of already applied migrations")
			}
			added := after[len(before):]
			all, err := loadMigrations(full)
			if err != nil {
				t.Fatal(err)
			}
			if len(after) != len(all) {
				t.Errorf("applied %d migrations, want %d", len(after), len(all))
			}
			for i, name := range tt.wantNew {
				j := slices.Index(added, name)
				if j < 0 {
					t.Errorf("%s not applied by the upgrade", name)
					continue
				}
				if i > 0 && j < slices.Index(added, tt.wantNew[i-1]) {
					t.Errorf("%s applied before %s", name, tt.wantNew[i-1])
				}
			}
			var username string
			if err := database.QueryRow(`SELECT username FROM users WHERE id = 'u1'`).Scan(&username); err != nil || username != "admin" {
				t.Errorf("user row after upgrade = %q, %v", username, err)
			}
			if !tableExists(t, database, "upgrade_probe") {
				t.Error("timestamped migration did not run")
			}
			if got := snapshotFiles(t, dir); len(got) != 1 {
				t.Errorf("snapshots = %v, want one", got)
			}
		})
	}
}

func TestMigrateOutOfOrderTimestamp(t *testing.T) {
	t.Setenv(SkipSnapshotEnv, "1")
	database, _ := openFileDB(t)
	fsys := fstest.MapFS{
		"001_base.sql":             {Data: []byte(`CREATE TABLE base (id INTEGER);`)},
		"20261102000000_newer.sql": {Data: []byte(`CREATE TABLE newer (id INTEGER);`)},
	}
	if err := migrate(database, fsys, ""); err != nil {
		t.Fatal(err)
	}
	fsys["20261101000000_older.sql"] = &fstest.MapFile{Data: []byte(`CREATE TABLE older (id INTEGER);`)}
	if err := migrate(database, fsys, ""); err != nil {
		t.Fatal(err)
	}
	want := []string{"001_base.sql", "20261102000000_newer.sql", "20261101000000_older.sql"}
	if got := appliedInOrder(t, database); !slices.Equal(got, want) {
		t.Errorf("applied = %v, want %v", got, want)
	}
	if !tableExists(t, database, "older") {
		t.Error("older migration did not run")
	}
}

func TestMigrateFailureRollsBackAndRetries(t *testing.T) {
	t.Setenv(SkipSnapshotEnv, "1")
	database, _ := openFileDB(t)
	const broken = "20261101000000_broken.sql"
	fsys := fstest.MapFS{
		"001_base.sql":             {Data: []byte(`CREATE TABLE base (id INTEGER);`)},
		broken:                     {Data: []byte(`CREATE TABLE half (id INTEGER); INSERT INTO base VALUES (1); INSERT INTO missing VALUES (1);`)},
		"20261102000000_after.sql": {Data: []byte(`CREATE TABLE after_broken (id INTEGER);`)},
	}
	for start := 1; start <= 2; start++ {
		err := migrate(database, fsys, "")
		if err == nil || !strings.Contains(err.Error(), broken) {
			t.Fatalf("start %d: error = %v, want one naming %s", start, err, broken)
		}
		if tableExists(t, database, "half") {
			t.Fatalf("start %d: failed migration left its table behind", start)
		}
		var rows int
		if err := database.QueryRow(`SELECT COUNT(1) FROM base`).Scan(&rows); err != nil || rows != 0 {
			t.Fatalf("start %d: failed migration left %d rows (%v)", start, rows, err)
		}
		if got := appliedInOrder(t, database); !slices.Equal(got, []string{"001_base.sql"}) {
			t.Fatalf("start %d: applied = %v", start, got)
		}
	}

	fsys[broken] = &fstest.MapFile{Data: []byte(`CREATE TABLE half (id INTEGER);`)}
	if err := migrate(database, fsys, ""); err != nil {
		t.Fatal(err)
	}
	want := []string{"001_base.sql", broken, "20261102000000_after.sql"}
	if got := appliedInOrder(t, database); !slices.Equal(got, want) {
		t.Errorf("applied = %v, want %v", got, want)
	}
}

func TestSnapshotReusedOnlyForSameState(t *testing.T) {
	database, dir := openFileDB(t)
	t.Setenv(SkipSnapshotEnv, "1")
	fsys := fstest.MapFS{
		"001_base.sql":             {Data: []byte(`CREATE TABLE base (id INTEGER);`)},
		"20261102000000_newer.sql": {Data: []byte(`CREATE TABLE newer (id INTEGER);`)},
	}
	if err := migrate(database, fsys, ""); err != nil {
		t.Fatal(err)
	}
	t.Setenv(SkipSnapshotEnv, "")

	// A pending migration with an older timestamp leaves MAX(version)
	// unchanged, so the snapshot must still be taken once it is applied
	// and another one arrives.
	fsys["20261101000000_older.sql"] = &fstest.MapFile{Data: []byte(`CREATE TABLE older (id INTEGER);`)}
	if err := migrate(database, fsys, ""); err != nil {
		t.Fatal(err)
	}
	fsys["20261101150000_third.sql"] = &fstest.MapFile{Data: []byte(`CREATE TABLE third (id INTEGER);`)}
	if err := migrate(database, fsys, ""); err != nil {
		t.Fatal(err)
	}
	if got := snapshotFiles(t, dir); len(got) != 2 {
		t.Errorf("snapshots = %v, want two (one per state)", got)
	}
}
