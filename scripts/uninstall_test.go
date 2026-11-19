package scripts

import (
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/dockmesh/dockmesh/internal/config"
	"github.com/dockmesh/dockmesh/internal/setup"
)

// --dry-run only holds if every mutating command goes through run().
func TestUninstallMutationsUseRun(t *testing.T) {
	b, err := os.ReadFile("uninstall.sh")
	if err != nil {
		t.Fatal(err)
	}
	mutating := regexp.MustCompile(`^\s*(pkill|kill|rm|rmdir|systemctl|userdel|groupdel|docker|launchctl|mv|chown)\b`)
	for i, line := range strings.Split(string(b), "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "#") {
			continue
		}
		if mutating.MatchString(line) {
			t.Errorf("uninstall.sh:%d runs a mutating command outside run(): %s", i+1, strings.TrimSpace(line))
		}
	}
}

func readUninstall(t *testing.T) string {
	t.Helper()
	b, err := os.ReadFile("uninstall.sh")
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func uninstallHelpers(t *testing.T) string {
	t.Helper()
	return scriptBlock(t, readUninstall(t), "#  Env file helpers", "#  Detect install layout")
}

func runUninstallHelper(t *testing.T, dir, call string, args ...string) (string, error) {
	t.Helper()
	script := "set -euo pipefail\n" + uninstallHelpers(t) + "\n" + call + " \"$@\"\n"
	for i := range args {
		args[i] = filepath.ToSlash(args[i])
	}
	cmd := exec.Command("bash", append([]string{"-c", script, "uninstall-test"}, args...)...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	return strings.TrimSpace(string(out)), err
}

func TestUninstallEnvFileIsNotExecuted(t *testing.T) {
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash not available")
	}
	root := t.TempDir()
	pwned := filepath.Join(root, "pwned")
	p := filepath.ToSlash(pwned)

	tests := []struct {
		name    string
		env     string
		wantDir string
	}{
		{"plain values", "DOCKMESH_INSTALL_SCRIPT=/opt/share/dockmesh/install-agent.sh\nDOCKMESH_BINARY_DIR=/opt/share/dockmesh/bin\n", "/opt/share/dockmesh"},
		{"quoted and exported", "export DOCKMESH_BINARY_DIR=\"/opt/x/bin\"\n", "/opt/x"},
		{"installer only", "DOCKMESH_INSTALL_SCRIPT='/opt/y/install-agent.sh'\n", "/opt/y"},
		{"command substitution", "X=$(touch " + p + ")\nY=`touch " + p + "`\ntouch " + p + "\nDOCKMESH_BINARY_DIR=/opt/z/bin\n", "/opt/z"},
		{"substitution in the value", "DOCKMESH_BINARY_DIR=$(touch " + p + ")/bin\n", ""},
		{"relative path", "DOCKMESH_BINARY_DIR=share/bin\n", ""},
		{"option-like value", "DOCKMESH_BINARY_DIR=--help\n", ""},
		{"no keys", "DOCKMESH_PORT=8080\n", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			env := filepath.Join(t.TempDir(), "dockmesh.env")
			mustWrite(t, env, tt.env)
			got, err := runUninstallHelper(t, root, "asset_dir_from_env", env)
			if err != nil {
				t.Fatalf("asset_dir_from_env: %v\n%s", err, got)
			}
			if got != tt.wantDir {
				t.Errorf("asset dir = %q, want %q", got, tt.wantDir)
			}
			if _, err := os.Stat(pwned); err == nil {
				t.Fatal("env file content was executed")
			}
		})
	}
}

// The asset dir is removed with rm -rf as root, so no env file may set it.
func TestUninstallAssetDirOnlyFromRootEnv(t *testing.T) {
	b, err := os.ReadFile("uninstall.sh")
	if err != nil {
		t.Fatal(err)
	}
	assign := regexp.MustCompile(`^\s*ASSET_DIR=`)
	var got []string
	for _, line := range strings.Split(string(b), "\n") {
		if assign.MatchString(line) {
			got = append(got, strings.TrimSpace(line))
		}
	}
	want := `ASSET_DIR="${DOCKMESH_ASSET_DIR:-$(dirname "$BIN_DIR")/share/dockmesh}"`
	if len(got) != 1 || got[0] != want {
		t.Errorf("ASSET_DIR assignments = %q, want only %q", got, want)
	}
}

func TestUninstallNeverSourcesEnvFile(t *testing.T) {
	b, err := os.ReadFile("uninstall.sh")
	if err != nil {
		t.Fatal(err)
	}
	source := regexp.MustCompile(`(^|[;&|]\s*|\s)(\.|source)\s+["$]`)
	for i, line := range strings.Split(string(b), "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "#") {
			continue
		}
		if source.MatchString(line) {
			t.Errorf("uninstall.sh:%d sources a file: %s", i+1, strings.TrimSpace(line))
		}
	}
}

