package dockerctx

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"io/fs"
	"net"
	"net/http"
	"os"
	"strings"
	"syscall"
	"time"
)

// Unreachable describes why a daemon could not be contacted.
type Unreachable struct {
	Endpoint Endpoint
	// Reason is a short, plain statement of what is wrong.
	Reason string
	// Hint is an optional suggestion; empty when there is nothing useful to say.
	Hint string
	Err  error
}

func (u *Unreachable) Error() string {
	return fmt.Sprintf("Docker daemon is not reachable at %s: %s", u.Endpoint.Host, u.Reason)
}

func (u *Unreachable) Unwrap() error { return u.Err }

// CheckSocket verifies that something accepts connections on a local unix
// socket endpoint. It does not speak HTTP; the engine ping does that next.
func CheckSocket(ctx context.Context, ep Endpoint, timeout time.Duration) error {
	t, err := ep.Target()
	if err != nil {
		return err
	}
	if t.Transport != TransportUnix {
		return fmt.Errorf("endpoint %s is not a local unix socket", ep.Host)
	}
	if _, err := os.Stat(t.Socket); err != nil {
		return Classify(ep, err)
	}
	d := net.Dialer{Timeout: timeout}
	conn, err := d.DialContext(ctx, "unix", t.Socket)
	if err != nil {
		return Classify(ep, err)
	}
	_ = conn.Close()
	return nil
}

// CheckTCP verifies that a tcp:// endpoint accepts connections, before the
// client library is asked to: it reports every failure to connect with the
// same sentence, and a refused port, a name that does not resolve and a
// firewall that drops packets deserve different ones.
//
// When the environment routes the daemon's address through a proxy nothing
// is checked here: the client will go through the proxy, and a direct
// connection proves nothing about that path.
func CheckTCP(ctx context.Context, ep Endpoint, timeout time.Duration) error {
	t, err := ep.Target()
	if err != nil {
		return err
	}
	if t.Transport != TransportTCP {
		return fmt.Errorf("endpoint %s is not a TCP endpoint", ep.Host)
	}
	scheme := "http"
	if ep.TLS != nil {
		scheme = "https"
	}
	if req, err := http.NewRequest(http.MethodGet, scheme+"://"+t.Address()+"/_ping", nil); err == nil {
		if proxy, _ := http.ProxyFromEnvironment(req); proxy != nil {
			return nil
		}
	}
	d := net.Dialer{Timeout: timeout}
	conn, err := d.DialContext(ctx, "tcp", t.Address())
	if err != nil {
		return Classify(ep, err)
	}
	_ = conn.Close()
	return nil
}

// Classify turns a low-level connection error into a plain explanation.
//
// It covers local sockets and TCP. A failing SSH endpoint is explained by
// the tunnel, which has ssh's own words for it.
func Classify(ep Endpoint, err error) *Unreachable {
	u := &Unreachable{Endpoint: ep, Err: err}
	t, perr := ep.Target()
	if perr == nil && t.Transport == TransportTCP {
		classifyTCP(u, t, err)
		return u
	}
	path := t.Socket
	var netErr net.Error
	switch {
	case errors.Is(err, fs.ErrNotExist) || errors.Is(err, syscall.ENOENT):
		u.Reason = fmt.Sprintf("the socket %s does not exist", path)
		u.Hint = "Is the Docker daemon installed and running? (e.g. `sudo systemctl start docker`)"
	case errors.Is(err, fs.ErrPermission) || errors.Is(err, syscall.EACCES):
		u.Reason = fmt.Sprintf("permission denied opening %s", path)
		u.Hint = "Your user may need to be in the `docker` group, or the socket belongs to another user."
	case errors.Is(err, syscall.ECONNREFUSED):
		u.Reason = fmt.Sprintf("connection refused on %s (the socket exists but nothing is listening)", path)
		u.Hint = "The daemon appears to be stopped."
	case errors.Is(err, context.DeadlineExceeded) || (errors.As(err, &netErr) && netErr.Timeout()):
		u.Reason = "the daemon did not answer in time"
	default:
		u.Reason = err.Error()
	}
	return u
}

