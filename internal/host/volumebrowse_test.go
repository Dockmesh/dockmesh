package host

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSanitizeVolumePath_Escape(t *testing.T) {
	mount := t.TempDir()
	// A sibling dir that a successful path-escape would reach.
	sibling := t.TempDir()
	if err := os.WriteFile(filepath.Join(sibling, "secret"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}

	escapes := []string{
		"..",
		"../",
		"../secret",
		"../../etc/passwd",
		"foo/../../" + filepath.Base(sibling) + "/secret",
		"/..",
		"/../..",
	}
	for _, sub := range escapes {
		t.Run(sub, func(t *testing.T) {
			_, err := SanitizeVolumePath(mount, sub)
			if !errors.Is(err, ErrVolumePathEscape) {
				t.Fatalf("sub %q: want ErrVolumePathEscape, got %v", sub, err)
			}
		})
	}
}

func TestSanitizeVolumePath_OK(t *testing.T) {
	mount := t.TempDir()
	for _, sub := range []string{"", "/", ".", "foo", "foo/bar", "/foo/bar", "foo//bar"} {
		got, err := SanitizeVolumePath(mount, sub)
		if err != nil {
			t.Fatalf("sub %q: unexpected error: %v", sub, err)
		}
		absMount, _ := filepath.Abs(mount)
		if got != absMount && !strings.HasPrefix(got, absMount+string(filepath.Separator)) {
			t.Fatalf("sub %q: result %q escaped %q", sub, got, absMount)
		}
	}
}

func TestSanitizeVolumePath_TooLong(t *testing.T) {
	mount := t.TempDir()
	long := strings.Repeat("a", MaxBrowsePathLen+1)
	_, err := SanitizeVolumePath(mount, long)
	if !errors.Is(err, ErrVolumePathTooLong) {
		t.Fatalf("want ErrVolumePathTooLong, got %v", err)
	}
}

func TestBrowseDir(t *testing.T) {
	mount := t.TempDir()
	// A dir + a file + a symlink for coverage.
	if err := os.Mkdir(filepath.Join(mount, "subdir"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(mount, "hello.txt"), []byte("hi"), 0o600); err != nil {
		t.Fatal(err)
	}
	_ = os.Symlink("hello.txt", filepath.Join(mount, "link"))

	entries, err := BrowseDir(mount)
	if err != nil {
		t.Fatal(err)
	}
	byName := map[string]VolumeEntry{}
	for _, e := range entries {
		byName[e.Name] = e
	}
	if byName["subdir"].Type != "dir" {
		t.Errorf("subdir type = %q, want dir", byName["subdir"].Type)
	}
	if byName["hello.txt"].Type != "file" || byName["hello.txt"].Size != 2 {
		t.Errorf("hello.txt entry wrong: %+v", byName["hello.txt"])
	}
	if link, ok := byName["link"]; ok {
		if link.Type != "symlink" || link.LinkDest != "hello.txt" {
			t.Errorf("symlink entry wrong: %+v", link)
		}
	}
}

func TestReadFile_TruncateAndBinary(t *testing.T) {
	mount := t.TempDir()
	// 2 KiB of zeros = binary-flagged, larger than cap=1 KiB = truncated.
	big := make([]byte, 2048)
	if err := os.WriteFile(filepath.Join(mount, "bin"), big, 0o600); err != nil {
		t.Fatal(err)
	}
	res, err := ReadFile(filepath.Join(mount, "bin"), 1024)
	if err != nil {
		t.Fatal(err)
	}
	if !res.Truncated {
		t.Errorf("want truncated")
	}
	if !res.Binary {
		t.Errorf("want binary (file is all NULs)")
	}
	if int64(len(res.Content)) != 1024 {
		t.Errorf("content len = %d, want 1024", len(res.Content))
	}
	if res.Size != 2048 {
		t.Errorf("reported size = %d, want 2048", res.Size)
	}
}

func TestReadFile_Text(t *testing.T) {
	mount := t.TempDir()
	if err := os.WriteFile(filepath.Join(mount, "conf.yaml"), []byte("foo: bar\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	res, err := ReadFile(filepath.Join(mount, "conf.yaml"), 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	if res.Truncated || res.Binary {
		t.Errorf("unexpected flags: %+v", res)
	}
	if string(res.Content) != "foo: bar\n" {
		t.Errorf("content mismatch: %q", res.Content)
	}
}
