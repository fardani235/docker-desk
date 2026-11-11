// Command docker-desk opens a full-screen terminal workspace on Docker
// daemons: first the one the current Docker context points at, or the one
// named with --host or --context — on this machine, or on another one over
// SSH or TCP — and then any others added from the picker of this machine's
// Docker contexts while it runs. One of them is focused at a time.
//
// Everything that can go wrong with reaching the launch daemon is checked
// before the terminal is taken over: if it cannot be used, the program says
// why in plain words, names the endpoint and exits without drawing anything.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"sync/atomic"
	"syscall"

	tea "github.com/charmbracelet/bubbletea"
	"golang.org/x/term"

	"github.com/fardani235/docker-desk/internal/dockerctx"
	"github.com/fardani235/docker-desk/internal/engine"
	"github.com/fardani235/docker-desk/internal/jobs"
	"github.com/fardani235/docker-desk/internal/session"
	"github.com/fardani235/docker-desk/internal/sshtunnel"
	"github.com/fardani235/docker-desk/internal/ui"
)

// version is set at build time (-ldflags "-X main.version=…").
var version = "dev"

// How long a daemon gets to answer the first requests: a local socket, and
// one that is a network — and perhaps a TLS handshake — away. (Variables so
// that a test of "nothing answered" need not wait this long.)
var (
	localTimeout  = session.DefaultLocalTimeout
	remoteTimeout = session.DefaultRemoteTimeout
)

func main() {
	os.Exit(run(os.Args[1:], os.Stdin, os.Stdout, os.Stderr))
}

const usageHead = `docker-desk — one screen for a Docker engine, local or remote

Usage:
  docker-desk [flags]

The daemon the desk opens on is decided as the Docker CLI decides it:
--context, then --host, then DOCKER_HOST, then DOCKER_CONTEXT, then the
context selected with "docker context use", then the default socket. It must
answer before the workspace opens. Inside, M adds more daemons from this
machine's Docker contexts (and removes any of them); one is focused at a time,
> and < move the focus, and the others report exits and disconnections in the
status line. A daemon added there never asks anything on the terminal.

The daemon may be:
  unix:///path/to/docker.sock      a socket on this machine
  ssh://[user@]host[:port][/path]  a socket on another machine, through your
                                   ssh client (keys, agent, ~/.ssh/config);
                                   /path defaults to /var/run/docker.sock
  tcp://host[:port]                the Engine API over TCP; with TLS when
                                   DOCKER_TLS_VERIFY is set (certificates in
                                   DOCKER_CERT_PATH) or the context stores
                                   certificates, otherwise NOT encrypted;
                                   port 2375 if none is given, as for docker

Flags:
  -H, --host URL      the daemon to talk to (overrides DOCKER_HOST)
  -c, --context NAME  the Docker context to use (overrides everything else)
  --read-only         look, don't touch: every action that changes or deletes
                      something, and the shell, is disabled; any request other
                      than GET/HEAD is refused before it is sent — on every
                      daemon of the session, including ones added later
  --no-registry       never contact a registry: the Images › Upstream tab and U
                      ask nothing, by the desk or through the daemon (the
                      air-gap switch; --read-only alone still allows the
                      check, which only reads)
  --no-compose-files  never read a compose file: the Compose › Declared tab
                      compares nothing (--read-only alone still allows the
                      comparison, which only reads)
  --check             resolve the daemon, verify it answers, print what would
                      be used, and exit without opening the workspace
  --report PATH       write a report of --report-object to PATH and exit,
                      without opening the workspace (no terminal needed)
  --report-format F   json (default: everything computed), spdx or cyclonedx
                      (packages only), or text (findings by group)
  --report-object REF the image to analyse, by reference or ID; or :space
                      for the daemon's disk account (json or text)
  --force             with --report: replace a file already at PATH
  --theme NAME        colour theme (or DOCKER_DESK_THEME); T cycles inside
  --version           print the version and exit
  -h, --help          this text

Themes:
`

