package engine

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
)

var testGuard atomic.Bool

// InstallTestGuard makes New refuse, for the rest of the process, to open a
// read-write connection to any daemon whose socket is not inside the system
// temporary directory. Every test package that builds an engine calls it from
// TestMain, so no test — present or future — can send a mutating request to a
// real daemon by accident: a real daemon can only be opened read-only.
//
// It also points DOCKER_HOST at a path that cannot exist, so code that
// resolves the default endpoint fails loudly instead of finding a real one.
func InstallTestGuard() {
	testGuard.Store(true)
	_ = os.Setenv("DOCKER_HOST", "unix:///nonexistent/docker-desk-tests-must-not-reach-a-real-daemon.sock")
	_ = os.Unsetenv("DOCKER_CONTEXT")
}

func checkTestGuard(opts Options) error {
	if !testGuard.Load() || opts.ReadOnly {
		return nil
	}
	path := strings.TrimPrefix(opts.Host, "unix://")
	tmp := filepath.Clean(os.TempDir()) + string(filepath.Separator)
	if strings.HasPrefix(opts.Host, "unix://") && strings.HasPrefix(filepath.Clean(path), tmp) {
		return nil
	}
	return fmt.Errorf("test guard: refusing a read-write connection to %s — tests may only mutate a fake daemon on a socket under %s", opts.Host, tmp)
}
