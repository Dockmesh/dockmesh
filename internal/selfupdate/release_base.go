package selfupdate

import "time"

// Lab builds (build tag labkeys) point downloads at a lab release server
// and may shorten the wait for busy stacks; release builds leave both nil.
var (
	labReleaseBase func() string
	labLockWait    func() time.Duration
)

func releaseBaseURL() string {
	if labReleaseBase != nil {
		if b := labReleaseBase(); b != "" {
			return b
		}
	}
	return assetPrefix
}
