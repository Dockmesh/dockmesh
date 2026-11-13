package stackhelper

import (
	"archive/tar"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/moby/moby/client"
)

// Walk calls fn for every entry below dir, which must lie below base, with
// its name relative to dir and the owner, mode and times of the file.
// Directories, regular files and symlinks are passed on; for a regular
// file r yields its content. When the service can read the whole tree it
// reads it itself, so a host without the helper image keeps working;
// otherwise the helper reads it.
func (h *Helper) Walk(ctx context.Context, base, dir string, fn func(hdr *tar.Header, r io.Reader) error) error {
	err := unreadable(dir)
	if err == nil {
		return walkDirect(dir, fn)
	}
	if !errors.Is(err, fs.ErrPermission) {
		return err
	}
	if h == nil || h.cli == nil {
		return fmt.Errorf("%w (files owned by another user need Docker to read them)", err)
	}
	return h.archive(ctx, base, dir, fn)
}

// unreadable walks dir and returns a permission error for the first entry
// the service could not read.
func unreadable(dir string) error {
	return filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.Type().IsRegular() {
			return canRead(p)
		}
		return nil
	})
}

func walkDirect(dir string, fn func(hdr *tar.Header, r io.Reader) error) error {
	return filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(dir, p)
		if err != nil {
			return err
		}
		if rel == "." {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		link := ""
		switch {
		case d.Type()&fs.ModeSymlink != 0:
			if link, err = os.Readlink(p); err != nil {
				return err
			}
		case !d.IsDir() && !d.Type().IsRegular():
			return nil
		}
		hdr, err := tar.FileInfoHeader(info, link)
		if err != nil {
			return err
		}
		hdr.Name = filepath.ToSlash(rel)
		if !d.Type().IsRegular() {
			return fn(hdr, nil)
		}
		f, err := os.Open(p)
		if err != nil {
			return err
		}
		defer f.Close()
		// The size at open time: a file a live container keeps writing
		// would otherwise not match its header.
		st, err := f.Stat()
		if err != nil {
			return err
		}
		hdr.Size = st.Size()
		return fn(hdr, io.LimitReader(f, st.Size()))
	})
}

// archive reads dir through the helper. The helper never starts: the
// Docker daemon reads the read-only mount as root and builds the archive.
func (h *Helper) archive(ctx context.Context, base, dir string, fn func(hdr *tar.Header, r io.Reader) error) error {
	if err := h.ensureImage(ctx); err != nil {
		return err
	}
	t, err := resolve(base, dir)
	if err != nil {
		return err
	}
	id, err := h.create(ctx, t, true, []string{"true"}, nil)
	if err != nil {
		return err
	}
	defer h.remove(ctx, id)
	res, err := h.cli.CopyFromContainer(ctx, id, client.CopyFromContainerOptions{SourcePath: mountPoint})
	if err != nil {
		return fmt.Errorf("read stack directory: %w", err)
	}
	defer res.Content.Close()
	prefix := path.Base(mountPoint)
	tr := tar.NewReader(res.Content)
	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("read stack directory: %w", err)
		}
		rel, ok := relName(hdr.Name, prefix)
		if !ok {
			return fmt.Errorf("archive entry %q leaves the stack directory", hdr.Name)
		}
		if rel == "" {
			continue
		}
		hdr.Name = rel
		switch hdr.Typeflag {
		case tar.TypeDir, tar.TypeSymlink:
			err = fn(hdr, nil)
		case tar.TypeReg:
			err = fn(hdr, tr)
		}
		if err != nil {
			return err
		}
	}
}

// relName strips the mount point's directory name from an archive entry.
// The empty name is the directory itself.
func relName(name, prefix string) (string, bool) {
	name = strings.TrimPrefix(filepath.ToSlash(name), "./")
	if name == prefix || name == prefix+"/" {
		return "", true
	}
	if !strings.HasPrefix(name, prefix+"/") {
		return "", false
	}
	rel := path.Clean(strings.TrimPrefix(name, prefix+"/"))
	if rel == "." || rel == ".." || strings.HasPrefix(rel, "../") || path.IsAbs(rel) {
		return "", false
	}
	return rel, true
}

// Extract unpacks the tar stream r into dir, which must lie below base.
// Entry names are relative to dir. The Docker daemon extracts as root and
// gives every file the owner, mode and times recorded in its header; it
// confines the archive to the mount.
func (h *Helper) Extract(ctx context.Context, base, dir string, r io.Reader) error {
	if h == nil || h.cli == nil {
		return errors.New("docker unavailable")
	}
	if err := h.ensureImage(ctx); err != nil {
		return err
	}
	t, err := resolve(base, dir)
	if err != nil {
		return err
	}
	if m, err := mountBelow(t.path); err != nil {
		return err
	} else if m != "" {
		return fmt.Errorf("restore into %s: %s is a mount point", filepath.Base(dir), m)
	}
	id, err := h.create(ctx, t, false, []string{"true"}, nil)
	if err != nil {
		return err
	}
	defer h.remove(ctx, id)
	if _, err := h.cli.CopyToContainer(ctx, id, client.CopyToContainerOptions{
		DestinationPath: mountPoint,
		Content:         r,
		CopyUIDGID:      true,
	}); err != nil {
		return fmt.Errorf("write stack directory: %w", err)
	}
	return nil
}
