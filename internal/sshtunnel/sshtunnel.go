// Package sshtunnel reaches a Docker daemon's unix socket on another machine
// by having the system's ssh client forward a private local socket to it.
//
// One ssh process carries every connection the desk makes — lists, the event
// stream, a statistics stream per container, the hijacked shell — as channels
// of a single authenticated SSH connection. The rest of the program sees an
// ordinary unix socket and is unchanged. The process is supervised: if it
// dies it is started again, without ever asking a question on a terminal the
// desk has taken over.
//
// Why ssh's forwarding and not `docker system dial-stdio` per connection (the
// Docker CLI's way), and not an SSH library: docs/DECISIONS.md, R2 and R3.
package sshtunnel

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/fardani235/docker-desk/internal/testguard"
)

// Options says where to tunnel to and how.
type Options struct {
	// User and Port are optional; when empty, ssh and its configuration
	// decide. Host may be a name from ~/.ssh/config.
	User string
	Host string
	Port string
	// RemoteSocket is the daemon's unix socket on the remote machine.
	RemoteSocket string

	// Command is the ssh program followed by any leading arguments
	// (default: ssh).
	Command []string
	// Interactive lets ssh ask its questions — a password, a key's
	// passphrase, an unknown host key — on the terminal while the first
	// connection is made. Without it, and for every reconnection, ssh runs
	// in batch mode and fails instead of asking.
	Interactive bool
	// Stderr receives what ssh says — the context of whatever it asks —
	// until Detach is called. May be nil.
	Stderr io.Writer

	// FirstTimeout bounds the first connection (default 3 minutes when
	// interactive, so that a passphrase can be typed; 30 seconds otherwise).
	FirstTimeout time.Duration
	// RetryTimeout bounds each reconnection attempt (default 30 seconds).
	RetryTimeout time.Duration
	// Backoff bounds between reconnection attempts (default 1 s … 15 s).
	MinBackoff time.Duration
	MaxBackoff time.Duration
}

// Destination is the remote machine as ssh would be given it by hand.
func (o Options) Destination() string {
	d := o.Host
	if o.User != "" {
		d = o.User + "@" + d
	}
	if o.Port != "" {
		d = "-p " + o.Port + " " + d
	}
	return d
}

func (o *Options) defaults() {
	if len(o.Command) == 0 {
		o.Command = []string{"ssh"}
	}
	if o.FirstTimeout <= 0 {
		o.FirstTimeout = 30 * time.Second
		if o.Interactive {
			o.FirstTimeout = 3 * time.Minute
		}
	}
	if o.RetryTimeout <= 0 {
		o.RetryTimeout = 30 * time.Second
	}
	if o.MinBackoff <= 0 {
		o.MinBackoff = time.Second
	}
	if o.MaxBackoff <= 0 {
		o.MaxBackoff = 15 * time.Second
	}
}

// args is the ssh command line for one connection.
//
// Options given here win over ~/.ssh/config, so each of them is one the
// tunnel cannot work without:
//
//   - -N -T: no remote command and no terminal — only the forwarding.
//   - ControlMaster=no, ControlPath=none: a connection of its own. Joining
//     the operator's shared connection would leave the forwarding alive in
//     that other process after the desk has quit.
//   - StreamLocalBindUnlink, StreamLocalBindMask=0177: replace a stale
//     socket after a reconnect; the socket is the operator's alone.
//   - ServerAliveInterval/CountMax: notice a dead link in about 30 seconds
//     instead of never, so that the supervisor can reconnect.
//   - ForkAfterAuthentication=no (stayInFront): an ssh that the operator's
//     configuration sends to the background after logging in would look to
//     the desk like one that had exited, and would be left running. The
//     option is newer than the others (OpenSSH 8.7); Open drops it for a
//     client that does not know it.
//
// The destination comes after "--" and can never be read as an option; see
// CheckDestination for what it may contain.
func (o Options) args(localSocket string, batch, stayInFront bool) []string {
	a := append([]string{}, o.Command[1:]...)
	a = append(a,
		"-N", "-T",
		"-o", "ControlMaster=no",
		"-o", "ControlPath=none",
		"-o", "StreamLocalBindUnlink=yes",
		"-o", "StreamLocalBindMask=0177",
		"-o", "ServerAliveInterval=10",
		"-o", "ServerAliveCountMax=3",
	)
	if stayInFront {
		a = append(a, "-o", "ForkAfterAuthentication=no")
	}
	if batch {
		a = append(a, "-o", "BatchMode=yes")
	}
	a = append(a, "-L", localSocket+":"+o.RemoteSocket)
	a = append(a, o.destArgs()...)
	return a
}

