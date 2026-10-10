package dockerctx

import (
	"errors"
	"fmt"
	"net"
	"net/url"
	"strings"

	"github.com/fardani235/docker-desk/internal/sshtunnel"
)

// Transport is how an endpoint is reached.
type Transport int

const (
	// TransportUnix is a unix socket on this machine.
	TransportUnix Transport = iota
	// TransportSSH is a unix socket on another machine, reached through an
	// SSH connection.
	TransportSSH
	// TransportTCP is the Engine API over TCP, with or without TLS.
	TransportTCP
)

func (t Transport) String() string {
	switch t {
	case TransportUnix:
		return "unix socket"
	case TransportSSH:
		return "ssh"
	case TransportTCP:
		return "tcp"
	}
	return "unknown"
}

const (
	// DefaultRemoteSocket is where the daemon listens on the far side of an
	// ssh:// endpoint that names no socket path.
	DefaultRemoteSocket = "/var/run/docker.sock"
	// defaultPort is what the Docker CLI assumes for a tcp:// endpoint that
	// names no port — with or without TLS. (By convention a TLS daemon
	// listens on 2376, but the CLI does not guess that, and the desk must
	// dial where the CLI dials.)
	defaultPort = "2375"
)

// Target is an endpoint URL taken apart.
type Target struct {
	Transport Transport

	// Socket is the unix socket path: on this machine for TransportUnix, on
	// the remote machine for TransportSSH.
	Socket string

	// User, Hostname and Port are the SSH destination (User and Port may be
	// empty: ssh then decides), or the TCP host and port.
	User     string
	Hostname string
	Port     string

	// DialHost is the endpoint in the form the Engine client takes
	// (unix://… or tcp://host:port[/base]). It is empty for TransportSSH,
	// whose local end exists only once the tunnel is up.
	DialHost string
}

// ErrUnsupported is returned for endpoint schemes the desk cannot use.
var ErrUnsupported = errors.New("unsupported endpoint scheme")

// Target parses the endpoint's URL. Anything it accepts is safe to hand to
// the transport: in particular an SSH destination can never be read by ssh
// as an option.
func (e Endpoint) Target() (Target, error) {
	scheme, rest, ok := strings.Cut(e.Host, "://")
	if !ok {
		return Target{}, fmt.Errorf("endpoint %q is not a URL (expected unix://, ssh:// or tcp://)", e.Host)
	}
	switch scheme {
	case "unix":
		if rest == "" {
			return Target{}, fmt.Errorf("endpoint %q has no socket path", e.Host)
		}
		return Target{Transport: TransportUnix, Socket: rest, DialHost: e.Host}, nil

	case "ssh":
		return parseSSH(e.Host)

	case "tcp":
		return parseTCP(e.Host)
	}
	return Target{}, fmt.Errorf("%w %q in %s (the desk speaks unix://, ssh:// and tcp://)", ErrUnsupported, scheme, e.Host)
}

// parseSSH accepts what the Docker CLI accepts for an SSH endpoint:
// ssh://[user@]host[:port][/path/to/docker.sock].
func parseSSH(raw string) (Target, error) {
	u, err := url.Parse(raw)
	if err != nil {
		return Target{}, fmt.Errorf("invalid SSH endpoint %q: %w", raw, err)
	}
	t := Target{Transport: TransportSSH, Hostname: u.Hostname(), Port: u.Port(), Socket: u.Path}
	if u.User != nil {
		t.User = u.User.Username()
		if _, has := u.User.Password(); has {
			return Target{}, fmt.Errorf("invalid SSH endpoint: a password in the URL is not supported (use keys, an agent, or type it when ssh asks)")
		}
	}
	if u.RawQuery != "" || u.Fragment != "" {
		return Target{}, fmt.Errorf("invalid SSH endpoint %q: nothing may follow the socket path", raw)
	}
	if t.Socket == "" || t.Socket == "/" {
		t.Socket = DefaultRemoteSocket
	}
	// Every part ends up on ssh's command line; the tunnel says what each
	// may contain, and why (ssh expands variables in a forwarding's path).
	if err := sshtunnel.CheckDestination(t.User, t.Hostname, t.Port, t.Socket); err != nil {
		return Target{}, fmt.Errorf("invalid SSH endpoint %q: %w", safe(raw), err)
	}
	return t, nil
}

