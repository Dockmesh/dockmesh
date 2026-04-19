// Command dmctl is the user-facing CLI for Dockmesh. It's a thin
// HTTP client against a running Dockmesh server's REST API, designed
// for scripting (CI/CD, shell one-liners) and interactive use.
//
// dmctl is NOT the admin binary — server-side operations like
// `dockmesh restore`, `dockmesh doctor`, `dockmesh ca rotate` live in
// the `dockmesh` binary and run on the server host. dmctl only needs
// a server URL and an API token to do anything.
//
// Config: ~/.config/dmctl/config.yaml (or %APPDATA%\dmctl\config.yaml
// on Windows). Env vars `DMCTL_SERVER` / `DMCTL_TOKEN` win over the
// config file. `--server` / `--token` flags win over env vars.
//
// P.12.9.
package main

import (
	"fmt"
	"os"
)

func main() {
	root := newRootCmd()
	if err := root.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, "Error:", err)
		os.Exit(1)
	}
}
