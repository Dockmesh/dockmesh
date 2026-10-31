package docker

import (
	"errors"
	"testing"
)

func TestCheckVolumeName(t *testing.T) {
	tests := []struct {
		name string
		ok   bool
	}{
		{"pgdata", true},
		{"fx-postgres-srv_pgdata", true},
		{"a.b-c_d", true},
		{"/", false},
		{"/etc", false},
		{"/var/run/docker.sock", false},
		{"./data", false},
		{"../x", false},
		{"vol:/x", false},
		{"x", false},
		{"", false},
		{"-leading", false},
	}
	for _, tt := range tests {
		err := CheckVolumeName(tt.name)
		if (err == nil) != tt.ok {
			t.Errorf("%q: err = %v, want ok=%v", tt.name, err, tt.ok)
		}
		if err != nil && !errors.Is(err, ErrInvalidVolumeName) {
			t.Errorf("%q: error not ErrInvalidVolumeName", tt.name)
		}
	}
}
