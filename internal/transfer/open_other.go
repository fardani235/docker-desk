//go:build !unix

package transfer

import "os"

// openNoFollow opens a file for reading; the caller compares what it opened
// with what the walk saw (os.SameFile), which is the check that remains
// where O_NOFOLLOW does not exist.
func openNoFollow(p string) (*os.File, error) { return os.Open(p) }
