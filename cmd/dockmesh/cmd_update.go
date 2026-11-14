package main

import (
	"fmt"
	"os"
)

// Internal subcommands the root updater runs as the dockmesh account, so
// root never opens the database itself.
const (
	updateSnapshotCmd = "snapshot-db"
	updateRestoreCmd  = "restore-db"
)

// runUpdateCmd handles `dockmesh update <subcommand>`. `apply` is run as
// root by dockmesh-update.service, never by hand.
func runUpdateCmd(args []string) {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "usage: dockmesh update apply --data-dir <dir>")
		os.Exit(2)
	}
	var err error
	switch args[0] {
	case "apply":
		err = updateApply(args[1:])
	case updateSnapshotCmd, updateRestoreCmd:
		err = updateDB(args[0], args[1:])
	default:
		fmt.Fprintf(os.Stderr, "unknown update subcommand: %s\n", args[0])
		os.Exit(2)
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "update %s: %v\n", args[0], err)
		os.Exit(1)
	}
}
