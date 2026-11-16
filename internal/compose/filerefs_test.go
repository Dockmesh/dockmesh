package compose

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

type stackFile struct {
	name    string
	content string
	link    string // symlink target instead of content
	fifo    bool
	size    int64 // sparse file of this size instead of content
}

func writeStack(t *testing.T, dir string, files []stackFile) {
	t.Helper()
	for _, f := range files {
		p := filepath.Join(dir, f.name)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		switch {
		case f.link != "":
			if err := os.Symlink(f.link, p); err != nil {
				t.Fatal(err)
			}
		case f.fifo:
			if err := syscall.Mkfifo(p, 0o600); err != nil {
				t.Fatal(err)
			}
		case f.size > 0:
			fh, err := os.Create(p)
			if err != nil {
				t.Fatal(err)
			}
			if err := fh.Truncate(f.size); err != nil {
				t.Fatal(err)
			}
			fh.Close()
		default:
			if err := os.WriteFile(p, []byte(f.content), 0o600); err != nil {
				t.Fatal(err)
			}
		}
	}
}

const svcNginx = "services:\n  web:\n    image: nginx\n"

func TestLoadProjectFileRefs(t *testing.T) {
	cases := []struct {
		name    string
		files   []stackFile
		outside []stackFile // written next to the stack folder
		env     string
		refused bool
		errText string
		check   func(t *testing.T, env map[string]string, labels map[string]string)
	}{
		{
			name:    "include dev zero",
			files:   []stackFile{{name: "compose.yaml", content: "include:\n  - /dev/zero\n" + svcNginx}},
			refused: true,
		},
		{
			name:    "include parent dir",
			files:   []stackFile{{name: "compose.yaml", content: "include:\n  - ../other.yaml\n" + svcNginx}},
			outside: []stackFile{{name: "other.yaml", content: "services:\n  db:\n    image: redis\n"}},
			refused: true,
		},
		{
			name:    "include through interpolation",
			files:   []stackFile{{name: "compose.yaml", content: "include:\n  - ${INC}\n" + svcNginx}},
			env:     "INC=/dev/zero\n",
			refused: true,
		},
		{
			name: "include symlink escape",
			files: []stackFile{
				{name: "compose.yaml", content: "include:\n  - inc.yaml\n" + svcNginx},
				{name: "inc.yaml", link: "../other.yaml"},
			},
			outside: []stackFile{{name: "other.yaml", content: "services:\n  db:\n    image: redis\n"}},
			refused: true,
		},
		{
			name: "include directory symlink escape",
			files: []stackFile{
				{name: "compose.yaml", content: "include:\n  - sub/other.yaml\n" + svcNginx},
				{name: "sub", link: ".."},
			},
			outside: []stackFile{{name: "other.yaml", content: "services:\n  db:\n    image: redis\n"}},
			refused: true,
		},
		{
			name: "include fifo",
			files: []stackFile{
				{name: "compose.yaml", content: "include:\n  - inc.yaml\n" + svcNginx},
				{name: "inc.yaml", fifo: true},
			},
			refused: true,
		},
		{
			name: "include large file",
			files: []stackFile{
				{name: "compose.yaml", content: "include:\n  - inc.yaml\n" + svcNginx},
				{name: "inc.yaml", size: maxComposeFileSize + 1},
			},
			refused: true,
		},
		{
			name: "include self-merging anchor",
			files: []stackFile{
				{name: "compose.yaml", content: "include:\n  - inc.yaml\n" + svcNginx},
				{name: "inc.yaml", content: "&a\n<<: *a\n"},
			},
			errText: "invalid compose file",
		},
		{
			name: "nested include relative project_directory",
			files: []stackFile{
				{name: "compose.yaml", content: "include:\n  - sub/inc.yaml\n" + svcNginx},
				{name: "sub/inc.yaml", content: "include:\n  - path: db.yaml\n    project_directory: .\nservices:\n  api:\n    image: redis\n"},
				{name: "sub/db.yaml", content: "services:\n  db:\n    image: redis\n"},
			},
			refused: true,
		},
		{
			name: "include env_file outside",
			files: []stackFile{
				{name: "compose.yaml", content: "include:\n  - path: inc.yaml\n    env_file: ../vars.env\n" + svcNginx},
				{name: "inc.yaml", content: "services:\n  db:\n    image: redis\n"},
			},
			outside: []stackFile{{name: "vars.env", content: "A=1\n"}},
			refused: true,
		},
		{
			name: "include default env is fifo",
			files: []stackFile{
				{name: "compose.yaml", content: "include:\n  - sub/inc.yaml\n" + svcNginx},
				{name: "sub/inc.yaml", content: "services:\n  db:\n    image: redis\n"},
				{name: "sub/.env", fifo: true},
			},
			refused: true,
		},
		{
			name:    "extends file outside",
			files:   []stackFile{{name: "compose.yaml", content: "services:\n  web:\n    extends:\n      file: /etc/passwd\n      service: x\n"}},
			refused: true,
		},
		{
			name: "extends chain escapes in base file",
			files: []stackFile{
				{name: "compose.yaml", content: "services:\n  web:\n    extends:\n      file: base/a.yaml\n      service: a\n"},
				{name: "base/a.yaml", content: "services:\n  a:\n    extends:\n      file: ../../b.yaml\n      service: b\n"},
			},
			outside: []stackFile{{name: "b.yaml", content: "services:\n  b:\n    image: redis\n"}},
			refused: true,
		},
		{
			name:    "env_file dev zero",
			files:   []stackFile{{name: "compose.yaml", content: "services:\n  web:\n    image: nginx\n    env_file: /dev/zero\n"}},
			refused: true,
		},
		{
			name:    "env_file outside",
			files:   []stackFile{{name: "compose.yaml", content: "services:\n  web:\n    image: nginx\n    env_file: ../vars.env\n"}},
			outside: []stackFile{{name: "vars.env", content: "SECRET=x\n"}},
			refused: true,
		},
		{
			name: "env_file in included file escapes",
			files: []stackFile{
				{name: "compose.yaml", content: "include:\n  - sub/inc.yaml\n" + svcNginx},
				{name: "sub/inc.yaml", content: "services:\n  db:\n    image: redis\n    env_file: ../../vars.env\n"},
			},
			outside: []stackFile{{name: "vars.env", content: "SECRET=x\n"}},
			refused: true,
		},
		{
			name:    "label_file outside",
			files:   []stackFile{{name: "compose.yaml", content: "services:\n  web:\n    image: nginx\n    label_file: /etc/hostname\n"}},
			refused: true,
		},
		{
			name: "label_file fifo",
			files: []stackFile{
				{name: "compose.yaml", content: "services:\n  web:\n    image: nginx\n    label_file: labels\n"},
				{name: "labels", fifo: true},
			},
			refused: true,
		},
		{
			name: "include and files inside the folder",
			files: []stackFile{
				{name: "compose.yaml", content: "include:\n  - path: sub/inc.yaml\n    env_file: inc.env\nservices:\n  web:\n    image: nginx\n    env_file:\n      - web.env\n      - path: missing.env\n        required: false\n    label_file: web.labels\n    extends:\n      file: base.yaml\n      service: base\n"},
				{name: "inc.env", content: "DBIMG=redis\n"},
				{name: "web.env", content: "FROM_FILE=yes\n"},
				{name: "web.labels", content: "team=ops\n"},
				{name: "base.yaml", content: "services:\n  base:\n    environment:\n      FROM_BASE: yes\n"},
				{name: "sub/inc.yaml", content: "include:\n  - db.yaml\nservices:\n  api:\n    image: ${DBIMG}\n"},
				{name: "sub/db.yaml", content: "services:\n  db:\n    image: redis\n    env_file: db.env\n"},
				{name: "sub/db.env", content: "DB=1\n"},
				{name: "link.env", link: "web.env"},
			},
			check: func(t *testing.T, env, labels map[string]string) {
				if env["FROM_FILE"] != "yes" || env["FROM_BASE"] != "yes" {
					t.Errorf("web env = %v", env)
				}
				if labels["team"] != "ops" {
					t.Errorf("web labels = %v", labels)
				}
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			base := t.TempDir()
			dir := filepath.Join(base, "stack")
			if err := os.Mkdir(dir, 0o755); err != nil {
				t.Fatal(err)
			}
			writeStack(t, dir, tc.files)
			writeStack(t, base, tc.outside)
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			proj, err := LoadProject(ctx, dir, "app", tc.env)
			if tc.errText != "" {
				if err == nil || !strings.Contains(err.Error(), tc.errText) {
					t.Fatalf("err = %v, want %q", err, tc.errText)
				}
				return
			}
			if tc.refused {
				if !errors.Is(err, ErrComposeFileRef) {
					t.Fatalf("err = %v, want refusal", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("load: %v", err)
			}
			if tc.check != nil {
				web := proj.Services["web"]
				env := map[string]string{}
				for k, v := range web.Environment {
					if v != nil {
						env[k] = *v
					}
				}
				tc.check(t, env, web.Labels)
			}
			for _, svc := range []string{"web", "api", "db"} {
				if _, ok := proj.Services[svc]; !ok {
					t.Errorf("service %s missing", svc)
				}
			}
			if img := proj.Services["api"].Image; img != "redis" {
				t.Errorf("api image = %q, include env_file not applied", img)
			}
			if v := proj.Services["db"].Environment["DB"]; v == nil || *v != "1" {
				t.Errorf("db env = %v", proj.Services["db"].Environment)
			}
		})
	}
}

func TestLoadProjectRefusesComposeSymlinkOutside(t *testing.T) {
	base := t.TempDir()
	dir := filepath.Join(base, "stack")
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	writeStack(t, dir, []stackFile{{name: "compose.yaml", link: "/dev/zero"}})
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if _, err := LoadProject(ctx, dir, "app", ""); !errors.Is(err, ErrComposeFileRef) {
		t.Fatalf("err = %v", err)
	}
}

func TestMergeEnvironmentRefusesOverlayInclude(t *testing.T) {
	dir := t.TempDir()
	writeStack(t, dir, []stackFile{
		{name: "compose.yaml", content: svcNginx},
		{name: "compose.prod.yaml", content: "include:\n  - /dev/zero\n"},
	})
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if _, _, err := MergeEnvironment(ctx, dir, "app", "", "prod"); !errors.Is(err, ErrComposeFileRef) {
		t.Fatalf("err = %v", err)
	}
	if _, err := MergeEnvironmentContent(ctx, dir, "app", "", "prod", []byte(svcNginx), []byte("services:\n  web:\n    env_file: /etc/hostname\n")); !errors.Is(err, ErrComposeFileRef) {
		t.Fatalf("content err = %v", err)
	}
}

func TestLoadProjectIncludeFanOutIsBounded(t *testing.T) {
	dir := t.TempDir()
	// Each level includes the next twice with a different env, so an
	// unbounded walk would visit 2^30 files.
	var files []stackFile
	files = append(files, stackFile{name: "compose.yaml", content: "include:\n  - path: l0.yaml\n    env_file: a.env\n  - path: l0.yaml\n    env_file: b.env\n" + svcNginx})
	files = append(files, stackFile{name: "a.env", content: "X=a\n"}, stackFile{name: "b.env", content: "X=b\n"})
	for i := 0; i < 30; i++ {
		next := "l" + string(rune('a'+i)) + ".yaml"
		cur := "l0.yaml"
		if i > 0 {
			cur = "l" + string(rune('a'+i-1)) + ".yaml"
		}
		files = append(files, stackFile{name: cur, content: "include:\n  - path: " + next + "\n    env_file: /dev/null\n  - " + next + "\n"})
	}
	files = append(files, stackFile{name: "l" + string(rune('a'+29)) + ".yaml", content: "services:\n  deep:\n    image: redis${X}\n"})
	writeStack(t, dir, files)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	start := time.Now()
	_, err := LoadProject(ctx, dir, "app", "")
	if err != nil && !strings.Contains(err.Error(), "conflict") {
		t.Fatalf("err = %v", err)
	}
	if time.Since(start) > 10*time.Second {
		t.Fatalf("load took %s", time.Since(start))
	}
}
