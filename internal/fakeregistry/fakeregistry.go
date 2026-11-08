// Package fakeregistry is an in-process OCI registry for tests and the
// sandbox: tags, indexes, manifests and config blobs held in memory, a
// Bearer or Basic challenge, a token realm that may live on another host,
// request counting and a quota. It speaks plain HTTP on 127.0.0.1, which is
// the only thing the registry client and the test guard let a test reach.
package fakeregistry

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Auth selects how the registry challenges.
type Auth int

const (
	// Anonymous: no challenge at all.
	Anonymous Auth = iota
	// Bearer: 401 with a Bearer challenge naming the realm; the realm
	// hands out a token to anyone, or only to User/Password when set.
	Bearer
	// Basic: 401 with a Basic challenge; User/Password must match.
	Basic
)

// Registry is one fake registry.
type Registry struct {
	*httptest.Server
	// Realm is the token service; a separate server, so the realm is on
	// another host than the registry.
	Realm *httptest.Server

	mu        sync.Mutex
	auth      Auth
	user, pw  string
	tokens    map[string]bool
	tokenN    int
	refuseOne bool
	quota     int               // requests allowed before 429; 0 = unlimited
	manifests map[string]stored // "path:tag" or "path@digest"
	blobs     map[string][]byte
	log       []Request
	// concealed repositories answer 401 whatever the credential, as Docker
	// Hub answers for one that does not exist or that may not be seen.
	concealed map[string]bool
	// linkBase, when set, is put before the path of a tags/list Link, so a
	// test can make the registry name another host.
	linkBase string
}

// Request is one request the registry saw.
type Request struct {
	Method, Path, Query, Accept, Authorization string
	Realm                                      bool
}

type stored struct {
	body      []byte
	mediaType string
}

// New starts a registry and its realm.
func New() *Registry {
	r := newRegistry()
	r.Realm = httptest.NewServer(http.HandlerFunc(r.serveToken))
	r.Server = httptest.NewServer(http.HandlerFunc(r.serve))
	return r
}

func newRegistry() *Registry {
	return &Registry{tokens: map[string]bool{}, manifests: map[string]stored{}, blobs: map[string][]byte{}, concealed: map[string]bool{}}
}

// Listen starts a registry on addr, which must be a loopback address
// ("127.0.0.1:0" picks a free port): the sandbox's registry, in plain HTTP,
// which the desk speaks to loopback hosts only.
func Listen(addr string) (*Registry, error) {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return nil, err
	}
	if ip := net.ParseIP(host); ip == nil || !ip.IsLoopback() {
		return nil, fmt.Errorf("the fake registry listens on a loopback address only, not %s", addr)
	}
	l, err := net.Listen("tcp", addr)
	if err != nil {
		return nil, err
	}
	r := newRegistry()
	r.Realm = httptest.NewServer(http.HandlerFunc(r.serveToken))
	r.Server = httptest.NewUnstartedServer(http.HandlerFunc(r.serve))
	_ = r.Server.Listener.Close()
	r.Server.Listener = l
	r.Server.Start()
	return r, nil
}

// Conceal makes the registry answer 401 for path whatever the credential.
func (r *Registry) Conceal(path string) {
	r.mu.Lock()
	r.concealed[path] = true
	r.mu.Unlock()
}

// SetLinkBase puts base (e.g. "http://elsewhere.example") before the path
// of every tags/list Link header.
func (r *Registry) SetLinkBase(base string) {
	r.mu.Lock()
	r.linkBase = base
	r.mu.Unlock()
}

// AliasTags points each of tags at what path:from names, as a registry
// does when several tags name one build. It is cheap: nothing is built.
func (r *Registry) AliasTags(path, from string, tags ...string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	m, ok := r.manifests[path+":"+from]
	if !ok {
		return
	}
	for _, t := range tags {
		r.manifests[path+":"+t] = m
	}
}

// Close stops both servers.
func (r *Registry) Close() { r.Server.Close(); r.Realm.Close() }

// Host is host:port, as a reference names it.
func (r *Registry) Host() string { return strings.TrimPrefix(r.URL, "http://") }

// SetAuth sets the challenge and, when user is non-empty, the only
// credential accepted.
func (r *Registry) SetAuth(a Auth, user, pw string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.auth, r.user, r.pw = a, user, pw
}

// RefuseNextToken makes the registry refuse one token it handed out
// before, as a registry does when a token expires.
func (r *Registry) RefuseNextToken() {
	r.mu.Lock()
	r.refuseOne = true
	r.mu.Unlock()
}

// SetQuota allows n more registry requests, then answers 429.
func (r *Registry) SetQuota(n int) {
	r.mu.Lock()
	r.quota = n
	r.mu.Unlock()
}

// Requests returns the requests seen so far.
func (r *Registry) Requests() []Request {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]Request(nil), r.log...)
}

