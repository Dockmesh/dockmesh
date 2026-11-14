//go:build !unix

package layout

import "errors"

var errNoSystemd = errors.New("systemd units are only written on Linux")

// WriteServerUnit fails off Unix.
func WriteServerUnit(ServerUnit, UnitWriteOptions) (bool, error) { return false, errNoSystemd }

// RestoreServerUnit fails off Unix.
func RestoreServerUnit(UnitWriteOptions) error { return errNoSystemd }

// CheckServerUnit fails off Unix.
func CheckServerUnit(ServerUnit) error { return errNoSystemd }

// SocketGroups gives no groups off Unix.
func SocketGroups(string, int) ([]string, string) { return nil, "" }

// RootOwnedFile is false off Unix.
func RootOwnedFile(string) bool { return false }

// ReadEnvFile fails off Unix.
func ReadEnvFile(string) (map[string]string, error) { return nil, errNoSystemd }

// PrepareConfigDir fails off Unix.
func PrepareConfigDir(int) error { return errNoSystemd }

// WriteUpdateUnits fails off Unix.
func WriteUpdateUnits(string, string, string) (bool, error) { return false, errNoSystemd }

// ReadUnitFile fails off Unix.
func ReadUnitFile(string) ([]byte, error) { return nil, errNoSystemd }

// PutUnitFile fails off Unix.
func PutUnitFile(string, []byte) error { return errNoSystemd }
