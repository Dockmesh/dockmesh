//go:build !unix

package main

import "errors"

func settingsWritablePaths(string) ([]string, error) {
	return nil, errors.New("systemd units are only written on Linux")
}

func checkUnitBinary(string) error {
	return errors.New("systemd units are only written on Linux")
}

func systemSnapshotDB() error {
	return errors.New("the database snapshot is only taken on Linux")
}