func classifyTCP(u *Unreachable, t Target, err error) {
	addr := t.Address()
	text := err.Error()
	var (
		dnsErr     *net.DNSError
		netErr     net.Error
		unknownCA  x509.UnknownAuthorityError
		hostErr    x509.HostnameError
		invalidErr x509.CertificateInvalidError
		recordErr  tls.RecordHeaderError
	)
	usingTLS := u.Endpoint.TLS != nil
	// Where TLS was (or was not) asked for decides what there is to change:
	// the environment for an endpoint named directly, the context itself
	// for a named one — which ignores the environment.
	named := u.Endpoint.Context != "" && u.Endpoint.Context != DefaultContext
	switch {
	case errors.As(err, &dnsErr):
		u.Reason = fmt.Sprintf("the host name %q could not be resolved", t.Hostname)
	case errors.Is(err, syscall.ECONNREFUSED):
		u.Reason = fmt.Sprintf("connection refused at %s (nothing is listening there, or a firewall rejects it)", addr)
		u.Hint = "Is the daemon configured to listen on TCP, and is the port right? (2375 without TLS, 2376 with, by convention)"
	case errors.Is(err, syscall.EHOSTUNREACH) || errors.Is(err, syscall.ENETUNREACH):
		u.Reason = fmt.Sprintf("there is no route to %s", addr)
	case errors.As(err, &unknownCA):
		u.Reason = "the daemon's certificate is not signed by a certificate authority this client trusts"
		if ca := u.Endpoint.TLS; usingTLS && ca.CAFile != "" {
			u.Reason = fmt.Sprintf("the daemon's certificate is not signed by the CA in %s", ca.CAFile)
		}
		u.Hint = "Either this is not the daemon you expect, or the CA certificate is the wrong one."
	case errors.As(err, &hostErr):
		u.Reason = fmt.Sprintf("the daemon's certificate is not valid for %q", t.Hostname)
		u.Hint = "Use the name or address the certificate was issued for."
	case errors.As(err, &invalidErr):
		u.Reason = "the daemon's certificate was rejected: " + invalidErr.Error()
	case errors.As(err, &recordErr) || strings.Contains(text, "server gave HTTP response to HTTPS client"):
		u.Reason = fmt.Sprintf("TLS was requested, but %s does not speak TLS", addr)
		u.Hint = "The daemon is listening without TLS there (by convention TLS is on port 2376, and a URL without a port means 2375). Unset DOCKER_TLS_VERIFY / DOCKER_TLS to connect in the clear, or name its TLS port."
		if named {
			u.Hint = fmt.Sprintf("The context %q asks for TLS (it stores certificates, or sets SkipTLSVerify), and the daemon is listening without TLS there. Point the context at the daemon's TLS port, or recreate it without certificates to connect in the clear.", u.Endpoint.Context)
		}
	case strings.Contains(text, "Client sent an HTTP request to an HTTPS server"):
		u.Reason = fmt.Sprintf("%s expects TLS, and none is configured", addr)
		u.Hint = "Set DOCKER_TLS_VERIFY=1 and DOCKER_CERT_PATH to the directory holding ca.pem, cert.pem and key.pem — or use a Docker context that stores them."
		if named {
			u.Hint = fmt.Sprintf("The context %q stores no certificates. Recreate it with them: docker context create NAME --docker \"host=tcp://…,ca=ca.pem,cert=cert.pem,key=key.pem\".", u.Endpoint.Context)
		}
	case strings.Contains(text, "tls: certificate required") || strings.Contains(text, "tls: bad certificate") ||
		strings.Contains(text, "tls: unknown certificate authority") || strings.Contains(text, "tls: unknown certificate"):
		u.Reason = "the daemon refused this client's certificate"
		if usingTLS && u.Endpoint.TLS.CertFile == "" {
			u.Reason = "the daemon requires a client certificate, and none is configured"
		}
		u.Hint = "The daemon accepts only clients holding a certificate signed by its CA (cert.pem and key.pem next to ca.pem)."
	case errors.Is(err, context.DeadlineExceeded) || (errors.As(err, &netErr) && netErr.Timeout()):
		u.Reason = fmt.Sprintf("no answer from %s in time", addr)
		u.Hint = "Is the address right, and is the port reachable from here (firewall, VPN)?"
	case strings.Contains(text, "Cannot connect to the Docker daemon"):
		// The client library's one sentence for every failure to connect
		// (only seen when the connection goes through a proxy; otherwise
		// CheckTCP has already said which failure it is).
		u.Reason = fmt.Sprintf("nothing answered at %s", addr)
		u.Hint = "Is the address right, and is the daemon listening there?"
	default:
		u.Reason = text
	}
}
