// Command docker-desk opens a full-screen terminal workspace on the Docker
// daemon the current Docker context points at.
//
// Everything that can go wrong with reaching the daemon is checked before
// the terminal is taken over: if the daemon cannot be used, the program says
// why in plain words, names the endpoint and exits without drawing anything.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"golang.org/x/term"

	"github.com/fardani235/docker-desk/internal/dockerctx"
	"github.com/fardani235/docker-desk/internal/engine"
	"github.com/fardani235/docker-desk/internal/jobs"
	"github.com/fardani235/docker-desk/internal/ui"
)

// version is set at build time (-ldflags "-X main.version=…").
var version = "dev"

const connectTimeout = 4 * time.Second

func main() {
	os.Exit(run(os.Args[1:], os.Stdin, os.Stdout, os.Stderr))
}

const usage = `docker-desk — one screen for your local Docker engine

Usage:
  docker-desk [flags]

Docker Desk talks to exactly one daemon: the one your current Docker context
points at (DOCKER_HOST, then DOCKER_CONTEXT, then the context selected with
"docker context use", then the default socket). It only accepts a local unix
socket and has no way to switch daemon while running.

Flags:
  --read-only   look, don't touch: every action that changes or deletes
                something, and the shell, is disabled; any request other than
                GET/HEAD is refused before it is sent
  --check       resolve the daemon, verify it answers, print what would be
                used, and exit without opening the workspace
  --version     print the version and exit
  -h, --help    this text

Inside the desk, press ? for every key.
`

func run(args []string, stdin *os.File, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("docker-desk", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	readOnly := fs.Bool("read-only", false, "")
	check := fs.Bool("check", false, "")
	showVersion := fs.Bool("version", false, "")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			fmt.Fprint(stdout, usage)
			return 0
		}
		fmt.Fprintf(stderr, "docker-desk: %v\n\n%s", err, usage)
		return 2
	}
	if fs.NArg() > 0 {
		fmt.Fprintf(stderr, "docker-desk: unexpected argument %q\n\n%s", fs.Arg(0), usage)
		return 2
	}
	if *showVersion {
		fmt.Fprintf(stdout, "docker-desk %s\n", version)
		return 0
	}

	// 1. Which daemon?
	home, _ := os.UserHomeDir()
	ep, err := dockerctx.Resolve(os.Getenv, home)
	if err != nil {
		fmt.Fprintf(stderr, "docker-desk: cannot work out which Docker daemon to use.\n\n  %v\n\nThe workspace was not opened.\n", err)
		return 1
	}

	// 2. Local only.
	if _, err := dockerctx.SocketPath(ep.Host); err != nil {
		if errors.Is(err, dockerctx.ErrRemote) {
			fmt.Fprintf(stderr, `docker-desk: refusing to start — the current Docker context is not a local daemon.

  endpoint   %s
  context    %s
  chosen by  %s

Docker Desk only talks to a local daemon over a unix socket; it does not
connect over the network (tcp://, ssh://). Point your context at the local
daemon ("docker context use default") or unset DOCKER_HOST.

The workspace was not opened.
`, ep.Host, ep.Context, ep.Source)
			return 1
		}
		fmt.Fprintf(stderr, "docker-desk: invalid Docker endpoint %q: %v\n\nThe workspace was not opened.\n", ep.Host, err)
		return 1
	}

	// 3. Is anything there, and does it speak the Engine API?
	ctx, cancel := context.WithTimeout(context.Background(), connectTimeout)
	defer cancel()
	if err := dockerctx.CheckSocket(ctx, ep, connectTimeout); err != nil {
		return unreachable(stderr, ep, err)
	}
	eng, err := engine.New(engine.Options{Host: ep.Host, ReadOnly: *readOnly, UserAgent: "docker-desk/" + version})
	if err != nil {
		return unreachable(stderr, ep, err)
	}
	defer eng.Close()
	if err := eng.Ping(ctx); err != nil {
		u := dockerctx.Classify(ep, err)
		u.Reason = "the socket accepted the connection but the Engine API did not answer: " + err.Error()
		return unreachable(stderr, ep, u)
	}
	info, err := eng.Info(ctx)
	if err != nil {
		u := dockerctx.Classify(ep, err)
		u.Reason = "the daemon answered the ping but not the info request: " + err.Error()
		return unreachable(stderr, ep, u)
	}

	if *check {
		mode := "read-write (every change still needs an in-app confirmation)"
		if *readOnly {
			mode = "read-only"
		}
		fmt.Fprintf(stdout, "endpoint   %s\ncontext    %s\nchosen by  %s\nengine     %s on %s (API %s, %s/%s)\nmode       %s\n",
			ep.Host, ep.Context, ep.Source, info.ServerVersion, info.Name, info.APIVersion, info.OSType, info.Architecture, mode)
		return 0
	}

	// 4. A full-screen workspace needs a terminal on both ends.
	if !term.IsTerminal(int(stdin.Fd())) || !isTerminal(stdout) {
		fmt.Fprintln(stderr, "docker-desk: this is a full-screen terminal program and needs an interactive terminal on stdin and stdout.\n(Use --check to verify the daemon connection from a script.)")
		return 1
	}

	// Only now is the terminal taken over.
	model := ui.New(ui.Options{Engine: eng, Endpoint: ep, Info: info, ReadOnly: *readOnly})
	prog := tea.NewProgram(model, tea.WithAltScreen(), tea.WithInput(stdin), tea.WithOutput(stdout))
	_, runErr := prog.Run()

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

func unreachable(stderr io.Writer, ep dockerctx.Endpoint, err error) int {
	reason, hint := err.Error(), ""
	var u *dockerctx.Unreachable
	if errors.As(err, &u) {
		reason, hint = u.Reason, u.Hint
	}
	fmt.Fprintf(stderr, `docker-desk: the Docker daemon is not reachable.

  endpoint   %s
  context    %s
  chosen by  %s
  reason     %s
`, ep.Host, ep.Context, ep.Source, reason)
	if hint != "" {
		fmt.Fprintf(stderr, "  hint       %s\n", hint)
	}
	fmt.Fprint(stderr, "\nThe workspace was not opened.\n")
	return 1
}
