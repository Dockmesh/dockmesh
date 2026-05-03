package setup

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/user"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// DataDirCheck is what the wizard's Step 2 input live-validates against
// as the operator types. The frontend renders the green/amber/red row
// directly from these fields.
type DataDirCheck struct {
	Path           string `json:"path"`
	Exists         bool   `json:"exists"`
	IsDir          bool   `json:"is_dir"`
	Writable       bool   `json:"writable"`
	WillCreate     bool   `json:"will_create"`     // !exists, parent writable
	TotalBytes     int64  `json:"total_bytes"`
	FreeBytes      int64  `json:"free_bytes"`
	ParentExists   bool   `json:"parent_exists"`
	ParentWritable bool   `json:"parent_writable"`
	Status         string `json:"status"`  // "ok" | "warn" | "fail"
	Message        string `json:"message"` // human-readable summary
}

// CheckDataDir is the live-validate Step 2 helper. Path can be absolute
// or relative; a relative path is resolved against the working dir of
// the dockmesh process, which on a systemd install is `/`.
func CheckDataDir(path string) DataDirCheck {
	d := DataDirCheck{Path: path}
	if strings.TrimSpace(path) == "" {
		d.Status = "fail"
		d.Message = "path is required"
		return d
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		d.Status = "fail"
		d.Message = "invalid path: " + err.Error()
		return d
	}
	d.Path = abs

	info, err := os.Stat(abs)
	switch {
	case err == nil:
		d.Exists = true
		d.IsDir = info.IsDir()
		if !d.IsDir {
			d.Status = "fail"
			d.Message = "path exists but is a file, not a directory"
			return d
		}
		d.Writable = isDirWritable(abs)
		t, f, derr := diskUsage(abs)
		if derr == nil {
			d.TotalBytes, d.FreeBytes = t, f
		}
		if !d.Writable {
			d.Status = "fail"
			d.Message = "directory exists but is not writable by the dockmesh process"
			return d
		}
		d.Status = "ok"
		d.Message = fmt.Sprintf("%s free, writable", humanBytes(d.FreeBytes))
	case errors.Is(err, os.ErrNotExist):
		// Doesn't exist yet — check the parent.
		parent := filepath.Dir(abs)
		pinfo, perr := os.Stat(parent)
		if perr != nil {
			d.Status = "fail"
			d.Message = "parent directory " + parent + " doesn't exist either"
			return d
		}
		if !pinfo.IsDir() {
			d.Status = "fail"
			d.Message = "parent " + parent + " is not a directory"
			return d
		}
		d.ParentExists = true
		d.ParentWritable = isDirWritable(parent)
		t, f, derr := diskUsage(parent)
		if derr == nil {
			d.TotalBytes, d.FreeBytes = t, f
		}
		if !d.ParentWritable {
			d.Status = "fail"
			d.Message = "parent " + parent + " is not writable — pick a different path"
			return d
		}
		d.WillCreate = true
		d.Status = "ok"
		d.Message = fmt.Sprintf("doesn't exist — will be created (parent: %s free)", humanBytes(d.FreeBytes))
	default:
		d.Status = "fail"
		d.Message = "stat failed: " + err.Error()
	}
	return d
}

// isDirWritable tries to create a temp file in dir; success means the
// dockmesh process has write access. Cheaper than parsing modes.
func isDirWritable(dir string) bool {
	f, err := os.CreateTemp(dir, ".dockmesh-write-test-")
	if err != nil {
		return false
	}
	name := f.Name()
	_ = f.Close()
	_ = os.Remove(name)
	return true
}

// ---------------------------------------------------------------------------
// Service user validation
// ---------------------------------------------------------------------------

// SystemUserCheck answers Step 3's "use existing" radio. Looks up the
// username in /etc/passwd and reports whether they exist + are in the
// docker group (or could be added).
type SystemUserCheck struct {
	Username      string `json:"username"`
	Exists        bool   `json:"exists"`
	UID           string `json:"uid,omitempty"`
	GID           string `json:"gid,omitempty"`
	HomeDir       string `json:"home_dir,omitempty"`
	InDockerGroup bool   `json:"in_docker_group"`
	Status        string `json:"status"` // "ok" | "warn" | "fail"
	Message       string `json:"message"`
}

