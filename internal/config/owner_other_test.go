//go:build !unix

package config

import "os"

func sameOwner(os.FileInfo, os.FileInfo) bool { return true }
