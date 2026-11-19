package setup

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCheckDataDir(t *testing.T) {
	root := t.TempDir()
	mk := func(rel string, files ...string) string {
		dir := filepath.Join(root, rel)
		if err := os.MkdirAll(dir, 0o750); err != nil {
			t.Fatal(err)
		}
		for _, f := range files {
			p := filepath.Join(dir, f)
			if err := os.MkdirAll(filepath.Dir(p), 0o750); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(p, []byte("x"), 0o600); err != nil {
				t.Fatal(err)
			}
		}
		return dir
	}
	empty := mk("empty")
	leftovers := mk("leftovers", ".setup/pki/ca.pem", "update/request", "data/dockmesh.db.failed-20261112T143200Z", PurgedMarker)
	mk("leftovers/stacks")
	full := mk("full", "notes.txt")
	install := mk("install", "data/dockmesh.db")
	file := filepath.Join(mk("files", "plain"), "plain")

	const gib = int64(1) << 30
	link := filepath.Join(root, "link")
	if err := os.Symlink(empty, link); err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name    string
		path    string
		fs      string
		free    int64
		unsafe  bool
		foreign bool
		status  string
		issue   string
		create  bool
	}{
		{name: "relative", path: "var/lib/dockmesh", status: "fail", issue: IssueNotAbsolute},
		{name: "system dir", path: "/etc", status: "fail", issue: IssueSystemDir},
		{name: "below system dir", path: "/usr/share/dockmesh", status: "fail", issue: IssueSystemDir},
		{name: "system parent", path: "/srv", status: "fail", issue: IssueSystemDir},
		{name: "docker", path: "/var/lib/docker/volumes/x", status: "fail", issue: IssueInsideDocker},
		{name: "space", path: "/srv/my data", status: "fail", issue: IssueBadCharacters},
		{name: "empty", path: empty, fs: "ext4", free: 40 * gib, status: "ok"},
		{name: "trailing slash cleaned", path: empty + "/", fs: "ext4", free: 40 * gib, status: "ok"},
		{name: "setup leftovers count as empty", path: leftovers, fs: "ext4", free: 40 * gib, status: "ok"},
		{name: "new in existing", path: filepath.Join(empty, "new"), fs: "xfs", free: 40 * gib, status: "ok", create: true},
		{name: "parent missing", path: filepath.Join(empty, "a", "b"), fs: "xfs", free: 40 * gib, status: "fail", issue: IssueNoParent},
		{name: "parent not root's alone", path: filepath.Join(empty, "new"), fs: "ext4", free: 40 * gib, unsafe: true, status: "fail", issue: IssueUnsafeParent},
		{name: "symlink", path: link, fs: "ext4", free: 40 * gib, status: "fail", issue: IssueSymlink},
		{name: "owned by another account", path: empty, fs: "ext4", free: 40 * gib, foreign: true, status: "fail", issue: IssueForeignOwner},
		{name: "not empty", path: full, fs: "ext4", free: 40 * gib, status: "fail", issue: IssueNotEmpty},
		{name: "existing install", path: install, fs: "ext4", free: 40 * gib, status: "fail", issue: IssueExistingInstall},
		{name: "file", path: file, fs: "ext4", free: 40 * gib, status: "fail", issue: IssueNotDir},
		{name: "network share", path: empty, fs: "nfs4", free: 40 * gib, status: "fail", issue: IssueNetworkShare},
		{name: "cifs", path: empty, fs: "cifs", free: 40 * gib, status: "fail", issue: IssueNetworkShare},
		{name: "no unix permissions", path: empty, fs: "exfat", free: 40 * gib, status: "fail", issue: IssueNoUnixPerms},
		{name: "too small", path: empty, fs: "ext4", free: 120 << 20, status: "fail", issue: IssueTooSmall},
		{name: "low space warns", path: empty, fs: "ext4", free: gib + gib/2, status: "warn", issue: IssueLowSpace},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := checkDataDir(tt.path, dataDirRules{
				fsType: func(string) string { return tt.fs },
				usage:  func(string) (int64, int64, error) { return 100 * gib, tt.free, nil },
				ancestors: func(string) error {
					if tt.unsafe {
						return errors.New("group-writable")
					}
					return nil
				},
				owned: func(fs.FileInfo) bool { return !tt.foreign },
			})
			if got.Status != tt.status || got.Issue != tt.issue {
				t.Fatalf("status %q issue %q (%s), want %q %q", got.Status, got.Issue, got.Message, tt.status, tt.issue)
			}
			if got.WillCreate != tt.create {
				t.Errorf("will_create = %v, want %v", got.WillCreate, tt.create)
			}
			if got.Message == "" {
				t.Error("empty message")
			}
			if tt.status == "ok" && got.FSType != tt.fs {
				t.Errorf("fs_type = %q, want %q", got.FSType, tt.fs)
			}
		})
	}
}

func TestCheckDataDirUsageUnknown(t *testing.T) {
	got := checkDataDir(t.TempDir(), dataDirRules{
		fsType:    func(string) string { return "" },
		usage:     func(string) (int64, int64, error) { return 0, 0, errors.New("no statfs") },
		ancestors: func(string) error { return nil },
		owned:     func(fs.FileInfo) bool { return true },
	})
	if got.Status != "ok" {
		t.Fatalf("status %q (%s), want ok when space is unknown", got.Status, got.Message)
	}
}

func TestFSTypeOf(t *testing.T) {
	const info = `22 1 8:1 / / rw,relatime - ext4 /dev/sda1 rw
30 22 8:17 / /srv rw,relatime - xfs /dev/sdb1 rw
31 22 0:50 / /mnt/nas rw - cifs //nas/share rw
32 22 0:51 / /srv/data\040dir rw - btrfs /dev/sdc1 rw
`
	tests := []struct{ path, want string }{
		{"/var/lib/dockmesh", "ext4"},
		{"/srv", "xfs"},
		{"/srv/dockmesh", "xfs"},
		{"/srvx", "ext4"},
		{"/mnt/nas/dockmesh", "cifs"},
		{"/srv/data dir/x", "btrfs"},
	}
	for _, tt := range tests {
		if got := fsTypeOf(strings.NewReader(info), tt.path); got != tt.want {
			t.Errorf("fsTypeOf(%q) = %q, want %q", tt.path, got, tt.want)
		}
	}
}