const usageTail = `
Environment:
  DOCKER_HOST, DOCKER_CONTEXT, DOCKER_CONFIG, DOCKER_TLS_VERIFY, DOCKER_TLS,
  DOCKER_CERT_PATH    as for the Docker CLI
  DOCKER_DESK_THEME   colour theme, when --theme is not given
  DOCKER_DESK_SSH     the ssh program and leading arguments (default: ssh)
  NO_COLOR            no colour at all, whatever the theme

Inside the desk, press ? for every key.
`

func usage() string { return usageHead + ui.ThemeHelp() + usageTail }

// access holds the flags that bound what the desk may send or read.
type access struct {
	readOnly       bool // --read-only: GET and HEAD only
	noRegistry     bool // --no-registry: no registry request at all
	noComposeFiles bool // --no-compose-files: no compose file is read
}

func (a *access) register(fs *flag.FlagSet) {
	fs.BoolVar(&a.readOnly, "read-only", false, "")
	fs.BoolVar(&a.noRegistry, "no-registry", false, "")
	fs.BoolVar(&a.noComposeFiles, "no-compose-files", false, "")
}

// apply threads the flags into the workspace's options.
func (a access) apply(o ui.Options) ui.Options {
	o.ReadOnly = a.readOnly
	o.NoRegistry = a.noRegistry
	o.NoComposeFiles = a.noComposeFiles
	return o
}

