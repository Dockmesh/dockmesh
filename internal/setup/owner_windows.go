//go:build windows

package setup

import (
	"errors"
	"io/fs"
)

// RootOwnedAncestors is not supported on Windows.
func RootOwnedAncestors(string) error { return errors.New("not supported on windows") }

// FileOwner is not supported on Windows.
func FileOwner(fs.FileInfo) (uint32, bool) { return 0, false }

func ownedBySelf(fs.FileInfo) bool { return true }
