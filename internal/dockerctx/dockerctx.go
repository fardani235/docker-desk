// Package dockerctx works out which Docker daemon the operator means, using
// the same precedence as the Docker CLI, how that daemon is reached (a local
// socket, SSH, or TCP with or without TLS), and — when it cannot be reached —
// why, in plain words.
//
// It deliberately does not import docker/cli: the rules are small and stable,
// and the desk needs to report where the endpoint came from, which the CLI's
// store does not expose in one place. See docs/DECISIONS.md, D4, R1 and R6.
package dockerctx

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

const (
	// DefaultContext is the name the Docker CLI gives the implicit context.
	DefaultContext = "default"
	// DefaultHost is the endpoint of the implicit context on Linux and macOS.
	DefaultHost = "unix:///var/run/docker.sock"
	// DefaultTLSHost is what the Docker CLI uses instead when TLS is asked
	// for and no host is named.
	DefaultTLSHost = "tcp://localhost:2376"
)

// Endpoint is the daemon the desk will talk to and how it was chosen.
type Endpoint struct {
	// Host is the endpoint URL as the operator wrote it, e.g.
	// unix:///var/run/docker.sock, ssh://ops@prod-1 or tcp://10.0.0.5:2376.
	Host string
	// Context is the Docker context name ("default" when none is configured).
	Context string
	// Source says, in words, which setting selected this endpoint.
	Source string
	// TLS is how a tcp:// endpoint is secured; nil means it is not. It is
	// never set for unix:// and ssh:// endpoints.
	TLS *TLS
}

// TLS is the client side of a TLS connection to a tcp:// daemon. Empty file
// names mean "none": no client certificate, or the system's root CAs.
type TLS struct {
	CAFile   string
	CertFile string
	KeyFile  string
	// SkipVerify accepts any server certificate. The connection is
	// encrypted but the daemon's identity is not checked.
	SkipVerify bool
	// Source says, in words, which setting asked for TLS.
	Source string
}

// Selection is what the operator asked for on the command line. Both fields
// are optional; they take precedence over the environment.
type Selection struct {
	// Host is the value of --host.
	Host string
	// Context is the value of --context.
	Context string
}

// Env is the part of the process environment that resolution depends on.
// It is a function so tests never touch the real environment.
type Env func(key string) string

// Resolve returns the endpoint the operator means.
//
// Precedence, identical to the Docker CLI's:
//
//  1. --context                        → the named context
//  2. --host                           → that host, context "default"
//  3. DOCKER_HOST                      → that host, context "default"
//  4. DOCKER_CONTEXT                   → the named context
//  5. currentContext in config.json    → the named context
//  6. otherwise                        → the default context
//
// --host and --context together are refused, as the CLI refuses them. The
// config directory is $DOCKER_CONFIG, or ~/.docker.
//
// The "default" context is not a stored one. As in the CLI it means
// DOCKER_HOST when that is set — so `--context default` does not get past
// DOCKER_HOST — and otherwise the local socket; or, when DOCKER_TLS_VERIFY or
// DOCKER_TLS is set and no host is named anywhere, tcp://localhost:2376.
// That last rule is an odd one, but it is where `docker` would go, and the
// desk must never be looking at a different daemon than `docker` is.
//
// TLS for a tcp:// endpoint comes from where the endpoint came from: for a
// host given directly, from DOCKER_TLS_VERIFY, DOCKER_TLS and
// DOCKER_CERT_PATH; for a named context, from the context store.
func Resolve(sel Selection, env Env, homeDir string) (Endpoint, error) {
	ep, err := resolve(sel, env, homeDir)
	if err != nil {
		return Endpoint{}, err
	}
	// The endpoint and the context name are printed — on the terminal
	// before the desk opens, and on its top line afterwards. A context file
	// is not always the operator's own writing; nothing in it may be a way
	// of sending control sequences to their terminal.
	if safe(ep.Host) != ep.Host || safe(ep.Context) != ep.Context {
		return Endpoint{}, fmt.Errorf("the endpoint %q (context %q, selected by %s) contains control characters", ep.Host, ep.Context, ep.Source)
	}
	return ep, nil
}

