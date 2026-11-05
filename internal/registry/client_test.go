package registry

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	ocispec "github.com/opencontainers/image-spec/specs-go/v1"

	"github.com/fardani235/docker-desk/internal/fakeregistry"
	"github.com/fardani235/docker-desk/internal/secret"
	"github.com/fardani235/docker-desk/internal/testguard"
)

func TestMain(m *testing.M) {
	testguard.Install()
	os.Exit(m.Run())
}

var amd64 = ocispec.Platform{OS: "linux", Architecture: "amd64"}

func at(s string) *time.Time {
	t, _ := time.Parse(time.RFC3339, s)
	return &t
}

func ref(t *testing.T, reg *fakeregistry.Registry, repo string) Ref {
	t.Helper()
	r, err := ParseRef(reg.Host() + "/" + repo)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func TestResolveRequestsAndMediaTypes(t *testing.T) {
	reg := fakeregistry.New()
	defer reg.Close()
	old := reg.Push("acme/api", "1", true,
		fakeregistry.Image{OS: "linux", Arch: "amd64", Created: at("2026-01-01T00:00:00Z"), DiffIDs: []string{"sha256:a"}},
		fakeregistry.Image{OS: "linux", Arch: "arm64", Variant: "v8", DiffIDs: []string{"sha256:a"}})
	c := New(Options{})
	r := ref(t, reg, "acme/api:1")

	rem, err := c.Resolve(context.Background(), r, Credential{}, Local{Digest: old.Digest, Platform: amd64})
	if err != nil || !rem.Same {
		t.Fatalf("current: %+v %v", rem, err)
	}
	if n := reg.Count(); n != 1 {
		t.Errorf("a current image cost %d requests, want 1", n)
	}
	for _, mt := range []string{ocispec.MediaTypeImageIndex, ocispec.MediaTypeImageManifest, mediaDockerList, mediaDockerSchema} {
		if !strings.Contains(reg.Requests()[0].Accept, mt) {
			t.Errorf("Accept lacks %s", mt)
		}
	}

	moved := reg.Push("acme/api", "1", true,
		fakeregistry.Image{OS: "linux", Arch: "amd64", Created: at("2026-02-01T00:00:00Z"), DiffIDs: []string{"sha256:a", "sha256:b"}})
	reg.Reset()
	rem, err = c.Resolve(context.Background(), r, Credential{}, Local{Digest: old.Digest, Platform: amd64})
	if err != nil || rem.Same || rem.Config == nil || rem.Digest != moved.Digest || rem.ConfigDigest != moved.Configs[0] {
		t.Fatalf("moved: %+v %v", rem, err)
	}
	if n := reg.Count(); n != 3 {
		t.Errorf("a moved image cost %d requests, want 3 (index, manifest, config)", n)
	}
	if !rem.Config.Created.Equal(*at("2026-02-01T00:00:00Z")) || len(rem.Config.DiffIDs) != 2 {
		t.Errorf("config: %+v", rem.Config)
	}

	// The config is cached by digest; the local config skips it.
	reg.Reset()
	if _, err := c.Resolve(context.Background(), r, Credential{}, Local{Digest: old.Digest, Platform: amd64}); err != nil || reg.Count() != 2 {
		t.Errorf("cached config still fetched: %d %v", reg.Count(), err)
	}
	reg.Reset()
	c2 := New(Options{})
	rem, _ = c2.Resolve(context.Background(), r, Credential{}, Local{Digest: old.Digest, ConfigDigest: moved.Configs[0], Platform: amd64})
	if !rem.SameConfig || reg.Count() != 2 {
		t.Errorf("same config: %+v after %d requests", rem, reg.Count())
	}

	// A platform no longer published.
	rem, err = c.Resolve(context.Background(), r, Credential{}, Local{Digest: old.Digest, Platform: ocispec.Platform{OS: "linux", Architecture: "arm64"}})
	if err != nil || !rem.PlatformMissing {
		t.Errorf("missing platform: %+v %v", rem, err)
	}
}

func TestBearerChallengeAndTokenCache(t *testing.T) {
	reg := fakeregistry.New()
	defer reg.Close()
	p := reg.Push("acme/api", "1", false, fakeregistry.Image{OS: "linux", Arch: "amd64"})
	reg.SetAuth(fakeregistry.Bearer, "op", "pw:with:colons")
	c := New(Options{})
	r := ref(t, reg, "acme/api:1")
	cred := Credential{Username: "op", Password: secret.New("pw:with:colons"), Source: "auths"}

	if _, err := c.Resolve(context.Background(), r, cred, Local{Digest: p.Digest}); err != nil {
		t.Fatal(err)
	}
	reqs := reg.Requests()
	if len(reqs) != 3 || !reqs[1].Realm || reqs[0].Authorization != "" || !strings.HasPrefix(reqs[2].Authorization, "Bearer ") {
		t.Fatalf("want anonymous, realm, bearer: %+v", reqs)
	}
	if !strings.Contains(reqs[1].Path, "scope=repository%3Aacme%2Fapi%3Apull") || !strings.Contains(reqs[1].Path, "service=fake-registry") {
		t.Errorf("token request: %s", reqs[1].Path)
	}
	// The realm is on another host than the registry.
	if strings.Contains(reg.Realm.URL, reg.Host()) {
		t.Fatal("realm on the same host")
	}

	// The token is reused…
	reg.Reset()
	c.Resolve(context.Background(), r, cred, Local{Digest: p.Digest})
	if reqs := reg.Requests(); len(reqs) != 1 || !strings.HasPrefix(reqs[0].Authorization, "Bearer ") {
		t.Errorf("cached token not reused: %+v", reqs)
	}
	// …and a refused one is dropped and fetched again, once.
	reg.Reset()
	reg.RefuseNextToken()
	if _, err := c.Resolve(context.Background(), r, cred, Local{Digest: p.Digest}); err != nil {
		t.Fatalf("stale token: %v", err)
	}
	if reqs := reg.Requests(); len(reqs) != 3 {
		t.Errorf("stale token: %d requests, want 3", len(reqs))
	}
	// A different credential is a different cache entry; refused at the realm.
	_, err := c.Resolve(context.Background(), r, Credential{Username: "op", Password: secret.New("wrong"), Source: "auths"}, Local{Digest: p.Digest})
	if KindOf(err) != KindTokenRefused || Fallback(err) {
		t.Errorf("wrong credential: %v", err)
	}
	if strings.Contains(err.Error(), "wrong") {
		t.Errorf("credential in error: %v", err)
	}
}

func TestBasicChallenge(t *testing.T) {
	reg := fakeregistry.New()
	defer reg.Close()
	p := reg.Push("x", "1", false, fakeregistry.Image{OS: "linux", Arch: "amd64"})
	reg.SetAuth(fakeregistry.Basic, "", "only:pw")
	r := ref(t, reg, "x:1")
	if _, err := New(Options{}).Resolve(context.Background(), r, Credential{Password: secret.New("only:pw"), Source: "auths"}, Local{Digest: p.Digest}); err != nil {
		t.Fatalf("basic with empty username: %v", err)
	}
	_, err := New(Options{}).Resolve(context.Background(), r, Credential{}, Local{Digest: p.Digest})
	var e *Error
	if !errors.As(err, &e) || e.Kind != KindUnauthorized || !e.Anonymous || Fallback(err) {
		t.Errorf("anonymous on a private registry: %v", err)
	}
}

func TestTypedErrors(t *testing.T) {
	reg := fakeregistry.New()
	defer reg.Close()
	reg.Push("x", "1", false, fakeregistry.Image{OS: "linux", Arch: "amd64"})
	c := New(Options{})
	ctx := context.Background()

	_, err := c.Resolve(ctx, ref(t, reg, "x:gone"), Credential{}, Local{})
	if KindOf(err) != KindNotFound || Fallback(err) {
		t.Errorf("missing manifest: %v", err)
	}

	reg.SetQuota(1)
	_, err = c.Resolve(ctx, ref(t, reg, "x:1"), Credential{}, Local{})
	var e *Error
	if !errors.As(err, &e) || e.Kind != KindQuota || e.Reset.IsZero() || time.Until(e.Reset) < 50*time.Minute || !Fallback(err) {
		t.Errorf("quota: %+v", err)
	}
	reg.SetQuota(0)

	status := func(code int) Kind {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(code) }))
		defer srv.Close()
		r, _ := ParseRef(strings.TrimPrefix(srv.URL, "http://") + "/x:1")
		_, err := c.Resolve(ctx, r, Credential{}, Local{})
		return KindOf(err)
	}
	if k := status(403); k != KindDenied {
		t.Errorf("403: %v", k)
	}
	if k := status(500); k != KindUnreadable {
		t.Errorf("500: %v", k)
	}

	// Name: a .invalid host never resolves — and is not loopback, so the
	// guard refuses it before any lookup. Classify a real DNS error instead.
	if k := KindOf(classifyTransport("h", &net.DNSError{Err: "no such host", Name: "h"})); k != KindName {
		t.Errorf("name: %v", k)
	}
	// TLS: a TLS server on loopback, which the client never trusts —
	// asked directly over https.
	tlsSrv := httptest.NewTLSServer(http.NotFoundHandler())
	defer tlsSrv.Close()
	_, err = c.send(ctx, "h", tlsSrv.URL+"/v2/", "", "")
	if KindOf(err) != KindTLS || !Fallback(err) {
		t.Errorf("tls: %v", err)
	}
	// Timeout.
	slow := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, q *http.Request) {
		select {
		case <-q.Context().Done():
		case <-time.After(5 * time.Second):
		}
	}))
	defer slow.Close()
	r, _ := ParseRef(strings.TrimPrefix(slow.URL, "http://") + "/x:1")
	_, err = New(Options{Timeout: 200 * time.Millisecond}).Resolve(ctx, r, Credential{}, Local{})
	if KindOf(err) != KindTimeout || !Fallback(err) {
		t.Errorf("timeout: %v", err)
	}
	// Connection refused.
	dead := httptest.NewServer(http.NotFoundHandler())
	addr := strings.TrimPrefix(dead.URL, "http://")
	dead.Close()
	r, _ = ParseRef(addr + "/x:1")
	if _, err = c.Resolve(ctx, r, Credential{}, Local{}); KindOf(err) != KindConnection || !Fallback(err) {
		t.Errorf("refused: %v", err)
	}
}

