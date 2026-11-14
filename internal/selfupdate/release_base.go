package selfupdate

import "time"

// Lab builds (build tag labkeys) point downloads at a lab release server
// and may shorten the wait for busy stacks; release builds leave both nil.
var (
	labReleaseBase func() string
	labLockWait    func() time.Duration
)

// ReleaseBaseURL is where release assets are downloaded from, one
// directory per tag.
func ReleaseBaseURL() string {
	if labReleaseBase != nil {
		if b := labReleaseBase(); b != "" {
			return b
		}
	}
	return assetPrefix
}
