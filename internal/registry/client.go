package registry

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	ocispec "github.com/opencontainers/image-spec/specs-go/v1"

	"github.com/fardani235/docker-desk/internal/secret"
	"github.com/fardani235/docker-desk/internal/testguard"
)

// The four manifest media types the desk understands, sent on every
// manifest request so no second negotiation is needed.
const (
	mediaOCIIndex     = ocispec.MediaTypeImageIndex
	mediaOCIManifest  = ocispec.MediaTypeImageManifest
	mediaDockerList   = "application/vnd.docker.distribution.manifest.list.v2+json"
	mediaDockerSchema = "application/vnd.docker.distribution.manifest.v2+json"
)

var manifestAccept = strings.Join([]string{mediaOCIIndex, mediaDockerList, mediaOCIManifest, mediaDockerSchema}, ", ")

// Body limits. What a limit cuts off is an unreadable answer, not a guess.
const (
	ManifestLimit = 4 << 20
	ConfigLimit   = 32 << 20
	tokenLimit    = 1 << 20
)

// DefaultTimeout bounds one check from the desk's side (U8).
const DefaultTimeout = 10 * time.Second

// Options configure a Client.
type Options struct {
	// Disabled refuses every request: --no-registry.
	Disabled bool
	// Timeout bounds one Resolve; zero means DefaultTimeout.
	Timeout time.Duration
}

// Client asks registries. It is safe for concurrent use; its token and
// config caches live in memory only.
type Client struct {
	http     *http.Client
	disabled bool
	timeout  time.Duration
	requests atomic.Int64

	mu         sync.Mutex
	tokens     map[tokenKey]secret.Secret
	challenges map[string]challenge // by registry host
	configs    map[string]Config    // by digest
}

type tokenKey struct{ host, realm, service, scope, cred string }

type challenge struct {
	scheme, realm, service string
}

// New returns a client that verifies TLS with the system roots and speaks
// plain HTTP to loopback hosts only.
func New(opts Options) *Client {
	dialer := &net.Dialer{Timeout: 5 * time.Second}
	tr := &http.Transport{
		Proxy: http.ProxyFromEnvironment,
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			// RFC 6761: a name under .invalid never resolves, and is not
			// looked up. The sandbox's registry lives there.
			if h, _, _ := net.SplitHostPort(addr); strings.HasSuffix(strings.ToLower(strings.TrimSuffix(h, ".")), ".invalid") {
				return nil, &net.DNSError{Err: "no such host", Name: h, IsNotFound: true}
			}
			if err := testguard.CheckRegistry(addr); err != nil {
				return nil, &Error{Kind: KindRefused, Host: addr, Reason: err.Error()}
			}
			return dialer.DialContext(ctx, network, addr)
		},
		TLSClientConfig:       &tls.Config{MinVersion: tls.VersionTLS12},
		TLSHandshakeTimeout:   5 * time.Second,
		ResponseHeaderTimeout: 10 * time.Second,
		MaxIdleConnsPerHost:   2,
	}
	c := &Client{
		disabled:   opts.Disabled,
		timeout:    opts.Timeout,
		tokens:     map[tokenKey]secret.Secret{},
		challenges: map[string]challenge{},
		configs:    map[string]Config{},
	}
	if c.timeout == 0 {
		c.timeout = DefaultTimeout
	}
	c.http = &http.Client{Transport: tr, CheckRedirect: checkRedirect}
	return c
}

// Disabled reports whether registry access is switched off.
func (c *Client) Disabled() bool { return c.disabled }

// Requests is how many HTTP requests the client has sent.
func (c *Client) Requests() int64 { return c.requests.Load() }

// IsLoopback reports whether hostport names this machine, the only place
// plain HTTP is spoken to.
func IsLoopback(hostport string) bool {
	h := hostport
	if hh, _, err := net.SplitHostPort(hostport); err == nil {
		h = hh
	}
	h = strings.Trim(h, "[]")
	if strings.EqualFold(h, "localhost") {
		return true
	}
	ip := net.ParseIP(h)
	return ip != nil && ip.IsLoopback()
}

func schemeFor(host string) string {
	if IsLoopback(host) {
		return "http"
	}
	return "https"
}

func allowedURL(u *url.URL) bool {
	return u.Scheme == "https" || (u.Scheme == "http" && IsLoopback(u.Host))
}

// checkRedirect refuses a redirect to plain HTTP anywhere but loopback and
// any downgrade from HTTPS, and drops the credential when the host changes.
func checkRedirect(req *http.Request, via []*http.Request) error {
	if len(via) >= 5 {
		return &Error{Kind: KindUnreadable, Host: via[0].URL.Host, Reason: "too many redirects"}
	}
	prev := via[len(via)-1]
	if !allowedURL(req.URL) || (prev.URL.Scheme == "https" && req.URL.Scheme != "https") {
		return &Error{Kind: KindRefused, Host: via[0].URL.Host, Reason: "refused a redirect to plain HTTP"}
	}
	if req.URL.Host != via[0].URL.Host {
		req.Header.Del("Authorization")
	}
	return nil
}

