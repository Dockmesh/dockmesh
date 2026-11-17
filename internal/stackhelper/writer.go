package stackhelper

import (
	"archive/tar"
	"context"
	"errors"
	"io"
	"log/slog"
)

// errHelperDone closes the pipe once the helper returned, so a write after
// that fails instead of blocking.
var errHelperDone = errors.New("helper stopped reading the archive")

// Writer streams entries into dir through Extract, so each keeps the
// owner, mode and times of its header. The helper starts with the first
// entry; a Writer that never gets one never starts it.
type Writer struct {
	h         *Helper
	ctx       context.Context
	base, dir string
	tw        *tar.Writer
	pw        *io.PipeWriter
	done      chan error
}

// NewWriter returns a Writer for dir, which must lie below base.
func (h *Helper) NewWriter(ctx context.Context, base, dir string) *Writer {
	return &Writer{h: h, ctx: ctx, base: base, dir: dir}
}

// WriteEntry writes a directory or regular file named rel (relative to
// dir) with the content of r. Setuid and setgid are dropped: a crafted
// archive must not leave a set-id binary owned by root on the host.
func (w *Writer) WriteEntry(hdr *tar.Header, rel string, r io.Reader) error {
	if w.tw == nil {
		pr, pw := io.Pipe()
		w.tw, w.pw, w.done = tar.NewWriter(pw), pw, make(chan error, 1)
		go func() {
			err := w.h.Extract(w.ctx, w.base, w.dir, pr)
			if err == nil {
				err = errHelperDone
			}
			pr.CloseWithError(err)
			w.done <- err
		}()
	}
	out := &tar.Header{
		Name: rel, Typeflag: hdr.Typeflag, Mode: hdr.Mode & 0o1777, Uid: hdr.Uid, Gid: hdr.Gid,
		Size: hdr.Size, ModTime: hdr.ModTime, Format: tar.FormatPAX,
	}
	if hdr.Typeflag == tar.TypeDir {
		out.Size = 0
	}
	if err := w.tw.WriteHeader(out); err != nil {
		return err
	}
	if out.Size > 0 {
		if _, err := io.CopyN(w.tw, r, out.Size); err != nil {
			return err
		}
	}
	return nil
}

// Close ends the archive and waits for the helper.
func (w *Writer) Close() error {
	if w.tw == nil {
		return nil
	}
	tw, pw, done := w.tw, w.pw, w.done
	w.tw = nil
	if err := tw.Close(); err != nil {
		pw.CloseWithError(err)
		if herr := <-done; herr != nil && !errors.Is(herr, errHelperDone) {
			return errors.Join(err, herr)
		}
		return err
	}
	_ = pw.Close()
	err := <-done
	if errors.Is(err, errHelperDone) {
		return nil
	}
	return err
}

// Abort stops a helper that a failed restore left running.
func (w *Writer) Abort() {
	if w.tw == nil {
		return
	}
	w.pw.CloseWithError(errors.New("restore aborted"))
	if err := <-w.done; err != nil {
		slog.Debug("restore helper stopped", "err", err)
	}
	w.tw = nil
}
