package backup

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"

	"github.com/dockmesh/dockmesh/internal/backup/targets"
)

// ErrArchiveMismatch means the archive on the target is not the one the
// run wrote.
var ErrArchiveMismatch = errors.New("archive does not match what the backup run wrote")

// readBack reads an uploaded archive back from the target and compares it
// with what was written. A target can accept every write and still fail
// or return other bytes on read, which otherwise only shows at restore.
// act, when set, is marked on every read, and the read is cut off when
// ctx ends.
func readBack(ctx context.Context, t targets.Target, path string, size int64, sum string, act *activity) error {
	rc, err := t.Read(ctx, path)
	if err != nil {
		return fmt.Errorf("read back %s: %w", path, err)
	}
	defer rc.Close()
	defer closeOnDone(ctx, func() error { return targets.Break(rc) })()
	h := sha256.New()
	n, err := io.Copy(h, touchReader{r: rc, act: act})
	if err != nil {
		return fmt.Errorf("read back %s: %w", path, err)
	}
	if got := hex.EncodeToString(h.Sum(nil)); n != size || got != sum {
		return fmt.Errorf("read back %s: %w: got %d bytes sha256 %s, wrote %d bytes sha256 %s",
			path, ErrArchiveMismatch, n, got, size, sum)
	}
	return nil
}

// fetchArchive copies a run's archive into a temp file and checks it
// against the size and hash recorded at backup time, so a restore never
// starts extracting bytes that differ from what was written. Closing the
// returned reader removes the temp file.
func fetchArchive(ctx context.Context, t targets.Target, run *Run) (io.ReadSeekCloser, error) {
	src, err := t.Read(ctx, run.TargetPath)
	if err != nil {
		return nil, err
	}
	defer src.Close()
	defer closeOnDone(ctx, func() error { return targets.Break(src) })()

	tmp, err := os.CreateTemp("", "dm-restore-archive-*")
	if err != nil {
		return nil, fmt.Errorf("restore temp file: %w", err)
	}
	f := &tempArchive{File: tmp}
	h := sha256.New()
	n, err := io.Copy(io.MultiWriter(tmp, h), src)
	if err != nil {
		_ = f.Close()
		return nil, fmt.Errorf("read archive %s: %w", run.TargetPath, err)
	}
	if run.SHA256 != "" {
		if got := hex.EncodeToString(h.Sum(nil)); got != run.SHA256 || n != run.SizeBytes {
			_ = f.Close()
			return nil, fmt.Errorf("%w: %s has %d bytes sha256 %s, run recorded %d bytes sha256 %s",
				ErrArchiveMismatch, run.TargetPath, n, got, run.SizeBytes, run.SHA256)
		}
	}
	if _, err := tmp.Seek(0, io.SeekStart); err != nil {
		_ = f.Close()
		return nil, err
	}
	return f, nil
}

type tempArchive struct{ *os.File }

func (t *tempArchive) Close() error {
	err := t.File.Close()
	if rerr := os.Remove(t.Name()); err == nil {
		err = rerr
	}
	return err
}
