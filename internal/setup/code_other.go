//go:build !unix

package setup

import (
	"errors"
	"io/fs"
)

func trustCodeFile(fs.FileInfo) error { return nil }

func writeRootFile(string, []byte, int) error {
	return errors.New("setup codes are only supported on Linux and macOS")
}
