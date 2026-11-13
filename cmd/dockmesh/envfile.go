package main

import (
	"errors"
	"os"

	"github.com/dockmesh/dockmesh/internal/layout"
)

// defaultEnvFiles are searched when no --env-file is given: the config
// location first, then the places older installs kept dockmesh.env.
var defaultEnvFiles = append([]string{layout.ConfigFile}, layout.LegacyEnvFiles...)

var errUntrustedEnvFile = errors.New("untrusted env file")

func firstExistingFile(candidates []string) string {
	for _, c := range candidates {
		if _, err := os.Stat(c); err == nil {
			return c
		}
	}
	return ""
}
