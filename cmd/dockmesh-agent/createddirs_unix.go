//go:build unix

package main

import (
	"bufio"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
)

// createdDirsFile in root's state dir lists the data and stacks dirs root
// created, so `dockmesh-agent uninstall --purge` removes only those
// directories themselves and leaves ones an admin prepared.
const createdDirsFile = "created-dirs"

// recordCreatedDir adds dir to the list; the caller holds lockStacksDir.
func recordCreatedDir(stateDir, dir string) error {
	if slices.Contains(readCreatedDirs(stateDir), dir) {
		return nil
	}
	f, err := os.OpenFile(filepath.Join(stateDir, createdDirsFile), os.O_WRONLY|os.O_APPEND|os.O_CREATE|syscall.O_NOFOLLOW, 0o600) // #nosec G304 -- root's state dir
	if err != nil {
		return err
	}
	_, err = f.WriteString(dir + "\n")
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	return err
}

func readCreatedDirs(stateDir string) []string {
	f, err := os.OpenFile(filepath.Join(stateDir, createdDirsFile), os.O_RDONLY|syscall.O_NOFOLLOW, 0) // #nosec G304 -- root's state dir
	if err != nil {
		return nil
	}
	defer f.Close()
	var out []string
	sc := bufio.NewScanner(io.LimitReader(f, 64<<10))
	for sc.Scan() {
		if d := strings.TrimSpace(sc.Text()); filepath.IsAbs(d) {
			out = append(out, filepath.Clean(d))
		}
	}
	return out
}