func resolve(sel Selection, env Env, homeDir string) (Endpoint, error) {
	sel.Host, sel.Context = strings.TrimSpace(sel.Host), strings.TrimSpace(sel.Context)
	if sel.Host != "" && sel.Context != "" {
		return Endpoint{}, errors.New("--host and --context cannot be used together: one names a daemon directly, the other names a stored context")
	}

	cfgDir := strings.TrimSpace(env("DOCKER_CONFIG"))
	if cfgDir == "" && homeDir != "" {
		cfgDir = filepath.Join(homeDir, ".docker")
	}
	envHost := strings.TrimSpace(env("DOCKER_HOST"))

	name, source := "", ""
	switch {
	case sel.Context != "":
		name, source = sel.Context, "--context flag"
	case sel.Host != "":
		return directEndpoint(sel.Host, "--host flag", env, cfgDir)
	case envHost != "":
		return directEndpoint(envHost, "DOCKER_HOST environment variable", env, cfgDir)
	case strings.TrimSpace(env("DOCKER_CONTEXT")) != "":
		name, source = strings.TrimSpace(env("DOCKER_CONTEXT")), "DOCKER_CONTEXT environment variable"
	case cfgDir != "":
		// Without a home directory and without DOCKER_CONFIG there is no
		// configuration to read — and no relative path is tried instead.
		cur, err := currentContextFromConfig(cfgDir)
		if err != nil {
			return Endpoint{}, err
		}
		if cur != "" {
			name, source = cur, "currentContext in "+filepath.Join(cfgDir, "config.json")
		}
	}

	if name == "" || name == DefaultContext {
		return defaultEndpoint(source, envHost, env, cfgDir)
	}
	if cfgDir == "" {
		return Endpoint{}, fmt.Errorf("docker context %q (selected by %s): there is no Docker configuration directory to look it up in (HOME and DOCKER_CONFIG are both unset)", name, source)
	}

	ep, err := contextEndpoint(cfgDir, name)
	if err != nil {
		return Endpoint{}, fmt.Errorf("docker context %q (selected by %s): %w", name, source, err)
	}
	ep.Context, ep.Source = name, source
	return ep, nil
}

// defaultEndpoint is the "default" context: DOCKER_HOST when set, else the
// CLI's default host — which depends on whether TLS was asked for.
func defaultEndpoint(source, envHost string, env Env, cfgDir string) (Endpoint, error) {
	selected := ""
	if source != "" {
		selected = " (the default context, selected by " + source + ")"
	}
	if envHost != "" {
		return directEndpoint(envHost, "DOCKER_HOST environment variable"+selected, env, cfgDir)
	}
	if env("DOCKER_TLS_VERIFY") != "" || env("DOCKER_TLS") != "" {
		return directEndpoint(DefaultTLSHost, "the Docker CLI's default when DOCKER_TLS_VERIFY or DOCKER_TLS is set and no host is named"+selected, env, cfgDir)
	}
	if source == "" {
		source = "Docker default (no context configured)"
	}
	return Endpoint{Host: DefaultHost, Context: DefaultContext, Source: source}, nil
}

