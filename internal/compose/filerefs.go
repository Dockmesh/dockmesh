package compose

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/compose-spec/compose-go/v2/dotenv"
	"github.com/compose-spec/compose-go/v2/loader"
	"github.com/compose-spec/compose-go/v2/types"
	"gopkg.in/yaml.v3"
)

// Limits for the files a stack load reads: the compose files, their
// include and extends targets, and env and label files.
const (
	maxComposeFileSize  = 4 << 20
	maxComposeReadTotal = 32 << 20
	maxComposeFiles     = 128
)

// ErrComposeFileRef marks a compose file that references a file Dockmesh
// refuses to read: outside the stack folder, not a regular file, or too
// large.
var ErrComposeFileRef = errors.New("compose file reference refused")

func refused(format string, args ...any) error {
	return fmt.Errorf("%w: %s", ErrComposeFileRef, fmt.Sprintf(format, args...))
}

// stackFiles reads files below one stack folder. Paths are checked
// lexically first so the error names the path the user wrote; the os.Root
// then refuses symlinks that leave the folder.
type stackFiles struct {
	dir   string
	root  *os.Root
	files int
	total int64
}

func openStackFiles(dir string) (*stackFiles, error) {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return nil, err
	}
	root, err := os.OpenRoot(abs)
	if err != nil {
		return nil, err
	}
	return &stackFiles{dir: abs, root: root}, nil
}

func (s *stackFiles) Close() { _ = s.root.Close() }

// rel also refuses a path that isn't in clean form: compose-go keeps
// absolute paths as written, and the kernel follows a symlink before it
// applies a later "..", which a lexical check would have removed first.
func (s *stackFiles) rel(p string) (string, error) {
	if !filepath.IsAbs(p) {
		return "", refused("%s: path is not absolute", p)
	}
	if filepath.Clean(p) != p {
		return "", refused("%s: path contains . or .. elements", p)
	}
	rel, err := filepath.Rel(s.dir, p)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", refused("%s is outside the stack folder", p)
	}
	return rel, nil
}

// stat returns the file info of p inside the folder, following symlinks
// that stay inside, and refuses anything but a regular file within the
// size limit. A FIFO or device is refused here, before an open could
// block or stream without end.
func (s *stackFiles) stat(p string) (string, fs.FileInfo, error) {
	rel, err := s.rel(p)
	if err != nil {
		return "", nil, err
	}
	fi, err := s.root.Stat(rel)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return "", nil, err
		}
		return "", nil, refused("%s: %v", p, err)
	}
	if !fi.Mode().IsRegular() {
		return "", nil, refused("%s is not a regular file", p)
	}
	if fi.Size() > maxComposeFileSize {
		return "", nil, refused("%s is larger than %d MiB", p, maxComposeFileSize>>20)
	}
	return rel, fi, nil
}

// checkDir refuses p unless it is a directory inside the folder.
func (s *stackFiles) checkDir(p string) error {
	rel, err := s.rel(p)
	if err != nil {
		return err
	}
	fi, err := s.root.Stat(rel)
	if err != nil {
		return refused("%s: %v", p, err)
	}
	if !fi.IsDir() {
		return refused("%s is not a directory", p)
	}
	return nil
}

func (s *stackFiles) count(n int64) error {
	s.files++
	s.total += n
	if s.files > maxComposeFiles {
		return refused("more than %d referenced files", maxComposeFiles)
	}
	if s.total > maxComposeReadTotal {
		return refused("referenced files exceed %d MiB in total", maxComposeReadTotal>>20)
	}
	return nil
}

// check verifies p like read does without reading it, for files that
// compose-go opens itself afterwards.
func (s *stackFiles) check(p string) error {
	_, fi, err := s.stat(p)
	if err != nil {
		return err
	}
	return s.count(fi.Size())
}

