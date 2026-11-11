package session

import (
	"context"
	"errors"
	"fmt"
	"net"
	"time"

	"github.com/fardani235/docker-desk/internal/dockerctx"
	"github.com/fardani235/docker-desk/internal/engine"
	"github.com/fardani235/docker-desk/internal/sshtunnel"
)

// Default time bounds for a daemon's first answer: a local socket, and one a
// network — and perhaps a TLS handshake — away. The launch path uses the same
// two (cmd/docker-desk).
const (
	DefaultLocalTimeout  = 4 * time.Second
	DefaultRemoteTimeout = 12 * time.Second
)

// DialOptions is how Connect reaches a daemon that was picked while the desk
// is open. There is deliberately no terminal in it: nothing Connect starts
// may ask the operator anything, or write to the screen the desk owns.
type DialOptions struct {
	// ReadOnly is the session's one read-only flag; every engine Connect
	// builds refuses mutations when it is set.
	ReadOnly bool
	// UserAgent identifies the desk in the daemon's logs.
	UserAgent string
	// LocalTimeout and RemoteTimeout bound the first answer (defaults above).
	LocalTimeout, RemoteTimeout time.Duration
	// SSHCommand is the ssh program and its leading arguments
	// (DOCKER_DESK_SSH); empty means ssh.
	SSHCommand []string
	// SSHTimeout bounds logging in over ssh (default: the tunnel's own, 30 s
	// for a connection that may not ask).
	SSHTimeout time.Duration
}

func (o *DialOptions) defaults() {
	if o.LocalTimeout <= 0 {
		o.LocalTimeout = DefaultLocalTimeout
	}
	if o.RemoteTimeout <= 0 {
		o.RemoteTimeout = DefaultRemoteTimeout
	}
	if o.UserAgent == "" {
		o.UserAgent = "docker-desk"
	}
}

// Conn is a daemon that answered: its engine, what it said about itself, and
// the tunnel it is reached through, if any. Close releases all of them.
type Conn struct {
	Engine engine.Engine
	Info   engine.Info
	Tunnel *sshtunnel.Tunnel
}

// Close closes the engine and ends the tunnel's ssh process.
func (c *Conn) Close() {
	if c == nil {
		return
	}
	if c.Engine != nil {
		_ = c.Engine.Close()
	}
	if c.Tunnel != nil {
		c.Tunnel.Close()
	}
}

// Problem is what the tunnel says about itself, or "" (no tunnel, or up).
func (c *Conn) Problem() string {
	if c == nil || c.Tunnel == nil {
		return ""
	}
	return c.Tunnel.Problem()
}

