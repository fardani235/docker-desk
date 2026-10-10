//go:build !linux

package shell

// flushInput is a no-op where the flush ioctl differs; the only effect is
// that keys typed blind while a session was ending may reach the desk.
func flushInput(int) {}
