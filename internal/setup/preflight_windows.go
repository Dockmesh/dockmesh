//go:build windows

package setup

import "errors"

// diskUsage on Windows is a stub — the install wizard targets Linux
// servers and we cross-compile the linux/amd64 binary from this dev
// machine. Returning an error here lets the wizard show "—" for the
// disk row when running on a Windows dev box, instead of refusing
// to build.
func diskUsage(path string) (int64, int64, error) {
	return 0, 0, errors.New("disk usage check not supported on windows")
}
