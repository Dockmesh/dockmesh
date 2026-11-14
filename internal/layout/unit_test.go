package layout

import (
	"errors"
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dockmesh/dockmesh/internal/config"
)

var updateGolden = flag.Bool("update", false, "rewrite the golden unit files")

func checkGolden(t *testing.T, name string, got []byte) {
	t.Helper()
	path := filepath.Join("testdata", name)
	if *updateGolden {
		if err := os.WriteFile(path, got, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(want) {
		t.Errorf("%s differs from the golden file (go test -run %s -update):\n%s", name, t.Name(), got)
	}
}

func TestRenderServerUnitGolden(t *testing.T) {
	tests := []struct {
		golden string
		unit   ServerUnit
	}{
		{"server-default.service", ServerUnit{
			Binary:  "/usr/local/bin/dockmesh",
			EnvFile: ConfigFile,
			DataDir: "/var/lib/dockmesh",
			WritablePaths: ServerWritablePaths(config.ResolvePaths(func(string) string { return "" }),
				"/srv/backups", "/var/lib/dockmesh/data/backups"),
			SupplementaryGroups: []string{"docker"},
		}},
		{"server-custom.service", ServerUnit{
			Binary:  "/opt/dockmesh/bin/dockmesh",
			EnvFile: "/data/dockmesh.env",
			DataDir: "/data",
			WritablePaths: []string{
				"/data/stacks", "/srv/stacks/sub", "/mnt/nas backups/dockmesh", "/srv/100%", "/srv/stacks", "/srv/stacks/",
			},
			SupplementaryGroups: []string{"docker", "998", "docker"},
		}},
		{"server-no-groups.service", ServerUnit{
			Binary:  "/usr/local/bin/dockmesh",
			EnvFile: ConfigFile,
			DataDir: "/var/lib/dockmesh/",
		}},
	}
	for _, tt := range tests {
		t.Run(tt.golden, func(t *testing.T) {
			got, err := RenderServerUnit(tt.unit)
			if err != nil {
				t.Fatal(err)
			}
			checkGolden(t, tt.golden, got)
		})
	}
}

func TestRenderAgentUnitGolden(t *testing.T) {
	tests := []struct {
		golden string
		unit   AgentUnit
	}{
		{"agent-service-user.service", AgentUnit{
			Binary:              "/usr/local/bin/dockmesh-agent",
			EnvFile:             "/etc/dockmesh-agent/agent.env",
			User:                "dockmesh-agent",
			DataDir:             "/var/lib/dockmesh-agent",
			WritablePaths:       []string{"/var/lib/dockmesh-agent/stacks"},
			SupplementaryGroups: []string{"docker"},
		}},
		{"agent-root.service", AgentUnit{
			Binary:  "/usr/local/bin/dockmesh-agent",
			EnvFile: "/etc/dockmesh-agent/agent.env",
			DataDir: "/var/lib/dockmesh-agent",
		}},
	}
	for _, tt := range tests {
		t.Run(tt.golden, func(t *testing.T) {
			got, err := RenderAgentUnit(tt.unit)
			if err != nil {
				t.Fatal(err)
			}
			checkGolden(t, tt.golden, got)
		})
	}
}

// TestRenderServerUnitRefuses covers every value that could add a line,
// a word or a systemd specifier to the unit.
func TestRenderServerUnitRefuses(t *testing.T) {
	base := func() ServerUnit {
		return ServerUnit{Binary: "/usr/local/bin/dockmesh", EnvFile: ConfigFile, DataDir: "/var/lib/dockmesh"}
	}
	tests := []struct {
		name string
		edit func(*ServerUnit)
	}{
		{"newline in backup path", func(u *ServerUnit) { u.WritablePaths = []string{"/srv/b\nExecStartPre=/bin/sh -c id"} }},
		{"carriage return in backup path", func(u *ServerUnit) { u.WritablePaths = []string{"/srv/b\rx"} }},
		{"tab in data dir", func(u *ServerUnit) { u.DataDir = "/var/lib/dock\tmesh" }},
		{"NUL in path", func(u *ServerUnit) { u.WritablePaths = []string{"/srv/b\x00"} }},
		{"DEL in path", func(u *ServerUnit) { u.WritablePaths = []string{"/srv/b\x7f"} }},
		{"C1 control in path", func(u *ServerUnit) { u.WritablePaths = []string{"/srv/b\u0085x"} }},
		{"line separator in path", func(u *ServerUnit) { u.WritablePaths = []string{"/srv/b x"} }},
		{"invalid UTF-8", func(u *ServerUnit) { u.WritablePaths = []string{"/srv/\xff"} }},
		{"double quote", func(u *ServerUnit) { u.WritablePaths = []string{`/srv/a" "/etc`} }},
		{"single quote", func(u *ServerUnit) { u.WritablePaths = []string{`/srv/a'`} }},
		{"backslash continuation", func(u *ServerUnit) { u.WritablePaths = []string{`/srv/a\`} }},
		{"relative path", func(u *ServerUnit) { u.WritablePaths = []string{"srv/backups"} }},
		{"option-like value", func(u *ServerUnit) { u.WritablePaths = []string{"-/srv"} }},
		{"empty path", func(u *ServerUnit) { u.WritablePaths = []string{""} }},
		{"root directory", func(u *ServerUnit) { u.WritablePaths = []string{"/"} }},
		{"root directory after clean", func(u *ServerUnit) { u.WritablePaths = []string{"/srv/.."} }},
		{"data dir root", func(u *ServerUnit) { u.DataDir = "/" }},
		{"system tree", func(u *ServerUnit) { u.WritablePaths = []string{"/etc"} }},
		{"below a system tree", func(u *ServerUnit) { u.WritablePaths = []string{"/usr/local/bin"} }},
		{"proc", func(u *ServerUnit) { u.WritablePaths = []string{"/proc/sys"} }},
		{"parent of the data dir", func(u *ServerUnit) { u.WritablePaths = []string{"/var/lib"} }},
		{"data dir in a system tree", func(u *ServerUnit) { u.DataDir = "/etc" }},
		{"docker's data", func(u *ServerUnit) { u.WritablePaths = []string{"/var/lib/docker/volumes"} }},
		{"empty data dir", func(u *ServerUnit) { u.DataDir = "" }},
		{"overlong path", func(u *ServerUnit) { u.WritablePaths = []string{"/" + strings.Repeat("a", 4096)} }},
		{"space in env file", func(u *ServerUnit) { u.EnvFile = "/data dir/dockmesh.env" }},
		{"newline in env file", func(u *ServerUnit) { u.EnvFile = "/data/dockmesh.env\nUser=root" }},
		{"relative env file", func(u *ServerUnit) { u.EnvFile = "dockmesh.env" }},
		{"space in binary", func(u *ServerUnit) { u.Binary = "/usr/local/bin/dockmesh --insecure" }},
		{"dollar in binary", func(u *ServerUnit) { u.Binary = "/usr/local/bin/$X" }},
		{"group with space", func(u *ServerUnit) { u.SupplementaryGroups = []string{"docker root"} }},
		{"group with newline", func(u *ServerUnit) { u.SupplementaryGroups = []string{"docker\nUser=root"} }},
		{"negative gid", func(u *ServerUnit) { u.SupplementaryGroups = []string{"-1"} }},
		{"empty group", func(u *ServerUnit) { u.SupplementaryGroups = []string{""} }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			u := base()
			tt.edit(&u)
			got, err := RenderServerUnit(u)
			if !errors.Is(err, ErrBadUnitValue) {
				t.Fatalf("want ErrBadUnitValue, got %v\n%s", err, got)
			}
		})
	}
}

func TestRenderAgentUnitRefusesUser(t *testing.T) {
	_, err := RenderAgentUnit(AgentUnit{
		Binary: "/usr/local/bin/dockmesh-agent", EnvFile: "/etc/dockmesh-agent/agent.env",
		DataDir: "/var/lib/dockmesh-agent", User: "agent\nUser=root",
	})
	if !errors.Is(err, ErrBadUnitValue) {
		t.Fatalf("want ErrBadUnitValue, got %v", err)
	}
}

func TestServerWritablePathsFollowOverrides(t *testing.T) {
	env := map[string]string{
		"DOCKMESH_DATA_DIR":    "/var/lib/dockmesh",
		"DOCKMESH_STACKS_ROOT": "/srv/stacks",
		"DOCKMESH_DB_PATH":     "/mnt/db/dockmesh.db",
	}
	p := config.ResolvePaths(func(k string) string { return env[k] })
	got, err := readWritePaths(p.DataDir, ServerWritablePaths(p, "/srv/backups"))
	if err != nil {
		t.Fatal(err)
	}
	want := []string{`"/var/lib/dockmesh"`, `"-/srv/stacks"`, `"-/mnt/db"`, `"-/srv/backups"`}
	if strings.Join(got, " ") != strings.Join(want, " ") {
		t.Errorf("got %v, want %v", got, want)
	}
}
