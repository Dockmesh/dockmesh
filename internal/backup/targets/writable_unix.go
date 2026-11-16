//go:build unix

package targets

import "golang.org/x/sys/unix"

// canCreateIn asks the kernel whether the service may create entries in
// dir; a read-only mount answers EROFS.
func canCreateIn(dir string) error {
	return unix.Access(dir, unix.W_OK|unix.X_OK)
}
