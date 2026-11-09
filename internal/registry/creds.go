package registry

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/fardani235/docker-desk/internal/secret"
)

// Credential is what the operator's Docker configuration holds for one
// registry. Every value is a secret.Secret: it prints as a mask in every
// format and is revealed only for the request it authenticates.
type Credential struct {
	Username string
	Password secret.Secret
	// IdentityToken is a refresh token for the registry's token realm.
	IdentityToken secret.Secret
	// RegistryToken is a bearer token for the registry itself.
	RegistryToken secret.Secret
	// Source names where it came from: "auths", "credsStore desktop",
	// "credHelpers ecr-login". Never a value.
	Source string
}

// Present reports whether there is a credential at all.
func (c Credential) Present() bool {
	return c.Password.Present() || c.IdentityToken.Present() || c.RegistryToken.Present()
}

// configFile is the part of config.json the desk reads.
type configFile struct {
	Auths       map[string]authEntry `json:"auths"`
	CredsStore  string               `json:"credsStore"`
	CredHelpers map[string]string    `json:"credHelpers"`
}

type authEntry struct {
	Auth          string `json:"auth"`
	Username      string `json:"username"`
	Password      string `json:"password"`
	IdentityToken string `json:"identitytoken"`
	RegistryToken string `json:"registrytoken"`
}

// HelperTimeout bounds one credential helper run.
var HelperTimeout = 5 * time.Second

// helperOutputLimit bounds what a helper may write.
const helperOutputLimit = 64 << 10

var helperName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)

// ResolveCredential reads cfgDir/config.json afresh — a `docker login`
// elsewhere takes effect at the next check — and returns the credential for
// key, the Docker CLI's server key (Ref.Key). The order is credHelpers,
// credsStore, auths, then none; in credHelpers and auths the entry is chosen
// by serverEntry, so the one filed under key itself wins over any sibling
// that only shares its host. A missing configuration directory or file is no
// credential, not an error.
func ResolveCredential(ctx context.Context, cfgDir, key string) (Credential, error) {
	if cfgDir == "" {
		return Credential{}, nil
	}
	b, err := os.ReadFile(filepath.Join(cfgDir, "config.json"))
	if errors.Is(err, os.ErrNotExist) {
		return Credential{}, nil
	}
	if err != nil {
		return Credential{}, &Error{Kind: KindHelper, Reason: "the Docker configuration could not be read", Err: err}
	}
	var cf configFile
	if err := json.Unmarshal(b, &cf); err != nil {
		// The decoder's message can quote the file; it is not kept.
		return Credential{}, &Error{Kind: KindHelper, Reason: "the Docker configuration is not valid JSON"}
	}
	if h := lookup(cf.CredHelpers, key); h != "" {
		return runHelper(ctx, h, key, "credHelpers "+h)
	}
	if cf.CredsStore != "" {
		c, err := runHelper(ctx, cf.CredsStore, key, "credsStore "+cf.CredsStore)
		if err != nil || c.Present() {
			return c, err
		}
	}
	if e, ok := serverEntry(cf.Auths, key); ok {
		return fromAuths(e)
	}
	return Credential{}, nil
}

// lookup names the credential helper credHelpers assigns to key, or "".
func lookup(m map[string]string, key string) string {
	v, _ := serverEntry(m, key)
	return v
}

// serverEntry picks key's entry from a configuration map as the Docker CLI
// does: the entry filed under key itself first, and only when there is none
// the legacy fallback, an entry whose key names the same host. Docker Hub's
// "https://index.docker.io/v1/" sits beside "…/v1/access-token" and
// "…/v1/refresh-token", which share its host but are not its credential; the
// exact pass is what keeps them from being chosen. The fallback walks the
// keys in sorted order, so when several legacy keys share a host the choice
// is the same on every call rather than whatever map iteration yields.
func serverEntry[V any](m map[string]V, key string) (V, bool) {
	if v, ok := m[key]; ok {
		return v, true
	}
	for _, k := range slices.Sorted(maps.Keys(m)) {
		if sameServer(k, key) {
			return m[k], true
		}
	}
	var zero V
	return zero, false
}

// sameServer compares configuration keys as the CLI does: by host name,
// ignoring a scheme and a path, so "https://ghcr.io" files ghcr.io.
func sameServer(a, b string) bool { return hostOf(a) == hostOf(b) }

func hostOf(k string) string {
	k = strings.TrimPrefix(strings.TrimPrefix(k, "https://"), "http://")
	if i := strings.IndexByte(k, '/'); i >= 0 {
		k = k[:i]
	}
	return strings.ToLower(k)
}