func run(args []string, stdin *os.File, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("docker-desk", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	var acc access
	acc.register(fs)
	readOnly := &acc.readOnly
	check := fs.Bool("check", false, "")
	showVersion := fs.Bool("version", false, "")
	var rep reportFlags
	fs.StringVar(&rep.path, "report", "", "")
	fs.StringVar(&rep.format, "report-format", "", "")
	fs.StringVar(&rep.object, "report-object", "", "")
	fs.BoolVar(&rep.force, "force", false, "")
	var sel dockerctx.Selection
	fs.StringVar(&sel.Host, "host", "", "")
	fs.StringVar(&sel.Host, "H", "", "")
	fs.StringVar(&sel.Context, "context", "", "")
	fs.StringVar(&sel.Context, "c", "", "")
	themeName := fs.String("theme", "", "")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			fmt.Fprint(stdout, usage())
			return 0
		}
		fmt.Fprintf(stderr, "docker-desk: %v\n\n%s", err, usage())
		return 2
	}
	if fs.NArg() > 0 {
		fmt.Fprintf(stderr, "docker-desk: unexpected argument %q\n\n%s", fs.Arg(0), usage())
		return 2
	}
	if *showVersion {
		fmt.Fprintf(stdout, "docker-desk %s\n", version)
		return 0
	}
	if *themeName == "" {
		*themeName = os.Getenv("DOCKER_DESK_THEME")
	}
	theme, err := ui.ThemeByName(*themeName)
	if err != nil {
		fmt.Fprintf(stderr, "docker-desk: %v\n", err)
		return 2
	}
	headless, err := rep.check(*check)
	if err != nil {
		fmt.Fprintf(stderr, "docker-desk: %v\n", err)
		return 2
	}

	// 1. Which daemon, and how is it reached?
	home, _ := os.UserHomeDir()
	ep, err := dockerctx.Resolve(sel, os.Getenv, home)
	if err != nil {
		fmt.Fprintf(stderr, "docker-desk: cannot work out which Docker daemon to use.\n\n  %v\n\nThe workspace was not opened.\n", err)
		return 1
	}
	target, err := ep.Target()
	if err != nil {
		fmt.Fprintf(stderr, `docker-desk: cannot use this Docker endpoint.

  endpoint   %s
  context    %s
  chosen by  %s
  reason     %v

The workspace was not opened.
`, ep.Host, ep.Context, ep.Source, err)
		return 1
	}
	link := ep.Link()

	// Until the desk owns the terminal, Ctrl+C — or the terminal being
	// closed — must end an ssh that is waiting for a password as well as
	// this program, and take the tunnel's socket away with it.
	launch, stopSignals := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM, syscall.SIGHUP)
	defer stopSignals()

	// 2. Open the way there. A local socket and a TCP port are tried
	// directly, so that "nothing there" is told apart from "not Docker";
	// an SSH endpoint gets its tunnel, whose local socket then stands in
	// for the daemon.
	// The ssh program, for the launch daemon and for any picked later. It
	// is checked now either way: the picker must not quietly fall back to
	// plain ssh when the one named cannot be used.
	var sshCommand []string
	if custom := strings.TrimSpace(os.Getenv("DOCKER_DESK_SSH")); custom != "" {
		if sshCommand, err = sshtunnel.SplitCommand(custom); err != nil {
			fmt.Fprintf(stderr, "docker-desk: DOCKER_DESK_SSH is not usable: %v\n", err)
			return 2
		}
	}

	dialHost := target.DialHost
	timeout := remoteTimeout
	var tunnel *sshtunnel.Tunnel
	switch target.Transport {
	case dockerctx.TransportUnix:
		timeout = localTimeout
		if err := dockerctx.CheckSocket(launch, ep, localTimeout); err != nil {
			return unreachable(stderr, ep, link, err)
		}
	case dockerctx.TransportTCP:
		if err := dockerctx.CheckTCP(launch, ep, remoteTimeout); err != nil {
			if launch.Err() != nil {
				return interrupted(stderr)
			}
			return unreachable(stderr, ep, link, err)
		}
	case dockerctx.TransportSSH:
		opts := sshtunnel.Options{
			User: target.User, Host: target.Hostname, Port: target.Port,
			RemoteSocket: target.Socket,
			Interactive:  term.IsTerminal(int(stdin.Fd())),
			Stderr:       stderr,
			Command:      sshCommand,
		}
		if isTerminal(stderr) {
			fmt.Fprintf(stderr, "docker-desk: opening an SSH tunnel to %s (ssh %s) …\n", target.Socket, opts.Destination())
		}
		tunnel, err = sshtunnel.Open(launch, opts)
		if err != nil {
			if launch.Err() != nil {
				return interrupted(stderr)
			}
			return unreachable(stderr, ep, link, session.ExplainTunnel(ep, err))
		}
		defer tunnel.Close()
		// ssh has asked what it had to ask. From here on what it says is
		// kept for the explanations below, and for the desk's status line,
		// and no longer written to the terminal.
		tunnel.Detach()
		dialHost = tunnel.Host()
	}

	// 3. Is anything there, and does it speak the Engine API?
	var tlsOpts *engine.TLS
	if t := ep.TLS; t != nil {
		tlsOpts = &engine.TLS{CAFile: t.CAFile, CertFile: t.CertFile, KeyFile: t.KeyFile, SkipVerify: t.SkipVerify}
	}
	eng, err := engine.New(engine.Options{Host: dialHost, ShownAs: ep.Host, TLS: tlsOpts, ReadOnly: *readOnly, UserAgent: "docker-desk/" + version})
	if err != nil {
		// Nothing was tried yet: this is the client's own set-up — a
		// certificate file that cannot be read, most likely.
		fmt.Fprintf(stderr, "docker-desk: cannot set up the connection to the Docker daemon.\n\n  endpoint   %s\n  context    %s\n  chosen by  %s\n  reason     %v\n\nThe workspace was not opened.\n",
			ep.Host, ep.Context, ep.Source, err)
		return 1
	}
	defer eng.Close()
	ctx, cancel := context.WithTimeout(launch, timeout)
	defer cancel()
	if err := eng.Ping(ctx); err != nil {
		if launch.Err() != nil {
			return interrupted(stderr)
		}
		// Explaining may itself ask the remote machine a question, so it
		// gets its own time rather than what the ping left over.
		return unreachable(stderr, ep, link, session.ExplainPing(launch, ep, target, tunnel, err, remoteTimeout))
	}
	info, err := eng.Info(ctx)
	if err != nil {
		if launch.Err() != nil {
			return interrupted(stderr)
		}
		u := dockerctx.Classify(ep, err)
		u.Reason = "the daemon answered the ping but not the info request: " + err.Error()
		return unreachable(stderr, ep, link, u)
	}

	if headless {
		return runReport(launch, eng, info, rep, stdout, stderr)
	}

	if *check {
		mode := "read-write (every change still needs an in-app confirmation)"
		if *readOnly {
			mode = "read-only"
		}
		fmt.Fprintf(stdout, "endpoint   %s\ncontext    %s\nchosen by  %s\nvia        %s\nengine     %s on %s (API %s, %s/%s)\nmode       %s\n",
			ep.Host, ep.Context, ep.Source, link.Words, info.ServerVersion, info.Name, info.APIVersion, info.OSType, info.Architecture, mode)
		return 0
	}

	// 4. A full-screen workspace needs a terminal on both ends.
	if !term.IsTerminal(int(stdin.Fd())) || !isTerminal(stdout) {
		fmt.Fprintln(stderr, "docker-desk: this is a full-screen terminal program and needs an interactive terminal on stdin and stdout.\n(Use --check to verify the daemon connection from a script.)")
		return 1
	}

	// Only now is the terminal taken over; Bubble Tea does its own signal
	// handling from here.
	uiOpts := acc.apply(ui.Options{Engine: eng, Endpoint: ep, Info: info, Theme: theme,
		DockerConfigDir: dockerctx.ConfigDir(os.Getenv, home), Version: version, HomeDir: home})
	if tunnel != nil {
		uiOpts.LinkProblem = tunnel.Problem
	}
	// The launch daemon may be removed from the session like any other:
	// its engine and its ssh process go with it. (The deferred closes
	// above find them closed already; both are idempotent.)
	uiOpts.CloseLaunch = func() {
		_ = eng.Close()
		if tunnel != nil {
			tunnel.Close()
		}
	}
	uiOpts.Dial = session.DialOptions{
		UserAgent:    "docker-desk/" + version,
		LocalTimeout: localTimeout, RemoteTimeout: remoteTimeout,
		SSHCommand: sshCommand,
	}
	stopSignals()
	model := ui.New(uiOpts)
	prog := tea.NewProgram(model, tea.WithAltScreen(), tea.WithInput(stdin), tea.WithOutput(stdout))
	// Bubble Tea ends the program on SIGINT and SIGTERM. Closing the
	// terminal window sends SIGHUP, which by default would end the process
	// on the spot — leaving the tunnel's socket behind and, where the
	// kernel cannot be asked to do it, its ssh. End the desk the ordinary
	// way instead, so that everything below runs.
	hup := make(chan os.Signal, 1)
	var hungUp atomic.Bool
	signal.Notify(hup, syscall.SIGHUP)
	go func() {
		if _, ok := <-hup; ok {
			hungUp.Store(true)
			prog.Kill()
		}
	}()
	_, runErr := prog.Run()
	signal.Stop(hup)
	close(hup)
	if hungUp.Load() {
		runErr = nil // the terminal is gone; there is nobody to tell
	}

	running := 0
	for _, j := range model.Jobs().List() {
		if j.State == jobs.Running {
			running++
		}
	}
	model.Close()
	if running > 0 {
		fmt.Fprintf(stderr, "docker-desk: %d job(s) were still running and have been cancelled; requests the daemon had already accepted may have completed.\n", running)
	}
	if runErr != nil {
		fmt.Fprintf(stderr, "docker-desk: %v\n", runErr)
		return 1
	}
	return 0
}