func scriptBlock(t *testing.T, s, from, to string) string {
	t.Helper()
	start := strings.Index(s, from)
	end := strings.Index(s, to)
	if start < 0 || end < start {
		t.Fatalf("block %q not found in uninstall.sh", from)
	}
	return s[start:end]
}

// purgeScript is uninstall.sh's output, runner and helpers, then call.
// TEST_MOUNT names a directory is_mountpoint reports as a mount point.
func purgeScript(t *testing.T, call string) string {
	t.Helper()
	s := readUninstall(t)
	return "set -euo pipefail\nexport LC_ALL=C\n" +
		scriptBlock(t, s, "#  Color / TTY", "#  Args") +
		"DRYRUN=\"${DRYRUN:-0}\"\n" +
		scriptBlock(t, s, "#  Runner", "#  Banner") +
		uninstallHelpers(t) +
		"is_mountpoint() { [ -n \"${TEST_MOUNT:-}\" ] && [ \"$1\" = \"$TEST_MOUNT\" ]; }\n" +
		call + "\n"
}

// buildTree creates files below root: a value "/" makes an empty
// directory, "->target" a symlink, anything else a file with that content.
func buildTree(t *testing.T, root string, files map[string]string) {
	t.Helper()
	for rel, v := range files {
		p := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		var err error
		switch {
		case v == "/":
			err = os.MkdirAll(p, 0o755)
		case strings.HasPrefix(v, "->"):
			err = os.Symlink(strings.TrimPrefix(v, "->"), p)
		default:
			err = os.WriteFile(p, []byte(v), 0o600)
		}
		if err != nil {
			t.Fatal(err)
		}
	}
}

