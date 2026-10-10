// Package shell runs an interactive process inside a container on the
// operator's own terminal and gives the terminal back afterwards.
//
// A Session is a tea.ExecCommand: Bubble Tea leaves the alternate screen and
// stops reading the keyboard, calls Run, and re-enters the desk when Run
// returns. Inside Run the session owns the terminal: raw mode, bytes copied
// both ways, window-size changes forwarded. Whatever happens — clean exit,
// the container dying, the daemon failing, a panic — the terminal modes the
// session changed are restored before Run returns, and the goroutine reading
// the keyboard is stopped so that it cannot swallow a key meant for the desk.
//
// It talks to the daemon through the engine layer only; the docker CLI is
// not involved. See docs/DECISIONS.md, D16.
package shell

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"github.com/muesli/cancelreader"
	"golang.org/x/term"

	"github.com/fardani235/docker-desk/internal/engine"
)

// DefaultScript starts bash when the container has it and sh otherwise.
const DefaultScript = `command -v bash >/dev/null 2>&1 && exec bash || exec sh`

// DefaultCommand is the command proposed for a shell.
var DefaultCommand = []string{"sh", "-c", DefaultScript}

// DefaultCommandText is DefaultCommand as shown in, and parsed back from,
// the shell form.
const DefaultCommandText = "sh -c '" + DefaultScript + "'"

// Result is what a finished session reports back to the desk.
type Result struct {
	// Started is true once the daemon accepted the exec and the stream opened.
	Started bool
	// ExitCode is the process's exit code (valid when Started and !Detached);
	// -1 when the daemon could not be asked.
	ExitCode int
	// Detached is true when the stream ended but the process is still
	// running in the container (the operator used the detach keys).
	Detached bool
	// Forced is true when the operator ended the session locally with the
	// escape sequence; the process may still be running in the container.
	Forced bool
	// Tail is the last output of the session. When a process cannot start,
	// the daemon reports why in the stream, so this is where the reason is.
	Tail string
	// Duration is how long the session lasted.
	Duration time.Duration
}

// Session is one interactive exec. Create it with New, hand it to tea.Exec,
// read Result afterwards.
type Session struct {
	eng  engine.Engine
	id   string
	name string
	cmd  []string

	stdin  io.Reader
	stdout io.Writer
	stderr io.Writer

	// Result is filled in by Run.
	Result Result
}

// New prepares a session that runs cmd in the container.
func New(eng engine.Engine, containerID, containerName string, cmd []string) *Session {
	return &Session{eng: eng, id: containerID, name: containerName, cmd: cmd}
}

// SetStdin implements tea.ExecCommand.
func (s *Session) SetStdin(r io.Reader) { s.stdin = r }

// SetStdout implements tea.ExecCommand.
func (s *Session) SetStdout(w io.Writer) { s.stdout = w }

// SetStderr implements tea.ExecCommand.
func (s *Session) SetStderr(w io.Writer) { s.stderr = w }

// terminalOf returns the file descriptor of v when it is a terminal.
func terminalOf(v any) (fd int, ok bool) {
	f, isFile := v.(*os.File)
	if !isFile || f == nil {
		return 0, false
	}
	fd = int(f.Fd())
	return fd, term.IsTerminal(fd)
}

