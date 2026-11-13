//go:build !linux

package stackhelper

import (
	"errors"
	"io/fs"
)

var errUnsupported = errors.New("removing files owned by another user is only supported on Linux")

func fileID(fs.FileInfo) (string, error) { return "", errUnsupported }

func mountBelow(string) (string, error) { return "", errUnsupported }

func canDeleteIn(string) error { return nil }
