//go:build !linux

package sshtunnel

import (
	"os/exec"
	"syscall"
)

// startBound starts cmd. Only Linux can tie the child's life to the desk's;
// elsewhere an ssh whose desk was killed outright stays until it is closed
// by hand or the link drops.
//
// With detach the child gets a session of its own and therefore no
// controlling terminal: neither it nor anything it starts can open /dev/tty
// to ask a question.
func startBound(cmd *exec.Cmd, detach bool) (wait func() error, err error) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: detach}
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	return cmd.Wait, nil
}