// safe makes text that came from outside fit to print: a URL from a context
// file can carry percent-encoded control characters, and an error message
// must not be a way of writing them to the operator's terminal.
func safe(s string) string {
	return strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return '?'
		}
		return r
	}, s)
}

func parseTCP(raw string) (Target, error) {
	u, err := url.Parse(raw)
	if err != nil {
		return Target{}, fmt.Errorf("invalid TCP endpoint %q: %w", raw, err)
	}
	t := Target{Transport: TransportTCP, Hostname: u.Hostname(), Port: u.Port()}
	switch {
	case t.Hostname == "":
		return Target{}, fmt.Errorf("invalid TCP endpoint %q: no host", raw)
	case u.User != nil || u.RawQuery != "" || u.Fragment != "":
		return Target{}, fmt.Errorf("invalid TCP endpoint %q: expected tcp://host:port", raw)
	case safe(u.Host+u.Path) != u.Host+u.Path:
		return Target{}, fmt.Errorf("invalid TCP endpoint %q: control characters", safe(raw))
	}
	if t.Port == "" {
		t.Port = defaultPort
	}
	t.DialHost = "tcp://" + net.JoinHostPort(t.Hostname, t.Port) + strings.TrimSuffix(u.Path, "/")
	return t, nil
}

// Address is the network address of a TCP or SSH target, for messages.
func (t Target) Address() string {
	if t.Port == "" {
		return t.Hostname
	}
	return net.JoinHostPort(t.Hostname, t.Port)
}

// loopback reports whether the target's host is, by its address, this
// machine. A name that merely resolves to this machine is not recognised.
func (t Target) loopback() bool {
	if strings.EqualFold(t.Hostname, "localhost") {
		return true
	}
	ip := net.ParseIP(t.Hostname)
	return ip != nil && ip.IsLoopback()
}

// Link is what the operator should be told about how the daemon is reached.
type Link struct {
	Transport Transport
	// Remote is set when the endpoint's address is another machine.
	Remote bool
	// Label is a short tag for the header and for confirmations, e.g.
	// "REMOTE ssh" or "REMOTE tcp, NO TLS". It is empty for a local socket.
	Label string
	// Insecure is set when traffic can be read or the daemon impersonated:
	// TCP without TLS, or TLS without verifying the server.
	Insecure bool
	// Words is the same thing as a phrase, for --check and launch messages.
	Words string
}

// Link describes how the endpoint is reached. It never fails: an endpoint
// that does not parse is described as a local socket, and is refused long
// before anything is drawn.
func (e Endpoint) Link() Link {
	t, err := e.Target()
	if err != nil || t.Transport == TransportUnix {
		return Link{Transport: TransportUnix, Words: "a unix socket on this machine"}
	}
	l := Link{Transport: t.Transport, Remote: !t.loopback()}
	where := "REMOTE "
	if !l.Remote {
		where = ""
	}
	switch {
	case t.Transport == TransportSSH:
		l.Label = where + "ssh"
		l.Words = "an SSH tunnel to " + t.Socket + " on " + t.Hostname
	case e.TLS == nil:
		l.Label, l.Insecure = where+"tcp, NO TLS", true
		l.Words = "TCP to " + t.Address() + " without TLS: not encrypted, open to whoever reaches the port"
	case e.TLS.SkipVerify:
		l.Label, l.Insecure = where+"tls, UNVERIFIED", true
		l.Words = "TCP to " + t.Address() + " with TLS, but the daemon's certificate is NOT verified"
	default:
		l.Label = where + "tls"
		l.Words = "TCP to " + t.Address() + " with TLS: daemon verified"
		if e.TLS.CertFile != "" {
			l.Words += ", client certificate presented"
		}
	}
	return l
}