// credID identifies a credential in the token cache without keeping it in
// a printable form.
func credID(c Credential) string {
	if !c.Present() {
		return ""
	}
	h := sha256.Sum256([]byte(c.Source + "\x00" + c.Username + "\x00" + c.Password.Reveal() + "\x00" + c.IdentityToken.Reveal() + "\x00" + c.RegistryToken.Reveal()))
	return hex.EncodeToString(h[:8])
}

// get fetches path from r's registry, answering a challenge once. It
// returns the body, cut off at limit, and the response headers.
func (c *Client) get(ctx context.Context, r Ref, cred Credential, path, accept string, limit int64) ([]byte, http.Header, error) {
	if c.disabled {
		return nil, nil, &Error{Kind: KindRefused, Host: r.Host, Reason: "registry access is switched off (--no-registry)"}
	}
	u := schemeFor(r.Host) + "://" + r.Host + path
	scope := "repository:" + r.Path + ":pull"
	id := credID(cred)

	auth := ""
	var cached *tokenKey
	switch {
	case cred.RegistryToken.Present():
		auth = "Bearer " + cred.RegistryToken.Reveal()
	default:
		c.mu.Lock()
		if ch, ok := c.challenges[r.Host]; ok && ch.scheme == "bearer" {
			k := tokenKey{r.Host, ch.realm, ch.service, scope, id}
			if t, ok := c.tokens[k]; ok {
				auth, cached = "Bearer "+t.Reveal(), &k
			}
		}
		c.mu.Unlock()
	}

	resp, err := c.send(ctx, r.Host, u, accept, auth)
	if err != nil {
		return nil, nil, err
	}
	if resp.StatusCode == http.StatusUnauthorized {
		ch := parseChallenge(resp.Header.Get("WWW-Authenticate"))
		drain(resp)
		if cached != nil {
			c.mu.Lock()
			delete(c.tokens, *cached)
			c.mu.Unlock()
		}
		switch {
		case ch.scheme == "bearer" && ch.realm != "":
			tok, err := c.token(ctx, r, ch, scope, cred)
			if err != nil {
				return nil, nil, err
			}
			c.mu.Lock()
			c.challenges[r.Host] = ch
			c.tokens[tokenKey{r.Host, ch.realm, ch.service, scope, id}] = tok
			c.mu.Unlock()
			auth = "Bearer " + tok.Reveal()
		case ch.scheme == "basic" && cred.Password.Present():
			auth = basic(cred.Username, cred.Password.Reveal())
		default:
			return nil, nil, unauthorized(r.Host, cred)
		}
		if resp, err = c.send(ctx, r.Host, u, accept, auth); err != nil {
			return nil, nil, err
		}
		if resp.StatusCode == http.StatusUnauthorized {
			drain(resp)
			return nil, nil, unauthorized(r.Host, cred)
		}
	}
	defer drain(resp)
	if err := statusError(r.Host, resp, "the registry"); err != nil {
		return nil, nil, err
	}
	body, err := readLimited(resp.Body, limit)
	if err != nil {
		return nil, nil, &Error{Kind: KindUnreadable, Host: r.Host, Reason: err.Error()}
	}
	return body, resp.Header, nil
}

func unauthorized(host string, cred Credential) error {
	if !cred.Present() {
		return &Error{Kind: KindUnauthorized, Host: host, Anonymous: true, Reason: "refused anonymous access, and no credential is configured for this host"}
	}
	return &Error{Kind: KindUnauthorized, Host: host, Reason: "refused the configured credential (from " + cred.Source + ")"}
}

func (c *Client) send(ctx context.Context, host, u, accept, auth string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, &Error{Kind: KindRefused, Host: host, Reason: "the request could not be built"}
	}
	if accept != "" {
		req.Header.Set("Accept", accept)
	}
	if auth != "" {
		req.Header.Set("Authorization", auth)
	}
	c.requests.Add(1)
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, classifyTransport(host, err)
	}
	return resp, nil
}