func TestUninstallPurge(t *testing.T) {
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash not available")
	}
	dockmeshFiles := map[string]string{
		"dm-root/data/dockmesh.db":                                      "db",
		"dm-root/data/dockmesh.db-wal":                                  "wal",
		"dm-root/data/dockmesh.db-shm":                                  "shm",
		"dm-root/data/dockmesh.db.pre-migrate-20261101T101010.000Z.bak": "snap",
		"dm-root/data/dockmesh.db.pre-update.bak":                       "snap",
		"dm-root/data/secrets.env":                                      "jwt",
		"dm-root/data/secrets.age-key":                                  "key",
		"dm-root/data/secrets.age-key.history":                          "old keys",
		"dm-root/data/secrets.age-key.old":                              "old key",
		"dm-root/data/audit-genesis.sha256":                             "g",
		"dm-root/data/agents-ca.crt":                                    "c",
		"dm-root/data/agents-ca.key":                                    "k",
		"dm-root/data/ui-server.crt":                                    "c",
		"dm-root/data/grype-db/5/vulnerability.db":                      "v",
		"dm-root/data/git-cache/web/.git/HEAD":                          "ref",
		"dm-root/data/scan-tmp":                                         "/",
		"dm-root/data/backups":                                          "/",
		"dm-root/stacks/web/compose.yaml":                               "services: {}",
		"dm-root/stacks/web/.env.age":                                   "age",
		"dm-root/stacks/web/.dockmesh.meta.json":                        "{}",
		"dm-root/stacks/web/compose.prod.yaml":                          "services: {}",
		"dm-root/stacks/web/html":                                       "/",
		"dm-root/update/result.json":                                    "{}",
		"dm-root/agent-release/v0.6.0/dockmesh-agent-linux-amd64":       "bin",
		"dm-root/.setup/code-counters.json":                             "{}",
	}
	with := func(extra map[string]string) map[string]string {
		m := map[string]string{}
		for k, v := range dockmeshFiles {
			m[k] = v
		}
		for k, v := range extra {
			m[k] = v
		}
		return m
	}
	tests := []struct {
		name   string
		files  map[string]string
		env    string
		dryRun bool
		mount  string
		agents string
		// inUse are colon-separated container bind sources.
		inUse      string
		dockerDown bool
		// owners replaces the accounts that may own Dockmesh's trees.
		owners string
		// shared is a directory below root that counts as a system
		// directory a data root may be.
		shared  string
		gone    []string
		present []string
		out     []string
		notOut  []string
	}{
		{
			name: "foreign file in a stack dir",
			files: with(map[string]string{
				"dm-root/stacks/app/compose.yaml":     "services: {}",
				"dm-root/stacks/app/.env":             "A=1",
				"dm-root/stacks/app/data/db.sqlite":   "container data",
				"dm-root/stacks/app/data/empty-mount": "/",
			}),
			env:     "DOCKMESH_DATA_DIR={root}/dm-root\n",
			gone:    []string{"dm-root/data", "dm-root/stacks/web", "dm-root/update", "dm-root/agent-release", "dm-root/.setup"},
			present: []string{"dm-root/stacks/app/compose.yaml", "dm-root/stacks/app/.env", "dm-root/stacks/app/data/db.sqlite", "dm-root/.dockmesh-purged"},
			out: []string{
				"keep    {root}/dm-root/stacks/app: contains files not written by Dockmesh (e.g. data/db.sqlite)",
				"removed {root}/dm-root/data/dockmesh.db",
				"removed {root}/dm-root/data/secrets.age-key.history",
				"removed {root}/dm-root/stacks/web",
				"wrote {root}/dm-root/.dockmesh-purged",
				"These were kept:\n\n     {root}/dm-root/stacks/app\n\n",
			},
			notOut: []string{"removed {root}/dm-root\n", "removed {root}/dm-root/stacks\n", "The database, stacks"},
		},
		{
			name:   "only Dockmesh files",
			files:  with(map[string]string{"dm-root/dockmesh.env": "->{root}/etc-dockmesh.env"}),
			env:    "export DOCKMESH_DATA_DIR=\"{root}/dm-root/\"\n",
			gone:   []string{"dm-root"},
			out:    []string{"removed {root}/dm-root\n", "The database, stacks, deployment history"},
			notOut: []string{"keep ", "wrote ", "These were kept"},
		},
		{
			name: "symlinks in the stacks dir",
			files: with(map[string]string{
				"outside/secret.txt":              "secret",
				"outside/linked/compose.yaml":     "services: {}",
				"dm-root/stacks/app/compose.yaml": "services: {}",
				"dm-root/stacks/app/conf":         "->{root}/outside/secret.txt",
				"dm-root/stacks/linked":           "->{root}/outside/linked",
			}),
			env:     "DOCKMESH_DATA_DIR={root}/dm-root\n",
			gone:    []string{"dm-root/data", "dm-root/stacks/web"},
			present: []string{"outside/secret.txt", "outside/linked/compose.yaml", "dm-root/stacks/app/compose.yaml", "dm-root/stacks/app/conf", "dm-root/stacks/linked"},
			out: []string{
				"keep    {root}/dm-root/stacks/app: contains a symbolic link (conf)",
				"keep    {root}/dm-root/stacks/linked: symbolic link",
			},
		},
		{
			name:    "data dir is /",
			files:   map[string]string{"fallback/data/dockmesh.db": "db", "fallback/stacks/web/compose.yaml": "x"},
			env:     "DOCKMESH_DATA_DIR=/\n",
			present: []string{"fallback/data/dockmesh.db", "fallback/stacks/web/compose.yaml"},
			out:     []string{"keep    /: is a system directory"},
			notOut:  []string{"[ok] removed"},
		},
		{
			name:    "data dir is /etc",
			files:   map[string]string{"fallback/data/dockmesh.db": "db"},
			env:     "DOCKMESH_DATA_DIR='/etc'\n",
			present: []string{"fallback/data/dockmesh.db"},
			out:     []string{"keep    /etc: is a system directory"},
			notOut:  []string{"[ok] removed"},
		},
		{
			name:   "data dir below a system dir",
			env:    "DOCKMESH_DATA_DIR=/usr/lib/dockmesh\n",
			out:    []string{"keep    /usr/lib/dockmesh: lies in a system directory"},
			notOut: []string{"[ok] removed"},
		},
		{
			name:   "relative data dir",
			env:    "DOCKMESH_DATA_DIR=dm-root\n",
			out:    []string{"keep    dm-root: not an absolute path"},
			notOut: []string{"[ok] removed"},
		},
		{
			name:    "data dir with ..",
			env:     "DOCKMESH_DATA_DIR={root}/x/../dm-root\n",
			files:   dockmeshFiles,
			present: []string{"dm-root/data/dockmesh.db"},
			out:     []string{"keep    {root}/x/../dm-root: not a plain path"},
			notOut:  []string{"[ok] removed"},
		},
		{
			name:    "symlink on the way to the data dir",
			files:   map[string]string{"real/dm-root/data/dockmesh.db": "db", "link": "->{root}/real"},
			env:     "DOCKMESH_DATA_DIR={root}/link/dm-root\n",
			present: []string{"real/dm-root/data/dockmesh.db"},
			out:     []string{"keep    {root}/link/dm-root: {root}/link is a symbolic link"},
			notOut:  []string{"[ok] removed"},
		},
		{
			name:    "mount point",
			files:   dockmeshFiles,
			env:     "DOCKMESH_DATA_DIR={root}/dm-root\n",
			mount:   "dm-root",
			gone:    []string{"dm-root/data", "dm-root/stacks", "dm-root/update"},
			present: []string{"dm-root"},
			out:     []string{"keep    {root}/dm-root: mount point, only its content was removed"},
		},
		{
			name:    "dry run",
			files:   with(map[string]string{"dm-root/stacks/app/data/db.sqlite": "x"}),
			env:     "DOCKMESH_DATA_DIR={root}/dm-root\n",
			dryRun:  true,
			present: []string{"dm-root/data/dockmesh.db", "dm-root/stacks/web/compose.yaml", "dm-root/update/result.json", "dm-root/stacks/app/data/db.sqlite"},
			out: []string{
				"[dry-run] rm -f -- {root}/dm-root/data/dockmesh.db",
				"[dry-run] rm -rf --one-file-system -- {root}/dm-root/stacks/web",
				"keep    {root}/dm-root/stacks/app: contains files not written by Dockmesh (e.g. data/db.sqlite)",
			},
			notOut: []string{"[ok] removed"},
		},
		{
			name:    "agent dirs",
			files:   with(map[string]string{"agent/agent.crt": "c", "agent/stacks/x/compose.yaml": "x"}),
			env:     "DOCKMESH_DATA_DIR={root}/dm-root\nDOCKMESH_STACKS_ROOT={root}/agent/stacks\n",
			agents:  "{root}/agent",
			gone:    []string{"dm-root/data"},
			present: []string{"agent/agent.crt", "agent/stacks/x/compose.yaml", "dm-root/stacks/web/compose.yaml"},
			out:     []string{"keep    {root}/agent/stacks: used by the agent on this host"},
		},
		{
			name:    "agent files in the data dir",
			files:   with(map[string]string{"dm-root/agent.crt": "c"}),
			env:     "DOCKMESH_DATA_DIR={root}/dm-root\n",
			present: []string{"dm-root/data/dockmesh.db", "dm-root/agent.crt"},
			out:     []string{"keep    {root}/dm-root: holds agent files; used by the agent on this host"},
		},
		{
			name:  "data dir inferred from the database path",
			files: dockmeshFiles,
			env:   "DOCKMESH_DB_PATH={root}/dm-root/data/dockmesh.db\n",
			gone:  []string{"dm-root"},
		},
		{
			name: "stacks and secrets elsewhere",
			files: with(map[string]string{
				"other-stacks/a/compose.yaml": "x",
				"other-stacks/b/compose.yaml": "x",
				"other-stacks/b/notes.txt":    "mine",
				"keys/my.key":                 "k",
			}),
			env:     "DOCKMESH_DATA_DIR={root}/dm-root\nDOCKMESH_STACKS_ROOT={root}/other-stacks\nDOCKMESH_SECRETS_KEY_PATH={root}/keys/my.key\n",
			gone:    []string{"other-stacks/a", "keys/my.key", "dm-root/data"},
			present: []string{"other-stacks/b/notes.txt", "dm-root/stacks/web/compose.yaml"},
			out: []string{
				"keep    {root}/other-stacks/b: contains files not written by Dockmesh (e.g. notes.txt)",
				"keep    {root}/dm-root/stacks: not written by Dockmesh",
			},
		},
		{
			name: "archives and set-aside files",
			files: with(map[string]string{
				"dm-root/data/backups/system-1.tar.gz": "b",
				"dm-root/data.failed-20261101T101010Z": "/",
				"dm-root/lost+found":                   "/",
			}),
			env:     "DOCKMESH_DATA_DIR={root}/dm-root\n",
			gone:    []string{"dm-root/data/dockmesh.db", "dm-root/data/secrets.age-key", "dm-root/stacks"},
			present: []string{"dm-root/data/backups/system-1.tar.gz", "dm-root/lost+found"},
			out: []string{
				"keep    {root}/dm-root/data/backups: backup or audit archives",
				"keep    {root}/dm-root/data.failed-20261101T101010Z: set aside by a restore or a failed setup",
				"keep    {root}/dm-root/lost+found: not written by Dockmesh",
			},
		},
		{
			name:  "fallback without a config",
			files: map[string]string{"fallback/data/dockmesh.db": "db", "fallback/dockmesh.env": "DOCKMESH_STACKS_ROOT=/etc\n", "fallback/stacks/web/compose.yaml": "x"},
			gone:  []string{"fallback"},
			out:   []string{"removed {root}/fallback\n"},
		},
		{
			name:  "empty default dir left by setup mode",
			files: map[string]string{"fallback": "/"},
			gone:  []string{"fallback"},
		},
		{
			name:    "trees owned by another account",
			files:   dockmeshFiles,
			env:     "DOCKMESH_DATA_DIR={root}/dm-root\n",
			owners:  "nobody",
			gone:    []string{"dm-root/data/dockmesh.db"},
			present: []string{"dm-root/update/result.json", "dm-root/stacks/web/compose.yaml", "dm-root/data/grype-db/5/vulnerability.db"},
			out:     []string{"keep    {root}/dm-root/update: not owned by the dockmesh account\n", "keep    {root}/dm-root/stacks: not owned by the dockmesh account\n"},
		},
		{
			name: "empty bind source of a container",
			files: with(map[string]string{
				"dm-root/stacks/app/compose.yaml": "services: {}",
				"dm-root/stacks/app/uploads":      "/",
			}),
			env:     "DOCKMESH_DATA_DIR={root}/dm-root\n",
			inUse:   "{root}/dm-root/stacks/app/uploads",
			present: []string{"dm-root/stacks/app/compose.yaml", "dm-root/stacks/app/uploads"},
			out:     []string{"keep    {root}/dm-root/stacks/app: a container mounts uploads from it\n"},
		},
		{
			name:       "docker not reachable",
			files:      dockmeshFiles,
			env:        "DOCKMESH_DATA_DIR={root}/dm-root\n",
			dockerDown: true,
			gone:       []string{"dm-root/data/dockmesh.db"},
			present:    []string{"dm-root/stacks/web/compose.yaml"},
			out:        []string{"keep    {root}/dm-root/stacks: Docker could not say which directories containers mount\n"},
		},
		{
			name: "control characters in a kept name",
			files: with(map[string]string{
				"dm-root/stacks/app/compose.yaml": "services: {}",
				"dm-root/stacks/app/\x1b[2Jx":     "x",
			}),
			env:    "DOCKMESH_DATA_DIR={root}/dm-root\n",
			notOut: []string{"\x1b"},
		},
		{
			name: "shared system directory as the data root",
			files: map[string]string{
				"shared/dockmesh.env":                   "DOCKMESH_DATA_DIR=/data\n",
				"shared/data/dockmesh.db":               "db",
				"shared/data/dockmesh.db-wal":           "wal",
				"shared/data/secrets.env":               "jwt",
				"shared/data/secrets.age-key":           "key",
				"shared/data/secrets.age-key.history":   "old keys",
				"shared/data/audit-genesis.sha256":      "g",
				"shared/data/agents-ca.key":             "k",
				"shared/data/backups/system-1.tar.gz":   "b",
				"shared/stacks/web/compose.yaml":        "services: {}",
				"shared/stacks/app/compose.yaml":        "services: {}",
				"shared/stacks/app/uploads":             "/",
				"shared/update/result.json":             "{}",
				"shared/other-app/state.json":           "not ours",
				"shared/data.pre-restore-20261101/x.db": "aside",
			},
			env:    "DOCKMESH_DATA_DIR={root}/shared\n",
			shared: "shared",
			inUse:  "{root}/shared/stacks/app/uploads",
			gone: []string{"shared/dockmesh.env", "shared/data/dockmesh.db", "shared/data/dockmesh.db-wal", "shared/data/secrets.env",
				"shared/data/secrets.age-key", "shared/data/secrets.age-key.history", "shared/data/audit-genesis.sha256",
				"shared/data/agents-ca.key", "shared/stacks/web", "shared/update"},
			present: []string{"shared/data/backups/system-1.tar.gz", "shared/stacks/app/compose.yaml", "shared/other-app/state.json",
				"shared/data.pre-restore-20261101/x.db", "shared/.dockmesh-purged"},
			out: []string{
				"keep    {root}/shared: is a system directory; only Dockmesh's files in it were removed\n",
				"keep    {root}/shared/other-app: not written by Dockmesh\n",
				"wrote {root}/shared/.dockmesh-purged",
				"     {root}/shared/data/backups\n",
				"     {root}/shared/stacks/app\n",
				"     {root}/shared\n",
			},
			notOut: []string{"The database, stacks", "is a system directory; not purged"},
		},
		{
			name: "shared system directory with trees of another account",
			files: map[string]string{
				"shared/data/dockmesh.db":        "db",
				"shared/data/secrets.age-key":    "key",
				"shared/stacks/web/compose.yaml": "services: {}",
				"shared/update/result.json":      "{}",
			},
			env:     "DOCKMESH_DATA_DIR={root}/shared\n",
			shared:  "shared",
			owners:  "nobody",
			present: []string{"shared/data/dockmesh.db", "shared/data/secrets.age-key", "shared/stacks/web/compose.yaml", "shared/update/result.json"},
			out: []string{
				"keep    {root}/shared/data: not owned by the dockmesh account\n",
				"keep    {root}/shared/update: not owned by the dockmesh account\n",
			},
			notOut: []string{"[ok] removed"},
		},
		{
			name:    "empty shared system directory",
			files:   map[string]string{"shared": "/"},
			env:     "DOCKMESH_DATA_DIR={root}/shared\n",
			shared:  "shared",
			present: []string{"shared"},
			notOut:  []string{"keep ", "[ok] removed", "wrote "},
		},
		{
			name: "symlink where the marker goes",
			files: with(map[string]string{
				"dm-root/stacks/app/compose.yaml":   "services: {}",
				"dm-root/stacks/app/data/db.sqlite": "x",
				"dm-root/.dockmesh-purged":          "->{root}/outside/planted",
				"outside":                           "/",
			}),
			env:     "DOCKMESH_DATA_DIR={root}/dm-root\n",
			gone:    []string{"outside/planted"},
			present: []string{"dm-root/.dockmesh-purged"},
			out:     []string{"removed {root}/dm-root/.dockmesh-purged", "wrote {root}/dm-root/.dockmesh-purged"},
			notOut:  []string{"keep    {root}/dm-root/.dockmesh-purged"},
		},
		{
			name:    "agent files at the root are checked first",
			files:   with(map[string]string{"dm-root/enroll-token": "t"}),
			env:     "DOCKMESH_DATA_DIR={root}/dm-root\n",
			present: []string{"dm-root/data/dockmesh.db", "dm-root/stacks/web/compose.yaml"},
			out:     []string{"keep    {root}/dm-root: holds agent files; used by the agent on this host\n"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root, err := filepath.EvalSymlinks(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			root = filepath.ToSlash(root)
			sub := func(s string) string { return strings.ReplaceAll(s, "{root}", root) }
			files := map[string]string{}
			for k, v := range tt.files {
				files[k] = sub(v)
			}
			buildTree(t, root, files)
			env := filepath.Join(root, "etc-dockmesh.env")
			if tt.env != "" {
				mustWrite(t, env, sub(tt.env))
			}
			call := `resolve_purge_paths "$1" "$2"
IFS=: read -ra AGENT_DIRS <<<"${TEST_AGENTS:-}"
IFS=: read -ra IN_USE <<<"${TEST_IN_USE:-}"
IN_USE_KNOWN="${TEST_IN_USE_KNOWN:-1}"
SERVICE_USERS="${TEST_SERVICE_USERS:-$(id -un)}"
SHARED_ROOTS="${TEST_SHARED_ROOTS:-$SHARED_ROOTS}"
purge_root || true
purge_summary`
			cmd := exec.Command("bash", "-c", purgeScript(t, call), "uninstall-test", env, root+"/fallback")
			cmd.Env = append(os.Environ(), "NO_COLOR=1", "TEST_AGENTS="+sub(tt.agents), "TEST_IN_USE="+sub(tt.inUse))
			if tt.dockerDown {
				cmd.Env = append(cmd.Env, "TEST_IN_USE_KNOWN=0")
			}
			if tt.owners != "" {
				cmd.Env = append(cmd.Env, "TEST_SERVICE_USERS="+tt.owners)
			}
			if tt.shared != "" {
				cmd.Env = append(cmd.Env, "TEST_SHARED_ROOTS="+root+"/"+tt.shared)
			}
			if tt.dryRun {
				cmd.Env = append(cmd.Env, "DRYRUN=1")
			}
			if tt.mount != "" {
				cmd.Env = append(cmd.Env, "TEST_MOUNT="+root+"/"+tt.mount)
			}
			b, err := cmd.CombinedOutput()
			out := string(b)
			if err != nil {
				t.Fatalf("purge: %v\n%s", err, out)
			}
			for _, rel := range tt.gone {
				if _, err := os.Lstat(filepath.Join(root, rel)); err == nil {
					t.Errorf("%s still exists\n%s", rel, out)
				}
			}
			for _, rel := range tt.present {
				if _, err := os.Lstat(filepath.Join(root, rel)); err != nil {
					t.Errorf("%s was removed: %v\n%s", rel, err, out)
				}
			}
			for _, w := range tt.out {
				if !strings.Contains(out, sub(w)) {
					t.Errorf("output lacks %q\n%s", sub(w), out)
				}
			}
			for _, w := range tt.notOut {
				if strings.Contains(out, sub(w)) {
					t.Errorf("output has %q\n%s", sub(w), out)
				}
			}
			if fi, err := os.Lstat(filepath.Join(root, "dm-root/.dockmesh-purged")); err == nil && !fi.Mode().IsRegular() {
				t.Errorf("the marker is not a regular file: %v", fi.Mode())
			}
			if tt.dryRun {
				for rel := range tt.files {
					if _, err := os.Lstat(filepath.Join(root, rel)); err != nil {
						t.Errorf("dry run removed %s", rel)
					}
				}
			}
		})
	}
}

