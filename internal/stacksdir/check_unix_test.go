//go:build unix

package stacksdir

import (
	"testing"

	"golang.org/x/sys/unix"
)

func TestProtectedDev(t *testing.T) {
	var root, tmp unix.Stat_t
	if err := unix.Stat("/", &root); err != nil {
		t.Fatal(err)
	}
	if err := unix.Stat(t.TempDir(), &tmp); err != nil {
		t.Fatal(err)
	}
	other := uint64(root.Dev) + 1  //nolint:unconvert // the types differ per OS
	for other == uint64(tmp.Dev) { //nolint:unconvert // the types differ per OS
		other++
	}
	denied := map[fileID]string{{dev: uint64(tmp.Dev), ino: 1}: "/var"} //nolint:unconvert // the types differ per OS
	var r Rules
	tests := []struct {
		name string
		dev  uint64
		want bool
	}{
		{"device of /", uint64(root.Dev), true},            //nolint:unconvert // the types differ per OS
		{"device of a refused dir", uint64(tmp.Dev), true}, //nolint:unconvert // the types differ per OS
		{"another disk", other, false},
	}
	for _, tt := range tests {
		if got := r.protectedDev(tt.dev, denied); got != tt.want {
			t.Errorf("%s: protectedDev = %v, want %v", tt.name, got, tt.want)
		}
	}
}
