package setup

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/dockmesh/dockmesh/internal/layout"
)

// DataDirCheck is the live check of the data dir step. Issue names the
// first problem so the wizard can show its own text for it; Message is
// the same in English.
type DataDirCheck struct {
	Path         string `json:"path"`
	Exists       bool   `json:"exists"`
	IsDir        bool   `json:"is_dir"`
	WillCreate   bool   `json:"will_create"`
	Empty        bool   `json:"empty"`
	FSType       string `json:"fs_type,omitempty"`
	TotalBytes   int64  `json:"total_bytes"`
	FreeBytes    int64  `json:"free_bytes"`
	ParentExists bool   `json:"parent_exists"`
	Status       string `json:"status"` // "ok" | "warn" | "fail"
	Issue        string `json:"issue,omitempty"`
	Message      string `json:"message"`
}

// Data dir issues, in the order CheckDataDir tests them.
const (
	IssueNotAbsolute     = "not_absolute"
	IssueBadCharacters   = "bad_characters"
	IssueInsideDocker    = "inside_docker"
	IssueSystemDir       = "system_dir"
	IssueNotDir          = "not_a_directory"
	IssueNoParent        = "no_parent"
	IssueUnsafeParent    = "unsafe_parent"
	IssueSymlink         = "symlink"
	IssueForeignOwner    = "foreign_owner"
	IssueExistingInstall = "existing_install"
	IssueNotEmpty        = "not_empty"
	IssueNetworkShare    = "network_share"
	IssueNoUnixPerms     = "no_unix_permissions"
	IssueTooSmall        = "too_small"
	IssueLowSpace        = "low_space"
	IssueUnreadable      = "unreadable"
)

const (
	minDataDirFree  = 200 << 20
	warnDataDirFree = 2 << 30
)

// CheckDataDir checks path as a data dir for a new install with the rules
// root's setup-apply enforces: a clean absolute path outside system and
// Docker directories whose parent exists and belongs to root alone, no
// symlink, empty or not there yet and owned by the service account if it
// exists, on a local filesystem with Unix permissions and enough space.
// It writes nothing.
func CheckDataDir(path string) DataDirCheck {
	return checkDataDir(path, dataDirRules{
		fsType:    mountFSType,
		usage:     diskUsage,
		ancestors: RootOwnedAncestors,
		owned:     ownedBySelf,
	})
}

// dataDirRules are the host lookups of checkDataDir.
type dataDirRules struct {
	fsType    func(string) string
	usage     func(string) (int64, int64, error)
	ancestors func(string) error
	owned     func(fs.FileInfo) bool
}

