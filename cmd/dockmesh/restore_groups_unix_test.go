//go:build unix

package main

import (
	"os/user"
	"path/filepath"
	"slices"
	"strconv"
	"testing"
)

// The restore child gets the docker group the unit gets, never gid 0.
func TestServiceDockerGIDs(t *testing.T) {
	grp, err := user.LookupGroup("docker")
	if err != nil {
		t.Skip("no docker group on this host")
	}
	want, err := strconv.ParseUint(grp.Gid, 10, 32)
	if err != nil {
		t.Fatal(err)
	}
	gids := serviceDockerGIDs(filepath.Join(t.TempDir(), "missing.env"))
	if !slices.Contains(gids, uint32(want)) {
		t.Errorf("gids %v lack docker %d", gids, want)
	}
	if slices.Contains(gids, 0) {
		t.Errorf("gids %v contain 0", gids)
	}
}
