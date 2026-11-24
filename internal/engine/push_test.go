package engine

import (
	"context"
	"errors"
	"net"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/moby/moby/api/pkg/authconfig"
	"github.com/moby/moby/api/types/registry"

	"github.com/fardani235/docker-desk/internal/fakeengine"
)

const (
	pushPassword = "pa55-w0rd-never-shown"
	pushToken    = "id-t0ken-never-shown"
)

func pushAuth(t *testing.T) string {
	t.Helper()
	enc, err := authconfig.Encode(registry.AuthConfig{
		Username: "ci", Password: pushPassword, IdentityToken: pushToken, ServerAddress: "host.test:5000",
	})
	if err != nil {
		t.Fatal(err)
	}
	return enc
}

// noCredential fails the test if any part of err — its message or any error
// it wraps — carries the credential.
func noCredential(t *testing.T, what string, err error, auth string) {
	t.Helper()
	for e := err; e != nil; e = errors.Unwrap(e) {
		s := e.Error()
		for _, secret := range []string{auth, pushPassword, pushToken} {
			if strings.Contains(s, secret) {
				t.Fatalf("%s: the credential appears in an error: %q", what, s)
			}
		}
	}
}

func TestPushImage(t *testing.T) {
	srv, eng := startFake(t, func(s *fakeengine.Server) {
		s.PushStepDelay = time.Millisecond
		s.AddImage(30_000_000, 0, "host.test:5000/acme/api:3.28.3", "host.test:5000/acme/api:latest")
	})
	auth := pushAuth(t)
	var seen []PushProgress
	res, err := eng.PushImage(ctxT(t), "host.test:5000/acme/api:3.28.3", auth, func(p PushProgress) { seen = append(seen, p) })
	if err != nil {
		t.Fatal(err)
	}
	reqs := srv.PushRequests()
	if len(reqs) != 1 {
		t.Fatalf("push requests: %+v", reqs)
	}
	if r := reqs[0]; r.Name != "host.test:5000/acme/api" || r.Tag != "3.28.3" || r.Auth != auth {
		t.Fatalf("request: name %q tag %q auth sent %v", r.Name, r.Tag, r.Auth == auth)
	}
	// Exactly one request, with the tag in the query: never all tags.
	if n := srv.CountRequests(http.MethodPost, `^/images/.*/push$`); n != 1 {
		t.Fatalf("%d push requests", n)
	}
	for _, r := range srv.Requests() {
		if strings.HasSuffix(r.Path, "/push") && r.Query != "tag=3.28.3" {
			t.Fatalf("query %q", r.Query)
		}
	}
	want := srv.PushedManifest("host.test:5000/acme/api:3.28.3")
	if res.Digest == "" || res.Digest != want || res.Tag != "3.28.3" || res.Size <= 0 {
		t.Fatalf("result %+v, registry holds %q", res, want)
	}
	if res.Layers != 2 || res.Pushed != 2 || res.Exists != 0 {
		t.Fatalf("layers %+v", res)
	}
	if len(seen) == 0 {
		t.Fatal("no progress")
	}
	last := seen[len(seen)-1]
	if last.Total <= 0 || last.Current != last.Total || last.Layers != 2 || last.Done != 2 {
		t.Fatalf("final progress %+v", last)
	}
	for i := 1; i < len(seen); i++ {
		if seen[i].Current < seen[i-1].Current {
			t.Fatalf("progress moved backwards: %+v then %+v", seen[i-1], seen[i])
		}
	}
	// Nothing local changed.
	imgs, err := eng.Images(ctxT(t))
	if err != nil {
		t.Fatal(err)
	}
	if len(imgs) != 1 || len(imgs[0].Refs()) != 2 {
		t.Fatalf("local images changed: %+v", imgs)
	}

	// A second push of the same build finds its layers in the registry.
	res2, err := eng.PushImage(ctxT(t), "host.test:5000/acme/api:latest", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	if res2.Exists != 2 || res2.Pushed != 0 || res2.Tag != "latest" {
		t.Fatalf("second push %+v", res2)
	}
	if got := srv.PushRequests()[1].Auth; got != "" {
		t.Fatalf("no credential, yet X-Registry-Auth %q", got)
	}
}

func TestPushImageRefusesBeforeSending(t *testing.T) {
	srv, eng := startFake(t, func(s *fakeengine.Server) { s.AddImage(1, 0, "acme/api:1") })
	for _, ref := range []string{
		"acme/api", // no tag: the client would push :latest
		"acme/api@sha256:" + strings.Repeat("a", 64),
		"acme/api:1@sha256:" + strings.Repeat("a", 64),
		" acme/api:1",
		"",
		"Not A Ref:1",
	} {
		if _, err := eng.PushImage(ctxT(t), ref, "", nil); err == nil {
			t.Errorf("%q: pushed", ref)
		}
	}
	if n := len(srv.PushRequests()); n != 0 {
		t.Fatalf("%d push requests sent", n)
	}
}

func TestPushImageReadOnly(t *testing.T) {
	srv := fakeengine.New()
	srv.AddImage(1, 0, "acme/api:1")
	cleanup, err := srv.StartTemp()
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	eng, err := New(Options{Host: srv.Host(), ReadOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	defer eng.Close()
	called := false
	_, err = eng.PushImage(ctxT(t), "acme/api:1", pushAuth(t), func(PushProgress) { called = true })
	if !errors.Is(err, ErrReadOnly) || KindOf(err) != KindReadOnly || called {
		t.Fatalf("read-only push: %v (progress called %v)", err, called)
	}
	if m := srv.Mutations(); len(m) != 0 {
		t.Fatalf("mutations: %v", m)
	}
}

func TestPushImageFailures(t *testing.T) {
	srv, eng := startFake(t, func(s *fakeengine.Server) {
		s.AddImage(1, 0, "acme/api:1", "acme/nosuch:1", "acme/private:1", "acme/echo:1")
		s.RequirePushAuth("acme/private")
	})
	auth := pushAuth(t)
	ctx := ctxT(t)

	// A tag the daemon does not have.
	_, err := eng.PushImage(ctx, "acme/api:2", auth, nil)
	if KindOf(err) != KindNotFound || !strings.Contains(err.Error(), "No such image") {
		t.Fatalf("missing tag: %v (kind %v)", err, KindOf(err))
	}
	noCredential(t, "missing tag", err, auth)

	// The registry refuses the repository: the daemon's words, in the stream.
	_, err = eng.PushImage(ctx, "acme/nosuch:1", auth, nil)
	if KindOf(err) != KindDenied || !strings.Contains(err.Error(), "denied: requested access to the resource is denied") {
		t.Fatalf("refused: %v (kind %v)", err, KindOf(err))
	}
	noCredential(t, "refused", err, auth)

	// The registry wants a credential and none was sent.
	_, err = eng.PushImage(ctx, "acme/private:1", "", nil)
	if KindOf(err) != KindDenied || !strings.Contains(err.Error(), "authentication required") {
		t.Fatalf("no credential: %v (kind %v)", err, KindOf(err))
	}

	// A daemon (or a proxy) that quotes the credential back, in the stream
	// and in an HTTP error: neither the header nor a value inside it may
	// reach the desk.
	srv.RefusePush("acme/echo", "denied: bad password "+pushPassword+" token "+pushToken+" header "+auth)
	_, err = eng.PushImage(ctx, "acme/echo:1", auth, nil)
	if err == nil || !strings.Contains(err.Error(), "[credential]") {
		t.Fatalf("echo in stream: %v", err)
	}
	noCredential(t, "echo in stream", err, auth)

	srv.FailNext(http.MethodPost, `/push$`, http.StatusUnauthorized, "unauthorized: "+pushPassword+" / "+auth, 1)
	_, err = eng.PushImage(ctx, "acme/api:1", auth, nil)
	if KindOf(err) != KindDenied {
		t.Fatalf("echo in HTTP error: %v (kind %v)", err, KindOf(err))
	}
	noCredential(t, "echo in HTTP error", err, auth)
}

func TestPushImageCancel(t *testing.T) {
	srv, eng := startFake(t, func(s *fakeengine.Server) {
		s.PushStepDelay = 50 * time.Millisecond
		s.AddImage(1, 0, "acme/api:1")
	})
	ctx, cancel := context.WithCancel(ctxT(t))
	got := make(chan struct{}, 1)
	go func() {
		<-got
		cancel()
	}()
	_, err := eng.PushImage(ctx, "acme/api:1", "", func(p PushProgress) {
		if p.Layers > 0 {
			select {
			case got <- struct{}{}:
			default:
			}
		}
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel: %v", err)
	}
	if d := srv.PushedManifest("acme/api:1"); d != "" {
		t.Fatalf("a cancelled push completed: %s", d)
	}
}

// A digest the daemon reports for another tag than the one sent is not
// taken as this push's result: the push is then "not known", never claimed.
func TestPushImageIgnoresAnotherTagsDigest(t *testing.T) {
	ln, host := startStreamDaemon(t, `{"status":"The push refers to repository [docker.io/acme/api]"}
{"status":"9.9: digest: sha256:`+strings.Repeat("ab", 32)+` size: 1"}
{"aux":{"Tag":"9.9","Digest":"sha256:`+strings.Repeat("ab", 32)+`","Size":1}}
`)
	defer ln.Close()
	eng, err := New(Options{Host: host})
	if err != nil {
		t.Fatal(err)
	}
	defer eng.Close()
	_, err = eng.PushImage(ctxT(t), "acme/api:1", "", nil)
	if err == nil || !strings.Contains(err.Error(), "not known") {
		t.Fatalf("another tag's digest was accepted: %v", err)
	}
}

func TestPushDigestParsing(t *testing.T) {
	d := "sha256:" + strings.Repeat("ab", 32)
	if tag, dg, n, ok := parsePushDigest("3.28.3: digest: " + d + " size: 1570"); !ok || tag != "3.28.3" || dg != d || n != 1570 {
		t.Fatalf("parsed %q %q %d %v", tag, dg, n, ok)
	}
	for _, bad := range []string{
		"3.28.3: digest: sha256:short size: 1",
		"digest: " + d + " size: 1",
		"3.28.3: digest: " + d + " size: 1 trailing",
		"3.28.3: digest: SHA256:" + strings.Repeat("ab", 32) + " size: 1",
	} {
		if _, _, _, ok := parsePushDigest(bad); ok {
			t.Errorf("accepted %q", bad)
		}
	}
	if isManifestDigest("sha256:" + strings.Repeat("g", 64)) {
		t.Error("accepted a non-hex digest")
	}
}

func TestScrubber(t *testing.T) {
	auth := pushAuth(t)
	s := newScrubber(auth)
	out := s.clean("x " + auth + " y " + pushPassword + " z " + pushToken)
	if strings.Contains(out, pushPassword) || strings.Contains(out, pushToken) || strings.Contains(out, auth) {
		t.Fatalf("scrubbed: %q", out)
	}
	// No credential, or one that is not decodable: nothing to remove but
	// the header itself.
	if got := newScrubber("").clean("plain words"); got != "plain words" {
		t.Fatalf("empty scrubber changed text: %q", got)
	}
	if got := newScrubber("not-base64!!").clean("a not-base64!! b"); got != "a [credential] b" {
		t.Fatalf("undecodable header: %q", got)
	}
	// A very short secret is removed too.
	short, err := authconfig.Encode(registry.AuthConfig{Username: "u", Password: "q7"})
	if err != nil {
		t.Fatal(err)
	}
	if got := newScrubber(short).clean("denied: q7"); strings.Contains(got, "q7") {
		t.Fatalf("short secret kept: %q", got)
	}
}

// startStreamDaemon serves a canned push stream on a unix socket in the
// temporary directory: a daemon whose answer the fake would never give.
func startStreamDaemon(t *testing.T, body string) (net.Listener, string) {
	t.Helper()
	sock := filepath.Join(t.TempDir(), "d.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	go func() {
		_ = http.Serve(ln, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Api-Version", "1.56")
			switch {
			case strings.HasSuffix(r.URL.Path, "/_ping"):
				_, _ = w.Write([]byte("OK"))
			case strings.HasSuffix(r.URL.Path, "/push"):
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(body))
			default:
				http.Error(w, `{"message":"page not found"}`, http.StatusNotFound)
			}
		}))
	}()
	return ln, "unix://" + sock
}