// Run executes the session. It returns an error only when the session could
// not be started; how the process ended is in Result.
func (s *Session) Run() (err error) {
	begin := time.Now()
	defer func() { s.Result.Duration = time.Since(begin) }()
	if s.stdin == nil || s.stdout == nil {
		return errors.New("shell: no terminal attached")
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	inFd, inIsTerm := terminalOf(s.stdin)
	outFd, outIsTerm := terminalOf(s.stdout)
	size := func() (rows, cols uint) {
		for _, t := range []struct {
			fd int
			ok bool
		}{{outFd, outIsTerm}, {inFd, inIsTerm}} {
			if t.ok {
				if w, h, err := term.GetSize(t.fd); err == nil && w > 0 && h > 0 {
					return uint(h), uint(w)
				}
			}
		}
		return 24, 80
	}

	rows, cols := size()
	fmt.Fprintf(s.stdout, "\x1b[2m── docker-desk: %s in %s — exit (or Ctrl-D) returns to the desk; Ctrl-] three times forces it ──\x1b[0m\r\n", s.cmd[0], s.name)

	// The keyboard reader must be cancellable: a goroutine left blocked in
	// Read would steal the first key pressed back at the desk. It is set up
	// before anything is started in the container, so that a failure here
	// cannot leave a process running that nobody is attached to.
	in, cerr := cancelreader.NewReader(s.stdin)
	if cerr != nil {
		return fmt.Errorf("shell: cannot read the terminal: %w", cerr)
	}
	defer in.Close()

	// Starting must not be able to hang the desk: while the terminal is
	// handed over nothing else can be pressed, so a daemon that accepts the
	// connection and never answers would otherwise be a dead screen.
	sctx, scancel := context.WithTimeout(ctx, startTimeout)
	execID, err := s.eng.ExecCreate(sctx, s.id, s.cmd, rows, cols)
	if err != nil {
		scancel()
		return startError(err)
	}
	conn, err := s.eng.ExecAttach(sctx, execID, rows, cols)
	scancel()
	if err != nil {
		return startError(err)
	}
	defer conn.Close()
	s.Result.Started = true

	// From here on the terminal is ours. Raw mode is undone by the deferred
	// restore on every path out of this function, including a panic.
	if inIsTerm {
		state, rerr := term.MakeRaw(inFd)
		if rerr == nil {
			defer func() { _ = term.Restore(inFd, state) }()
		}
	}
	// Whatever the remote program switched on in the terminal (mouse
	// reporting, bracketed paste, the alternate screen, a hidden cursor) is
	// switched off again when the session ends, however it ends.
	defer fmt.Fprint(s.stdout, resetTerminalModes)

	// Window size: once now (the size given at creation can be lost if the
	// process was not yet running), then on every SIGWINCH. SIGTERM and
	// SIGHUP end the session instead of being lost: Bubble Tea ignores
	// signals while the terminal is handed over, and a session that cannot
	// be ended from outside would leave the terminal raw if killed harder.
	_ = s.eng.ExecResize(ctx, execID, rows, cols)
	sigs := make(chan os.Signal, 4)
	signal.Notify(sigs, syscall.SIGWINCH, syscall.SIGTERM, syscall.SIGHUP)
	defer signal.Stop(sigs)
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		defer func() { _ = recover() }()
		for {
			select {
			case <-ctx.Done():
				return
			case sig := <-sigs:
				if sig != syscall.SIGWINCH {
					_ = conn.Close() // ends the output copy below
					return
				}
				r, c := size()
				_ = s.eng.ExecResize(ctx, execID, r, c)
			}
		}
	}()

	// Keyboard → process.
	inDone := make(chan struct{})
	go func() {
		defer close(inDone)
		defer func() { _ = recover() }()
		_, _ = io.Copy(&escapeWriter{w: conn, onEscape: func() { s.Result.Forced = true; _ = conn.Close() }}, in)
		_ = conn.CloseWrite()
	}()

	// Process → screen, until the process ends or the stream is cut.
	tail := &tailBuffer{max: 2048}
	_, _ = io.Copy(io.MultiWriter(s.stdout, tail), conn)

	in.Cancel()
	select {
	case <-inDone:
	case <-time.After(time.Second):
		// The reader could not be cancelled (not a pollable file). Closing
		// the connection unblocks the writer side; nothing more can be done
		// for a blocked Read, and it only happens off a real terminal.
	}
	cancel()
	wg.Wait()
	s.Result.Tail = tail.String()
	if inIsTerm {
		flushInput(inFd) // keys typed blind while the session was ending are not for the desk
	}
	if s.Result.Forced {
		s.Result.Detached = true
		return nil
	}

	// The daemon records the exit code a moment after the stream closes.
	ictx, icancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer icancel()
	for i := 0; i < 20; i++ {
		st, ierr := s.eng.ExecInspect(ictx, execID)
		if ierr != nil {
			s.Result.ExitCode = -1 // unknown: the daemon could not be asked
			return nil
		}
		if !st.Running {
			s.Result.ExitCode = st.ExitCode
			return nil
		}
		time.Sleep(50 * time.Millisecond)
	}
	s.Result.Detached = true
	return nil
}

// startTimeout bounds how long the daemon may take to accept and start an
// exec before the desk gives up and comes back. A variable so that tests can
// shorten it.
var startTimeout = 15 * time.Second

func startError(err error) error {
	if errors.Is(err, context.DeadlineExceeded) {
		return fmt.Errorf("the daemon did not start the process within %s", startTimeout)
	}
	return err
}

// resetTerminalModes turns off what a full-screen program inside the
// container may have left on: mouse reporting, bracketed paste, application
// cursor keys, the alternate screen, colours, a hidden cursor.
const resetTerminalModes = "\x1b[?1000l\x1b[?1002l\x1b[?1003l\x1b[?1006l\x1b[?2004l\x1b[?1l\x1b[?1049l\x1b[0m\x1b[?25h"

// escapeByte is Ctrl-]. Pressed three times in a row it ends the session
// locally, without the daemon's cooperation — the way out when the daemon
// has stopped answering but the connection is still open.
const escapeByte = 0x1d

// escapeWriter forwards keystrokes and watches for the local escape.
type escapeWriter struct {
	w        io.Writer
	run      int
	onEscape func()
}

func (e *escapeWriter) Write(p []byte) (int, error) {
	for _, b := range p {
		if b == escapeByte {
			e.run++
			if e.run >= 3 {
				e.onEscape()
				return 0, io.ErrClosedPipe
			}
		} else {
			e.run = 0
		}
	}
	return e.w.Write(p)
}

// tailBuffer keeps the last max bytes written to it.
type tailBuffer struct {
	mu  sync.Mutex
	buf []byte
	max int
}

func (t *tailBuffer) Write(p []byte) (int, error) {
	t.mu.Lock()
	t.buf = append(t.buf, p...)
	if len(t.buf) > t.max {
		t.buf = append([]byte(nil), t.buf[len(t.buf)-t.max:]...)
	}
	t.mu.Unlock()
	return len(p), nil
}

func (t *tailBuffer) String() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return string(t.buf)
}
