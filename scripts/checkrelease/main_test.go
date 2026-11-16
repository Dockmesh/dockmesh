package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime/debug"
	"strings"
	"testing"

	"github.com/dockmesh/dockmesh/internal/releasesig"
)

const (
	server = "github.com/dockmesh/dockmesh/cmd/dockmesh"
	agent  = "github.com/dockmesh/dockmesh/cmd/dockmesh-agent"
	dmctl  = "github.com/dockmesh/dockmesh/cmd/dmctl"
)

func labKey(t *testing.T) string {
	t.Helper()
	src, err := os.ReadFile("../../internal/releasesig/labkeys.go")
	if err != nil {
		t.Fatal(err)
	}
	m := regexp.MustCompile(`labKey = "([^"]+)"`).FindSubmatch(src)
	if m == nil {
		t.Fatal("no labKey in internal/releasesig/labkeys.go")
	}
	return string(m[1])
}

func TestCheckReleaseKeys(t *testing.T) {
	rel, rot := releaseKeys[0].key, releaseKeys[1].key
	tests := []struct {
		name string
		keys []string
		ok   bool
	}{
		{name: "releasesig keys", keys: []string{releasesig.ReleaseKey, releasesig.RotationKey}, ok: true},
		{name: "rotation key swapped", keys: []string{rel, labKey(t)}},
		{name: "keys reordered", keys: []string{rot, rel}},
		{name: "one key", keys: []string{rel}},
		{name: "extra key", keys: []string{rel, rot, labKey(t)}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := checkReleaseKeys(releaseKeys, tt.keys...); (err == nil) != tt.ok {
				t.Errorf("err %v, want ok=%v", err, tt.ok)
			}
		})
	}
}

func TestReleaseKeyIDs(t *testing.T) {
	if releaseKeys[0].id != 0x01A0941E358E1C5A || releaseKeys[1].id != 0x73BBC751F8715F03 {
		t.Fatalf("pinned ids changed: %016X %016X", releaseKeys[0].id, releaseKeys[1].id)
	}
	wrongID := []releaseKey{releaseKeys[0], {key: releaseKeys[1].key, id: 0x0123456789ABCDEF}}
	if err := checkReleaseKeys(wrongID, releaseKeys[0].key, releaseKeys[1].key); err == nil {
		t.Error("key with the wrong id accepted")
	}
}

func TestCheck(t *testing.T) {
	lab := labKey(t)
	both := "\x00" + releaseKeys[0].key + "\x00" + releaseKeys[1].key + "\x00"
	packed := "fooBar" + releaseKeys[0].key + "Baz9" + releaseKeys[1].key + "Qux"
	tags := func(v string) []debug.BuildSetting { return []debug.BuildSetting{{Key: "-tags", Value: v}} }
	tests := []struct {
		name     string
		path     string
		settings []debug.BuildSetting
		data     string
		want     []string
	}{
		{name: "server with release keys", path: server, data: both},
		{name: "agent with keys packed between strings", path: agent, data: packed},
		{name: "dmctl without keys", path: dmctl, data: "no keys here"},
		{name: "any tag", path: server, settings: tags("netgo"), data: both, want: []string{"built with -tags=netgo"}},
		{name: "empty tags setting", path: server, settings: tags(""), data: both},
		{name: "missing rotation key", path: server, data: releaseKeys[0].key, want: []string{"misses release key 73BBC751F8715F03"}},
		{name: "no keys in server", path: server, data: "", want: []string{"misses release key 01A0941E358E1C5A", "misses release key 73BBC751F8715F03"}},
		{name: "lab key in agent", path: agent, data: both + "x" + lab + "y", want: []string{"trusts unknown minisign key"}},
		{name: "lab key in dmctl", path: dmctl, data: lab, want: []string{"trusts unknown minisign key"}},
		{name: "lab hook", path: dmctl, data: "os.Getenv DOCKMESH_LAB_DAY", want: []string{"contains DOCKMESH_LAB_ hooks"}},
		{name: "labkeys tag", path: dmctl, settings: tags("netgo,labkeys"), want: []string{"built with -tags=netgo,labkeys"}},
		{name: "unknown command", path: "github.com/dockmesh/dockmesh/cmd/other", want: []string{`unknown command "github.com/dockmesh/dockmesh/cmd/other"`}},
		{name: "truncated key", path: dmctl, data: lab[:minisignKeyLen-1]},
		{name: "key with invalid base64", path: dmctl, data: lab[:20] + "-" + lab[21:]},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := check(&debug.BuildInfo{Path: tt.path, Settings: tt.settings}, []byte(tt.data))
			if len(got) != len(tt.want) {
				t.Fatalf("got %q, want %d problems like %q", got, len(tt.want), tt.want)
			}
			for i := range got {
				if !strings.HasPrefix(got[i], tt.want[i]) {
					t.Errorf("problem %d: %q, want prefix %q", i, got[i], tt.want[i])
				}
			}
		})
	}
}

// TestBuiltBinaries builds the released commands the way the release
// workflow does, once plain and once with the lab tag, and runs the check
// on the real output.
func TestBuiltBinaries(t *testing.T) {
	if testing.Short() {
		t.Skip("builds three commands twice")
	}
	goBin, err := exec.LookPath("go")
	if err != nil {
		t.Skip("go not on PATH")
	}
	lab := labKey(t)
	cmds := []string{"dockmesh", "dockmesh-agent", "dmctl"}
	for _, tag := range []string{"", "labkeys"} {
		t.Run("tags="+tag, func(t *testing.T) {
			t.Parallel()
			dir := t.TempDir()
			var paths []string
			for _, c := range cmds {
				out := filepath.Join(dir, c)
				cmd := exec.Command(goBin, "build", "-trimpath", "-tags", tag, "-ldflags", "-s -w", "-o", out, "./cmd/"+c)
				cmd.Dir = "../.."
				cmd.Env = append(os.Environ(), "CGO_ENABLED=0")
				if b, err := cmd.CombinedOutput(); err != nil {
					t.Fatalf("build %s: %v\n%s", c, err, b)
				}
				paths = append(paths, out)
			}
			for _, p := range paths {
				problems, err := checkFile(p)
				if err != nil {
					t.Fatal(err)
				}
				joined := strings.Join(problems, "\n")
				if tag == "" {
					if len(problems) != 0 {
						t.Errorf("%s: %s", filepath.Base(p), joined)
					}
					continue
				}
				if !strings.Contains(joined, "built with -tags=labkeys") {
					t.Errorf("%s: tag not reported: %q", filepath.Base(p), problems)
				}
				if filepath.Base(p) == "dmctl" {
					continue
				}
				for _, want := range []string{"contains " + labHookPrefix, lab} {
					if !strings.Contains(joined, want) {
						t.Errorf("%s: %q not reported: %q", filepath.Base(p), want, problems)
					}
				}
			}
			var sb strings.Builder
			if err := run(paths, &sb); (err != nil) != (tag != "") {
				t.Errorf("run: err %v\n%s", err, sb.String())
			}
		})
	}
}
