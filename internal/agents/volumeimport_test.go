package agents

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"testing"
)

// runScript runs an import script with /dest pointed at dir, the way the
// helper container runs it with the volume mounted there.
func runScript(t *testing.T, dir, script string, stdin []byte) int {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("needs a POSIX sh")
	}
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("no sh")
	}
	cmd := exec.Command("sh", "-c", strings.ReplaceAll(script, importDest, dir))
	cmd.Stdin = bytes.NewReader(stdin)
	out, err := cmd.CombinedOutput()
	var exit *exec.ExitError
	switch {
	case err == nil:
		return 0
	case errors.As(err, &exit):
		t.Logf("script exit %d: %s", exit.ExitCode(), out)
		return exit.ExitCode()
	}
	t.Fatalf("run script: %v %s", err, out)
	return -1
}

func archive(t *testing.T, files map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	_ = tw.WriteHeader(&tar.Header{Name: "./", Typeflag: tar.TypeDir, Mode: 0o755})
	names := make([]string, 0, len(files))
	for n := range files {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		if dir := filepath.Dir(n); dir != "." {
			_ = tw.WriteHeader(&tar.Header{Name: "./" + dir + "/", Typeflag: tar.TypeDir, Mode: 0o755})
		}
		_ = tw.WriteHeader(&tar.Header{Name: "./" + n, Typeflag: tar.TypeReg, Mode: 0o644, Size: int64(len(files[n]))})
		_, _ = tw.Write([]byte(files[n]))
	}
	_ = tw.Close()
	_ = gz.Close()
	return buf.Bytes()
}

func writeTree(t *testing.T, dir string, files map[string]string) {
	t.Helper()
	for n, c := range files {
		p := filepath.Join(dir, n)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(c), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func readTree(t *testing.T, dir string) map[string]string {
	t.Helper()
	got := map[string]string{}
	err := filepath.Walk(dir, func(p string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return err
		}
		rel, _ := filepath.Rel(dir, p)
		b, err := os.ReadFile(p)
		got[filepath.ToSlash(rel)] = string(b)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	return got
}

func sameTree(t *testing.T, label string, got, want map[string]string) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("%s: got %v, want %v", label, got, want)
	}
	for k, v := range want {
		if got[k] != v {
			t.Fatalf("%s: %s = %q, want %q (tree %v)", label, k, got[k], v, got)
		}
	}
}

var oldFiles = map[string]string{"data.db": "old", ".hidden": "old-dot", "..odd": "old-dd", "sub/a.txt": "old-a"}

func TestVolumeImportReplaceSwaps(t *testing.T) {
	dir := t.TempDir()
	writeTree(t, dir, oldFiles)
	newFiles := map[string]string{"data.db": "new", "sub/b.txt": "new-b", ".env": "new-dot"}
	if code := runScript(t, dir, VolumeImportScript(true, 1), archive(t, newFiles)); code != 0 {
		t.Fatalf("exit %d", code)
	}
	sameTree(t, "after replace", readTree(t, dir), newFiles)
}

func TestVolumeImportReplaceBrokenStreamKeepsVolume(t *testing.T) {
	full := archive(t, map[string]string{"data.db": strings.Repeat("x", 64<<10), "b": "y"})
	cases := []struct {
		name  string
		input []byte
	}{
		{"truncated", full[:len(full)/2]},
		{"empty", nil},
		{"garbage", []byte("not a tar.gz")},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			dir := t.TempDir()
			writeTree(t, dir, oldFiles)
			code := runScript(t, dir, VolumeImportScript(true, 0), c.input)
			if code == 0 {
				t.Fatal("broken archive restored without error")
			}
			if err := VolumeImportExitError(int64(code), true); err == nil {
				t.Fatal("no error for a failed import")
			}
			sameTree(t, "after failed replace", readTree(t, dir), oldFiles)
		})
	}
}

func TestVolumeImportReplaceNeedsFreeSpace(t *testing.T) {
	dir := t.TempDir()
	writeTree(t, dir, oldFiles)
	code := runScript(t, dir, VolumeImportScript(true, 1<<62), archive(t, map[string]string{"x": "y"}))
	if code != importExitNoSpace {
		t.Fatalf("exit %d, want %d", code, importExitNoSpace)
	}
	if err := VolumeImportExitError(int64(code), true); err == nil || !strings.Contains(err.Error(), "free space") {
		t.Fatalf("error %v", err)
	}
	sameTree(t, "after refused replace", readTree(t, dir), oldFiles)
}