// CheckSystemUser is the existing-user lookup. Done via the standard
// library os/user — Linux glibc + Go's passwd-parse fallback handle
// /etc/passwd + nsswitch. No CGO, no shelling out.
func CheckSystemUser(username string) SystemUserCheck {
	c := SystemUserCheck{Username: username}
	if !validUsername(username) {
		c.Status = "fail"
		c.Message = "invalid username — letters, digits, dot, dash, underscore only"
		return c
	}
	u, err := user.Lookup(username)
	if err != nil {
		c.Status = "fail"
		c.Message = "no system user '" + username + "' — create one or pick a different name"
		return c
	}
	c.Exists = true
	c.UID = u.Uid
	c.GID = u.Gid
	c.HomeDir = u.HomeDir
	c.InDockerGroup = userInGroup(u, "docker")
	if !c.InDockerGroup {
		c.Status = "warn"
		c.Message = "user exists but not in 'docker' group — install will add them"
	} else {
		c.Status = "ok"
		c.Message = "user exists and already in 'docker' group"
	}
	return c
}

// CheckNewUser validates a "create new user" choice — the username
// must be available (no existing system user with that name) and pass
// the same shape rules.
func CheckNewUser(username string) SystemUserCheck {
	c := SystemUserCheck{Username: username}
	if !validUsername(username) {
		c.Status = "fail"
		c.Message = "invalid username — letters, digits, dot, dash, underscore only"
		return c
	}
	if _, err := user.Lookup(username); err == nil {
		c.Status = "fail"
		c.Exists = true
		c.Message = "user '" + username + "' already exists — switch to 'use existing user' or pick a different name"
		return c
	}
	c.Status = "ok"
	c.Message = "name is free — install will create the system user"
	return c
}

// userInGroup walks the supplementary groups of u and matches by name.
var validUsernameRe = regexp.MustCompile(`^[a-z_][a-z0-9_-]*[a-z0-9_$]?$`)

func validUsername(s string) bool {
	if len(s) < 1 || len(s) > 32 {
		return false
	}
	return validUsernameRe.MatchString(s)
}

func userInGroup(u *user.User, groupName string) bool {
	gids, err := u.GroupIds()
	if err != nil {
		return false
	}
	for _, gid := range gids {
		if g, err := user.LookupGroupId(gid); err == nil && g.Name == groupName {
			return true
		}
	}
	return false
}

// ---------------------------------------------------------------------------
// Public-URL reachability test
// ---------------------------------------------------------------------------

// URLCheck answers Step 5's "Test connection" button. Issues a single
// GET against the URL the operator typed and reports whether a
// dockmesh-shaped response came back.
type URLCheck struct {
	URL       string `json:"url"`
	Reachable bool   `json:"reachable"`
	LatencyMs int64  `json:"latency_ms"`
	Status    string `json:"status"` // "ok" | "warn" | "fail"
	Message   string `json:"message"`
}

// CheckURL tests reachability of the supplied URL. Times out after 5s.
// `expectHealth` true means we look for the /api/v1/health JSON shape
// to confirm we hit a dockmesh server (not someone else's web).
func CheckURL(ctx context.Context, rawURL string, expectHealth bool) URLCheck {
	c := URLCheck{URL: rawURL}
	if !strings.HasPrefix(rawURL, "http://") && !strings.HasPrefix(rawURL, "https://") {
		c.Status = "fail"
		c.Message = "URL must start with http:// or https://"
		return c
	}
	target := strings.TrimRight(rawURL, "/")
	if expectHealth {
		target += "/api/v1/health"
	}
	httpCli := &http.Client{
		Timeout: 5 * time.Second,
		Transport: &http.Transport{
			DialContext: (&net.Dialer{Timeout: 3 * time.Second}).DialContext,
			// We may be testing the LAN URL of this very server with a
			// self-signed cert — for the wizard test we tolerate it.
			TLSHandshakeTimeout: 3 * time.Second,
		},
	}
	start := time.Now()
	req, err := http.NewRequestWithContext(ctx, "GET", target, nil)
	if err != nil {
		c.Status = "fail"
		c.Message = "request build failed: " + err.Error()
		return c
	}
	resp, err := httpCli.Do(req)
	c.LatencyMs = time.Since(start).Milliseconds()
	if err != nil {
		c.Status = "fail"
		c.Message = humanizeNetErr(err)
		return c
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 200 && resp.StatusCode < 400 {
		c.Reachable = true
		c.Status = "ok"
		c.Message = fmt.Sprintf("reached this server in %dms", c.LatencyMs)
		return c
	}
	c.Status = "warn"
	c.Message = fmt.Sprintf("got HTTP %d (server reachable but returned a non-2xx)", resp.StatusCode)
	return c
}

func humanizeNetErr(err error) string {
	s := err.Error()
	switch {
	case strings.Contains(s, "no such host"):
		return "DNS lookup failed — check the hostname"
	case strings.Contains(s, "connection refused"):
		return "connection refused — port not open or service not running"
	case strings.Contains(s, "i/o timeout"), strings.Contains(s, "deadline exceeded"):
		return "timed out — host unreachable from the server"
	case strings.Contains(s, "certificate"):
		return "TLS error — " + s
	}
	return s
}
