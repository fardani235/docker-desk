package sshtunnel

import (
	"os/exec"
	"runtime"
	"syscall"
)

// startBound starts cmd so that the kernel ends it when the desk dies, even
// if the desk is killed outright and never gets to clean up: an ssh left
// behind would keep an authenticated tunnel to the remote daemon open.
//
// The "parent death" signal is tied to the thread that started the child,
// not to the process, so the child is started — and waited for — on a thread
// that is locked to this goroutine and therefore lives until the child is
// gone.
//
// With detach the child gets a session of its own and therefore no
// controlling terminal: neither it nor anything it starts can open /dev/tty
// to ask a question.
func startBound(cmd *exec.Cmd, detach bool) (wait func() error, err error) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Pdeathsig: syscall.SIGTERM, Setsid: detach}
	started := make(chan error, 1)
	exited := make(chan error, 1)
	go func() {
		runtime.LockOSThread() // not unlocked: the thread ends with the goroutine
		if err := cmd.Start(); err != nil {
			started <- err
			return
		}
		started <- nil
		exited <- cmd.Wait()
	}()
	if err := <-started; err != nil {
		return nil, err
	}
	return func() error { return <-exited }, nil
}
