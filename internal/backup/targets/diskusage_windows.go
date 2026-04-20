//go:build windows

package targets

// diskUsage stub for non-unix builds. Windows dockmesh-server isn't a
// supported deployment target for v1 (single-binary ships to Linux),
// so cross-building the dmctl CLI for Windows doesn't need actual
// statfs semantics here.
func diskUsage(path string) (int64, int64, error) {
	return 0, 0, nil
}