func (s *stackFiles) read(p string) ([]byte, error) {
	rel, _, err := s.stat(p)
	if err != nil {
		return nil, err
	}
	f, err := s.root.Open(rel)
	if err != nil {
		return nil, refused("%s: %v", p, err)
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if !fi.Mode().IsRegular() {
		return nil, refused("%s is not a regular file", p)
	}
	b, err := io.ReadAll(io.LimitReader(f, maxComposeFileSize+1))
	if err != nil {
		return nil, err
	}
	if len(b) > maxComposeFileSize {
		return nil, refused("%s is larger than %d MiB", p, maxComposeFileSize>>20)
	}
	if err := s.count(int64(len(b))); err != nil {
		return nil, err
	}
	return b, nil
}

// refWalker follows include and extends.file the way compose-go does, but
// reads every file through stackFiles, so compose-go only ever opens files
// that were checked before. Env and label files of services are checked
// on the loaded project (checkProjectFileRefs), where compose-go has
// already resolved their final paths.
type refWalker struct {
	files *stackFiles
	name  string
	seen  map[string]bool
}

// walk checks one compose file loaded with loaderDir as the directory
// relative include and extends paths start from. nested is set for files
// pulled in through include: compose-go resolves their relative
// project_directory and env_file against the process working directory,
// so those are refused there.
func (w *refWalker) walk(ctx context.Context, file, loaderDir string, env types.Mapping, nested bool) error {
	key := walkKey(file, loaderDir, env)
	if w.seen[key] {
		return nil
	}
	w.seen[key] = true

	model, err := w.model(ctx, file, loaderDir, env)
	if err != nil {
		return err
	}
	if err := w.walkExtends(ctx, model, loaderDir, env); err != nil {
		return err
	}
	includes, ok := model["include"].([]any)
	if !ok && model["include"] != nil {
		return refused("%s: include must be a list", file)
	}
	for _, inc := range includes {
		entry, ok := inc.(map[string]any)
		if !ok {
			return refused("%s: include entry must be a path or a mapping", file)
		}
		// compose-go matches these keys case-insensitively and ignores
		// unknown ones; only the exact names are read here, so any other
		// key is refused.
		for k := range entry {
			if !includeKeys[k] {
				return refused("%s: unsupported include key %q", file, k)
			}
		}
		// compose-go casts numbers and booleans to strings; only plain
		// strings are accepted so the walker reads the values it uses.
		paths, err := strictStrings(file, "include path", entry["path"])
		if err != nil {
			return err
		}
		if len(paths) == 0 {
			return refused("%s: include entry without a path", file)
		}
		envFiles, err := strictStrings(file, "include env_file", entry["env_file"])
		if err != nil {
			return err
		}
		pd, err := strictStrings(file, "include project_directory", entry["project_directory"])
		if err != nil {
			return err
		}
		if len(pd) > 1 || (entry["project_directory"] != nil && len(pd) == 0) {
			return refused("%s: include project_directory must be one path", file)
		}
		for i, p := range paths {
			if err := noHome(file, p); err != nil {
				return err
			}
			if !filepath.IsAbs(p) {
				paths[i] = filepath.Join(loaderDir, p)
			}
		}
		for _, p := range paths {
			if _, err := w.files.rel(p); err != nil {
				return err
			}
		}
		var projectDir string
		if len(pd) == 1 {
			projectDir = pd[0]
		}
		if err := noHome(file, projectDir); err != nil {
			return err
		}
		switch {
		case projectDir == "":
			projectDir = filepath.Dir(paths[0])
		case !filepath.IsAbs(projectDir):
			if nested {
				return refused("%s: relative project_directory in an included file", file)
			}
			projectDir = filepath.Join(loaderDir, projectDir)
		}
		// compose-go bases nested paths on the parent of a project
		// directory that doesn't exist, so it must be a real directory
		// inside the folder for both sides to agree.
		if err := w.files.checkDir(projectDir); err != nil {
			return err
		}
		incEnv, err := w.includeEnv(file, projectDir, loaderDir, envFiles, env, nested)
		if err != nil {
			return err
		}
		for _, p := range paths {
			if err := w.walk(ctx, p, projectDir, incEnv, true); err != nil {
				return err
			}
		}
	}
	return nil
}

// includeEnv returns the environment compose-go interpolates an included
// file with: the parent environment plus the include's env files, or the
// .env next to the included project when none are listed.
func (w *refWalker) includeEnv(file, projectDir, loaderDir string, envFiles []string, env types.Mapping, nested bool) (types.Mapping, error) {
	var files []string
	if len(envFiles) == 0 {
		dotEnv := filepath.Join(projectDir, ".env")
		// compose-go reads the .env of an included project when it exists
		// and is not a directory; a symlink leaving the folder is refused
		// by check below.
		if fi, err := os.Stat(dotEnv); err == nil && !fi.IsDir() {
			files = append(files, dotEnv)
		}
	}
	for _, f := range envFiles {
		if f == "/dev/null" {
			continue
		}
		if err := noHome(file, f); err != nil {
			return nil, err
		}
		if !filepath.IsAbs(f) {
			if nested {
				return nil, refused("%s: relative include env_file in an included file", file)
			}
			f = filepath.Join(loaderDir, f)
		}
		files = append(files, f)
	}
	for _, f := range files {
		if err := w.files.check(f); err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				return nil, fmt.Errorf("couldn't find env file: %s", f)
			}
			return nil, err
		}
	}
	fromFiles, err := dotenv.GetEnvFromFile(env, files)
	if err != nil {
		return nil, err
	}
	return env.Clone().Merge(fromFiles), nil
}