func (o Options) destArgs() []string {
	var a []string
	if o.User != "" {
		a = append(a, "-l", o.User)
	}
	if o.Port != "" {
		a = append(a, "-p", o.Port)
	}
	return append(a, "--", o.Host)
}

// CheckDestination decides whether a destination may be handed to ssh. It is
// deliberately narrower than what ssh accepts: every part ends up on a
// command line, and ssh itself expands `${VAR}` and `%`-tokens in the paths
// of a forwarding — a socket path of `/${AWS_SECRET_ACCESS_KEY}` would send
// that variable to the server. So each part is held to a list of characters
// that mean nothing to ssh, to a shell, or to a terminal.
//
// A user name or socket path that needs anything else belongs in
// ~/.ssh/config, under a host alias, where the operator wrote it themselves.
func CheckDestination(user, host, port, socket string) error {
	switch {
	case host == "":
		return errors.New("no host")
	case strings.HasPrefix(host, "-") || strings.HasPrefix(user, "-"):
		return errors.New("the host and user may not start with '-' (ssh would read them as an option)")
	case !only(host, "._:-"):
		return fmt.Errorf("the host %q may contain only letters, digits and . _ : -", host)
	case !only(user, "._@+-"):
		return fmt.Errorf("the user %q may contain only letters, digits and . _ @ + - (give other names as `User` in ~/.ssh/config)", user)
	case !only(port, "") || len(port) > 5:
		return fmt.Errorf("the port %q is not a number", port)
	}
	return checkSocketPath(socket)
}

// checkSocketPath holds a path that goes into ssh's forwarding specification
// to characters that ssh neither splits on (:), nor unescapes (\), nor
// expands (% and $).
func checkSocketPath(path string) error {
	if !strings.HasPrefix(path, "/") {
		return fmt.Errorf("the socket path %q is not absolute", path)
	}
	if !only(path, "._/@+,=-") {
		return fmt.Errorf("the socket path %q may contain only letters, digits and . _ / @ + , = -", path)
	}
	return nil
}

// only reports whether s consists of ASCII letters, digits and the
// characters in also.
func only(s, also string) bool {
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
		case strings.ContainsRune(also, r):
		default:
			return false
		}
	}
	return true
}

// Error is a tunnel that could not be established, in the operator's terms.
type Error struct {
	// Reason is what went wrong; when ssh said something, it is ssh's words.
	Reason string
	// Hint is an optional suggestion.
	Hint string
	Err  error
}

func (e *Error) Error() string { return e.Reason }
func (e *Error) Unwrap() error { return e.Err }

// Tunnel is a running forward from a private local socket to the remote
// daemon's socket.
type Tunnel struct {
	opts   Options
	dir    string
	socket string

	up atomic.Bool
	// passMu serialises ssh's messages being passed on with Detach, so
	// that once Detach has returned nothing more is written.
	passMu   sync.Mutex
	detached bool
	mu       sync.Mutex
	problem  string
	restarts int

	// stayInFront is whether ssh is given ForkAfterAuthentication=no. It
	// is settled by Open and only read afterwards.
	stayInFront bool

	closed    chan struct{}
	closeOnce sync.Once
	wg        sync.WaitGroup
}

