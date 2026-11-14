//go:build unix

package rootupdate

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/dockmesh/dockmesh/internal/releasesig"
)

// staged is a file extracted from the verified tarball into a temp file
// next to its destination, ready to be renamed into place.
type staged struct {
	File
	tmp string
}

// copyArtifact copies the tarball from the request directory into the
// state directory while hashing it, and returns the path of the copy once
// its hash matches want. Everything after this reads only the copy.
func copyArtifact(req *os.Root, name, stateDir string, max int64, want []byte) (string, error) {
	src, err := openUntrusted(req, name, max)
	if err != nil {
		return "", err
	}
	defer src.Close()
	dst := filepath.Join(stateDir, name)
	if err := os.Remove(dst); err != nil && !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_EXCL|syscall.O_NOFOLLOW, 0o600)
	if err != nil {
		return "", err
	}
	h := sha256.New()
	n, err := io.Copy(io.MultiWriter(out, h), io.LimitReader(src, max+1))
	if err == nil && n > max {
		err = fmt.Errorf("%s is larger than %d bytes", name, max)
	}
	if err == nil {
		err = out.Sync()
	}
	if cerr := out.Close(); err == nil {
		err = cerr
	}
	if err == nil && !bytes.Equal(h.Sum(nil), want) {
		err = fmt.Errorf("%w: %s", releasesig.ErrChecksum, name)
	}
	if err != nil {
		_ = os.Remove(dst)
		return "", err
	}
	return dst, nil
}

// extract pulls files out of the verified tarball copy at path into temp
// files next to their destinations. Only regular entries whose name matches
// exactly are taken; a wanted name listed twice is refused. A required file
// missing from the tarball fails the extraction.
func extract(path string, files []File) ([]staged, error) {
	f, err := os.Open(path) //nolint:gosec // path is the root-only copy made by copyArtifact
	if err != nil {
		return nil, err
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		return nil, fmt.Errorf("read tarball: %w", err)
	}
	defer gz.Close()

	want := map[string]File{}
	for _, fl := range files {
		if fl.Optional && rootOnlyDir(filepath.Dir(fl.Dest)) != nil {
			continue
		}
		want[fl.Name] = fl
	}
	var out []staged
	cleanup := func() {
		for _, s := range out {
			_ = os.Remove(s.tmp)
		}
	}
	seen := map[string]bool{}
	tr := tar.NewReader(gz)
	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			cleanup()
			return nil, fmt.Errorf("read tarball: %w", err)
		}
		name := strings.TrimPrefix(hdr.Name, "./")
		fl, ok := want[name]
		if !ok {
			continue
		}
		if seen[name] {
			cleanup()
			return nil, fmt.Errorf("%s is in the tarball more than once", name)
		}
		seen[name] = true
		if hdr.Typeflag != tar.TypeReg {
			cleanup()
			return nil, fmt.Errorf("%s in the tarball is not a regular file", name)
		}
		if hdr.Size > maxFileSize {
			cleanup()
			return nil, fmt.Errorf("%s in the tarball is larger than %d bytes", name, maxFileSize)
		}
		tmp, err := writeStaged(fl.Dest, io.LimitReader(tr, hdr.Size))
		if err != nil {
			cleanup()
			return nil, fmt.Errorf("stage %s: %w", name, err)
		}
		out = append(out, staged{File: fl, tmp: tmp})
	}
	for _, fl := range want {
		if !fl.Optional && !seen[fl.Name] {
			cleanup()
			return nil, fmt.Errorf("%s is missing from the tarball", fl.Name)
		}
	}
	return out, nil
}

// stagePrefix starts the temp files staged next to a destination.
func stagePrefix(dest string) string {
	return filepath.Join(filepath.Dir(dest), "."+filepath.Base(dest)+".dmupdate-")
}

// writeStaged writes r to a new temp file next to dest, which only root
// may create, and makes it executable once complete.
func writeStaged(dest string, r io.Reader) (string, error) {
	if err := rootOnlyDir(filepath.Dir(dest)); err != nil {
		return "", err
	}
	tmp := stagePrefix(dest) + randSuffix()
	out, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL|syscall.O_NOFOLLOW, 0o600)
	if err != nil {
		return "", err
	}
	_, err = io.Copy(out, r)
	if err == nil {
		err = out.Chmod(0o755)
	}
	if err == nil {
		err = out.Sync()
	}
	if cerr := out.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		_ = os.Remove(tmp)
		return "", err
	}
	return tmp, nil
}

// removeStale deletes temp files an interrupted run left next to the
// destinations.
func removeStale(files []File) {
	for _, fl := range files {
		matches, _ := filepath.Glob(stagePrefix(fl.Dest) + "*")
		for _, m := range matches {
			_ = os.Remove(m)
		}
	}
}

// copyRootFile copies src to dst through a temp file and a rename, so dst
// is never half-written. Both are in root-only directories.
func copyRootFile(src, dst string, mode os.FileMode) error {
	in, err := os.OpenFile(src, os.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return err
	}
	defer in.Close()
	tmp := dst + ".tmp-" + randSuffix()
	out, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL|syscall.O_NOFOLLOW, 0o600)
	if err != nil {
		return err
	}
	_, err = io.Copy(out, in)
	if err == nil {
		err = out.Chmod(mode)
	}
	if err == nil {
		err = out.Sync()
	}
	if cerr := out.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Rename(tmp, dst)
	}
	if err != nil {
		_ = os.Remove(tmp)
	}
	return err
}
