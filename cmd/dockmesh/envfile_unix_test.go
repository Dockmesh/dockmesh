//go:build unix

package main

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestReadTrustedEnvFile(t *testing.T) {
	tests := []struct {
		name    string
		mode    os.FileMode
		chownTo int
		wantErr bool
	}{
		{name: "owner only", mode: 0o600},
		{name: "world readable", mode: 0o644},
		{name: "group writable", mode: 0o660, wantErr: true},
		{name: "world writable", mode: 0o602, wantErr: true},
		{name: "other owner", mode: 0o600, chownTo: 12345, wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := filepath.Join(t.TempDir(), "dockmesh.env")
			if err := os.WriteFile(p, []byte("DOCKMESH_DB_PATH=/x\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			if err := os.Chmod(p, tt.mode); err != nil {
				t.Fatal(err)
			}
			if tt.chownTo != 0 {
				if os.Getuid() != 0 {
					t.Skip("changing the owner needs root")
				}
				if err := os.Chown(p, tt.chownTo, -1); err != nil {
					t.Fatal(err)
				}
			}
			data, err := readTrustedEnvFile(p)
			if tt.wantErr {
				if !errors.Is(err, errUntrustedEnvFile) {
					t.Fatalf("err = %v, want errUntrustedEnvFile", err)
				}
				return
			}
			if err != nil || string(data) != "DOCKMESH_DB_PATH=/x\n" {
				t.Fatalf("data %q, err %v", data, err)
			}
		})
	}
}
