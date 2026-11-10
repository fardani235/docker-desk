//go:build unix

package composefile

import (
	"path/filepath"
	"syscall"
	"testing"
)

func makeFIFO(t *testing.T, dir string) string {
	t.Helper()
	p := filepath.Join(dir, "fifo")
	if err := syscall.Mkfifo(p, 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}