func TestUninstallLeavesAgentAlone(t *testing.T) {
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash not available")
	}
	dir := t.TempDir()
	bin := filepath.Join(dir, "bin")
	buildTree(t, bin, map[string]string{"dockmesh": "s", "dockmesh.bak": "s", "dmctl": "s", "dockmesh-agent": "a", "grype": "g"})
	cmd := exec.Command("bash", "-c", purgeScript(t, `remove_binaries "$1"`), "uninstall-test", filepath.ToSlash(bin))
	cmd.Env = append(os.Environ(), "NO_COLOR=1")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("remove_binaries: %v\n%s", err, out)
	}
	for name, want := range map[string]bool{"dockmesh": false, "dockmesh.bak": false, "dmctl": false, "dockmesh-agent": true, "grype": true} {
		_, err := os.Lstat(filepath.Join(bin, name))
		if got := err == nil; got != want {
			t.Errorf("%s present = %v, want %v", name, got, want)
		}
	}

	agentFiles := []string{filepath.Join(dir, "dockmesh-agent.service"), filepath.Join(dir, "dockmesh-agent"), filepath.Join(dir, "etc-dockmesh-agent")}
	tests := []struct {
		name    string
		present int
		want    bool
	}{
		{"none", -1, false},
		{"unit", 0, true},
		{"binary", 1, true},
		{"config dir", 2, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			for i, p := range agentFiles {
				_ = os.RemoveAll(p)
				if i == tt.present {
					mustWrite(t, p, "x")
				}
			}
			args := append([]string{"-c", purgeScript(t, `agent_installed "$@"`), "uninstall-test"}, agentFiles...)
			err := exec.Command("bash", args...).Run()
			if got := err == nil; got != tt.want {
				t.Errorf("agent_installed = %v, want %v", got, tt.want)
			}
		})
	}

	b, err := os.ReadFile("uninstall.sh")
	if err != nil {
		t.Fatal(err)
	}
	s := string(b)
	for _, want := range []string{
		`agent_installed /etc/systemd/system/dockmesh-agent.service /usr/local/bin/dockmesh-agent /etc/dockmesh-agent`,
		`info "An agent is installed on this host. It is not removed. Remove it with: sudo dockmesh-agent uninstall"`,
	} {
		if !strings.Contains(s, want) {
			t.Errorf("uninstall.sh lacks %s", want)
		}
	}
	if strings.Contains(s, "dockmesh-agent\"\n") || regexp.MustCompile(`rm [^\n]*dockmesh-agent`).MatchString(s) {
		t.Error("uninstall.sh removes an agent file")
	}
}

