//go:build integration

package integration

import (
	"net"
	"strconv"
)

func splitAddr(addr string) (string, int) {
	h, p, err := net.SplitHostPort(addr)
	if err != nil {
		return addr, 0
	}
	n, _ := strconv.Atoi(p)
	return h, n
}
