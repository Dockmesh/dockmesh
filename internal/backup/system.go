package backup

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"database/sql"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
)

// DefaultSystemJobName is the well-known name of the auto-created daily
// system backup job. Used by defaults.go to detect existence and by the
// handler to expose its status to the sidebar pill.
const DefaultSystemJobName = "dockmesh-system"

// tarSystem snapshots the Dockmesh server's own state into a single
// gzipped tar:
//
//	dockmesh.db    — consistent snapshot via SQLite VACUUM INTO
//	stacks/…       — recursive tar of the on-disk /stacks root
//	data/…         — recursive tar of the data dir (secrets keys,
//	                 jwt secret, audit genesis), EXCLUDING the live DB
//	                 to avoid an inconsistent raw copy alongside the
//	                 VACUUM snapshot
//
// Restoring this archive + pointing DOCKMESH_DB_PATH / DOCKMESH_STACKS_ROOT
// at the extracted contents is enough to bring a new server back up.
func tarSystem(ctx context.Context, db *sql.DB, paths SystemPaths, w io.Writer) (int64, error) {
	if db == nil {
		return 0, fmt.Errorf("system backup: db handle unavailable")
	}
	if paths.DBPath == "" || paths.StacksRoot == "" || paths.DataDir == "" {
		return 0, fmt.Errorf("system backup: paths not configured")
	}

	// Snapshot the DB first so any failure short-circuits before we
	// start streaming tar bytes to the caller.
	snapFile, err := os.CreateTemp("", "dockmesh-backup-*.db")
	if err != nil {
		return 0, fmt.Errorf("system backup: temp db: %w", err)
	}
	snapPath := snapFile.Name()
	snapFile.Close()
	// os.CreateTemp leaves an empty file; VACUUM INTO needs the target
	// to not exist.
	_ = os.Remove(snapPath)
	defer os.Remove(snapPath)

	if _, err := db.ExecContext(ctx, "VACUUM INTO ?", snapPath); err != nil {
		return 0, fmt.Errorf("system backup: vacuum into: %w", err)
	}

	gz := gzip.NewWriter(w)
	tw := tar.NewWriter(gz)

	var total int64
	add := func(src, archivePath string, info fs.FileInfo) error {
		hdr, err := tar.FileInfoHeader(info, "")
		if err != nil {
			return err
		}
		hdr.Name = filepath.ToSlash(archivePath)

		if info.IsDir() {
			if err := tw.WriteHeader(hdr); err != nil {
				return err
			}
			return nil
		}

		// Re-stat through the open fd to lock in the size we're about to
		// copy. Without this, `info.Size()` is whatever the walker saw
		// earlier; by the time we stream bytes a live file (audit log,
		// DB-WAL) can have grown, and tar fails with "write too long"
		// when io.Copy emits more bytes than the header declared.
		//
		// With this pattern the tar entry is a consistent at-open-time
		// snapshot: subsequent growth is ignored (fine for logs), and a
		// shrunk file gets zero-padded by io.CopyN returning EOF early.
		f, err := os.Open(src)
		if err != nil {
			return err
		}
		defer f.Close()

		st, err := f.Stat()
		if err != nil {
			return err
		}
		hdr.Size = st.Size()
		if err := tw.WriteHeader(hdr); err != nil {
			return err
		}

		n, copyErr := io.CopyN(tw, f, st.Size())
		total += n
		if copyErr != nil && copyErr != io.EOF {
			return copyErr
		}
		// Zero-pad if the file shrank after we recorded its size in the
		// header. tar.Writer requires exactly hdr.Size bytes between
		// WriteHeader calls; without padding the next WriteHeader would
		// fail with "wrote too little data".
		if n < st.Size() {
			pad := make([]byte, st.Size()-n)
			if _, err := tw.Write(pad); err != nil {
				return err
			}
		}
		return nil
	}

	// 1. DB snapshot → dockmesh.db
	info, err := os.Stat(snapPath)
	if err != nil {
		_ = tw.Close()
		_ = gz.Close()
		return 0, fmt.Errorf("system backup: stat snapshot: %w", err)
	}
	if err := add(snapPath, "dockmesh.db", info); err != nil {
		_ = tw.Close()
		_ = gz.Close()
		return total, fmt.Errorf("system backup: tar db: %w", err)
	}

	// 2. Stacks root → stacks/…
	if err := walkInto(paths.StacksRoot, "stacks", nil, add); err != nil {
		_ = tw.Close()
		_ = gz.Close()
		return total, fmt.Errorf("system backup: tar stacks: %w", err)
	}

	// 3. Data dir → data/…, excluding the live DB (plus WAL/SHM siblings)
	// since VACUUM INTO already gave us a consistent copy.
	liveDB, _ := filepath.Abs(paths.DBPath)
	skip := map[string]bool{
		liveDB:            true,
		liveDB + "-wal":   true,
		liveDB + "-shm":   true,
		liveDB + "-journal": true,
	}
	if err := walkInto(paths.DataDir, "data", skip, add); err != nil {
		_ = tw.Close()
		_ = gz.Close()
		return total, fmt.Errorf("system backup: tar data: %w", err)
	}

	if err := tw.Close(); err != nil {
		_ = gz.Close()
		return total, err
	}
	if err := gz.Close(); err != nil {
		return total, err
	}
	return total, nil
}

// walkInto walks root and emits each entry under prefix/<rel> via add,
// skipping absolute paths present in skip. Missing root is treated as
// an empty set (first-boot installs may not have the dir yet).
func walkInto(root, prefix string, skip map[string]bool, add func(src, archivePath string, info fs.FileInfo) error) error {
	if _, err := os.Stat(root); err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	return filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		abs, _ := filepath.Abs(p)
		if skip != nil && skip[abs] {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(root, p)
		if err != nil {
			return err
		}
		if rel == "." {
			return nil
		}
		return add(p, filepath.ToSlash(filepath.Join(prefix, rel)), info)
	})
}