func isTerminal(w io.Writer) bool {
	f, ok := w.(*os.File)
	return ok && term.IsTerminal(int(f.Fd()))
}

// interrupted is Ctrl+C (or SIGTERM) before the workspace opened.
func interrupted(stderr io.Writer) int {
	fmt.Fprintln(stderr, "\ndocker-desk: interrupted. The workspace was not opened.")
	return 130
}

func unreachable(stderr io.Writer, ep dockerctx.Endpoint, link dockerctx.Link, err error) int {
	reason, hint := err.Error(), ""
	var u *dockerctx.Unreachable
	if errors.As(err, &u) {
		reason, hint = u.Reason, u.Hint
	}
	fmt.Fprintf(stderr, `docker-desk: the Docker daemon is not reachable.

  endpoint   %s
  context    %s
  chosen by  %s
`, ep.Host, ep.Context, ep.Source)
	if link.Transport != dockerctx.TransportUnix {
		fmt.Fprintf(stderr, "  via        %s\n", link.Words)
	}
	fmt.Fprintf(stderr, "  reason     %s\n", reason)
	if hint != "" {
		fmt.Fprintf(stderr, "  hint       %s\n", hint)
	}
	fmt.Fprint(stderr, "\nThe workspace was not opened.\n")
	return 1
}
