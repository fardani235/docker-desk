// Package dockerctx works out which Docker daemon the operator's current
// context points at, using the same precedence as the Docker CLI, and checks
// that the daemon is a local one that actually answers.
//
// It deliberately does not import docker/cli: the rules are small and stable,
// and the desk needs to report where the endpoint came from, which the CLI's
// store does not expose in one place. See docs/DECISIONS.md, D4 and D5.
package dockerctx

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

const (
	// DefaultContext is the name the Docker CLI gives the implicit context.
	DefaultContext = "default"
	// DefaultHost is the endpoint of the implicit context on Linux and macOS.
	DefaultHost = "unix:///var/run/docker.sock"
)

// Endpoint is the daemon the desk will talk to and how it was chosen.
type Endpoint struct {
	// Host is the endpoint URL, e.g. unix:///var/run/docker.sock.
	Host string
	// Context is the Docker context name ("default" when none is configured).
	Context string
	// Source says, in words, which setting selected this endpoint.
	Source string
}

// Env is the part of the process environment that resolution depends on.
// It is a function so tests never touch the real environment.
type Env func(key string) string

// Resolve returns the endpoint of the current Docker context.
//
// Precedence, identical to the Docker CLI's:
//
//  1. DOCKER_HOST                      → that host, context "default"
//  2. DOCKER_CONTEXT                   → the named context
//  3. currentContext in config.json    → the named context
//  4. otherwise                        → the default socket
//
// The config directory is $DOCKER_CONFIG, or ~/.docker.
func Resolve(env Env, homeDir string) (Endpoint, error) {
	if h := strings.TrimSpace(env("DOCKER_HOST")); h != "" {
		return Endpoint{Host: h, Context: DefaultContext, Source: "DOCKER_HOST environment variable"}, nil
	}

	cfgDir := strings.TrimSpace(env("DOCKER_CONFIG"))
	if cfgDir == "" {
		cfgDir = filepath.Join(homeDir, ".docker")
	}

	name, source := "", ""
	if c := strings.TrimSpace(env("DOCKER_CONTEXT")); c != "" {
		name, source = c, "DOCKER_CONTEXT environment variable"
	} else {
		cur, err := currentContextFromConfig(cfgDir)
		if err != nil {
			return Endpoint{}, err
		}
		if cur != "" {
			name, source = cur, "currentContext in "+filepath.Join(cfgDir, "config.json")
		}
	}

	if name == "" || name == DefaultContext {
		if source == "" {
			source = "Docker default (no context configured)"
		}
		return Endpoint{Host: DefaultHost, Context: DefaultContext, Source: source}, nil
	}

	host, err := contextHost(cfgDir, name)
	if err != nil {
		return Endpoint{}, fmt.Errorf("docker context %q (selected by %s): %w", name, source, err)
	}
	return Endpoint{Host: host, Context: name, Source: source}, nil
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

// contextHost reads the docker endpoint of a named context from the CLI's
// context store: contexts/meta/<sha256(name)>/meta.json.
func contextHost(cfgDir, name string) (string, error) {
	sum := sha256.Sum256([]byte(name))
	path := filepath.Join(cfgDir, "contexts", "meta", hex.EncodeToString(sum[:]), "meta.json")
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return "", fmt.Errorf("context not found (no metadata at %s)", path)
	}
	if err != nil {
		return "", fmt.Errorf("reading context metadata: %w", err)
	}
	var meta struct {
		Name      string `json:"Name"`
		Endpoints map[string]struct {
			Host string `json:"Host"`
		} `json:"Endpoints"`
	}
	if err := json.Unmarshal(data, &meta); err != nil {
		return "", fmt.Errorf("parsing context metadata %s: %w", path, err)
	}
	ep, ok := meta.Endpoints["docker"]
	if !ok || strings.TrimSpace(ep.Host) == "" {
		// The CLI treats an endpoint without a host as the default socket.
		return DefaultHost, nil
	}
	return strings.TrimSpace(ep.Host), nil
}

// ErrRemote is returned for endpoints that are not a local unix socket.
var ErrRemote = errors.New("only local unix-socket daemons are supported")

// SocketPath returns the filesystem path of a unix:// endpoint, or ErrRemote
// for anything else (tcp://, ssh://, npipe://, fd://, …).
func SocketPath(host string) (string, error) {
	const scheme = "unix://"
	if !strings.HasPrefix(host, scheme) {
		return "", ErrRemote
	}
	path := strings.TrimPrefix(host, scheme)
	if path == "" {
		return "", fmt.Errorf("endpoint %q has no socket path", host)
	}
	return path, nil
}

// Unreachable describes why a local daemon could not be contacted.
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

// CheckSocket verifies that something accepts connections on the endpoint's
// socket. It does not speak HTTP; the engine ping does that next.
func CheckSocket(ctx context.Context, ep Endpoint, timeout time.Duration) error {
	path, err := SocketPath(ep.Host)
	if err != nil {
		return err
	}
	if _, err := os.Stat(path); err != nil {
		return Classify(ep, err)
	}
	d := net.Dialer{Timeout: timeout}
	conn, err := d.DialContext(ctx, "unix", path)
	if err != nil {
		return Classify(ep, err)
	}
	_ = conn.Close()
	return nil
}

// Classify turns a low-level connection error into a plain explanation.
func Classify(ep Endpoint, err error) *Unreachable {
	u := &Unreachable{Endpoint: ep, Err: err}
	path, _ := SocketPath(ep.Host)
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
