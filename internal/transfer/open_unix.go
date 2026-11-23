//go:build unix

package transfer

import (
	"os"
	"syscall"
)

// openNoFollow opens a regular file for reading and refuses a symlink put in
// its place since the walk saw it.
func openNoFollow(p string) (*os.File, error) {
	return os.OpenFile(p, os.O_RDONLY|syscall.O_NOFOLLOW, 0)
}
