package layout

import (
	"bytes"
	"embed"
	"errors"
	"fmt"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"text/template"
	"unicode"
	"unicode/utf8"

	"github.com/dockmesh/dockmesh/internal/config"
)

const (
	// UnitFile is the server's systemd unit.
	UnitFile = "/etc/systemd/system/dockmesh.service"
	// PrevUnitFile keeps the unit WriteServerUnit replaced, for
	// RestoreServerUnit. It lives in the root-owned config dir because
	// the service must not be able to plant the unit it is restored to.
	PrevUnitFile = ConfigDir + "/dockmesh.service.prev"

	// AgentExitEnrollRejected is the agent's exit status when the server
	// refused its enrollment token. The agent unit lists it in
	// RestartPreventExitStatus=, so a spent token is not retried forever.
	AgentExitEnrollRejected = 78

	// ServiceGroup is the group the server runs with; it may read
	// ConfigDir.
	ServiceGroup = "dockmesh"

	// UpdatePathUnitFile watches for an update request and starts
	// UpdateServiceUnitFile, the root oneshot that applies it.
	UpdatePathUnitFile    = "/etc/systemd/system/dockmesh-update.path"
	UpdateServiceUnitFile = "/etc/systemd/system/dockmesh-update.service"
	// UpdateStateDir is the oneshot's root-only working directory.
	UpdateStateDir = "/var/lib/dockmesh-update"
)

// UpdateRequestDir is the directory below the data dir where the server
// stages a release and asks the oneshot to install it (config.Paths.UpdateDir).
func UpdateRequestDir(dataDir string) string { return filepath.Join(dataDir, "update") }

//go:embed units/*.tmpl
var unitFS embed.FS

var unitTemplates = template.Must(template.New("").
	Funcs(template.FuncMap{"join": func(s []string) string { return strings.Join(s, " ") }}).
	ParseFS(unitFS, "units/*.tmpl"))

// UnitWriteOptions configure WriteServerUnit and RestoreServerUnit. Empty
// paths default to UnitFile and PrevUnitFile; tests point them elsewhere.
type UnitWriteOptions struct {
	UnitFile     string
	PrevUnitFile string
}

// ErrBadUnitValue marks a value that cannot go into a unit file safely.
var ErrBadUnitValue = errors.New("value not allowed in a systemd unit")

// ServerUnit holds the values of the server's unit. Paths are raw; the
// render step validates and escapes them.
type ServerUnit struct {
	Binary  string
	EnvFile string
	// DataDir must exist when the service starts.
	DataDir string
	// WritablePaths are further directories the service writes: stacks,
	// database and secrets dirs set apart from the data dir, backup roots.
	// A path that does not exist when the service starts is skipped.
	WritablePaths []string
	// SupplementaryGroups are group names or numeric gids.
	SupplementaryGroups []string
}

// AgentUnit holds the values of the agent's unit in the managed layout.
type AgentUnit struct {
	Binary  string
	EnvFile string
	// User is the service account; empty runs the agent as root, which
	// only agents installed with --as-root keep.
	User          string
	DataDir       string
	WritablePaths []string
	// SupplementaryGroups are group names or numeric gids.
	SupplementaryGroups []string
}

type unitValues struct {
	Binary              string
	EnvFile             string
	User                string
	ReadWritePaths      []string
	SupplementaryGroups []string
	EnrollRejectedExit  int
}

// RenderServerUnit returns the server's unit file. A value that is not a
// clean absolute path or a valid group fails the whole render.
func RenderServerUnit(u ServerUnit) ([]byte, error) {
	v, err := commonValues(u.Binary, u.EnvFile, u.DataDir, u.WritablePaths, u.SupplementaryGroups)
	if err != nil {
		return nil, err
	}
	return render("dockmesh.service.tmpl", v)
}

// RenderAgentUnit returns the agent's unit file for the managed layout.
func RenderAgentUnit(u AgentUnit) ([]byte, error) {
	v, err := commonValues(u.Binary, u.EnvFile, u.DataDir, u.WritablePaths, u.SupplementaryGroups)
	if err != nil {
		return nil, err
	}
	if u.User != "" {
		if !accountName.MatchString(u.User) {
			return nil, fmt.Errorf("%w: user %q", ErrBadUnitValue, u.User)
		}
		v.User = u.User
	}
	v.EnrollRejectedExit = AgentExitEnrollRejected
	return render("dockmesh-agent.service.tmpl", v)
}

