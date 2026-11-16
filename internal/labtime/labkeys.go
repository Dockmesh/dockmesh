//go:build labkeys

package labtime

import (
	"os"
	"time"
)

func init() {
	day = func() time.Duration {
		d, _ := time.ParseDuration(os.Getenv("DOCKMESH_LAB_DAY"))
		return d
	}
}
