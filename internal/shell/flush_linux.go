//go:build linux

package shell

import "golang.org/x/sys/unix"

// flushInput discards keys that were typed but not yet read.
func flushInput(fd int) {
	_ = unix.IoctlSetInt(fd, unix.TCFLSH, unix.TCIFLUSH)
}