func TestUninstallReadsContainerMounts(t *testing.T) {
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash not available")
	}
	bin := t.TempDir()
	fake := "#!/bin/sh\ncase \"$1\" in\n  ps) printf 'aaa\\nbbb\\n' ;;\n  inspect) printf '\\n/srv/dm/stacks/app/uploads\\n\\n' ;;\nesac\n"
	if err := os.WriteFile(filepath.Join(bin, "docker"), []byte(fake), 0o755); err != nil {
		t.Fatal(err)
	}
	call := `read_container_mounts
echo "known=$IN_USE_KNOWN mounts=${IN_USE[*]}"`
	cmd := exec.Command("bash", "-c", purgeScript(t, call), "uninstall-test")
	cmd.Env = append(os.Environ(), "NO_COLOR=1", "PATH="+bin+":"+os.Getenv("PATH"))
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("read_container_mounts failed under set -e: %v\n%s", err, out)
	}
	if got := strings.TrimSpace(string(out)); got != "known=1 mounts=/srv/dm/stacks/app/uploads" {
		t.Fatalf("got %q", got)
	}
}

// A reinstall after a purge decides on what the purge kept: setup mode
// unless a database or key of the old install is left.
func TestPurgeThenSetupDecision(t *testing.T) {
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash not available")
	}
	install := func(dataRoot string) map[string]string {
		return map[string]string{
			dataRoot + "/data/dockmesh.db":               "db",
			dataRoot + "/data/dockmesh.db-wal":           "wal",
			dataRoot + "/data/secrets.env":               "jwt",
			dataRoot + "/data/secrets.age-key":           "key",
			dataRoot + "/data/secrets.age-key.history":   "old keys",
			dataRoot + "/data/audit-genesis.sha256":      "g",
			dataRoot + "/data/agents-ca.key":             "k",
			dataRoot + "/data/agents-ca.crt":             "c",
			dataRoot + "/data/backups/system-1.tar.gz":   "b",
			dataRoot + "/data/.pre-restore/dockmesh.db":  "aside",
			dataRoot + "/stacks/web/compose.yaml":        "services: {}",
			dataRoot + "/stacks/app/compose.yaml":        "services: {}",
			dataRoot + "/stacks/app/uploads/a.png":       "x",
			dataRoot + "/stacks.pre-restore-20261101/x/": "/",
		}
	}
	tests := []struct {
		name     string
		dataRoot string
		shared   bool
		purge    bool
		owners   string
		// dropMarker removes the purge's marker again.
		dropMarker bool
		want       setup.Mode
	}{
		{name: "default layout kept", dataRoot: "var/lib/dockmesh", want: setup.SetUp},
		{name: "default layout purged", dataRoot: "var/lib/dockmesh", purge: true, want: setup.NeverSetUp},
		{name: "kept stacks without the marker", dataRoot: "var/lib/dockmesh", purge: true, dropMarker: true, want: setup.SetUp},
		{name: "data layout kept", dataRoot: "data", shared: true, want: setup.SetUp},
		{name: "data layout purged", dataRoot: "data", shared: true, purge: true, want: setup.NeverSetUp},
		{name: "database kept", dataRoot: "data", shared: true, purge: true, owners: "nobody", want: setup.SetUp},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			host, err := filepath.EvalSymlinks(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			host = filepath.ToSlash(host)
			buildTree(t, host, install(tt.dataRoot))
			root := host + "/" + tt.dataRoot
			if tt.purge {
				env := filepath.Join(t.TempDir(), "dockmesh.env")
				mustWrite(t, env, "DOCKMESH_DATA_DIR="+root+"\n")
				call := `resolve_purge_paths "$1" ""
IN_USE=("$2/stacks/app/uploads")
IN_USE_KNOWN=1
SERVICE_USERS="${TEST_SERVICE_USERS:-$(id -un)}"
SHARED_ROOTS="${TEST_SHARED_ROOTS:-}"
purge_root || true`
				cmd := exec.Command("bash", "-c", purgeScript(t, call), "uninstall-test", env, root)
				cmd.Env = append(os.Environ(), "NO_COLOR=1")
				if tt.shared {
					cmd.Env = append(cmd.Env, "TEST_SHARED_ROOTS="+root)
				}
				if tt.owners != "" {
					cmd.Env = append(cmd.Env, "TEST_SERVICE_USERS="+tt.owners)
				}
				out, err := cmd.CombinedOutput()
				if err != nil {
					t.Fatalf("purge: %v\n%s", err, out)
				}
				for _, rel := range []string{"data/dockmesh.db", "data/secrets.age-key", "data/secrets.age-key.history"} {
					if _, err := os.Lstat(filepath.Join(root, rel)); (err == nil) != (tt.owners != "") {
						t.Fatalf("%s present = %v\n%s", rel, err == nil, out)
					}
				}
				if tt.dropMarker {
					if err := os.Remove(filepath.Join(root, setup.PurgedMarker)); err != nil {
						t.Fatal(err)
					}
				}
			}
			// The new install runs setup mode on the default data dir, with
			// every absolute path below host.
			below := func(p string) string { return filepath.Join(host, p) }
			facts := setup.Probe{
				Paths:   config.ResolvePaths(func(string) string { return "" }),
				Lstat:   func(p string) (fs.FileInfo, error) { return os.Lstat(below(p)) },
				Stat:    func(p string) (fs.FileInfo, error) { return os.Stat(below(p)) },
				ReadDir: func(p string) ([]fs.DirEntry, error) { return os.ReadDir(below(p)) },
			}.Scan()
			if !facts.DB && !facts.ForeignDB {
				facts.Users = 0
			}
			if got := setup.Decide(facts); got != tt.want {
				t.Fatalf("mode = %v, want %v (facts %+v)", got, tt.want, facts)
			}
		})
	}
}

