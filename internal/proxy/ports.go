package proxy

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net"
	"os"
	"slices"
	"strconv"
	"strings"

	"github.com/dockmesh/dockmesh/internal/i18n"
	"github.com/dockmesh/dockmesh/internal/notices"
)

const noticeKeyPorts = "proxy.port-conflict"

// tcpListen is the "st" value of a listening socket in /proc/net/tcp.
const tcpListen = "0A"

var (
	proxyPorts = []int{80, 443}
	// procNetTCP is a variable so tests can supply their own tables.
	procNetTCP = []string{"/proc/net/tcp", "/proc/net/tcp6"}
)

// countListeners adds the listening sockets per local port of one
// /proc/net/tcp table to counts.
func countListeners(r io.Reader, counts map[int]int) error {
	sc := bufio.NewScanner(r)
	for sc.Scan() {
		f := strings.Fields(sc.Text())
		if len(f) < 4 || f[3] != tcpListen {
			continue
		}
		_, hexPort, ok := strings.Cut(f[1], ":")
		if !ok {
			continue
		}
		port, err := strconv.ParseInt(hexPort, 16, 32)
		if err != nil {
			continue
		}
		counts[int(port)]++
	}
	return sc.Err()
}

func readListeners() (map[int]int, error) {
	counts := map[int]int{}
	for _, p := range procNetTCP {
		f, err := os.Open(p)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			return nil, err
		}
		err = countListeners(f, counts)
		_ = f.Close()
		if err != nil {
			return nil, err
		}
	}
	return counts, nil
}

// boundPorts returns the ports the given running Caddy config listens on.
func boundPorts(running []byte) map[int]bool {
	var cfg struct {
		Apps struct {
			HTTP struct {
				Servers map[string]struct {
					Listen []string `json:"listen"`
				} `json:"servers"`
			} `json:"http"`
		} `json:"apps"`
	}
	bound := map[int]bool{}
	if json.Unmarshal(running, &cfg) != nil {
		return bound
	}
	for _, srv := range cfg.Apps.HTTP.Servers {
		for _, l := range srv.Listen {
			if _, p, err := net.SplitHostPort(l); err == nil {
				if n, err := strconv.Atoi(p); err == nil {
					bound[n] = true
				}
			}
		}
	}
	return bound
}

// foreignListeners returns the proxy ports with more listening sockets
// than the managed Caddy explains. Caddy holds one socket per port it
// binds. Without root the owning process of a socket can't be read, so
// the count is what tells them apart.
func foreignListeners(counts map[int]int, bound map[int]bool) []int {
	var out []int
	for _, p := range proxyPorts {
		want := 0
		if bound[p] {
			want = 1
		}
		if counts[p] > want {
			out = append(out, p)
		}
	}
	return out
}

// checkPorts updates the foreign-listener state. A reading only counts
// when the next one agrees, because Caddy briefly holds two sockets per
// port while it reloads.
func (s *Service) checkPorts(ctx context.Context, wanted bool) {
	var foreign []int
	if s.Enabled() || wanted {
		counts, err := readListeners()
		if err != nil {
			slog.Debug("proxy: reading listening sockets failed", "err", err)
			return
		}
		var bound map[int]bool
		if running, err := runningConfig(ctx); err == nil {
			bound = boundPorts(running)
		}
		foreign = foreignListeners(counts, bound)
	}
	confirmed := len(foreign) == 0 || slices.Equal(foreign, s.foreignSeen)
	s.foreignSeen = foreign
	if confirmed {
		s.setForeignPorts(ctx, foreign)
	}
}

// PortList formats ports for a message, as "80, 443".
func PortList(ports []int) string {
	names := make([]string, len(ports))
	for i, p := range ports {
		names[i] = strconv.Itoa(p)
	}
	return strings.Join(names, ", ")
}

func (s *Service) setForeignPorts(ctx context.Context, ports []int) {
	s.foreignPorts.Store(&ports)
	if s.notices == nil {
		return
	}
	in := notices.RaiseInput{Key: noticeKeyPorts, Audience: notices.Admins(), Severity: notices.SevWarning, Link: "/proxy"}
	if len(ports) > 0 {
		list := PortList(ports)
		in.Title = i18n.Msg("notice.proxy_ports.title")
		in.Body = i18n.Msg("notice.proxy_ports.body", list, ports[0])
	}
	if err := s.notices.Set(ctx, len(ports) > 0, in); err != nil {
		slog.Warn("proxy: update port conflict notice failed", "err", err)
	}
}
