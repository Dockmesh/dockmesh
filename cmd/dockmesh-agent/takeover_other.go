//go:build unix && !linux

package main

import (
	"errors"
	"os"
	"syscall"
)

const takeOverOpenFlags = syscall.O_RDONLY | syscall.O_NOFOLLOW | syscall.O_NONBLOCK | syscall.O_NOCTTY

func chownOpened(*os.File, int, int) error {
	return errors.New("the agent is installed with systemd on Linux only")
}
