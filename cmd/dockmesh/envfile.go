package main

import "os"

// defaultEnvFiles are the places install.sh and `dockmesh init` put
// dockmesh.env: the Linux default, /data for hosts with a separate data
// volume, and the macOS default.
var defaultEnvFiles = []string{
	"/var/lib/dockmesh/dockmesh.env",
	"/data/dockmesh.env",
	"/usr/local/var/dockmesh/dockmesh.env",
}

func firstExistingFile(candidates []string) string {
	for _, c := range candidates {
		if _, err := os.Stat(c); err == nil {
			return c
		}
	}
	return ""
}
