package proxy

import (
	"context"
	"fmt"
	"net"

	"golang.org/x/sys/unix"
)

// dialSocket connects to the socket at path without following a symlink
// at its last component: the directory is writable from inside the proxy
// container, so a compromised Caddy could otherwise point Dockmesh at
// another socket. The O_PATH descriptor pins what was checked, which a
// check on the path followed by a connect to it would not.
func dialSocket(ctx context.Context, path string) (net.Conn, error) {
	fd, err := unix.Open(path, unix.O_PATH|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", path, err)
	}
	defer unix.Close(fd)
	var st unix.Stat_t
	if err := unix.Fstat(fd, &st); err != nil {
		return nil, fmt.Errorf("stat %s: %w", path, err)
	}
	if st.Mode&unix.S_IFMT != unix.S_IFSOCK {
		return nil, fmt.Errorf("%s is not a socket", path)
	}
	var d net.Dialer
	return d.DialContext(ctx, "unix", fmt.Sprintf("/proc/self/fd/%d", fd))
}
