package handlers

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"github.com/dockmesh/dockmesh/internal/backup/targets"
)

func TestCheckNewLocalRoots(t *testing.T) {
	tests := []struct {
		name    string
		prev    string
		next    string
		wantErr string
	}{
		{name: "add a root", prev: "/srv/backups", next: "/srv/backups\n/mnt/nas/dockmesh"},
		{name: "remove a root", prev: "/srv/backups\n/mnt/nas", next: "/srv/backups"},
		{name: "relative", prev: "/srv/backups", next: "/srv/backups\nbackups", wantErr: "absolute"},
		{name: "system tree", prev: "", next: "/etc/dockmesh-backups", wantErr: "/etc"},
		{name: "quote", prev: "", next: `/srv/a"b`, wantErr: "quote"},
		// A stored value from before the check stays savable.
		{name: "stored value kept", prev: "/etc/old\n/srv/backups", next: "/etc/old\n/srv/backups\n/srv/more"},
		{name: "comma separated", prev: "", next: "/srv/a,/srv/b"},
	}
	h := &Handlers{}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := h.checkNewLocalRoots(tt.prev, tt.next)
			if tt.wantErr == "" && err != nil || tt.wantErr != "" && (err == nil || !strings.Contains(err.Error(), tt.wantErr)) {
				t.Fatalf("checkNewLocalRoots = %v, want %q", err, tt.wantErr)
			}
		})
	}
}

func TestTargetTestErrorMarksReadOnly(t *testing.T) {
	ro := targetTestError(&os.PathError{Op: "openat", Path: "jobs/x", Err: syscall.EROFS})
	if ro["read_only"] != true || ro["status"] != "error" || !strings.Contains(ro["error"].(string), "Backups page") {
		t.Fatalf("read-only answer %+v", ro)
	}
	other := targetTestError(errors.New("connection refused"))
	if _, ok := other["read_only"]; ok || other["error"] != "connection refused" {
		t.Fatalf("other answer %+v", other)
	}
}

func TestGetUnitRewriteChecksDirs(t *testing.T) {
	base := t.TempDir()
	writable := filepath.Join(base, "ok")
	targets.SetLocalRoots(func() []string { return []string{writable} })
	t.Cleanup(func() { targets.SetLocalRoots(nil) })
	h := &Handlers{}
	rr := httptest.NewRecorder()
	h.GetUnitRewrite(rr, httptest.NewRequest(http.MethodGet, "/api/v1/system/unit-rewrite", nil))
	var st unitRewriteState
	if err := json.Unmarshal(rr.Body.Bytes(), &st); err != nil {
		t.Fatal(err)
	}
	if rr.Code != http.StatusOK || st.Unavailable == "" || st.RestartPending ||
		len(st.Dirs) != 1 || st.Dirs[0].Path != writable || st.Dirs[0].State != targets.DirWritable {
		t.Fatalf("answer %d %s", rr.Code, rr.Body.String())
	}
	rr = httptest.NewRecorder()
	h.StartUnitRewrite(rr, httptest.NewRequest(http.MethodPost, "/api/v1/system/unit-rewrite", nil))
	if rr.Code != http.StatusConflict {
		t.Fatalf("POST without the updater: %d %s", rr.Code, rr.Body.String())
	}
}
