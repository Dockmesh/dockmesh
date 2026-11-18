package compose

import (
	"archive/tar"
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	composetypes "github.com/compose-spec/compose-go/v2/types"
)

// A relative bind mount must end up in the persistent stack directory, not
// in the staging copy compose was loaded from (which is deleted after the
// deploy and left containers writing into a vanished directory).
func TestRebaseBindSourcesWithRealLoader(t *testing.T) {
	staging := t.TempDir()
	stackDir := t.TempDir()
	composeYAML := `services:
  site:
    image: nginx
    volumes:
      - ./html:/usr/share/nginx/html
      - ./conf/nginx.conf:/etc/nginx/nginx.conf:ro
      - /srv/shared:/shared
      - data:/data
volumes:
  data: {}
`
	if err := os.WriteFile(filepath.Join(staging, "compose.yaml"), []byte(composeYAML), 0o600); err != nil {
		t.Fatal(err)
	}
	proj, err := LoadProject(context.Background(), staging, "fx-bindmount", "")
	if err != nil {
		t.Fatal(err)
	}
	RebaseBindSources(proj, staging, stackDir)

	got := map[string]string{}
	for _, v := range proj.Services["site"].Volumes {
		got[v.Target] = v.Source
	}
	want := map[string]string{
		"/usr/share/nginx/html": filepath.Join(stackDir, "html"),
		"/etc/nginx/nginx.conf": filepath.Join(stackDir, "conf", "nginx.conf"),
		"/shared":               "/srv/shared",
		"/data":                 "data",
	}
	for target, src := range want {
		if got[target] != src {
			t.Errorf("%s: source %q, want %q", target, got[target], src)
		}
	}
}

func TestRebaseLeavesSiblingDirsAlone(t *testing.T) {
	proj := &composetypes.Project{Services: composetypes.Services{
		"a": {Name: "a", Volumes: []composetypes.ServiceVolumeConfig{
			{Type: composetypes.VolumeTypeBind, Source: filepath.FromSlash("/tmp/stage-x-other/data"), Target: "/d"},
		}},
	}}
	RebaseBindSources(proj, filepath.FromSlash("/tmp/stage-x"), filepath.FromSlash("/stacks/x"))
	if got := proj.Services["a"].Volumes[0].Source; got != filepath.FromSlash("/tmp/stage-x-other/data") {
		t.Errorf("prefix-sharing sibling rewritten to %q", got)
	}
}

func TestIsStagingPath(t *testing.T) {
	tests := []struct {
		src  string
		want bool
	}{
		{"/tmp/dockmesh-deploy-web-123456/html", true},
		{"/tmp/systemd-private-x/tmp/dockmesh-deploy-web-1/data", true},
		{"/var/lib/dockmesh/staging/web-998/html", true},
		{"/opt/dm/staging/web-998", true},
		{"/srv/staging/data", false},
		{"/var/lib/dockmesh/stacks/web/html", false},
		{"/srv/data", false},
	}
	for _, tt := range tests {
		if got := IsStagingPath(tt.src); got != tt.want {
			t.Errorf("IsStagingPath(%q) = %v, want %v", tt.src, got, tt.want)
		}
	}
}

func TestRewriteBindTar(t *testing.T) {
	var in bytes.Buffer
	tw := tar.NewWriter(&in)
	add := func(name string, typ byte, body string) {
		hdr := &tar.Header{Name: name, Typeflag: typ, Mode: 0o4755, Size: int64(len(body)), Uid: 999, Gid: 999}
		if typ == tar.TypeSymlink {
			hdr.Linkname, hdr.Size = "/etc/passwd", 0
		}
		if err := tw.WriteHeader(hdr); err != nil {
			t.Fatal(err)
		}
		if typ == tar.TypeReg {
			if _, err := tw.Write([]byte(body)); err != nil {
				t.Fatal(err)
			}
		}
	}
	add("html/", tar.TypeDir, "")
	add("html/index.html", tar.TypeReg, "hello")
	add("html/sub/a.txt", tar.TypeReg, "a")
	add("html/link", tar.TypeSymlink, "")
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}

	var out bytes.Buffer
	n, err := rewriteBindTar(tar.NewReader(&in), tar.NewWriter(&out), "html")
	if err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Errorf("files = %d, want 2", n)
	}
	tr := tar.NewReader(&out)
	var names []string
	for {
		hdr, err := tr.Next()
		if err != nil {
			break
		}
		names = append(names, hdr.Name)
		if hdr.Uid != 999 || hdr.Mode&0o7000 != 0 {
			t.Errorf("%s: uid %d mode %o — owner must be kept, setuid bits dropped", hdr.Name, hdr.Uid, hdr.Mode)
		}
	}
	if got := strings.Join(names, ","); got != "index.html,sub/a.txt" {
		t.Errorf("entries = %s", got)
	}
}