func checkDataDir(path string, r dataDirRules) DataDirCheck {
	d := DataDirCheck{Path: path}
	fail := func(issue, format string, a ...any) DataDirCheck {
		d.Status, d.Issue, d.Message = "fail", issue, fmt.Sprintf(format, a...)
		return d
	}
	if !filepath.IsAbs(path) {
		return fail(IssueNotAbsolute, "Enter an absolute path, for example /var/lib/dockmesh.")
	}
	path = filepath.Clean(path)
	d.Path = path
	if !plainValueRe.MatchString(path) {
		return fail(IssueBadCharacters, "Use only letters, digits and . _ - / in the path.")
	}
	if underAny(path, []string{"/var/lib/docker"}) {
		return fail(IssueInsideDocker, "Choose a directory outside /var/lib/docker.")
	}
	if CheckDataDirPath(path) != nil {
		return fail(IssueSystemDir, "%s is a system directory. Choose a directory of its own, for example /srv/dockmesh.", path)
	}
	parent := filepath.Dir(path)
	if pi, err := os.Stat(parent); err != nil || !pi.IsDir() {
		return fail(IssueNoParent, "%s does not exist. Create it first, or choose a directory in an existing one.", parent)
	}
	d.ParentExists = true
	if err := r.ancestors(parent); err != nil {
		return fail(IssueUnsafeParent, "%s and every directory above it must belong to root and be writable only by root.", parent)
	}

	probe := path
	info, err := os.Lstat(path)
	switch {
	case err == nil:
		d.Exists = true
		d.IsDir = info.IsDir()
		if info.Mode()&fs.ModeSymlink != 0 {
			return fail(IssueSymlink, "%s is a symbolic link. Enter the real path.", path)
		}
		if !d.IsDir {
			return fail(IssueNotDir, "%s is a file, not a directory.", path)
		}
		if hasInstall(path) {
			return fail(IssueExistingInstall, "%s already holds a Dockmesh installation.", path)
		}
		empty, err := emptyForSetup(path)
		if err != nil {
			return fail(IssueUnreadable, "Cannot read %s: %v.", path, err)
		}
		if !empty {
			return fail(IssueNotEmpty, "%s is not empty. Choose an empty or new directory.", path)
		}
		if !r.owned(info) {
			return fail(IssueForeignOwner, "%s belongs to another account. Remove it or choose a new directory; setup creates it.", path)
		}
		d.Empty = true
	case errors.Is(err, os.ErrNotExist):
		d.WillCreate = true
		probe = parent
	default:
		return fail(IssueUnreadable, "Cannot read %s: %v.", path, err)
	}

	d.FSType = r.fsType(probe)
	switch {
	case networkFS[d.FSType] || strings.HasPrefix(d.FSType, "nfs"):
		return fail(IssueNetworkShare, "%s is on a network share (%s). Dockmesh needs a local filesystem such as ext4, xfs or btrfs.", path, d.FSType)
	case noPermFS[d.FSType]:
		return fail(IssueNoUnixPerms, "%s is on a filesystem without Unix permissions (%s). Dockmesh needs a local filesystem such as ext4, xfs or btrfs.", path, d.FSType)
	}
	if t, f, err := r.usage(probe); err == nil {
		d.TotalBytes, d.FreeBytes = t, f
		switch {
		case f < minDataDirFree:
			return fail(IssueTooSmall, "Only %d MiB free on %s. Dockmesh needs at least 200 MiB.", f>>20, path)
		case f < warnDataDirFree:
			d.Status, d.Issue = "warn", IssueLowSpace
			d.Message = fmt.Sprintf("Only %.1f GiB free. Backups and images can fill this quickly.", float64(f)/(1<<30))
			return d
		}
	}
	d.Status = "ok"
	d.Message = fmt.Sprintf("Free space %s", humanBytes(d.FreeBytes))
	return d
}

// hasInstall reports a database or config of an earlier install in dir.
func hasInstall(dir string) bool {
	for _, name := range []string{"data/dockmesh.db", "dockmesh.db", "dockmesh.env"} {
		if _, err := os.Lstat(filepath.Join(dir, name)); err == nil {
			return true
		}
	}
	return false
}

// emptyForSetup is true for a dir that holds nothing but what this setup
// mode keeps there itself and empty data and stacks dirs; setup-apply
// accepts the same.
func emptyForSetup(dir string) (bool, error) {
	f, err := os.Open(dir)
	if err != nil {
		return false, err
	}
	defer f.Close()
	for {
		entries, err := f.ReadDir(64)
		for _, e := range entries {
			if !setupLeftover(dir, e) {
				return false, nil
			}
		}
		if errors.Is(err, io.EOF) {
			return true, nil
		}
		if err != nil {
			return false, err
		}
	}
}

func setupLeftover(dir string, e fs.DirEntry) bool {
	name := e.Name()
	switch {
	case name == StateDirName || name == filepath.Base(layout.UpdateRequestDir(dir)):
		return true
	case (name == "data" || name == "stacks") && e.IsDir():
		sub, err := os.ReadDir(filepath.Join(dir, name))
		if err != nil || len(sub) > 64 {
			return false
		}
		for _, s := range sub {
			if !strings.Contains(s.Name(), ".failed-") {
				return false
			}
		}
		return true
	default:
		return strings.Contains(name, ".failed-")
	}
}

// mountFSType is the filesystem type of the mount that holds path, ""
// when unknown.
func mountFSType(path string) string {
	f, err := os.Open("/proc/self/mountinfo")
	if err != nil {
		return ""
	}
	defer f.Close()
	return fsTypeOf(f, path)
}

func fsTypeOf(r io.Reader, path string) string {
	best, fsType := "", ""
	sc := bufio.NewScanner(r)
	for sc.Scan() {
		fields := strings.Fields(sc.Text())
		sep := -1
		for i, f := range fields {
			if f == "-" {
				sep = i
				break
			}
		}
		if sep < 5 || sep+1 >= len(fields) {
			continue
		}
		mp := unescapeMount(fields[4])
		if (path == mp || mp == "/" || strings.HasPrefix(path, mp+"/")) && len(mp) >= len(best) {
			best, fsType = mp, fields[sep+1]
		}
	}
	return fsType
}
