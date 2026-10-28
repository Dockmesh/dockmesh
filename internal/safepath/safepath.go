// Package safepath resolves user- or archive-supplied paths below a root
// directory so that neither ".." nor a symlink can lead outside it.
//
// Lexical checks alone are not enough: a symlink inside a Docker volume, a
// git checkout or a backup archive can point at "/etc", and following it
// would read or write the host filesystem. Resolve follows every symlink
// that exists on disk and then proves the result is still inside the root.
package safepath

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// ErrEscape is returned when a path resolves outside its root.
var ErrEscape = errors.New("path escapes its root")

// Resolve returns the absolute, symlink-free path for rel below root. rel is
// interpreted relative to root; a leading "/" and ".." segments can't climb
// above it. Components that don't exist yet are allowed (for files about to
// be created) and are appended after the existing part has been resolved.
func Resolve(root, rel string) (string, error) {
	realRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		return "", err
	}
	realRoot, err = filepath.Abs(realRoot)
	if err != nil {
		return "", err
	}
	// Cleaning against "/" first keeps ".." from climbing above root.
	clean := filepath.Clean(string(filepath.Separator) + filepath.FromSlash(rel))
	candidate := filepath.Join(realRoot, clean)

	existing, rest := candidate, ""
	for {
		if _, err := os.Lstat(existing); err == nil {
			break
		} else if !errors.Is(err, fs.ErrNotExist) {
			return "", err
		}
		parent := filepath.Dir(existing)
		if parent == existing {
			return "", ErrEscape
		}
		rest = filepath.Join(filepath.Base(existing), rest)
		existing = parent
	}
	resolved, err := filepath.EvalSymlinks(existing)
	if err != nil {
		return "", err
	}
	full := filepath.Join(resolved, rest)
	if !Within(realRoot, full) {
		return "", ErrEscape
	}
	return full, nil
}

// Within reports whether p is root or lies below it. Both must be
// absolute and already resolved; no filesystem access.
func Within(root, p string) bool {
	rel, err := filepath.Rel(root, p)
	if err != nil {
		return false
	}
	return rel == "." || (rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)))
}
