//go:build !unix

package main

import "errors"

var errUpdateNotLinux = errors.New("updates are applied by systemd on Linux only")

func updateApply([]string) error { return errUpdateNotLinux }

func updateDB(string, []string) error { return errUpdateNotLinux }
