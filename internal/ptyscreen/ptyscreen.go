// Package ptyscreen runs a program on a pseudo-terminal and keeps an emulated
// screen of what it draws, so tests can drive the real docker-desk binary the
// way a person does — keys in, screen out — without a human or a terminal
// multiplexer.
//
// It is test tooling: nothing in the shipped binary imports it.
package ptyscreen

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"
	"syscall"
	"time"
	"unicode/utf8"

	"github.com/creack/pty"
	"github.com/hinshun/vt10x"
)

// Term is a running program and the screen it has drawn.
type Term struct {
	cmd  *exec.Cmd
	ptmx *os.File

	mu     sync.Mutex
	vt     vt10x.Terminal
	raw    []byte // everything the program wrote, for sequence checks
	rows   int
	cols   int
	done   chan struct{}
	waitMu sync.Mutex
	exit   int
	waited bool
}

// Start runs bin on a new pty of the given size. env is appended to a
// minimal environment; the caller's DOCKER_* variables are never inherited.
func Start(rows, cols int, env []string, bin string, args ...string) (*Term, error) {
	cmd := exec.Command(bin, args...)
	cmd.Env = append([]string{
		"TERM=xterm-256color",
		"HOME=" + os.Getenv("HOME"),
		"PATH=" + os.Getenv("PATH"),
		"LANG=C.UTF-8",
	}, env...)
	ptmx, err := pty.StartWithSize(cmd, &pty.Winsize{Rows: uint16(rows), Cols: uint16(cols)})
	if err != nil {
		return nil, err
	}
	t := &Term{
		cmd: cmd, ptmx: ptmx, rows: rows, cols: cols,
		vt:   vt10x.New(vt10x.WithSize(cols, rows)),
		done: make(chan struct{}),
	}
	go func() {
		defer close(t.done)
		buf := make([]byte, 32<<10)
		var pending []byte
		for {
			n, err := ptmx.Read(buf)
			if n > 0 {
				// Answer the questions every real terminal answers. Without
				// this, terminal libraries wait seconds for a reply at
				// start-up (background-colour and cursor-position queries).
				chunk := string(buf[:n])
				if strings.Contains(chunk, "\x1b]11;?") {
					_, _ = io.WriteString(ptmx, "\x1b]11;rgb:0000/0000/0000\x1b\\")
				}
				if strings.Contains(chunk, "\x1b]10;?") {
					_, _ = io.WriteString(ptmx, "\x1b]10;rgb:ffff/ffff/ffff\x1b\\")
				}
				if strings.Contains(chunk, "\x1b[6n") {
					_, _ = io.WriteString(ptmx, "\x1b[1;1R")
				}
				t.mu.Lock()
				t.raw = append(t.raw, buf[:n]...)
				// A read can end in the middle of a multi-byte character.
				// The emulator decodes each Write on its own, so only whole
				// characters may be handed to it.
				pending = append(pending, buf[:n]...)
				cut := completeUTF8(pending)
				_, _ = t.vt.Write(pending[:cut])
				pending = append(pending[:0], pending[cut:]...)
				t.mu.Unlock()
			}
			if err != nil {
				return
			}
		}
	}()
	return t, nil
}

// completeUTF8 returns the length of the longest prefix of p that does not
// end inside a UTF-8 sequence.
func completeUTF8(p []byte) int {
	for back := 1; back <= utf8.UTFMax && back <= len(p); back++ {
		i := len(p) - back
		if utf8.RuneStart(p[i]) {
			if !utf8.FullRune(p[i:]) {
				return i
			}
			break
		}
	}
	return len(p)
}

// Send types keys.
func (t *Term) Send(keys string) error {
	_, err := io.WriteString(t.ptmx, keys)
	return err
}

// Key names for Send.
const (
	Enter    = "\r"
	Esc      = "\x1b"
	Tab      = "\t"
	ShiftTab = "\x1b[Z"
	Up       = "\x1b[A"
	Down     = "\x1b[B"
	Right    = "\x1b[C"
	Left     = "\x1b[D"
	PgUp     = "\x1b[5~"
	PgDn     = "\x1b[6~"
	CtrlC    = "\x03"
	CtrlD    = "\x04"
)

// Screen returns the text currently on the emulated screen, one line per
// row, trailing spaces removed.
func (t *Term) Screen() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	lines := strings.Split(t.vt.String(), "\n")
	for i, l := range lines {
		lines[i] = strings.TrimRight(l, " ")
	}
	for len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	return strings.Join(lines, "\n")
}

// Raw returns every byte the program has written.
func (t *Term) Raw() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return string(t.raw)
}

// AltScreen reports whether the program is currently on the alternate screen.
func (t *Term) AltScreen() bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.vt.Mode()&vt10x.ModeAltScreen != 0
}

// WaitFor waits until the screen contains every one of subs.
func (t *Term) WaitFor(timeout time.Duration, subs ...string) error {
	return t.WaitUntil(timeout, strings.Join(subs, " + "), func(screen string) bool {
		for _, s := range subs {
			if !strings.Contains(screen, s) {
				return false
			}
		}
		return true
	})
}

// WaitGone waits until the screen no longer contains sub.
func (t *Term) WaitGone(timeout time.Duration, sub string) error {
	return t.WaitUntil(timeout, "absence of "+sub, func(screen string) bool { return !strings.Contains(screen, sub) })
}

// WaitUntil waits until cond holds for the screen.
func (t *Term) WaitUntil(timeout time.Duration, what string, cond func(screen string) bool) error {
	deadline := time.Now().Add(timeout)
	for {
		if cond(t.Screen()) {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("timed out after %s waiting for %s; screen was:\n%s", timeout, what, t.Screen())
		}
		select {
		case <-t.done:
			if cond(t.Screen()) {
				return nil
			}
			return fmt.Errorf("the program exited while waiting for %s; screen was:\n%s", what, t.Screen())
		case <-time.After(10 * time.Millisecond):
		}
	}
}

// Resize changes the terminal size, as dragging a window corner would.
func (t *Term) Resize(rows, cols int) error {
	t.mu.Lock()
	t.rows, t.cols = rows, cols
	t.vt.Resize(cols, rows)
	t.mu.Unlock()
	return pty.Setsize(t.ptmx, &pty.Winsize{Rows: uint16(rows), Cols: uint16(cols)})
}

// Wait waits for the program to exit and returns its exit code.
func (t *Term) Wait(timeout time.Duration) (int, error) {
	t.waitMu.Lock()
	defer t.waitMu.Unlock()
	if t.waited {
		return t.exit, nil
	}
	res := make(chan error, 1)
	go func() { res <- t.cmd.Wait() }()
	select {
	case err := <-res:
		t.waited = true
		var ee *exec.ExitError
		switch {
		case err == nil:
			t.exit = 0
		case errors.As(err, &ee):
			t.exit = ee.ExitCode()
		default:
			return -1, err
		}
		return t.exit, nil
	case <-time.After(timeout):
		return -1, fmt.Errorf("the program did not exit within %s; screen:\n%s", timeout, t.Screen())
	}
}

// Close kills the program if it is still running and releases the pty.
func (t *Term) Close() {
	if t.cmd.Process != nil {
		_ = t.cmd.Process.Signal(syscall.SIGKILL)
	}
	_, _ = t.Wait(2 * time.Second)
	_ = t.ptmx.Close()
}