// RenderUpdateUnits returns the path unit and the oneshot service of the
// root updater for the server binary and data dir. Both values go into
// settings systemd takes as one unquoted word.
func RenderUpdateUnits(binary, dataDir string) (pathUnit, serviceUnit []byte, err error) {
	bin, err := plainPath("binary", binary)
	if err != nil {
		return nil, nil, err
	}
	dir, err := plainPath("data dir", dataDir)
	if err != nil {
		return nil, nil, err
	}
	v := struct{ Binary, DataDir, RequestFile string }{bin, dir, UpdateRequestDir(dir) + "/request"}
	var p, s bytes.Buffer
	if err := unitTemplates.ExecuteTemplate(&p, "dockmesh-update.path.tmpl", v); err != nil {
		return nil, nil, err
	}
	if err := unitTemplates.ExecuteTemplate(&s, "dockmesh-update.service.tmpl", v); err != nil {
		return nil, nil, err
	}
	return p.Bytes(), s.Bytes(), nil
}

func render(name string, v unitValues) ([]byte, error) {
	var buf bytes.Buffer
	if err := unitTemplates.ExecuteTemplate(&buf, name, v); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func commonValues(binary, envFile, dataDir string, writable, groups []string) (unitValues, error) {
	var v unitValues
	var err error
	if v.Binary, err = plainPath("binary", binary); err != nil {
		return v, err
	}
	if v.EnvFile, err = plainPath("environment file", envFile); err != nil {
		return v, err
	}
	if v.ReadWritePaths, err = readWritePaths(dataDir, writable); err != nil {
		return v, err
	}
	for _, g := range groups {
		if !accountName.MatchString(g) {
			if _, err := strconv.ParseUint(g, 10, 32); err != nil {
				return v, fmt.Errorf("%w: group %q", ErrBadUnitValue, g)
			}
		}
		if !slices.Contains(v.SupplementaryGroups, g) {
			v.SupplementaryGroups = append(v.SupplementaryGroups, g)
		}
	}
	return v, nil
}

// accountName is the portable user and group name set of useradd.
var accountName = regexp.MustCompile(`^[a-z_][a-z0-9_-]{0,31}$`)

// checkPath returns the cleaned form of an absolute path that has no
// character systemd's unit parser treats specially: control characters
// end the line or the word, quotes and backslashes change how the rest is
// split. '%' is allowed; the callers escape it.
func checkPath(what, p string) (string, error) {
	bad := func(why string) (string, error) {
		return "", fmt.Errorf("%w: %s %q %s", ErrBadUnitValue, what, p, why)
	}
	switch {
	case p == "":
		return bad("is empty")
	case len(p) > 4096:
		return bad("is too long")
	case !utf8.ValidString(p):
		return bad("is not valid UTF-8")
	case !strings.HasPrefix(p, "/"):
		return bad("is not an absolute path")
	}
	for _, r := range p {
		switch {
		case unicode.IsControl(r), r == unicode.ReplacementChar:
			return bad("contains a control character")
		case r == '"', r == '\'', r == '\\':
			return bad("contains a quote or backslash")
		case unicode.IsSpace(r) && r != ' ':
			return bad("contains whitespace other than a space")
		}
	}
	return filepath.Clean(p), nil
}

// plainPath is a path for a setting systemd takes as one unquoted word
// (EnvironmentFile=, the command of ExecStart=), so spaces and '$' (which
// ExecStart= expands) are refused as well.
func plainPath(what, p string) (string, error) {
	c, err := checkPath(what, p)
	if err != nil {
		return "", err
	}
	if strings.ContainsAny(c, " $") {
		return "", fmt.Errorf("%w: %s %q contains a space or '$'", ErrBadUnitValue, what, p)
	}
	return escapeSpecifiers(c), nil
}

// systemTrees may never become writable for the service: a path below
// one, set through a setting, would undo ProtectSystem= for it.
var systemTrees = []string{"/bin", "/boot", "/dev", "/etc", "/lib", "/lib32", "/lib64", "/proc", "/root", "/run", "/sbin", "/sys", "/usr", "/var/lib/docker"}

// readWritePaths returns one ReadWritePaths= value per directory: the
// data dir first and required, the rest prefixed with '-' so a missing
// one does not stop the service. A path below another is dropped. Every
// value is quoted, so spaces stay inside it. No path may lie in a system
// tree, and paths other than the data dir may not contain it.
func readWritePaths(dataDir string, extra []string) ([]string, error) {
	var dirs []string
	for i, p := range append([]string{dataDir}, extra...) {
		what := "writable path"
		if i == 0 {
			what = "data dir"
		}
		c, err := checkPath(what, p)
		if err != nil {
			return nil, err
		}
		dataDir := ""
		if i > 0 {
			dataDir = dirs[0]
		}
		if err := checkWritable(what, p, c, dataDir); err != nil {
			return nil, err
		}
		dirs = append(dirs, c)
	}
	var out []string
	for i, d := range dirs {
		if coveredBy(d, dirs[:i]) || hasLaterParent(d, dirs[i+1:]) {
			continue
		}
		prefix := "-"
		if i == 0 {
			prefix = ""
		}
		out = append(out, `"`+prefix+escapeSpecifiers(d)+`"`)
	}
	return out, nil
}

// checkLocation refuses a writable path c (spelled p in the error) in a
// system tree, or one containing dataDir when that is set.
func checkLocation(what, p, c, dataDir string) error {
	for _, sys := range systemTrees {
		if within(sys, c) {
			return fmt.Errorf("%w: %s %q lies in %s", ErrBadUnitValue, what, p, sys)
		}
	}
	if dataDir != "" && c != dataDir && within(c, dataDir) {
		return fmt.Errorf("%w: %s %q contains the data dir", ErrBadUnitValue, what, p)
	}
	return nil
}

// CheckWritablePath applies the rules the unit writer uses for a writable
// path taken from a setting, so a value it would refuse is refused when
// it is saved. dataDir may be empty when it is not known.
func CheckWritablePath(p, dataDir string) error {
	const what = "writable path"
	c, err := checkPath(what, p)
	if err != nil {
		return err
	}
	if dataDir != "" {
		dataDir = filepath.Clean(dataDir)
	}
	if err := checkWritable(what, p, c, dataDir); err != nil {
		return err
	}
	// Like the unit writer: systemd follows symlinks when it mounts the
	// path. A path that does not exist yet is checked as written.
	r, err := filepath.EvalSymlinks(c)
	if err != nil {
		return nil
	}
	if dataDir != "" {
		if rd, err := filepath.EvalSymlinks(dataDir); err == nil {
			dataDir = rd
		}
	}
	return checkWritable(what, p, r, dataDir)
}

// checkWritable refuses the root directory and what checkLocation refuses.
func checkWritable(what, p, c, dataDir string) error {
	if c == "/" {
		return fmt.Errorf("%w: %s is the root directory", ErrBadUnitValue, what)
	}
	return checkLocation(what, p, c, dataDir)
}

// hasLaterParent reports whether an entry of dirs is a strict parent of d.
func hasLaterParent(d string, dirs []string) bool {
	for _, p := range dirs {
		if p != d && within(p, d) {
			return true
		}
	}
	return false
}

func coveredBy(d string, dirs []string) bool {
	for _, p := range dirs {
		if within(p, d) {
			return true
		}
	}
	return false
}

// within reports whether p is root or lies below it; both are clean.
func within(root, p string) bool {
	return p == root || strings.HasPrefix(p, strings.TrimSuffix(root, "/")+"/")
}

func escapeSpecifiers(s string) string { return strings.ReplaceAll(s, "%", "%%") }

// ServerWritablePaths returns the directories of p the server writes
// outside its data dir when the per-path variables move them, followed
// by extra.
func ServerWritablePaths(p config.Paths, extra ...string) []string {
	out := []string{p.StacksRoot}
	for _, f := range []string{p.DBPath, p.SecretsPath, p.SecretsKeyPath, p.AuditGenesisPath} {
		if f != "" {
			out = append(out, filepath.Dir(f))
		}
	}
	return append(out, extra...)
}

// DefaultDockerSocket is where the Docker SDK connects without DOCKER_HOST.
const DefaultDockerSocket = "/var/run/docker.sock"

// DockerSocketPath is the socket a service with env connects to, empty
// when DOCKER_HOST names something other than a unix socket.
func DockerSocketPath(env map[string]string) string {
	host := env["DOCKER_HOST"]
	if host == "" {
		return DefaultDockerSocket
	}
	if p, ok := strings.CutPrefix(host, "unix://"); ok && filepath.IsAbs(p) {
		return filepath.Clean(p)
	}
	return ""
}
