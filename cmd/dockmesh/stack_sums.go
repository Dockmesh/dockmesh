package main

import (
	"log/slog"

	"github.com/dockmesh/dockmesh/internal/db"
	"github.com/dockmesh/dockmesh/internal/stacks"
)

// attachSumStore lets an offline command record the stack files it
// writes, so the server does not report them as changed outside
// Dockmesh. Without a database the command still runs.
func attachSumStore(mgr *stacks.Manager, dbPath string) func() {
	database, err := db.Open(dbPath)
	if err != nil {
		slog.Warn("stack file checksums not recorded", "err", err)
		return func() {}
	}
	mgr.SetSumStore(stacks.NewSumStore(database))
	return func() { database.Close() }
}
