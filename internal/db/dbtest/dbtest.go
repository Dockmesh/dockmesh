// Package dbtest gives tests a migrated database without running the
// migrations for each one: under the race detector they take seconds.
package dbtest

import (
	"database/sql"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"sync"

	"github.com/dockmesh/dockmesh/internal/db"
)

var (
	once     sync.Once
	template []byte
	tmplErr  error
)

func build() ([]byte, error) {
	dir, err := os.MkdirTemp("", "dockmesh-dbtest-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(dir)
	path := filepath.Join(dir, "template.db")
	conn, err := db.Open(path)
	if err != nil {
		return nil, err
	}
	if err := db.Migrate(conn); err != nil {
		conn.Close()
		return nil, err
	}
	// Closing the last connection checkpoints the WAL into the file.
	if err := conn.Close(); err != nil {
		return nil, err
	}
	return os.ReadFile(path)
}

// Open is db.Open on a database that already has every migration applied.
// A file that exists at path is opened as it is.
func Open(path string) (*sql.DB, error) {
	if _, err := os.Stat(path); errors.Is(err, fs.ErrNotExist) {
		once.Do(func() { template, tmplErr = build() })
		if tmplErr != nil {
			return nil, tmplErr
		}
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return nil, err
		}
		if err := os.WriteFile(path, template, 0o600); err != nil {
			return nil, err
		}
	} else if err != nil {
		return nil, err
	}
	return db.Open(path)
}
