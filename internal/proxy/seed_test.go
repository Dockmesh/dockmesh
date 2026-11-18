package proxy

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/dockmesh/dockmesh/internal/db/dbtest"
)

func TestSeedRoute(t *testing.T) {
	conn, err := dbtest.Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	ctx := context.Background()
	for _, host := range []string{"localhost", "127.0.0.1", "bad host", ""} {
		if err := SeedRoute(ctx, conn, host, 8080); err == nil {
			t.Errorf("host %q was accepted", host)
		}
	}
	if err := SeedRoute(ctx, conn, "dm.example.org", 2019); err == nil {
		t.Error("an upstream on the admin port was accepted")
	}
	if err := SeedRoute(ctx, conn, "dm.example.org", 8080); err != nil {
		t.Fatal(err)
	}
	routes, err := NewService(conn, nil, true).ListRoutes(ctx)
	if err != nil || len(routes) != 1 || routes[0].Upstream != "127.0.0.1:8080" || !routes[0].Enabled || routes[0].TLSMode != "auto" {
		t.Fatalf("routes %+v %v", routes, err)
	}
}

func TestBusyPorts(t *testing.T) {
	dir := t.TempDir()
	table := filepath.Join(dir, "tcp")
	// 0050 = 80 listening, 01BB = 443 established, 1F90 = 8080 listening.
	data := "  sl  local_address rem_address   st\n" +
		"   0: 00000000:0050 00000000:0000 0A\n" +
		"   1: 0100007F:01BB 0100007F:9C40 01\n" +
		"   2: 00000000:1F90 00000000:0000 0A\n"
	if err := os.WriteFile(table, []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}
	prev := procNetTCP
	procNetTCP = []string{table}
	defer func() { procNetTCP = prev }()
	got, err := BusyPorts()
	if err != nil || len(got) != 1 || got[0] != 80 {
		t.Fatalf("BusyPorts = %v %v", got, err)
	}
}