// token asks the challenge's realm for a pull token. The credential sent is
// the one configured for the registry host — never one chosen by the realm's
// host, so a hostile registry cannot draw out another registry's credential.
func (c *Client) token(ctx context.Context, r Ref, ch challenge, scope string, cred Credential) (secret.Secret, error) {
	ru, err := url.Parse(ch.realm)
	if err != nil || !allowedURL(ru) {
		return secret.Secret{}, &Error{Kind: KindRefused, Host: r.Host, Reason: "refused a token realm that is not HTTPS"}
	}
	q := ru.Query()
	if ch.service != "" {
		q.Set("service", ch.service)
	}
	q.Set("scope", scope)
	ru.RawQuery = q.Encode()
	auth := ""
	switch {
	case cred.IdentityToken.Present():
		// A refresh token is exchanged on GET as a Basic password; the
		// OAuth form is a POST, and the desk sends GETs only (U12).
		user := cred.Username
		if user == "" {
			user = "<token>"
		}
		auth = basic(user, cred.IdentityToken.Reveal())
	case cred.Password.Present():
		auth = basic(cred.Username, cred.Password.Reveal())
	}
	resp, err := c.send(ctx, ru.Host, ru.String(), "application/json", auth)
	if err != nil {
		return secret.Secret{}, err
	}
	defer drain(resp)
	switch resp.StatusCode {
	case http.StatusUnauthorized, http.StatusForbidden:
		return secret.Secret{}, &Error{Kind: KindTokenRefused, Host: ru.Host, Reason: fmt.Sprintf("the token service refused the request for %s (%d)", scope, resp.StatusCode)}
	}
	if err := statusError(ru.Host, resp, "the token service"); err != nil {
		return secret.Secret{}, err
	}
	body, err := readLimited(resp.Body, tokenLimit)
	if err != nil {
		return secret.Secret{}, &Error{Kind: KindUnreadable, Host: ru.Host, Reason: err.Error()}
	}
	var t struct {
		Token       string `json:"token"`
		AccessToken string `json:"access_token"`
	}
	if json.Unmarshal(body, &t) != nil || (t.Token == "" && t.AccessToken == "") {
		return secret.Secret{}, &Error{Kind: KindUnreadable, Host: ru.Host, Reason: "the token service's answer holds no token"}
	}
	if t.Token == "" {
		t.Token = t.AccessToken
	}
	return secret.New(t.Token), nil
}

func basic(user, pass string) string {
	req := http.Request{Header: http.Header{}}
	req.SetBasicAuth(user, pass)
	return req.Header.Get("Authorization")
}

// statusError classifies a non-200 answer.
func statusError(host string, resp *http.Response, who string) error {
	switch resp.StatusCode {
	case http.StatusOK:
		return nil
	case http.StatusNotFound:
		return &Error{Kind: KindNotFound, Host: host, Reason: who + " has no such manifest"}
	case http.StatusForbidden:
		return &Error{Kind: KindDenied, Host: host, Reason: who + " denied access (403)"}
	case http.StatusTooManyRequests:
		e := &Error{Kind: KindQuota, Host: host, Reason: who + " refused: pull quota exhausted (429)", Reset: quotaReset(resp.Header, time.Now())}
		if !e.Reset.IsZero() {
			e.Reason += ", resets " + e.Reset.Local().Format("15:04")
		}
		return e
	}
	return &Error{Kind: KindUnreadable, Host: host, Reason: fmt.Sprintf("%s answered %d", who, resp.StatusCode)}
}

// quotaReset reads when a quota allows again: Retry-After (seconds or a
// date), else RateLimit-Reset or X-RateLimit-Reset (seconds from now, or a
// Unix time).
func quotaReset(h http.Header, now time.Time) time.Time {
	if v := strings.TrimSpace(h.Get("Retry-After")); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n >= 0 {
			return now.Add(time.Duration(n) * time.Second)
		}
		if t, err := http.ParseTime(v); err == nil {
			return t
		}
	}
	for _, k := range []string{"RateLimit-Reset", "X-RateLimit-Reset"} {
		v := strings.TrimSpace(strings.SplitN(h.Get(k), ";", 2)[0])
		if n, err := strconv.ParseInt(v, 10, 64); err == nil && n > 0 {
			if n > 1_000_000_000 {
				return time.Unix(n, 0)
			}
			return now.Add(time.Duration(n) * time.Second)
		}
	}
	return time.Time{}
}

var errTooLarge = errors.New("the answer exceeds the size limit and was cut off")

func readLimited(r io.Reader, limit int64) ([]byte, error) {
	b, err := io.ReadAll(io.LimitReader(r, limit+1))
	if err != nil {
		return nil, errors.New("the answer was cut short")
	}
	if int64(len(b)) > limit {
		return nil, errTooLarge
	}
	return b, nil
}

func drain(resp *http.Response) {
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))
	_ = resp.Body.Close()
}

// parseChallenge reads a WWW-Authenticate header: a scheme, then
// comma-separated key="value" parameters.
func parseChallenge(h string) challenge {
	scheme, rest, _ := strings.Cut(strings.TrimSpace(h), " ")
	ch := challenge{scheme: strings.ToLower(scheme)}
	for rest != "" {
		rest = strings.TrimLeft(rest, " ,")
		k, v, ok := strings.Cut(rest, "=")
		if !ok {
			break
		}
		k = strings.ToLower(strings.TrimSpace(k))
		v = strings.TrimSpace(v)
		var val string
		if strings.HasPrefix(v, `"`) {
			end := strings.IndexByte(v[1:], '"')
			if end < 0 {
				break
			}
			val, rest = v[1:1+end], v[2+end:]
		} else {
			val, rest, _ = strings.Cut(v, ",")
		}
		switch k {
		case "realm":
			ch.realm = val
		case "service":
			ch.service = val
		}
	}
	return ch
}

func digestOf(b []byte) string {
	h := sha256.Sum256(b)
	return "sha256:" + hex.EncodeToString(h[:])
}
