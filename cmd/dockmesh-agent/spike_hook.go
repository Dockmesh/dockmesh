package main

import (
	"fmt"
	"os"
)

// composeSpikeCmd is set by spike_composelib.go when built with -tags composelib.
var composeSpikeCmd func(args []string)

func runComposeSpike(args []string) {
	if composeSpikeCmd == nil {
		fmt.Fprintln(os.Stderr, "compose-spike: binary built without -tags composelib")
		os.Exit(2)
	}
	composeSpikeCmd(args)
}
