//go:build !unix

package config

import "os"

// keepOwner keeps the permission bits of old; there are no owners to copy.
func keepOwner(f *os.File, old os.FileInfo) error {
	return f.Chmod(old.Mode().Perm())
}
