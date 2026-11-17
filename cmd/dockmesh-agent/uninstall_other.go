//go:build !linux

package main

import (
	"context"
	"fmt"
	"os"
)

func runUninstallCmd([]string) {
	fmt.Fprintln(os.Stderr, "dockmesh-agent uninstall: the agent is installed with systemd on Linux only")
	os.Exit(1)
}

func watchUninstallSignal(context.Context) {}
