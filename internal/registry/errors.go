package registry

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"net"
	"time"
)

// Kind classifies why a registry gave no answer, or which answer it gave.
// The fallback is decided from the Kind alone (Fallback), never from text.
type Kind int

const (
	// KindNotFound: the registry has no manifest for the tag — gone.
	KindNotFound Kind = iota + 1
	// KindUnauthorized: a 401 that stayed a 401 after the challenge was
	// answered. Anonymous says whether no credential was involved.
	KindUnauthorized
	// KindDenied: a 403.
	KindDenied
	// KindTokenRefused: the token realm refused the credential.
	KindTokenRefused
	// KindQuota: a 429. Reset is when the quota allows again, if said.
	KindQuota
	// KindName: the host name did not resolve.
	KindName
	// KindTLS: the certificate could not be verified, or the handshake failed.
	KindTLS
	// KindTimeout: no answer within the bound.
	KindTimeout
	// KindConnection: the connection was refused or broken.
	KindConnection
	// KindUnreadable: an answer arrived but could not be used — oversized,
	// malformed, a digest that does not match, a server error.
	KindUnreadable
	// KindHelper: the credential helper failed on this machine.
	KindHelper
	// KindRefused: refused locally — --no-registry, an insecure scheme, a
	// downgrading redirect.
	KindRefused
)

// Error is a registry failure with its class. Its message never contains a
// credential or a credential helper's output.
type Error struct {
	Kind Kind
	Host string
	// Reason is the desk's own description of what happened.
	Reason string
	// Reset is when a quota allows again; zero when the registry did not say.
	Reset time.Time
	// Anonymous is set on KindUnauthorized when no credential was sent.
	Anonymous bool
	Err       error
}

func (e *Error) Error() string {
	if e.Host == "" {
		return e.Reason
	}
	return e.Host + ": " + e.Reason
}

func (e *Error) Unwrap() error { return e.Err }

// KindOf returns the class of err, or 0 for an error that is not one.
func KindOf(err error) Kind {
	var e *Error
	if errors.As(err, &e) {
		return e.Kind
	}
	return 0
}

// Fallback reports whether the daemon should be asked instead: the desk got
// no answer (name, connection, TLS, timeout, quota, unreadable, a helper
// that failed here). A definitive answer — not found, 401, 403, a refused
// token — is never retried through the daemon, which would send the same
// credential. Neither is a local refusal: --no-registry forbids the egress
// the daemon would make on the desk's behalf.
func Fallback(err error) bool {
	switch KindOf(err) {
	case KindName, KindTLS, KindTimeout, KindConnection, KindQuota, KindUnreadable, KindHelper:
		return true
	}
	return false
}

// classifyTransport turns an error from the HTTP client into an *Error.
func classifyTransport(host string, err error) error {
	var re *Error
	if errors.As(err, &re) {
		return err
	}
	if errors.Is(err, context.Canceled) {
		return err
	}
	var (
		dnsErr  *net.DNSError
		unk     x509.UnknownAuthorityError
		hostErr x509.HostnameError
		inv     x509.CertificateInvalidError
		verr    *tls.CertificateVerificationError
		recErr  tls.RecordHeaderError
		netErr  net.Error
	)
	switch {
	case errors.Is(err, context.DeadlineExceeded):
		return &Error{Kind: KindTimeout, Host: host, Reason: "no answer in time", Err: err}
	case errors.As(err, &dnsErr):
		return &Error{Kind: KindName, Host: host, Reason: "the name does not resolve from this machine", Err: err}
	case errors.As(err, &unk), errors.As(err, &hostErr), errors.As(err, &inv), errors.As(err, &verr):
		return &Error{Kind: KindTLS, Host: host, Reason: "the certificate could not be verified with the system roots", Err: err}
	case errors.As(err, &recErr):
		return &Error{Kind: KindTLS, Host: host, Reason: "the TLS handshake failed", Err: err}
	case errors.As(err, &netErr) && netErr.Timeout():
		return &Error{Kind: KindTimeout, Host: host, Reason: "no answer in time", Err: err}
	}
	return &Error{Kind: KindConnection, Host: host, Reason: fmt.Sprintf("could not connect (%s)", short(err)), Err: err}
}

// short is the innermost message of err, for a reason that does not repeat
// the URL the client library quotes.
func short(err error) string {
	for {
		u := errors.Unwrap(err)
		if u == nil {
			return err.Error()
		}
		err = u
	}
}