// Without --purge the data dir and config stay, and so does the account
// that owns them.
func TestUninstallOwnsKeptInstall(t *testing.T) {
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash not available")
	}
	if _, err := exec.Command("stat", "-c", "%U", ".").Output(); err != nil {
		t.Skip("GNU stat not available")
	}
	u, err := exec.Command("id", "-un").Output()
	if err != nil {
		t.Fatal(err)
	}
	g, err := exec.Command("id", "-gn").Output()
	if err != nil {
		t.Fatal(err)
	}
	user, group := strings.TrimSpace(string(u)), strings.TrimSpace(string(g))
	dir := filepath.ToSlash(t.TempDir())
	tests := []struct {
		name    string
		account string
		data    string
		config  string
		want    bool
	}{
		{name: "owns the data dir", account: user, data: dir, config: dir + "/missing", want: true},
		{name: "group of the config dir", account: group, data: dir + "/missing", config: dir, want: true},
		{name: "another account", account: "dockmesh-test-nobody", data: dir, config: dir},
		{name: "nothing kept", account: user, data: dir + "/missing", config: dir + "/missing"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cmd := exec.Command("bash", "-c", purgeScript(t, `owns_kept_install "$@"`), "uninstall-test", tt.account, tt.data, tt.config)
			if got := cmd.Run() == nil; got != tt.want {
				t.Errorf("owns_kept_install = %v, want %v", got, tt.want)
			}
		})
	}
}

// Root's trees in a shared root such as /opt belong to other software,
// also where root otherwise counts as the service account (macOS).
func TestUninstallSharedRootIgnoresRootOwner(t *testing.T) {
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash not available")
	}
	if fi, err := os.Stat("/"); err != nil || !fi.IsDir() {
		t.Skip("no root directory")
	}
	if out, err := exec.Command("stat", "-c", "%U", "/").Output(); err != nil || strings.TrimSpace(string(out)) != "root" {
		t.Skip("/ is not owned by root")
	}
	tests := []struct {
		name   string
		shared string
		want   bool
	}{
		{name: "own data root", shared: "0", want: true},
		{name: "shared root", shared: "1"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			call := `SERVICE_USERS="dockmesh root"
PURGE_SHARED="$1"
owner_ok /`
			err := exec.Command("bash", "-c", purgeScript(t, call), "uninstall-test", tt.shared).Run()
			if got := err == nil; got != tt.want {
				t.Errorf("owner_ok / = %v, want %v", got, tt.want)
			}
		})
	}
}
