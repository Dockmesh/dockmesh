//go:build unix

package webtls

import "syscall"

const oNonBlock = syscall.O_NONBLOCK
