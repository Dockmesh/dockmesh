//go:build composelib

package main

import "github.com/dockmesh/dockmesh/internal/composelib"

func init() { composeSpikeCmd = composelib.RunCLI }
