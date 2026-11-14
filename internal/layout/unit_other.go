//go:build !unix

package layout

import "errors"

var errNoSystemd = errors.New("systemd units are only written on Linux")

// WriteServerUnit fails off Unix.
func WriteServerUnit(ServerUnit, UnitWriteOptions) (bool, error) { return false, errNoSystemd }

// RestoreServerUnit fails off Unix.
func RestoreServerUnit(UnitWriteOptions) error { return errNoSystemd }

// ReadEnvFile fails off Unix.
func ReadEnvFile(string) (map[string]string, error) { return nil, errNoSystemd }

// PrepareConfigDir fails off Unix.
func PrepareConfigDir(int) error { return errNoSystemd }