// Connect reaches the daemon at ep the way the launch does — the same
// checks, the same engine construction, the same ping and time bound — but
// never exits and never asks: an ssh login that would need a passphrase, a
// password or a host-key confirmation fails, in ssh's words. A failure is a
// *dockerctx.Unreachable whose Reason says why.
func Connect(ctx context.Context, ep dockerctx.Endpoint, o DialOptions) (*Conn, error) {
	o.defaults()
	target, err := ep.Target()
	if err != nil {
		return nil, &dockerctx.Unreachable{Endpoint: ep, Reason: err.Error(), Err: err}
	}
	dialHost := target.DialHost
	timeout := o.RemoteTimeout
	conn := &Conn{}
	switch target.Transport {
	case dockerctx.TransportUnix:
		timeout = o.LocalTimeout
		if err := dockerctx.CheckSocket(ctx, ep, o.LocalTimeout); err != nil {
			return nil, err
		}
	case dockerctx.TransportTCP:
		if err := dockerctx.CheckTCP(ctx, ep, o.RemoteTimeout); err != nil {
			return nil, err
		}
	case dockerctx.TransportSSH:
		opts := sshtunnel.Options{
			User: target.User, Host: target.Hostname, Port: target.Port,
			RemoteSocket: target.Socket,
			Command:      o.SSHCommand,
			// Batch mode from the first connection on: the desk owns the
			// terminal, and ssh must fail rather than ask (spec: a daemon
			// added from the picker never asks on the terminal). No Stderr:
			// what ssh says is kept for the reason, never written out.
			Interactive:  false,
			FirstTimeout: o.SSHTimeout,
		}
		tunnel, err := sshtunnel.Open(ctx, opts)
		if err != nil {
			return nil, ExplainTunnel(ep, err)
		}
		tunnel.Detach()
		conn.Tunnel = tunnel
		dialHost = tunnel.Host()
	}

	var tlsOpts *engine.TLS
	if t := ep.TLS; t != nil {
		tlsOpts = &engine.TLS{CAFile: t.CAFile, CertFile: t.CertFile, KeyFile: t.KeyFile, SkipVerify: t.SkipVerify}
	}
	eng, err := engine.New(engine.Options{Host: dialHost, ShownAs: ep.Host, TLS: tlsOpts, ReadOnly: o.ReadOnly, UserAgent: o.UserAgent})
	if err != nil {
		conn.Close()
		// Nothing was tried yet: the client's own set-up is at fault.
		return nil, &dockerctx.Unreachable{Endpoint: ep, Reason: "cannot set up the connection: " + err.Error(), Err: err}
	}
	// With ReadOnly, engine.New has refused mutations twice over (D17): the
	// wrapper before any request is built, the request hook below it.
	conn.Engine = eng
	pctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	if err := eng.Ping(pctx); err != nil {
		u := ExplainPing(ctx, ep, target, conn.Tunnel, err, timeout)
		conn.Close()
		return nil, u
	}
	info, err := eng.Info(pctx)
	if err != nil {
		conn.Close()
		u := dockerctx.Classify(ep, err)
		u.Reason = "the daemon answered the ping but not the info request: " + err.Error()
		return nil, u
	}
	conn.Info = info
	return conn, nil
}

// ExplainTunnel puts the tunnel's explanation — ssh's own words — into the
// shape every other connection failure has.
func ExplainTunnel(ep dockerctx.Endpoint, err error) error {
	var te *sshtunnel.Error
	if errors.As(err, &te) {
		return &dockerctx.Unreachable{Endpoint: ep, Reason: te.Reason, Hint: te.Hint, Err: err}
	}
	return &dockerctx.Unreachable{Endpoint: ep, Reason: err.Error(), Err: err}
}

// ExplainPing explains a daemon that did not answer its first request within
// timeout.
func ExplainPing(ctx context.Context, ep dockerctx.Endpoint, target dockerctx.Target, tunnel *sshtunnel.Tunnel, err error, timeout time.Duration) *dockerctx.Unreachable {
	switch target.Transport {
	case dockerctx.TransportSSH:
		u := &dockerctx.Unreachable{Endpoint: ep, Err: err}
		var netErr net.Error
		switch {
		case tunnel != nil && !tunnel.Up():
			u.Reason = "the SSH connection dropped: " + tunnel.Problem()
		case errors.Is(err, context.DeadlineExceeded) || (errors.As(err, &netErr) && netErr.Timeout()):
			// Silence is not a refusal: the far end may well have been
			// opened, by a daemon that is not answering.
			u.Reason = fmt.Sprintf("the SSH connection to %s is up, but nothing answered on %s within %s", target.Hostname, target.Socket, timeout)
			u.Hint = "The daemon there may be hung or very busy, or the link very slow. `docker ps` on that machine will tell."
		case engine.IsUnavailable(err) && tunnel != nil:
			// ssh is connected, and the connection through it was closed
			// at once: the far end of the forwarding could not be opened.
			u.Reason, u.Hint = tunnel.Explain(ctx)
		default:
			u.Reason = fmt.Sprintf("%s on %s answered, but not as a Docker Engine API: %v", target.Socket, target.Hostname, err)
		}
		return u
	case dockerctx.TransportTCP:
		return dockerctx.Classify(ep, err)
	}
	u := dockerctx.Classify(ep, err)
	u.Reason = "the socket accepted the connection but the Engine API did not answer: " + err.Error()
	return u
}