func TestRewriteBindTarRefusesClimbing(t *testing.T) {
	var in bytes.Buffer
	tw := tar.NewWriter(&in)
	if err := tw.WriteHeader(&tar.Header{Name: "html/../../escape", Typeflag: tar.TypeReg, Mode: 0o644}); err != nil {
		t.Fatal(err)
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := rewriteBindTar(tar.NewReader(&in), tar.NewWriter(io.Discard), "html"); err == nil {
		t.Fatal("entry climbing out of the mount accepted")
	}
}

func TestRebaseStackRootBind(t *testing.T) {
	staging := filepath.FromSlash("/tmp/stage-x")
	proj := &composetypes.Project{Services: composetypes.Services{
		"a": {Name: "a", Volumes: []composetypes.ServiceVolumeConfig{
			{Type: composetypes.VolumeTypeBind, Source: staging, Target: "/app"},
		}},
	}}
	stackDir := filepath.FromSlash("/stacks/x")
	RebaseBindSources(proj, staging, stackDir)
	if got := proj.Services["a"].Volumes[0].Source; got != stackDir {
		t.Errorf("stack root bind = %q, want %q", got, stackDir)
	}
}

func TestWritableStackDirMounts(t *testing.T) {
	dir := "/srv/stacks/web"
	tests := []struct {
		name string
		yaml string
		want []string // service: source
	}{
		{"short", "services:\n  a:\n    volumes: [\".:/app\"]\n", []string{"a: ."}},
		{"short slash", "services:\n  a:\n    volumes: [\"./:/app:rw\"]\n", []string{"a: ./"}},
		{"short read-only", "services:\n  a:\n    volumes: [\".:/app:ro\"]\n", nil},
		{"subdir", "services:\n  a:\n    volumes: [\"./data:/data\"]\n", nil},
		{"parent", "services:\n  a:\n    volumes: [\"..:/stacks\"]\n", []string{"a: .."}},
		{"via parent back in", "services:\n  a:\n    volumes: [\"../web:/x\"]\n", []string{"a: ../web"}},
		{"other stack", "services:\n  a:\n    volumes: [\"../db:/x\"]\n", nil},
		{"absolute dir", "services:\n  a:\n    volumes: [\"/srv/stacks/web:/x\"]\n", []string{"a: /srv/stacks/web"}},
		{"absolute parent", "services:\n  a:\n    volumes: [\"/srv:/x\"]\n", []string{"a: /srv"}},
		{"host root", "services:\n  a:\n    volumes: [\"/:/host\"]\n", []string{"a: /"}},
		{"unrelated absolute", "services:\n  a:\n    volumes: [\"/var/log:/x\"]\n", nil},
		{"compose file", "services:\n  a:\n    volumes: [\"./compose.yaml:/c.yaml\"]\n", []string{"a: ./compose.yaml"}},
		{"env file", "services:\n  a:\n    volumes: [\"./.env:/e\"]\n", []string{"a: ./.env"}},
		{"meta file", "services:\n  a:\n    volumes: [\"./.dockmesh.meta.json:/m\"]\n", []string{"a: ./.dockmesh.meta.json"}},
		{"override", "services:\n  a:\n    volumes: [\"./compose.prod.yaml:/m\"]\n", []string{"a: ./compose.prod.yaml"}},
		{"other file", "services:\n  a:\n    volumes: [\"./nginx.conf:/etc/nginx.conf\"]\n", nil},
		{"long", "services:\n  a:\n    volumes:\n      - {type: bind, source: ., target: /app}\n", []string{"a: ."}},
		{"long read-only", "services:\n  a:\n    volumes:\n      - {type: bind, source: ., target: /app, read_only: true}\n", nil},
		{"named volume", "services:\n  a:\n    volumes: [\"data:/data\"]\n", nil},
		{"variable", "services:\n  a:\n    volumes: [\"${DIR}:/data\"]\n", nil},
		{"two services", "services:\n  b:\n    volumes: [\"..:/s\"]\n  a:\n    volumes: [\".:/app\"]\n", []string{"a: .", "b: .."}},
		{"host bind volume", "services:\n  a:\n    volumes: [\"v:/x\"]\nvolumes:\n  v:\n    driver_opts: {type: none, o: bind, device: /srv/stacks/web}\n", []string{"a: /srv/stacks/web"}},
		{"host bind volume long", "services:\n  a:\n    volumes:\n      - {type: volume, source: v, target: /x}\nvolumes:\n  v:\n    driver_opts: {o: bind, device: /srv/stacks}\n", []string{"a: /srv/stacks"}},
		{"plain named volume", "services:\n  a:\n    volumes: [\"v:/x\"]\nvolumes:\n  v: {}\n", nil},
		{"invalid", "services: [", nil},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var got []string
			for _, m := range WritableStackDirMounts([]string{tc.yaml}, []StackDirOnHost{{Path: dir}}) {
				got = append(got, m.Service+": "+m.Source)
			}
			if strings.Join(got, ",") != strings.Join(tc.want, ",") {
				t.Errorf("got %v, want %v", got, tc.want)
			}
		})
	}
}