// Open starts ssh and waits until the local socket accepts connections,
// which it does once ssh has authenticated. It returns an *Error when ssh
// gives up, cannot be started, or does not get there in time.
func Open(ctx context.Context, o Options) (*Tunnel, error) {
	o.defaults()
	if err := CheckDestination(o.User, o.Host, o.Port, o.RemoteSocket); err != nil {
		return nil, &Error{Reason: "this SSH destination cannot be used: " + err.Error(), Err: err}
	}
	if err := testguard.CheckRemoteSocket(o.RemoteSocket); err != nil {
		return nil, err
	}
	dir, err := os.MkdirTemp("", "docker-desk-ssh-")
	if err != nil {
		return nil, &Error{Reason: "cannot create a private directory for the tunnel's socket: " + err.Error(), Err: err}
	}
	t := &Tunnel{opts: o, dir: dir, socket: filepath.Join(dir, "d.sock"), stayInFront: true, closed: make(chan struct{})}
	// The local path goes into the same forwarding specification as the
	// remote one, and the kernel limits a unix socket's path to about a
	// hundred bytes.
	if err := checkSocketPath(t.socket); err != nil || len(t.socket) > 100 {
		_ = os.RemoveAll(dir)
		return nil, &Error{Reason: fmt.Sprintf("the temporary directory makes a socket path ssh cannot be given (%s); set TMPDIR to a short, plain path such as /tmp", t.socket)}
	}

	ctx, cancel := context.WithTimeout(ctx, o.FirstTimeout)
	defer cancel()
	p, err := t.connect(ctx, !o.Interactive)
	if err != nil && t.stayInFront && p != nil && unknownOption(p.out.String(), "forkafterauthentication") {
		// An ssh older than the option: it refused its command line
		// before connecting to anything. Without the option, then.
		t.stayInFront = false
		p, err = t.connect(ctx, !o.Interactive)
	}
	if err != nil {
		_ = os.RemoveAll(dir)
		return nil, err
	}
	t.up.Store(true)
	t.wg.Add(1)
	go t.supervise(p)
	return t, nil
}

// connect starts ssh and waits for the tunnel. The process is returned even
// when it failed, for what it said.
func (t *Tunnel) connect(ctx context.Context, batch bool) (*proc, error) {
	p, err := t.start(batch)
	if err != nil {
		return nil, err
	}
	return p, t.await(ctx, p)
}

// unknownOption reports whether ssh rejected the named -o option.
func unknownOption(said, option string) bool {
	said = strings.ToLower(said)
	return strings.Contains(said, "bad configuration option") && strings.Contains(said, option)
}

// Host is the local end of the tunnel as a Docker endpoint.
func (t *Tunnel) Host() string { return "unix://" + t.socket }

// Up reports whether the ssh connection is currently established.
func (t *Tunnel) Up() bool { return t.up.Load() }

