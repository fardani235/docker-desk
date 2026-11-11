package session

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/fardani235/docker-desk/internal/dockerctx"
)

// Canonical is the identity of the daemon an endpoint reaches: two endpoints
// with the same canonical form are one daemon, and one member of a session.
//
// It is built from the scheme, the host, the port, the socket path and the
// TLS identity — not from the context name, nor from how the URL was
// spelled:
//
//   - unix: the socket path, cleaned, with symbolic links resolved where
//     they can be (/var/run is often a link to /run);
//   - ssh: the user, the host (case-insensitively), the port (22 is the same
//     as none) and the remote socket path, cleaned;
//   - tcp: the host (case-insensitively), the port and the base path, and the
//     TLS identity: none, or the contents of the CA and client certificate
//     (two contexts each keep their own copy of the same files) and whether
//     the server is verified. The client's private key is never read.
//
// A host name and the address it resolves to are not taken to be the same:
// nothing is looked up.
func Canonical(ep dockerctx.Endpoint) (string, error) {
	t, err := ep.Target()
	if err != nil {
		return "", err
	}
	switch t.Transport {
	case dockerctx.TransportUnix:
		p := filepath.Clean(t.Socket)
		if real, err := filepath.EvalSymlinks(p); err == nil {
			p = real
		}
		return "unix:" + p, nil
	case dockerctx.TransportSSH:
		port := t.Port
		if port == "22" {
			port = ""
		}
		return "ssh:" + t.User + "@" + strings.ToLower(t.Hostname) + ":" + port + path.Clean(t.Socket), nil
	}
	// TCP: DialHost is tcp://host:port[/base], with the default port filled in.
	host := strings.TrimPrefix(t.DialHost, "tcp://")
	addr, base, _ := strings.Cut(host, "/")
	return "tcp:" + strings.ToLower(addr) + "/" + base + "|" + tlsIdentity(ep.TLS), nil
}

func tlsIdentity(t *dockerctx.TLS) string {
	if t == nil {
		return "plain"
	}
	id := "tls"
	if t.SkipVerify {
		id += ",unverified"
	}
	id += ",ca=" + fileIdentity(t.CAFile)
	id += ",cert=" + fileIdentity(t.CertFile)
	return id
}

// fileIdentity is a file's contents, hashed; its path when it cannot be read.
func fileIdentity(name string) string {
	if name == "" {
		return ""
	}
	data, err := os.ReadFile(name)
	if err != nil {
		return "path:" + name
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:8])
}