// Count is the number of registry (not realm) requests seen.
func (r *Registry) Count() int {
	n := 0
	for _, q := range r.Requests() {
		if !q.Realm {
			n++
		}
	}
	return n
}

// Reset forgets the request log.
func (r *Registry) Reset() {
	r.mu.Lock()
	r.log = nil
	r.mu.Unlock()
}

// Image is one platform's build.
type Image struct {
	OS, Arch, Variant string
	// Created is written as the config's created field; nil leaves it out.
	Created *time.Time
	DiffIDs []string
}

// Pushed is what Push stored.
type Pushed struct {
	// Digest is the tag's digest: the index, or the single manifest.
	Digest string
	// Manifests and Configs are per image, in the order given.
	Manifests, Configs []string
}

// Push stores images under path:tag — an index when more than one image
// (or index is set), a single schema-2 manifest otherwise.
func (r *Registry) Push(path, tag string, index bool, imgs ...Image) Pushed {
	var p Pushed
	type desc struct {
		MediaType string            `json:"mediaType"`
		Digest    string            `json:"digest"`
		Size      int               `json:"size"`
		Platform  map[string]string `json:"platform,omitempty"`
	}
	var children []desc
	var last stored
	for _, im := range imgs {
		cfg := map[string]any{
			"architecture": im.Arch, "os": im.OS,
			"rootfs": map[string]any{"type": "layers", "diff_ids": im.DiffIDs},
		}
		if im.Variant != "" {
			cfg["variant"] = im.Variant
		}
		if im.Created != nil {
			cfg["created"] = im.Created.UTC().Format(time.RFC3339Nano)
		}
		cb, _ := json.Marshal(cfg)
		cd := Digest(cb)
		r.mu.Lock()
		r.blobs[path+"@"+cd] = cb
		r.mu.Unlock()
		m := map[string]any{
			"schemaVersion": 2, "mediaType": "application/vnd.docker.distribution.manifest.v2+json",
			"config": desc{MediaType: "application/vnd.docker.container.image.v1+json", Digest: cd, Size: len(cb)},
			"layers": []any{},
		}
		mb, _ := json.Marshal(m)
		md := Digest(mb)
		last = stored{mb, "application/vnd.docker.distribution.manifest.v2+json"}
		r.mu.Lock()
		r.manifests[path+"@"+md] = last
		r.mu.Unlock()
		pl := map[string]string{"os": im.OS, "architecture": im.Arch}
		if im.Variant != "" {
			pl["variant"] = im.Variant
		}
		children = append(children, desc{MediaType: last.mediaType, Digest: md, Size: len(mb), Platform: pl})
		p.Manifests, p.Configs = append(p.Manifests, md), append(p.Configs, cd)
	}
	if index || len(imgs) > 1 {
		ib, _ := json.Marshal(map[string]any{
			"schemaVersion": 2, "mediaType": "application/vnd.oci.image.index.v1+json", "manifests": children,
		})
		last = stored{ib, "application/vnd.oci.image.index.v1+json"}
		r.mu.Lock()
		r.manifests[path+"@"+Digest(ib)] = last
		r.mu.Unlock()
	}
	r.mu.Lock()
	r.manifests[path+":"+tag] = last
	r.mu.Unlock()
	p.Digest = Digest(last.body)
	return p
}

// Delete removes a tag.
func (r *Registry) Delete(path, tag string) {
	r.mu.Lock()
	delete(r.manifests, path+":"+tag)
	r.mu.Unlock()
}

// SetBlob replaces a stored blob's bytes without changing its digest — a
// registry serving a config that is not the one its manifest names.
func (r *Registry) SetBlob(path, digest string, body []byte) {
	r.mu.Lock()
	r.blobs[path+"@"+digest] = body
	r.mu.Unlock()
}

// Digest is the sha256 digest of b.
func Digest(b []byte) string {
	h := sha256.Sum256(b)
	return "sha256:" + hex.EncodeToString(h[:])
}

