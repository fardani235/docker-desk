package engine

import "github.com/fardani235/docker-desk/internal/testguard"

// InstallTestGuard makes New refuse, for the rest of the process, to open a
// read-write connection to anything that is not a fake daemon: a unix socket
// inside the system temporary directory, or a network listener the fake
// engine opened itself. Every test package that builds an engine calls it
// from TestMain, so no test — present or future — can send a mutating request
// to a real daemon by accident: a real daemon can only be opened read-only.
//
// It also points DOCKER_HOST at a path that cannot exist, so code that
// resolves the default endpoint fails loudly instead of finding a real one.
func InstallTestGuard() { testguard.Install() }

func checkTestGuard(opts Options) error {
	if opts.ReadOnly {
		return nil
	}
	return testguard.CheckHost(opts.Host)
}
