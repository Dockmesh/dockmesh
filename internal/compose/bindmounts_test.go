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

func TestRebaseSkipsStackRootBind(t *testing.T) {
	staging := filepath.FromSlash("/tmp/stage-x")
	proj := &composetypes.Project{Services: composetypes.Services{
		"a": {Name: "a", Volumes: []composetypes.ServiceVolumeConfig{
			{Type: composetypes.VolumeTypeBind, Source: staging, Target: "/app"},
		}},
	}}
	RebaseBindSources(proj, staging, filepath.FromSlash("/stacks/x"))
	if got := proj.Services["a"].Volumes[0].Source; got != staging {
		t.Errorf("stack root bind rebased to %q; the container could rewrite compose.yaml", got)
	}
}
