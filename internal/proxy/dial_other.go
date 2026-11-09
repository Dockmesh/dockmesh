//go:build !linux

package proxy

import (
	"context"
	"fmt"
	"io/fs"
	"net"
	"os"
)

// dialSocket refuses anything but a socket at path.
func dialSocket(ctx context.Context, path string) (net.Conn, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if info.Mode()&fs.ModeSocket == 0 {
		return nil, fmt.Errorf("%s is not a socket", path)
	}
	var d net.Dialer
	return d.DialContext(ctx, "unix", path)
}
