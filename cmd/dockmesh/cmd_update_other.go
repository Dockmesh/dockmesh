//go:build !unix

package main

import "errors"

func updateApply([]string) error { return errors.New("updates are applied by systemd on Linux only") }

func updateDB(string, []string) error {
	return errors.New("updates are applied by systemd on Linux only")
}