// Problem is why the tunnel is down, or empty while it is up.
func (t *Tunnel) Problem() string {
	if t.Up() {
		return ""
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.problem
}

// Restarts is how many times ssh has been started again.
func (t *Tunnel) Restarts() int {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.restarts
}

// Detach stops passing ssh's messages on to Options.Stderr; they are still
// kept for Problem. Once it has returned, nothing more is written there: the
// caller may write to it, or hand the terminal to something else.
func (t *Tunnel) Detach() {
	t.passMu.Lock()
	t.detached = true
	t.passMu.Unlock()
}

// Close ends the ssh process and removes the local socket.
func (t *Tunnel) Close() {
	t.closeOnce.Do(func() { close(t.closed) })
	t.wg.Wait()
	t.up.Store(false)
	_ = os.RemoveAll(t.dir)
}

// ---- the ssh process -------------------------------------------------------

type proc struct {
	cmd  *exec.Cmd
	done chan struct{} // closed when the process has exited
	err  error         // its exit status, valid after done
	out  *tail
}

func (t *Tunnel) start(batch bool) (*proc, error) {
	o := t.opts
	p := &proc{done: make(chan struct{}), out: &tail{max: 4 << 10}}
	p.cmd = exec.Command(o.Command[0], o.args(t.socket, batch, t.stayInFront)...)
	// No stdin: nothing is ever typed into ssh through the desk. When ssh
	// asks something it asks the terminal directly — which is why a batch
	// run is given no terminal at all (see quiet).
	p.cmd.Stdin = nil
	if batch {
		quiet(p.cmd)
	}
	p.cmd.Stdout = io.Discard
	p.cmd.Stderr = &sink{keep: p.out, pass: o.Stderr, t: t}
	// A helper that ssh started (a ProxyCommand, a jump host's ssh) can
	// outlive it holding the same stderr; do not wait for it for ever.
	p.cmd.WaitDelay = 2 * time.Second
	wait, err := startBound(p.cmd, batch)
	if err != nil {
		if errors.Is(err, exec.ErrNotFound) {
			return nil, &Error{
				Reason: fmt.Sprintf("the %q program was not found", o.Command[0]),
				Hint:   "ssh:// endpoints are reached through the OpenSSH client. Install it, or name another with DOCKER_DESK_SSH.",
				Err:    err,
			}
		}
		return nil, &Error{Reason: "cannot start " + o.Command[0] + ": " + err.Error(), Err: err}
	}
	go func() {
		p.err = wait()
		close(p.done)
	}()
	return p, nil
}

// quiet makes sure a non-interactive ssh has no way of asking anything.
// BatchMode is not enough: ssh does not pass it on to the ssh it starts for a
// jump host (ProxyJump), and a ProxyCommand can open the terminal by itself —
// either would prompt underneath the desk and read the operator's keys. So
// the process is also started without a controlling terminal (the detach
// argument of startBound), and told not to try a graphical prompt instead.
func quiet(cmd *exec.Cmd) {
	cmd.Env = append(os.Environ(), "SSH_ASKPASS_REQUIRE=never")
}

func (p *proc) stop() {
	select {
	case <-p.done:
		return
	default:
	}
	_ = p.cmd.Process.Signal(syscall.SIGTERM)
	select {
	case <-p.done:
	case <-time.After(2 * time.Second):
		_ = p.cmd.Process.Kill()
		<-p.done
	}
}

// await waits for the local socket to accept a connection. ssh creates it
// only after it has authenticated, so the socket answering means the tunnel
// is up; ssh exiting first means it is not going to be.
func (t *Tunnel) await(ctx context.Context, p *proc) error {
	tick := time.NewTicker(40 * time.Millisecond)
	defer tick.Stop()
	for {
		if conn, err := net.DialTimeout("unix", t.socket, time.Second); err == nil {
			_ = conn.Close()
			return nil
		}
		// ssh keeps running when it cannot create the forwarding it was
		// asked for; waiting would only end at the timeout.
		if said := p.out.String(); strings.Contains(said, "cannot bind to path "+t.socket) || strings.Contains(said, "Bad local forwarding specification") {
			p.stop()
			return t.failure(p, "ssh could not set up the forwarding")
		}
		select {
		case <-p.done:
			return t.failure(p, "")
		case <-t.closed:
			p.stop()
			return &Error{Reason: "the tunnel was closed", Err: context.Canceled}
		case <-ctx.Done():
			p.stop()
			if errors.Is(ctx.Err(), context.Canceled) {
				return &Error{Reason: "interrupted while ssh was connecting", Err: ctx.Err()}
			}
			return t.failure(p, "ssh did not finish connecting in time")
		case <-tick.C:
		}
	}
}

// failure explains an ssh process that ended (or was ended) before the
// tunnel was up, using what ssh itself said.
func (t *Tunnel) failure(p *proc, why string) *Error {
	said := lastLines(p.out.String(), 3)
	e := &Error{Reason: why, Err: p.err}
	switch {
	case e.Reason != "" && said != "":
		e.Reason += ": " + said
	case e.Reason == "" && said != "":
		e.Reason = "ssh: " + strings.TrimPrefix(said, "ssh: ")
	case e.Reason == "":
		e.Reason = "ssh exited without saying why"
		if p.err != nil {
			e.Reason += " (" + p.err.Error() + ")"
		}
	}
	// The command to try by hand is the one the desk ran, not a bare ssh.
	try := "`" + strings.Join(t.opts.Command, " ") + " " + t.opts.Destination() + "`"
	switch {
	case p.err == nil && said == "":
		// Exit status 0 without a tunnel: ssh logged in and then left.
		e.Reason = "ssh logged in and went into the background instead of staying connected"
		e.Hint = "The desk needs ssh in the foreground: remove -f / ForkAfterAuthentication for this host (~/.ssh/config, DOCKER_DESK_SSH). The ssh that went to the background may still be running."
	case strings.Contains(said, "cannot bind to path"):
		e.Hint = "ssh could not create its local socket " + t.socket + " (is the temporary directory writable?)."
	case strings.Contains(said, "Bad local forwarding specification"):
		e.Hint = "Forwarding to a unix socket needs OpenSSH 6.7 or newer on both sides."
	case strings.Contains(said, "Host key verification failed"):
		e.Hint = "The host's key is unknown or has changed. Run " + try + " once to review it."
	case strings.Contains(said, "Permission denied"):
		e.Hint = "ssh could not log in. Check that " + try + " works from this terminal (key, agent, user name)."
	default:
		e.Hint = "Check that " + try + " works on its own."
	}
	return e
}

// supervise keeps the tunnel alive: when ssh exits, it is started again in
// batch mode, with a growing pause between attempts, until Close.
func (t *Tunnel) supervise(p *proc) {
	defer t.wg.Done()
	o := t.opts
	for {
		select {
		case <-t.closed:
			p.stop()
			return
		case <-p.done:
		}
		t.setDown(downReason(p.out.String()))
		// A socket file nobody listens on would turn "no tunnel" into
		// "connection refused"; without it the reason is unmistakable.
		_ = os.Remove(t.socket)

		backoff := o.MinBackoff
		for {
			select {
			case <-t.closed:
				return
			case <-time.After(backoff):
			}
			next, err := t.start(true)
			if err == nil {
				ctx, cancel := context.WithTimeout(context.Background(), o.RetryTimeout)
				err = t.await(ctx, next)
				cancel()
			}
			select {
			case <-t.closed:
				if next != nil {
					next.stop()
				}
				return
			default:
			}
			if err == nil {
				p = next
				t.mu.Lock()
				t.restarts++
				t.problem = ""
				t.mu.Unlock()
				t.up.Store(true)
				break
			}
			t.setDown(err.Error())
			if backoff *= 2; backoff > o.MaxBackoff {
				backoff = o.MaxBackoff
			}
		}
	}
}

func (t *Tunnel) setDown(problem string) {
	t.mu.Lock()
	t.problem = problem
	t.mu.Unlock()
	t.up.Store(false)
}

// ---- what ssh said ---------------------------------------------------------

// tail keeps the last max bytes written to it.
type tail struct {
	mu  sync.Mutex
	buf []byte
	max int
}

func (t *tail) Write(p []byte) (int, error) {
	t.mu.Lock()
	t.buf = append(t.buf, p...)
	if over := len(t.buf) - t.max; over > 0 {
		t.buf = append(t.buf[:0], t.buf[over:]...)
	}
	t.mu.Unlock()
	return len(p), nil
}

func (t *tail) String() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return string(t.buf)
}