func fromAuths(e authEntry) (Credential, error) {
	c := Credential{Username: e.Username, Source: "auths"}
	if e.Password != "" {
		c.Password = secret.New(e.Password)
	}
	if e.Auth != "" {
		raw, err := base64.StdEncoding.DecodeString(e.Auth)
		if err != nil {
			return Credential{}, &Error{Kind: KindHelper, Reason: "the auths entry is not valid base64"}
		}
		user, pass, ok := strings.Cut(string(raw), ":")
		if !ok {
			return Credential{}, &Error{Kind: KindHelper, Reason: "the auths entry has no colon between user and password"}
		}
		c.Username, c.Password = user, secret.New(pass)
	}
	if e.IdentityToken != "" {
		c.IdentityToken = secret.New(e.IdentityToken)
	}
	if e.RegistryToken != "" {
		c.RegistryToken = secret.New(e.RegistryToken)
	}
	return c, nil
}

// runHelper runs docker-credential-<name> get with key on its standard
// input. Its output is a secret: it never reaches an error, a job line or a
// log, and a failure is described by how it failed, not by what it said.
func runHelper(ctx context.Context, name, key, source string) (Credential, error) {
	fail := func(reason string, err error) (Credential, error) {
		return Credential{}, &Error{Kind: KindHelper, Reason: "credential helper " + name + " " + reason, Err: err}
	}
	if !helperName.MatchString(name) {
		return fail("has a name that is not a plain program name", nil)
	}
	path, err := exec.LookPath("docker-credential-" + name)
	if err != nil {
		return fail("is not on PATH", nil)
	}
	ctx, cancel := context.WithTimeout(ctx, HelperTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, path, "get")
	cmd.Stdin = strings.NewReader(key)
	var out bytes.Buffer
	cmd.Stdout = &limitedWriter{w: &out, n: helperOutputLimit}
	cmd.Stderr = io.Discard
	// CommandContext kills only the direct child; a grandchild holding the
	// output pipe open would otherwise keep Wait blocked.
	cmd.WaitDelay = time.Second
	err = cmd.Run()
	if ctx.Err() == context.DeadlineExceeded {
		return fail(fmt.Sprintf("did not answer within %s", HelperTimeout), nil)
	}
	if errors.Is(ctx.Err(), context.Canceled) {
		return Credential{}, context.Canceled
	}
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		// "credentials not found" is how every helper says it holds
		// nothing for this server: that is no credential, not a failure.
		if strings.Contains(strings.ToLower(out.String()), "credentials not found") {
			return Credential{}, nil
		}
		return fail(fmt.Sprintf("exited with status %d", exit.ExitCode()), nil)
	}
	if err != nil {
		return fail("could not be run", nil)
	}
	var ans struct {
		Username string `json:"Username"`
		Secret   string `json:"Secret"`
	}
	if err := json.Unmarshal(out.Bytes(), &ans); err != nil {
		return fail("gave an answer that is not JSON", nil)
	}
	c := Credential{Username: ans.Username, Source: source}
	if ans.Secret == "" {
		return Credential{}, nil
	}
	// The CLI's convention: the user "<token>" means the secret is an
	// identity token.
	if ans.Username == "<token>" {
		c.Username, c.IdentityToken = "", secret.New(ans.Secret)
	} else {
		c.Password = secret.New(ans.Secret)
	}
	return c, nil
}

type limitedWriter struct {
	w io.Writer
	n int
}

func (l *limitedWriter) Write(p []byte) (int, error) {
	k := len(p)
	if len(p) > l.n {
		p = p[:l.n]
	}
	l.n -= len(p)
	if len(p) > 0 {
		_, _ = l.w.Write(p)
	}
	return k, nil
}

// CredentialSource names where ResolveCredential would look for key's
// credential, without running a helper: "credHelpers ecr-login",
// "credsStore desktop", "auths", or "" for an anonymous request. It is what
// the Upstream tab says before it asks.
func CredentialSource(cfgDir, key string) string {
	if cfgDir == "" {
		return ""
	}
	b, err := os.ReadFile(filepath.Join(cfgDir, "config.json"))
	if err != nil {
		return ""
	}
	var cf configFile
	if json.Unmarshal(b, &cf) != nil {
		return ""
	}
	if h := lookup(cf.CredHelpers, key); h != "" {
		return "credHelpers " + h
	}
	if cf.CredsStore != "" {
		return "credsStore " + cf.CredsStore
	}
	if e, ok := serverEntry(cf.Auths, key); ok && (e.Auth != "" || e.Password != "" || e.IdentityToken != "" || e.RegistryToken != "") {
		return "auths"
	}
	return ""
}
