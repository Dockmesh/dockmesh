package host

import (
	"bytes"
	"context"
	"errors"
	"io"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"github.com/moby/moby/client"
)

// The download returns the whole file, not the 1 MiB preview.
func TestOpenVolumeFileOnHost(t *testing.T) {
	mount := t.TempDir()
	big := bytes.Repeat([]byte("0123456789abcdef"), 3<<16) // 3 MiB
	if err := os.WriteFile(filepath.Join(mount, "big.bin"), big, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(mount, "dir"), 0o755); err != nil {
		t.Fatal(err)
	}
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "shadow"), []byte("secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	haveFIFO := syscall.Mkfifo(filepath.Join(mount, "pipe"), 0o600) == nil
	haveLinks := os.Symlink(filepath.Join(outside, "shadow"), filepath.Join(mount, "hostfile")) == nil

	rc, err := OpenVolumeFileOnHost(context.Background(), nil, "v", mount, "/big.bin")
	if err != nil {
		t.Fatal(err)
	}
	got, err := io.ReadAll(rc)
	rc.Close()
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, big) {
		t.Fatalf("read %d bytes, want the whole %d", len(got), len(big))
	}

	tests := []struct {
		sub      string
		want     error
		needLink bool
		needFIFO bool
	}{
		{sub: "dir", want: ErrVolumeNotFile},
		{sub: "pipe", want: ErrVolumeNotFile, needFIFO: true},
		{sub: "missing", want: fs.ErrNotExist},
		{sub: "../../etc/passwd", want: ErrVolumePathEscape},
		{sub: "hostfile", want: ErrVolumePathEscape, needLink: true},
	}
	for _, tt := range tests {
		t.Run(tt.sub, func(t *testing.T) {
			if (tt.needLink && !haveLinks) || (tt.needFIFO && !haveFIFO) {
				t.Skip("not available on this filesystem")
			}
			rc, err := OpenVolumeFileOnHost(context.Background(), nil, "v", mount, tt.sub)
			if err == nil {
				rc.Close()
			}
			if !errors.Is(err, tt.want) {
				t.Fatalf("err = %v, want %v", err, tt.want)
			}
		})
	}
}

func TestDownloadFileScript(t *testing.T) {
	script := downloadFileScript("/mnt/target/it's")
	for _, want := range []string{
		`t='/mnt/target/it'\''s'`,
		`case "$r" in /mnt/target|/mnt/target/*) ;; *) exit 70 ;; esac`,
		`[ -f "$r" ] || exit 73`,
		`exec cat "$r"`,
	} {
		if !strings.Contains(script, want) {
			t.Errorf("download script lacks %q:\n%s", want, script)
		}
	}
}

// A helper image that can't be pulled reports the registry's error instead
// of failing later with "No such image".
func TestEnsureBrowseHelperImageReportsPullError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/images/create") {
			_, _ = w.Write([]byte(`{"error":"toomanyrequests: rate limit"}` + "\n"))
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()
	cli, err := client.New(client.WithHost("tcp://"+strings.TrimPrefix(srv.URL, "http://")), client.WithAPIVersion("1.45"), client.WithHTTPClient(srv.Client()))
	if err != nil {
		t.Fatal(err)
	}
	if err := ensureBrowseHelperImage(context.Background(), cli); err == nil || !strings.Contains(err.Error(), "toomanyrequests") {
		t.Fatalf("err = %v", err)
	}
}
