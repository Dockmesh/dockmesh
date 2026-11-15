package scripts

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestInstallDetectInstall(t *testing.T) {
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash not available")
	}
	helpers := envHelpers(t)

	tests := []struct {
		name    string
		files   []string // created below the test root
		env     map[string]string
		staged  string // passed as STAGED, relative to the root
		unitEnv string // passed as UNIT_ENV, relative to the root
		loaded  bool   // systemd reports the unit loaded
		want    string // "" for a new install, else FOUND_* that is set
	}{
		{name: "nothing there", want: ""},
		{name: "binary alone", files: []string{"bin/dockmesh"}, want: "binary"},
		{name: "staged binary does not count", files: []string{"bin/dockmesh"}, staged: "bin/dockmesh", want: ""},
		{name: "staged binary with a unit file", files: []string{"bin/dockmesh", "units/dockmesh.service"}, staged: "bin/dockmesh", want: "unit"},
		{name: "staged binary with a database", files: []string{"bin/dockmesh", "lib/data/dockmesh.db"}, staged: "bin/dockmesh", want: "db"},
		{name: "loaded unit alone", loaded: true, want: "unit"},
		{name: "unit file alone", files: []string{"units/dockmesh.service"}, want: "unit"},
		{name: "config in /etc alone", files: []string{"etc/dockmesh.env"}, want: "config"},
		{name: "legacy config alone", files: []string{"legacy/dockmesh.env"}, want: "config"},
		{name: "config in DOCKMESH_DATA_DIR", files: []string{"custom/dockmesh.env"}, env: map[string]string{"DOCKMESH_DATA_DIR": "custom"}, want: "config"},
		{name: "config named by the unit", files: []string{"unitdir/dockmesh.env"}, unitEnv: "unitdir/dockmesh.env", want: "config"},
		{name: "database at a default place", files: []string{"lib/data/dockmesh.db"}, want: "db"},
		{name: "database at DOCKMESH_DB_PATH", files: []string{"elsewhere/x.db"}, env: map[string]string{"DOCKMESH_DB_PATH": "elsewhere/x.db"}, want: "db"},
		{name: "database next to the unit's config", files: []string{"unitdir/data/dockmesh.db"}, unitEnv: "unitdir/dockmesh.env", want: "db"},
		{name: "database in DOCKMESH_DATA_DIR", files: []string{"custom/data/dockmesh.db"}, env: map[string]string{"DOCKMESH_DATA_DIR": "custom"}, want: "db"},
		{name: "dangling symlink counts", files: []string{"@etc/dockmesh.env"}, want: "config"},
		{name: "setup state alone is a new install", files: []string{"lib/.setup/pki/ui-server.crt"}, want: ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := t.TempDir()
			abs := func(p string) string {
				if p == "" {
					return ""
				}
				return filepath.ToSlash(filepath.Join(root, p))
			}
			for _, f := range tt.files {
				link := strings.HasPrefix(f, "@")
				p := filepath.Join(root, strings.TrimPrefix(f, "@"))
				if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
					t.Fatal(err)
				}
				if link {
					if err := os.Symlink(filepath.Join(root, "gone"), p); err != nil {
						t.Skip("no symlinks:", err)
					}
					continue
				}
				mustWrite(t, p, "")
			}
			loaded := "0"
			if tt.loaded {
				loaded = "1"
			}
			script := "set -euo pipefail\nUSE_SUDO=\n" + helpers + `
ETC_ENV_FILE="$R/etc/dockmesh.env"
ENV_FILE_CANDIDATES=("$R/legacy/dockmesh.env")
BINARY_CANDIDATES=("$R/bin/dockmesh")
UNIT_FILES=("$R/units/dockmesh.service")
DB_CANDIDATES=("$R/lib/data/dockmesh.db")
HAS_SYSTEMD_UNIT=$LOADED
if detect_install "$STAGED" "$UNIT_ENV"; then
  [ -n "$FOUND_BINARY" ] && echo binary
  [ -n "$FOUND_UNIT" ] && echo unit
  [ -n "$FOUND_CONFIG" ] && echo config
  [ -n "$FOUND_DB" ] && echo db
fi
true
`
			cmd := exec.Command("bash", "-c", script)
			cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "R=" + filepath.ToSlash(root), "LOADED=" + loaded,
				"STAGED=" + abs(tt.staged), "UNIT_ENV=" + abs(tt.unitEnv)}
			for k, v := range tt.env {
				if !filepath.IsAbs(v) {
					v = abs(v)
				}
				cmd.Env = append(cmd.Env, k+"="+v)
			}
			out, err := cmd.CombinedOutput()
			if err != nil {
				t.Fatalf("%v\n%s", err, out)
			}
			got := strings.TrimSpace(string(out))
			if got != tt.want {
				t.Errorf("found %q, want %q", got, tt.want)
			}
		})
	}
}

func TestInstallPorcelainHelpers(t *testing.T) {
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash not available")
	}
	helpers := envHelpers(t)
	fp := "3F:A2:00:11:22:33:44:55:66:77:88:99:AA:BB:CC:DD:EE:FF:01:23:45:67:89:AB:CD:EF:10:32:54:76:98:9C"
	out := "code=K7QD-3MXA-9PRT\nurl=https://10.0.0.5:8080/setup#K7QD-3MXA-9PRT\nfingerprint=" + fp + "\n"
	tests := []struct {
		call string
		want string
	}{
		{`kv code "$OUT"`, "K7QD-3MXA-9PRT"},
		{`kv url "$OUT"`, "https://10.0.0.5:8080/setup#K7QD-3MXA-9PRT"},
		{`kv missing "$OUT"`, ""},
		{`fingerprint_lines "$FP"`, fp[:47] + "\n" + fp[48:]},
	}
	for _, tt := range tests {
		cmd := exec.Command("bash", "-c", "set -euo pipefail\n"+helpers+"\n"+tt.call)
		cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "OUT=" + out, "FP=" + fp}
		got, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("%s: %v\n%s", tt.call, err, got)
		}
		if strings.TrimRight(string(got), "\n") != tt.want {
			t.Errorf("%s = %q, want %q", tt.call, got, tt.want)
		}
	}
}

func TestInstallChownTargetAllowed(t *testing.T) {
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash not available")
	}
	helpers := envHelpers(t)
	tests := []struct {
		dir string
		ok  bool
	}{
		{"/var/lib/dockmesh", true},
		{"/data", true},
		{"/srv/dm", true},
		{"/etc/dockmesh", false},
		{"/etc", false},
		{"/etc/dockmesh/", false},
		{"/var/lib", false},
		{"", false},
	}
	for _, tt := range tests {
		_, _, err := runHelper(t, helpers, nil, "chown_target_allowed", tt.dir)
		if (err == nil) != tt.ok {
			t.Errorf("chown_target_allowed(%q) = %v, want ok=%v", tt.dir, err, tt.ok)
		}
	}
}

func TestInstallPrintable(t *testing.T) {
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash not available")
	}
	out, _, err := runHelper(t, envHelpers(t), nil, "printable", "a\x1b]0;x\x07b\tc")
	if err != nil {
		t.Fatal(err)
	}
	if out != "a]0;xbc" {
		t.Errorf("printable = %q", out)
	}
}