// sink keeps ssh's stderr and, until the tunnel is detached, passes it on.
type sink struct {
	keep *tail
	pass io.Writer
	t    *Tunnel
}

func (s *sink) Write(p []byte) (int, error) {
	_, _ = s.keep.Write(p)
	// An older ssh's complaint about the one option it does not know is
	// the desk's business, not the operator's: Open runs ssh again without
	// the option, and the launch would otherwise begin with an error
	// message about something that then works.
	if s.pass != nil && !unknownOption(string(p), "forkafterauthentication") {
		s.t.passMu.Lock()
		if !s.t.detached {
			_, _ = s.pass.Write(p)
		}
		s.t.passMu.Unlock()
	}
	return len(p), nil
}

// downReason is why an established tunnel ended, from what ssh said. Lines
// about single connections that could not be forwarded ("channel 3: open
// failed") are older news about the daemon, not about the link.
func downReason(said string) string {
	var keep []string
	for _, line := range strings.Split(said, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "channel ") && strings.Contains(line, "open failed") {
			continue
		}
		keep = append(keep, line)
	}
	if reason := lastLines(strings.Join(keep, "\n"), 2); reason != "" {
		return reason
	}
	return "the ssh process ended"
}

// lastLines returns the last n meaningful lines of ssh's output as one line.
// Notices that are not errors are left out.
func lastLines(s string, n int) string {
	var keep []string
	for _, line := range strings.Split(strings.ReplaceAll(s, "\r", ""), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "Warning: Permanently added") || strings.HasPrefix(line, "** ") {
			continue
		}
		keep = append(keep, line)
	}
	if len(keep) > n {
		keep = keep[len(keep)-n:]
	}
	return strings.Join(keep, " / ")
}
