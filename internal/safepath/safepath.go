// Package safepath holds helpers for confining file access to a root
// directory. The confinement itself is os.Root: every lookup through it is
// checked by the kernel-level path walk, symlinks included, so there is no
// window between a containment check and the access.
package safepath

import "strings"

// IsRootEscape reports whether err is os.Root refusing a path that would
// leave the root; the os package doesn't export that error.
func IsRootEscape(err error) bool {
	return err != nil && strings.Contains(err.Error(), "escapes from parent")
}