func TestVolumeImportMergeKeepsOtherFiles(t *testing.T) {
	dir := t.TempDir()
	writeTree(t, dir, oldFiles)
	if code := runScript(t, dir, VolumeImportScript(false, 0), archive(t, map[string]string{"data.db": "new"})); code != 0 {
		t.Fatalf("exit %d", code)
	}
	want := map[string]string{}
	for k, v := range oldFiles {
		want[k] = v
	}
	want["data.db"] = "new"
	sameTree(t, "after merge", readTree(t, dir), want)
}

// A helper killed mid-way leaves one of these states; recovery must end
// with either the complete old or the complete new contents.
func TestVolumeRecoverInterruptedImport(t *testing.T) {
	newFiles := map[string]string{"data.db": "new", "n.txt": "new-n"}
	rel := func(p string) string { return strings.TrimPrefix(p, importDest+"/") }
	cases := []struct {
		name  string
		setup func(dir string)
		want  map[string]string
	}{
		{"during extraction", func(dir string) {
			writeTree(t, dir, oldFiles)
			writeTree(t, filepath.Join(dir, rel(importStaging)), map[string]string{"data.db": "ne"})
		}, oldFiles},
		{"while moving the old contents out", func(dir string) {
			writeTree(t, dir, map[string]string{"data.db": "old", "sub/a.txt": "old-a"})
			writeTree(t, filepath.Join(dir, rel(importOld)), map[string]string{".hidden": "old-dot", "..odd": "old-dd"})
			writeTree(t, filepath.Join(dir, rel(importStaging)), newFiles)
		}, oldFiles},
		{"while moving the new contents in", func(dir string) {
			writeTree(t, filepath.Join(dir, rel(importOld)), oldFiles)
			writeTree(t, dir, map[string]string{"data.db": "new"})
			writeTree(t, filepath.Join(dir, rel(importStaging)), map[string]string{"n.txt": "new-n"})
			writeTree(t, dir, map[string]string{rel(importMarker): ""})
		}, newFiles},
		{"while dropping the old contents", func(dir string) {
			writeTree(t, dir, newFiles)
			writeTree(t, filepath.Join(dir, rel(importTrash)), map[string]string{"data.db": "old"})
			writeTree(t, dir, map[string]string{rel(importMarker): ""})
		}, newFiles},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			dir := t.TempDir()
			c.setup(dir)
			if code := runScript(t, dir, VolumeRecoverScript(), nil); code != 0 {
				t.Fatalf("recover exit %d", code)
			}
			sameTree(t, "after recover", readTree(t, dir), c.want)
		})
	}
}

// The restore's own names in an archive (or left in a volume as files)
// must not break the swap.
func TestVolumeImportIgnoresReservedNames(t *testing.T) {
	newFiles := map[string]string{"data.db": "new"}
	poisoned := map[string]string{"data.db": "new", ".dockmesh-restore-old/x": "a", ".dockmesh-restore-new/y": "b", ".dockmesh-restore-swap": ""}
	for _, replace := range []bool{true, false} {
		dir := t.TempDir()
		writeTree(t, dir, map[string]string{"data.db": "old", ".dockmesh-restore-trash": "file"})
		if code := runScript(t, dir, VolumeImportScript(replace, 0), archive(t, poisoned)); code != 0 {
			t.Fatalf("replace=%v: exit %d", replace, code)
		}
		sameTree(t, "after restore", readTree(t, dir), newFiles)
	}
}

func TestVolumeExportSkipsReservedNames(t *testing.T) {
	src := t.TempDir()
	writeTree(t, src, map[string]string{"keep/a": "1", "keep/.dockmesh-restore-notes": "4", ".dockmesh-restore-old/x": "2", ".dockmesh-restore-new": "3"})
	out := filepath.Join(t.TempDir(), "a.tgz")
	script := strings.ReplaceAll(VolumeExportScript, "/source", src) + " > " + out
	if code := runScript(t, src, script, nil); code != 0 {
		t.Fatalf("export exit %d", code)
	}
	data, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	if code := runScript(t, dir, "tar xzf - -C "+importDest, data); code != 0 {
		t.Fatalf("extract exit %d", code)
	}
	sameTree(t, "exported", readTree(t, dir), map[string]string{"keep/a": "1", "keep/.dockmesh-restore-notes": "4"})
}

func TestVolumeImportRecoversBeforeExtracting(t *testing.T) {
	dir := t.TempDir()
	writeTree(t, dir, map[string]string{"data.db": "old"})
	writeTree(t, filepath.Join(dir, strings.TrimPrefix(importOld, importDest+"/")), map[string]string{"sub/a.txt": "old-a"})
	newFiles := map[string]string{"data.db": "new"}
	if code := runScript(t, dir, VolumeImportScript(true, 0), archive(t, newFiles)); code != 0 {
		t.Fatalf("exit %d", code)
	}
	sameTree(t, "after replace", readTree(t, dir), newFiles)
}
