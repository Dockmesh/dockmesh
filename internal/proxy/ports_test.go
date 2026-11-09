package proxy

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

const tcp6Table = `  sl  local_address                         remote_address                        st tx_queue rx_queue tr tm->when retrnsmt   uid  timeout inode
   0: 00000000000000000000000000000000:0050 00000000000000000000000000000000:0000 0A 00000000:00000000 00:00000000 00000000     0        0 11111 1
   1: 00000000000000000000000000000000:01BB 00000000000000000000000000000000:0000 0A 00000000:00000000 00:00000000 00000000     0        0 22222 1
   2: 00000000000000000000000000000000:0050 0100007F000000000000000000000000:D431 01 00000000:00000000 00:00000000 00000000     0        0 33333 1
`

func TestCountListeners(t *testing.T) {
	counts := map[int]int{}
	if err := countListeners(strings.NewReader(tcp6Table), counts); err != nil {
		t.Fatal(err)
	}
	if want := (map[int]int{80: 1, 443: 1}); !reflect.DeepEqual(counts, want) {
		t.Fatalf("counts = %v, want %v (established sockets don't count)", counts, want)
	}
}

func TestBoundPorts(t *testing.T) {
	got := boundPorts([]byte(`{"apps":{"http":{"servers":{"a":{"listen":[":80"]},"b":{"listen":[":443"]}}}}}`))
	if want := (map[int]bool{80: true, 443: true}); !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v", got)
	}
	if got := boundPorts([]byte(`null`)); len(got) != 0 {
		t.Fatalf("null config bound %v", got)
	}
}

func TestForeignListeners(t *testing.T) {
	cases := []struct {
		name   string
		counts map[int]int
		bound  map[int]bool
		want   []int
	}{
		{"only caddy", map[int]int{80: 1, 443: 1}, map[int]bool{80: true, 443: true}, nil},
		{"second caddy sharing the port", map[int]int{80: 2}, map[int]bool{80: true}, []int{80}},
		{"other program, caddy failed to bind", map[int]int{80: 1}, nil, []int{80}},
		{"443 used while caddy serves only 80", map[int]int{80: 1, 443: 1}, map[int]bool{80: true}, []int{443}},
		{"free ports", map[int]int{8080: 3}, nil, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := foreignListeners(tc.counts, tc.bound); !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("got %v, want %v", got, tc.want)
			}
		})
	}
}

// One reading is not enough: Caddy holds two sockets during a reload.
func TestCheckPortsNeedsTwoReadings(t *testing.T) {
	dir := t.TempDir()
	table := filepath.Join(dir, "tcp6")
	write := func(s string) {
		if err := os.WriteFile(table, []byte(s), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	old := procNetTCP
	procNetTCP = []string{table}
	t.Cleanup(func() { procNetTCP = old })

	s := NewService(nil, nil, true)
	log := &noticeLog{}
	s.SetNotices(log)
	ctx := context.Background()
	foreign := func() []int {
		p := s.foreignPorts.Load()
		if p == nil {
			return nil
		}
		return *p
	}

	write(tcp6Table)
	s.checkPorts(ctx, true)
	if got := foreign(); len(got) != 0 {
		t.Fatalf("first reading reported %v", got)
	}
	s.checkPorts(ctx, true)
	if got := foreign(); !reflect.DeepEqual(got, []int{80, 443}) {
		t.Fatalf("second reading = %v", got)
	}
	if n := log.got[len(log.got)-1]; !n.active || n.in.Key != noticeKeyPorts || !strings.Contains(n.in.Body, "sudo ss -ltnp") {
		t.Fatalf("notice = %+v", n)
	}

	write("")
	s.checkPorts(ctx, true)
	if got := foreign(); len(got) != 0 {
		t.Fatalf("cleared reading = %v", got)
	}
	if n := log.got[len(log.got)-1]; n.active {
		t.Fatalf("notice still active: %+v", n)
	}
}

func TestCheckPortsIdleWhenProxyOff(t *testing.T) {
	s := NewService(nil, nil, false)
	s.checkPorts(context.Background(), false)
	if p := s.foreignPorts.Load(); p != nil && len(*p) != 0 {
		t.Fatalf("reported %v with the proxy off", *p)
	}
}