func TestBoundsAndRedirects(t *testing.T) {
	ctx := context.Background()
	c := New(Options{})

	big := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		chunk := make([]byte, 1<<20)
		for i := 0; i < ManifestLimit>>20+2; i++ {
			if _, err := w.Write(chunk); err != nil {
				return
			}
		}
	}))
	defer big.Close()
	r, _ := ParseRef(strings.TrimPrefix(big.URL, "http://") + "/x:1")
	if _, err := c.Resolve(ctx, r, Credential{}, Local{}); KindOf(err) != KindUnreadable || !errors.Is(err, err) || !strings.Contains(err.Error(), "size limit") {
		t.Errorf("oversized: %v", err)
	}

	// A cross-host redirect must not carry the credential.
	var gotAuth = "unset"
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, q *http.Request) {
		gotAuth = q.Header.Get("Authorization")
		http.NotFound(w, q)
	}))
	defer other.Close()
	redir := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, q *http.Request) {
		if q.Header.Get("Authorization") == "" {
			w.Header().Set("WWW-Authenticate", `Basic realm="x"`)
			w.WriteHeader(401)
			return
		}
		http.Redirect(w, q, other.URL+q.URL.Path, http.StatusTemporaryRedirect)
	}))
	defer redir.Close()
	r, _ = ParseRef(strings.TrimPrefix(redir.URL, "http://") + "/x:1")
	c.Resolve(ctx, r, Credential{Username: "u", Password: secret.New("p"), Source: "auths"}, Local{})
	if gotAuth != "" {
		t.Errorf("the redirected host received Authorization %q", gotAuth)
	}

	// No redirect to plain HTTP on a non-loopback host, and no downgrade.
	req, _ := http.NewRequest("GET", "http://registry.example/v2/", nil)
	via, _ := http.NewRequest("GET", "https://registry.example/v2/", nil)
	if err := checkRedirect(req, []*http.Request{via}); KindOf(err) != KindRefused {
		t.Errorf("downgrade allowed: %v", err)
	}
	req, _ = http.NewRequest("GET", "http://127.0.0.1:1/v2/", nil)
	via, _ = http.NewRequest("GET", "https://127.0.0.1:2/v2/", nil)
	if err := checkRedirect(req, []*http.Request{via}); KindOf(err) != KindRefused {
		t.Errorf("https→http on loopback allowed: %v", err)
	}
	if schemeFor("registry.example") != "https" || schemeFor("127.0.0.1:5000") != "http" || schemeFor("localhost:5000") != "http" || schemeFor("[::1]:5000") != "http" {
		t.Error("scheme selection")
	}

	// A config that is not the one its manifest names.
	reg := fakeregistry.New()
	defer reg.Close()
	p := reg.Push("x", "1", false, fakeregistry.Image{OS: "linux", Arch: "amd64"})
	reg.SetBlob("x", p.Configs[0], []byte(`{"created":"2030-01-01T00:00:00Z"}`))
	if _, err := New(Options{}).Resolve(ctx, ref(t, reg, "x:1"), Credential{}, Local{Digest: "sha256:old"}); KindOf(err) != KindUnreadable {
		t.Errorf("tampered config: %v", err)
	}

	// --no-registry sends nothing.
	reg.Reset()
	if _, err := New(Options{Disabled: true}).Resolve(ctx, ref(t, reg, "x:1"), Credential{}, Local{}); KindOf(err) != KindRefused || Fallback(err) || reg.Count() != 0 {
		t.Errorf("disabled: %v, %d requests", err, reg.Count())
	}
}

func TestGuardRefusesRealRegistry(t *testing.T) {
	r, _ := ParseRef("docker.io/library/alpine:3.20")
	_, err := New(Options{}).Resolve(context.Background(), r, Credential{}, Local{})
	if KindOf(err) != KindRefused || !strings.Contains(err.Error(), "test guard") {
		t.Fatalf("a test reached for Docker Hub and was not refused loudly: %v", err)
	}
}
