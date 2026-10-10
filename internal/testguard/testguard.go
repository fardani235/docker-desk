// Package testguard is the switch that keeps the test suite away from real
// daemons. Once installed it stays on for the rest of the process.
//
// It is its own package because two layers have to obey it: the engine,
// which opens the connection, and the SSH tunnel, whose local socket lives in
// the temporary directory whatever it leads to. See docs/DECISIONS.md, B3
// and R9.
package testguard

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
)

var (
	on      atomic.Bool
	mu      sync.Mutex
	allowed = map[string]bool{}
	// tmp is the temporary directory as it was when the package was
	// loaded. It is read once: a test that sets TMPDIR — several do, to a
	// directory of their own inside it — must not thereby be able to
	// redefine what counts as "a fake daemon's socket".
	tmp = filepath.Clean(os.TempDir()) + string(filepath.Separator)
)

// Install turns the guard on. It also points DOCKER_HOST at a path that
// cannot exist, so code that resolves the default endpoint fails loudly
// instead of finding a real one.
func Install() {
	on.Store(true)
	_ = os.Setenv("DOCKER_HOST", "unix:///nonexistent/docker-desk-tests-must-not-reach-a-real-daemon.sock")
	_ = os.Unsetenv("DOCKER_CONTEXT")
}

// Active reports whether the guard has been installed.
func Active() bool { return on.Load() }

// UnderTemp reports whether path lies inside the system temporary directory,
// which is where every fake daemon listens and where no real one does.
func UnderTemp(path string) bool {
	return filepath.IsAbs(path) && strings.HasPrefix(filepath.Clean(path), tmp)
}

// Allow registers a network endpoint (e.g. tcp://127.0.0.1:41873) as a fake
// daemon. The fake engine calls it for the listeners it opens itself; a TCP
// address has no directory to tell a fake from a real daemon by.
func Allow(host string) {
	mu.Lock()
	allowed[host] = true
	mu.Unlock()
}

// CheckHost decides whether a read-write connection to host may be opened.
// With the guard off everything may; with it on, only a unix socket under the
// temporary directory or an endpoint registered with Allow.
func CheckHost(host string) error {
	if !Active() {
		return nil
	}
	if path, ok := strings.CutPrefix(host, "unix://"); ok && UnderTemp(path) {
		return nil
	}
	mu.Lock()
	ok := allowed[host]
	mu.Unlock()
	if ok {
		return nil
	}
	return fmt.Errorf("test guard: refusing a read-write connection to %s — tests may only mutate a fake daemon (a socket under %s, or a listener the fake engine opened)", host, tmp)
}

// CheckRemoteSocket decides whether an SSH tunnel to a remote socket path may
// be opened. Under the guard the far end must itself be a socket under the
// temporary directory: the tunnel's local end always is, so without this
// check a tunnel would be a way around CheckHost.
func CheckRemoteSocket(path string) error {
	if !Active() || UnderTemp(path) {
		return nil
	}
	return fmt.Errorf("test guard: refusing an SSH tunnel to %s — tests may only tunnel to a fake daemon's socket under %s", path, tmp)
}