func TestWritableStackDirMountsResolves(t *testing.T) {
	base := t.TempDir()
	real := filepath.Join(base, "real")
	if err := os.MkdirAll(filepath.Join(real, "web", "data"), 0o755); err != nil {
		t.Fatal(err)
	}
	// The stacks root itself is reached through a symlink.
	if err := os.Symlink(real, filepath.Join(base, "stacks")); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(base, "stacks", "web")
	if err := os.Symlink(dir, filepath.Join(base, "link")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("..", filepath.Join(real, "web", "data", "up")); err != nil {
		t.Fatal(err)
	}
	resolve := func(p string) string {
		if r, err := filepath.EvalSymlinks(p); err == nil {
			return r
		}
		return p
	}
	server := []StackDirOnHost{{Path: dir, Resolve: resolve}}
	for _, src := range []string{".", "./", "..", "./compose.yaml", filepath.ToSlash(filepath.Join(base, "link")), "./data/up"} {
		y := "services:\n  a:\n    volumes: [\"" + src + ":/x\"]\n"
		if got := WritableStackDirMounts([]string{y}, server); len(got) != 1 {
			t.Errorf("%s not found under a symlinked root: %+v", src, got)
		}
	}
	y := "services:\n  a:\n    volumes: [\"./data:/x\"]\n"
	if got := WritableStackDirMounts([]string{y}, server); len(got) != 0 {
		t.Errorf("subdir reported: %+v", got)
	}
	y = "services:\n  a:\n    volumes: [\"/var/lib/dockmesh-agent/stacks/web:/x\"]\n"
	agent := append(server, StackDirOnHost{Path: "/var/lib/dockmesh-agent/stacks/web"})
	if got := WritableStackDirMounts([]string{y}, agent); len(got) != 1 {
		t.Fatalf("agent stack dir not found: %+v", got)
	}
	override := "services:\n  b:\n    volumes: [\".:/app\"]\n"
	if got := WritableStackDirMounts([]string{"services: {}\n", override}, server); len(got) != 1 || got[0].Service != "b" {
		t.Fatalf("override not checked: %+v", got)
	}
}