// walkExtends follows extends.file of every service and job. A base file
// resolves its own extends.file against its directory.
func (w *refWalker) walkExtends(ctx context.Context, model map[string]any, loaderDir string, env types.Mapping) error {
	for _, section := range []string{"services", "jobs"} {
		entries, _ := model[section].(map[string]any)
		for _, name := range slices.Sorted(maps.Keys(entries)) {
			svc, _ := entries[name].(map[string]any)
			ext, _ := svc["extends"].(map[string]any)
			for k := range ext {
				if k != "file" && k != "service" {
					return refused("%s: unsupported extends key %q", name, k)
				}
			}
			if ext["file"] == nil {
				continue
			}
			ref, ok := ext["file"].(string)
			if !ok {
				return refused("%s: extends.file must be a string", name)
			}
			if ref == "" {
				continue
			}
			if err := noHome(name, ref); err != nil {
				return err
			}
			if !filepath.IsAbs(ref) {
				ref = filepath.Join(loaderDir, ref)
			}
			key := walkKey(ref, "extends", env)
			if w.seen[key] {
				continue
			}
			w.seen[key] = true
			base, err := w.model(ctx, ref, filepath.Dir(ref), env)
			if err != nil {
				return err
			}
			if err := w.walkExtends(ctx, base, filepath.Dir(ref), env); err != nil {
				return err
			}
		}
	}
	return nil
}

// model reads file through the checked reader and has compose-go parse
// and interpolate it on its own, without following anything.
func (w *refWalker) model(ctx context.Context, file, workingDir string, env types.Mapping) (map[string]any, error) {
	content, err := w.files.read(file)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, fmt.Errorf("%s: %w", file, err)
		}
		return nil, err
	}
	if err := checkPlainYAML(content); err != nil {
		return nil, err
	}
	model, err := loader.LoadModelWithContext(ctx, types.ConfigDetails{
		WorkingDir:  workingDir,
		ConfigFiles: []types.ConfigFile{{Filename: file, Content: content}},
		Environment: env.Clone(),
	}, func(o *loader.Options) {
		o.SkipValidation = true
		o.SkipNormalization = true
		o.SkipConsistencyCheck = true
		o.SkipInclude = true
		o.SkipExtends = true
		o.SkipDefaultValues = true
		o.ResolvePaths = false
		o.SetProjectName(w.name, true)
	})
	if err != nil {
		return nil, err
	}
	if err := w.checkServices(file, workingDir, model); err != nil {
		return nil, err
	}
	return model, nil
}