func (r *Registry) serve(w http.ResponseWriter, q *http.Request) {
	r.mu.Lock()
	r.log = append(r.log, Request{Method: q.Method, Path: q.URL.Path, Query: q.URL.RawQuery, Accept: q.Header.Get("Accept"), Authorization: q.Header.Get("Authorization")})
	auth, user, pw := r.auth, r.user, r.pw
	concealed := false
	for p := range r.concealed {
		concealed = concealed || strings.HasPrefix(q.URL.Path, "/v2/"+p+"/")
	}
	quotaHit := false
	if r.quota > 0 {
		r.quota--
		quotaHit = r.quota == 0
	}
	r.mu.Unlock()
	if q.Method != http.MethodGet && q.Method != http.MethodHead {
		http.Error(w, "read-only fake", http.StatusMethodNotAllowed)
		return
	}
	if !strings.HasPrefix(q.URL.Path, "/v2/") {
		http.NotFound(w, q)
		return
	}
	if concealed || !r.authorized(auth, user, pw, q.Header.Get("Authorization")) {
		if auth == Bearer || (auth == Anonymous && concealed) {
			w.Header().Set("WWW-Authenticate", fmt.Sprintf(`Bearer realm="%s/token",service="fake-registry"`, r.Realm.URL))
		} else {
			w.Header().Set("WWW-Authenticate", `Basic realm="fake"`)
		}
		http.Error(w, `{"errors":[{"code":"UNAUTHORIZED"}]}`, http.StatusUnauthorized)
		return
	}
	if quotaHit {
		w.Header().Set("Retry-After", "3600")
		w.Header().Set("RateLimit-Remaining", "0;w=3600")
		http.Error(w, `{"errors":[{"code":"TOOMANYREQUESTS"}]}`, http.StatusTooManyRequests)
		return
	}
	rest := strings.TrimPrefix(q.URL.Path, "/v2/")
	r.mu.Lock()
	defer r.mu.Unlock()
	if i := strings.LastIndex(rest, "/manifests/"); i >= 0 {
		path, ref := rest[:i], rest[i+len("/manifests/"):]
		key := path + ":" + ref
		if strings.HasPrefix(ref, "sha256:") {
			key = path + "@" + ref
		}
		m, ok := r.manifests[key]
		if !ok {
			http.Error(w, `{"errors":[{"code":"MANIFEST_UNKNOWN"}]}`, http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", m.mediaType)
		w.Header().Set("Docker-Content-Digest", Digest(m.body))
		_, _ = w.Write(m.body)
		return
	}
	if path, ok := strings.CutSuffix(rest, "/tags/list"); ok {
		r.serveTagsLocked(w, q, path)
		return
	}
	if i := strings.LastIndex(rest, "/blobs/"); i >= 0 {
		b, ok := r.blobs[rest[:i]+"@"+rest[i+len("/blobs/"):]]
		if !ok {
			http.Error(w, `{"errors":[{"code":"BLOB_UNKNOWN"}]}`, http.StatusNotFound)
			return
		}
		_, _ = w.Write(b)
		return
	}
	http.NotFound(w, q)
}

func (r *Registry) authorized(auth Auth, user, pw, h string) bool {
	switch auth {
	case Anonymous:
		return true
	case Basic:
		return h == "Basic "+base64.StdEncoding.EncodeToString([]byte(user+":"+pw))
	}
	tok, ok := strings.CutPrefix(h, "Bearer ")
	if !ok {
		return false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if !r.tokens[tok] {
		return false
	}
	if r.refuseOne {
		r.refuseOne = false
		delete(r.tokens, tok)
		return false
	}
	return true
}

func (r *Registry) serveToken(w http.ResponseWriter, q *http.Request) {
	r.mu.Lock()
	r.log = append(r.log, Request{Method: q.Method, Path: q.URL.Path + "?" + q.URL.RawQuery, Authorization: q.Header.Get("Authorization"), Realm: true})
	user, pw := r.user, r.pw
	r.mu.Unlock()
	if q.Method != http.MethodGet {
		http.Error(w, "GET only", http.StatusMethodNotAllowed)
		return
	}
	if user != "" {
		u, p, ok := q.BasicAuth()
		if !ok || u != user || p != pw {
			http.Error(w, "refused", http.StatusUnauthorized)
			return
		}
	}
	r.mu.Lock()
	r.tokenN++
	tok := fmt.Sprintf("fake-token-%d", r.tokenN)
	r.tokens[tok] = true
	r.mu.Unlock()
	_ = json.NewEncoder(w).Encode(map[string]any{"token": tok, "expires_in": 300})
}

// serveTagsLocked answers GET /v2/<path>/tags/list in lexical order, as
// distribution does: n bounds the page, last starts after a tag, and a
// Link names the next page when more remain.
func (r *Registry) serveTagsLocked(w http.ResponseWriter, q *http.Request, path string) {
	var tags []string
	for k := range r.manifests {
		if t, ok := strings.CutPrefix(k, path+":"); ok {
			tags = append(tags, t)
		}
	}
	if len(tags) == 0 {
		http.Error(w, `{"errors":[{"code":"NAME_UNKNOWN"}]}`, http.StatusNotFound)
		return
	}
	sort.Strings(tags)
	if last := q.URL.Query().Get("last"); last != "" {
		i := sort.SearchStrings(tags, last)
		for i < len(tags) && tags[i] <= last {
			i++
		}
		tags = tags[i:]
	}
	n, _ := strconv.Atoi(q.URL.Query().Get("n"))
	if n > 0 && len(tags) > n {
		tags = tags[:n]
		w.Header().Set("Link", fmt.Sprintf(`<%s/v2/%s/tags/list?n=%d&last=%s>; rel="next"`, r.linkBase, path, n, tags[n-1]))
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"name": path, "tags": tags})
}
