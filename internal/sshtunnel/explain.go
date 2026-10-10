package sshtunnel

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
	"time"
)

// Explain works out why the remote socket cannot be opened although the SSH
// connection itself is up.
//
// ssh reports every such failure with the same words ("open failed"), so the
// remote machine is asked directly, over a second, non-interactive
// connection. When that is not possible either — the login needed a typed
// password, or the server runs no commands — the answer lists the
// possibilities instead of guessing.
func (t *Tunnel) Explain(ctx context.Context) (reason, hint string) {
	o := t.opts
	sock, host := o.RemoteSocket, o.Host
	who := "the remote user"
	if o.User != "" {
		who = "the remote user " + o.User
	}

	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	// When the path is not there, the nearest directory above it that does
	// exist decides what can be said: if the remote user may not look into
	// it, the socket may well be there — it is hidden, not missing.
	script := "p=" + shQuote(sock) + `; if [ -e "$p" ]; then ` +
		`if [ ! -S "$p" ]; then echo dd-probe:notsocket; elif [ ! -r "$p" ] || [ ! -w "$p" ]; then echo dd-probe:denied; else echo dd-probe:ok; fi; ` +
		`else d=$p; while :; do d=$(dirname "$d"); if [ -e "$d" ] || [ "$d" = / ]; then break; fi; done; ` +
		`if [ -x "$d" ]; then echo dd-probe:missing; else echo "dd-probe:hidden $d"; fi; fi`
	args := append([]string{}, o.Command[1:]...)
	args = append(args, "-T", "-o", "BatchMode=yes")
	args = append(args, o.destArgs()...)
	args = append(args, "sh -c "+shQuote(script))
	probe := exec.CommandContext(ctx, o.Command[0], args...)
	probe.WaitDelay = 2 * time.Second
	quiet(probe)
	var out []byte
	if wait, err := startProbe(probe); err == nil {
		out = wait()
	}

	if _, dir, hidden := strings.Cut(string(out), "dd-probe:hidden "); hidden {
		dir = strings.TrimSpace(strings.SplitN(dir, "\n", 2)[0])
		return fmt.Sprintf("%s may not look inside %s on %s, where the socket would be", who, dir, host),
			"That directory is private to another user — a rootless daemon's socket belongs to the user who runs it. Log in as that user, or name a socket this user may reach."
	}
	switch {
	case strings.Contains(string(out), "dd-probe:missing"):
		return fmt.Sprintf("there is no socket at %s on %s", sock, host),
			"Is Docker running there? A daemon that listens elsewhere (rootless: /run/user/<uid>/docker.sock) is named by putting the path in the URL: ssh://user@host/run/user/1000/docker.sock"
	case strings.Contains(string(out), "dd-probe:notsocket"):
		return fmt.Sprintf("%s on %s exists but is not a socket", sock, host), ""
	case strings.Contains(string(out), "dd-probe:denied"):
		return fmt.Sprintf("%s may not open %s on %s", who, sock, host),
			"The socket usually belongs to the `docker` group: add the remote user to it and log in again."
	case strings.Contains(string(out), "dd-probe:ok"):
		// The socket is there and may be opened, so it is one of the two
		// things that cannot be told apart from here.
		return fmt.Sprintf("%s on %s is there and %s may open it, yet the SSH server could not connect to it", sock, host, who),
			"Either nothing is listening on it (is the Docker daemon running there?), or the server's sshd_config forbids forwarding to sockets: AllowStreamLocalForwarding and AllowTcpForwarding must not be `no`, PermitOpen must not be `none`, and DisableForwarding must not be set."
	}
	return fmt.Sprintf("the SSH connection to %s is up, but the server could not open %s", host, sock),
		"Either the socket is not there (a rootless daemon listens at /run/user/<uid>/docker.sock — put the path in the URL), or the remote user may not open it (the `docker` group), or no daemon is listening on it, or the server's sshd_config forbids forwarding to sockets (AllowStreamLocalForwarding, AllowTcpForwarding, PermitOpen, DisableForwarding)."
}

// startProbe runs the probe without a terminal (see quiet) and returns a
// function that waits for it and gives its output.
func startProbe(cmd *exec.Cmd) (wait func() []byte, err error) {
	var out strings.Builder
	cmd.Stdout = &out
	done, err := startBound(cmd, true)
	if err != nil {
		return nil, err
	}
	return func() []byte {
		_ = done()
		return []byte(out.String())
	}, nil
}

// shQuote quotes s for a POSIX shell.
func shQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// SplitCommand turns the value of DOCKER_DESK_SSH into the ssh program and
// its leading arguments. It understands single and double quotes, so that a
// path with spaces can be given; nothing is expanded.
func SplitCommand(s string) ([]string, error) {
	var args []string
	var cur strings.Builder
	in, quote := false, rune(0)
	for _, r := range s {
		switch {
		case quote != 0:
			if r == quote {
				quote = 0
			} else {
				cur.WriteRune(r)
			}
		case r == '\'' || r == '"':
			quote, in = r, true
		case r == ' ' || r == '\t':
			if in {
				args = append(args, cur.String())
				cur.Reset()
				in = false
			}
		default:
			cur.WriteRune(r)
			in = true
		}
	}
	if quote != 0 {
		return nil, fmt.Errorf("unterminated quote in %q", s)
	}
	if in {
		args = append(args, cur.String())
	}
	if len(args) == 0 {
		return nil, fmt.Errorf("no program named")
	}
	return args, nil
}
