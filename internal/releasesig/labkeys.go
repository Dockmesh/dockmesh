//go:build labkeys

package releasesig

// labKey is a throwaway key for lab builds that update between test
// releases. Its secret key never leaves the lab; a binary built with this
// tag must never be published.
const labKey = "RWQzyQ/ZCOVEk8sjJcvzihq7dtEkoQ6fBYjOQUl23fmhuObG903m4Mg6"

func init() { extraKeys = append(extraKeys, labKey) }
