//go:build !unix

package layout

import "errors"

// UnitWriteOptions configure WriteServerUnit and RestoreServerUnit.
type UnitWriteOptions struct {
	UnitFile     string
	PrevUnitFile string
}

var errNoSystemd = errors.New("systemd units are only written on Linux")

// WriteServerUnit fails off Unix.
func WriteServerUnit(ServerUnit, UnitWriteOptions) (bool, error) { return false, errNoSystemd }

// RestoreServerUnit fails off Unix.
func RestoreServerUnit(UnitWriteOptions) error { return errNoSystemd }

// PrepareConfigDir fails off Unix.
func PrepareConfigDir(int) error { return errNoSystemd }
