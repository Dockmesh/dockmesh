//go:build !unix

package main

import "os"

func readTrustedEnvFile(path string) ([]byte, error) {
	return os.ReadFile(path)
}
