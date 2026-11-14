//go:build labkeys

package selfupdate

import (
	"os"
	"time"
)

func init() {
	labReleaseBase = func() string { return os.Getenv("DOCKMESH_LAB_RELEASE_BASE") }
	labLockWait = func() time.Duration {
		d, _ := time.ParseDuration(os.Getenv("DOCKMESH_LAB_UPDATE_LOCK_WAIT"))
		return d
	}
}
