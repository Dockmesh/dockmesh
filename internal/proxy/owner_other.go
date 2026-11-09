//go:build !unix

package proxy

import "os"

func ownedByMe(os.FileInfo) bool { return true }