// checkServices refuses empty service definitions and develop.watch
// paths outside the folder. compose-go leaves an extends of an empty
// service unresolved, and a parent file then applies it again with its
// own environment, past what the walker followed. Watch paths are
// resolved through symlinks while loading.
func (w *refWalker) checkServices(file, workingDir string, model map[string]any) error {
	for _, section := range []string{"services", "jobs"} {
		entries, _ := model[section].(map[string]any)
		for _, name := range slices.Sorted(maps.Keys(entries)) {
			svc, ok := entries[name].(map[string]any)
			if !ok {
				return refused("%s: %s.%s is empty", file, section, name)
			}
			dev, _ := svc["develop"].(map[string]any)
			watch, _ := dev["watch"].([]any)
			for _, item := range watch {
				rule, _ := item.(map[string]any)
				p, ok := rule["path"].(string)
				if !ok {
					continue
				}
				if err := noHome(file, p); err != nil {
					return err
				}
				if !filepath.IsAbs(p) {
					p = filepath.Join(workingDir, p)
				}
				if _, err := w.files.rel(p); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

// noHome refuses a path starting with ~: compose-go expands it to the
// home directory of the Dockmesh process in some places and not in others.
func noHome(file, p string) error {
	if strings.HasPrefix(p, "~") {
		return refused("%s: path %s starts with ~", file, p)
	}
	return nil
}

func walkKey(file, dir string, env types.Mapping) string {
	h := sha256.New()
	fmt.Fprintf(h, "%d:%s%d:%s", len(file), file, len(dir), dir)
	for _, k := range slices.Sorted(maps.Keys(env)) {
		fmt.Fprintf(h, "%d:%s%d:%s", len(k), k, len(env[k]), env[k])
	}
	return string(h.Sum(nil))
}

var includeKeys = map[string]bool{"path": true, "env_file": true, "project_directory": true}

// strictStrings returns v as a list of strings: absent, one string, or a
// list of strings. Any other type is refused.
func strictStrings(file, field string, v any) ([]string, error) {
	switch t := v.(type) {
	case nil:
		return nil, nil
	case string:
		return []string{t}, nil
	case []any:
		out := make([]string, 0, len(t))
		for _, e := range t {
			s, ok := e.(string)
			if !ok {
				return nil, refused("%s: %s must be a string", file, field)
			}
			out = append(out, s)
		}
		return out, nil
	}
	return nil, refused("%s: %s must be a string", file, field)
}

// checkProjectFileRefs checks the env and label files compose-go resolved
// for every service and pre_start hook, mirroring when compose-go would
// read them: a missing optional env file is skipped.
func checkProjectFileRefs(files *stackFiles, proj *types.Project) error {
	checkSpec := func(spec *types.ContainerSpec) error {
		for _, ef := range spec.EnvFiles {
			if err := files.check(ef.Path); err != nil {
				if errors.Is(err, fs.ErrNotExist) {
					if ef.Required {
						return fmt.Errorf("env file %s not found", ef.Path)
					}
					continue
				}
				return err
			}
		}
		for _, lf := range spec.LabelFiles {
			if err := files.check(lf); err != nil {
				if errors.Is(err, fs.ErrNotExist) {
					return fmt.Errorf("label file %s not found", lf)
				}
				return err
			}
		}
		return nil
	}
	for _, name := range slices.Sorted(maps.Keys(proj.Services)) {
		svc := proj.Services[name]
		if err := checkSpec(&svc.ContainerSpec); err != nil {
			return err
		}
		for i := range svc.PreStart {
			if err := checkSpec(&svc.PreStart[i].ContainerSpec); err != nil {
				return err
			}
		}
	}
	return nil
}

// checkPlainYAML refuses a compose file that doesn't decode as plain YAML
// before compose-go sees it: compose's merge-key handling never finishes
// on an anchor that merges itself, and grows until the process dies.
// Files with several YAML documents are refused as well: compose-go
// follows include and extends per document, so a later document could
// hide a reference from the merged model the walker checks.
func checkPlainYAML(raw []byte) error {
	dec := yaml.NewDecoder(bytes.NewReader(raw))
	docs := 0
	for {
		var doc any
		err := dec.Decode(&doc)
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("invalid compose file: %w", err)
		}
		docs++
		if docs > 1 {
			return refused("compose files with more than one YAML document are not supported")
		}
	}
}