// directEndpoint is an endpoint named by its URL rather than by a context.
// Like the CLI, it belongs to the "default" context and takes its TLS
// settings from the environment.
func directEndpoint(host, source string, env Env, cfgDir string) (Endpoint, error) {
	ep := Endpoint{Host: host, Context: DefaultContext, Source: source}
	if !strings.HasPrefix(host, "tcp://") {
		return ep, nil
	}
	verify := env("DOCKER_TLS_VERIFY") != ""
	if !verify && env("DOCKER_TLS") == "" {
		return ep, nil
	}
	certDir := strings.TrimSpace(env("DOCKER_CERT_PATH"))
	if certDir == "" {
		certDir = cfgDir
	}
	t := &TLS{SkipVerify: !verify, Source: "DOCKER_TLS (the server's certificate is not verified)"}
	if verify {
		// The CLI insists on this file when verifying; so does the desk,
		// rather than silently trusting some other set of roots.
		t.Source = "DOCKER_TLS_VERIFY"
		t.CAFile = filepath.Join(certDir, "ca.pem")
		if _, err := os.Stat(t.CAFile); err != nil {
			return Endpoint{}, fmt.Errorf("DOCKER_TLS_VERIFY is set, but the CA certificate to verify %s with cannot be read: %w (certificates are looked for in DOCKER_CERT_PATH, or %s)", host, err, cfgDir)
		}
	}
	// A client certificate is used when both halves are there.
	cert, key := filepath.Join(certDir, "cert.pem"), filepath.Join(certDir, "key.pem")
	if fileExists(cert) && fileExists(key) {
		t.CertFile, t.KeyFile = cert, key
	}
	ep.TLS = t
	return ep, nil
}

func fileExists(path string) bool {
	st, err := os.Stat(path)
	return err == nil && !st.IsDir()
}

func currentContextFromConfig(cfgDir string) (string, error) {
	path := filepath.Join(cfgDir, "config.json")
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("reading %s: %w", path, err)
	}
	var cfg struct {
		CurrentContext string `json:"currentContext"`
	}
	if err := json.Unmarshal(data, &cfg); err != nil {
		return "", fmt.Errorf("parsing %s: %w", path, err)
	}
	return strings.TrimSpace(cfg.CurrentContext), nil
}

// contextEndpoint reads the docker endpoint of a named context from the CLI's
// context store: contexts/meta/<sha256(name)>/meta.json for the host, and
// contexts/tls/<sha256(name)>/docker/{ca,cert,key}.pem for its TLS material.
func contextEndpoint(cfgDir, name string) (Endpoint, error) {
	sum := sha256.Sum256([]byte(name))
	id := hex.EncodeToString(sum[:])
	path := filepath.Join(cfgDir, "contexts", "meta", id, "meta.json")
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return Endpoint{}, fmt.Errorf("context not found (no metadata at %s)", path)
	}
	if err != nil {
		return Endpoint{}, fmt.Errorf("reading context metadata: %w", err)
	}
	var meta struct {
		Name      string `json:"Name"`
		Endpoints map[string]struct {
			Host          string `json:"Host"`
			SkipTLSVerify bool   `json:"SkipTLSVerify"`
		} `json:"Endpoints"`
	}
	if err := json.Unmarshal(data, &meta); err != nil {
		return Endpoint{}, fmt.Errorf("parsing context metadata %s: %w", path, err)
	}
	dock, ok := meta.Endpoints["docker"]
	host := strings.TrimSpace(dock.Host)
	if !ok || host == "" {
		// The CLI treats an endpoint without a host as the default socket.
		return Endpoint{Host: DefaultHost}, nil
	}
	ep := Endpoint{Host: host}
	if !strings.HasPrefix(host, "tcp://") {
		return ep, nil
	}
	// As in the CLI: a context uses TLS when it stores any TLS material or
	// asks to skip verification, and plain TCP otherwise. "Any" matters: a
	// store holding only half of a client key pair still means TLS — with
	// the system's authorities and no client certificate — never a silent
	// fall-back to an unencrypted connection.
	dir := filepath.Join(cfgDir, "contexts", "tls", id, "docker")
	t := &TLS{SkipVerify: dock.SkipTLSVerify, Source: "the context's stored TLS material"}
	ca, cert, key := filepath.Join(dir, "ca.pem"), filepath.Join(dir, "cert.pem"), filepath.Join(dir, "key.pem")
	if fileExists(ca) {
		t.CAFile = ca
	}
	if fileExists(cert) && fileExists(key) {
		t.CertFile, t.KeyFile = cert, key
	}
	if !fileExists(ca) && !fileExists(cert) && !fileExists(key) && !t.SkipVerify {
		return ep, nil
	}
	if t.SkipVerify {
		t.Source = "the context (SkipTLSVerify: the server's certificate is not verified)"
	}
	ep.TLS = t
	return ep, nil
}
