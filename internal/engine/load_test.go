package engine

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/fardani235/docker-desk/internal/fakeengine"
	"github.com/fardani235/docker-desk/internal/forensics/fixture"
)

// loadSeen is what the stub daemon received for POST /images/load.
type loadSeen struct {
	mu          sync.Mutex
	method      string
	path, query string
	contentType string
	body        []byte
	bodyErr     error
	other       []string // any other non-ping request
}

// startLoadDaemon serves a canned load answer on a unix socket in the
// temporary directory, after reading the archive it is sent. api is the API
// version it advertises. hold, when set, keeps the answer open until the
// request is closed.
func startLoadDaemon(t *testing.T, api, answer string, hold bool) (*loadSeen, Engine) {
	t.Helper()
	sock := filepath.Join(t.TempDir(), "d.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	seen := &loadSeen{}
	go func() {
		_ = http.Serve(ln, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Api-Version", api)
			switch {
			case strings.HasSuffix(r.URL.Path, "/_ping"):
				_, _ = w.Write([]byte("OK"))
			case strings.HasSuffix(r.URL.Path, "/images/load"):
				body, err := io.ReadAll(r.Body)
				seen.mu.Lock()
				seen.method, seen.path, seen.query = r.Method, r.URL.Path, r.URL.RawQuery
				seen.contentType, seen.body, seen.bodyErr = r.Header.Get("Content-Type"), body, err
				seen.mu.Unlock()
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(answer))
				if hold {
					w.(http.Flusher).Flush()
					<-r.Context().Done()
				}
			default:
				seen.mu.Lock()
				seen.other = append(seen.other, r.Method+" "+r.URL.Path)
				seen.mu.Unlock()
				http.Error(w, `{"message":"page not found"}`, http.StatusNotFound)
			}
		}))
	}()
	eng, err := New(Options{Host: "unix://" + sock})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { eng.Close() })
	// Negotiate the API version, as any earlier request would have.
	if err := eng.Ping(ctxT(t)); err != nil {
		t.Fatal(err)
	}
	return seen, eng
}

var loadPlatform = Platform{OS: "linux", Architecture: "arm64", Variant: "v8"}

// TestLoadImageRequest: one POST /images/load with a raw tar body, the
// archive reaches the daemon byte for byte, the platform is sent, and the
// daemon's messages are decoded.
func TestLoadImageRequest(t *testing.T) {
	answer := `{"status":"Loading layer","id":"1a2b3c","progressDetail":{"current":512,"total":2048}}` + "\r\n" +
		`{"status":"Loading layer","id":"1a2b3c","progressDetail":{"current":4096,"total":2048}}` + "\r\n" +
		`{"some":"message the desk does not know"}` + "\r\n" +
		"a line that is not JSON\r\n" +
		`{"stream":"Loaded image: host/acme/api:3.28.3\n"}` + "\r\n" +
		`{"stream":"Loaded image ID: sha256:abc\n"}`
	seen, eng := startLoadDaemon(t, "1.56", answer, false)
	archive := bytes.Repeat([]byte("archive bytes \x00\xff "), 20_000)
	var got []LoadProgress
	res, err := eng.LoadImage(ctxT(t), bytes.NewReader(archive), loadPlatform, func(p LoadProgress) { got = append(got, p) })
	if err != nil {
		t.Fatal(err)
	}
	seen.mu.Lock()
	defer seen.mu.Unlock()
	if seen.method != http.MethodPost || !strings.HasSuffix(seen.path, "/images/load") || seen.contentType != "application/x-tar" {
		t.Fatalf("request %s %s, content type %q", seen.method, seen.path, seen.contentType)
	}
	if seen.bodyErr != nil || !bytes.Equal(seen.body, archive) {
		t.Fatalf("the archive did not arrive unchanged: %d bytes of %d, %v", len(seen.body), len(archive), seen.bodyErr)
	}
	if !strings.Contains(seen.query, "platform=") || !strings.Contains(seen.query, "arm64") || !strings.Contains(seen.query, "quiet=0") {
		t.Fatalf("query %q", seen.query)
	}
	if len(seen.other) != 0 {
		t.Fatalf("other requests: %v", seen.other)
	}
	want := []LoadProgress{
		{Status: "Loading layer", ID: "1a2b3c", Current: 512, Total: 2048},
		{Status: "Loading layer", ID: "1a2b3c", Current: 2048, Total: 2048}, // never past its total
		{Text: "a line that is not JSON"},
		{Text: "Loaded image: host/acme/api:3.28.3"},
		{Text: "Loaded image ID: sha256:abc"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("progress\n got %+v\nwant %+v", got, want)
	}
	if !reflect.DeepEqual(res, LoadResult{Refs: []string{"host/acme/api:3.28.3"}, IDs: []string{"sha256:abc"}}) {
		t.Fatalf("result %+v", res)
	}
}

// TestLoadImagePlatformGuard: a daemon older than the parameter gets no
// platform, and the request is still sent.
func TestLoadImagePlatformGuard(t *testing.T) {
	seen, eng := startLoadDaemon(t, "1.47", `{"stream":"Loaded image: a:1\n"}`, false)
	if _, err := eng.LoadImage(ctxT(t), strings.NewReader("x"), loadPlatform, nil); err != nil {
		t.Fatal(err)
	}
	seen.mu.Lock()
	defer seen.mu.Unlock()
	if seen.method != http.MethodPost || strings.Contains(seen.query, "platform") {
		t.Fatalf("request %s ?%s", seen.method, seen.query)
	}
}

// TestLoadImageStreamError: an error message inside a 200 answer is the
// load's failure, in the daemon's words, and nothing is claimed.
func TestLoadImageStreamError(t *testing.T) {
	for name, answer := range map[string]string{
		"errorDetail": `{"status":"Loading layer","id":"1a"}` + "\n" + `{"errorDetail":{"message":"no space left on device"},"error":"no space left on device"}` + "\n" + `{"stream":"Loaded image: a:1\n"}`,
		"error only":  `{"error":"no space left on device"}`,
	} {
		_, eng := startLoadDaemon(t, "1.56", answer, false)
		res, err := eng.LoadImage(ctxT(t), strings.NewReader("x"), loadPlatform, nil)
		var e *Error
		if !errors.As(err, &e) || e.Msg != "no space left on device" || e.Op != "load image" {
			t.Fatalf("%s: err %#v", name, err)
		}
		if len(res.Refs)+len(res.IDs) != 0 {
			t.Fatalf("%s: a failed load reported %+v", name, res)
		}
	}
}

// TestLoadImageHTTPError: a refusal before the stream is an *Error too.
func TestLoadImageHTTPError(t *testing.T) {
	srv, eng := startFake(t, nil)
	srv.FailNext(http.MethodPost, `^/images/load$`, http.StatusInternalServerError, "invalid tar header", 1)
	_, err := eng.LoadImage(ctxT(t), strings.NewReader("x"), Platform{}, nil)
	var e *Error
	if !errors.As(err, &e) || e.Msg != "invalid tar header" {
		t.Fatalf("err %#v", err)
	}
}

// TestLoadImageBoundsTheStream: a message of unbounded length is refused
// rather than buffered, and long texts are clipped.
func TestLoadImageBoundsTheStream(t *testing.T) {
	_, eng := startLoadDaemon(t, "1.56", `{"stream":"`+strings.Repeat("A", 2<<20)+`"}`, false)
	if _, err := eng.LoadImage(ctxT(t), strings.NewReader("x"), Platform{}, nil); err == nil || !strings.Contains(err.Error(), "larger than 1 MiB") {
		t.Fatalf("err %v", err)
	}
	var many strings.Builder
	for i := 0; i < 1000; i++ {
		many.WriteString(`{"stream":"Loaded image: a:` + strings.Repeat("9", 5) + `\n"}` + "\n")
	}
	many.WriteString(`{"status":"` + strings.Repeat("B", 100_000) + `"}`)
	_, eng = startLoadDaemon(t, "1.56", many.String(), false)
	var last LoadProgress
	res, err := eng.LoadImage(ctxT(t), strings.NewReader("x"), Platform{}, func(p LoadProgress) { last = p })
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Refs) != maxLoadNames || len(last.Status) > maxLoadText+8 {
		t.Fatalf("%d refs kept, last status %d bytes", len(res.Refs), len(last.Status))
	}
}

// TestLoadImageCancel: cancelling the context closes the request — the
// daemon sees its side end — and the result is the context's error.
func TestLoadImageCancel(t *testing.T) {
	// During the answer.
	_, eng := startLoadDaemon(t, "1.56", `{"status":"Loading layer","id":"1a"}`+"\n", true)
	ctx, cancel := context.WithCancel(ctxT(t))
	done := make(chan error, 1)
	go func() {
		_, err := eng.LoadImage(ctx, strings.NewReader("x"), Platform{}, func(LoadProgress) { cancel() })
		done <- err
	}()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("err %v, want context.Canceled", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the load did not end when its context was cancelled")
	}

	// During the upload: the fake reads slowly, the archive never ends.
	srv, feng := startFake(t, func(s *fakeengine.Server) { s.LoadChunkDelay = 20 * time.Millisecond })
	ctx, cancel = context.WithCancel(ctxT(t))
	pr, pw := io.Pipe()
	go func() {
		for {
			if _, err := pw.Write(make([]byte, 4096)); err != nil {
				return
			}
		}
	}()
	go func() {
		_, err := feng.LoadImage(ctx, pr, Platform{}, nil)
		done <- err
	}()
	deadline := time.Now().Add(5 * time.Second)
	for srv.CountRequests(http.MethodPost, `^/images/load$`) == 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("err %v, want context.Canceled", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the upload did not end when its context was cancelled")
	}
	pr.Close()
	for len(srv.LoadRequests()) == 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if reqs := srv.LoadRequests(); len(reqs) != 1 || reqs[0].Err == "" || len(srv.ImageIDs()) != 0 {
		t.Fatalf("after a cancelled upload: %+v, images %v", reqs, srv.ImageIDs())
	}
}

// TestLoadImageIntoTheFake: save from one fake, load into another, through
// the real client on both sides.
func TestLoadImageIntoTheFake(t *testing.T) {
	f := fixture.ChangesReport("acme/report:1.4", true)
	_, src := startFake(t, func(s *fakeengine.Server) {
		s.SetImageFixture(s.AddImage(0, 0, "acme/report:1.4"), f)
	})
	dstSrv, dst := startFake(t, nil)
	ctx := ctxT(t)
	want, err := src.ImageFacts(ctx, "acme/report:1.4")
	if err != nil {
		t.Fatal(err)
	}
	rc, err := src.SaveImage(ctx, "acme/report:1.4", want.Platform())
	if err != nil {
		t.Fatal(err)
	}
	defer rc.Close()
	var statuses int
	res, err := dst.LoadImage(ctx, rc, want.Platform(), func(p LoadProgress) {
		if p.Status == "Loading layer" {
			statuses++
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(res.Refs, []string{"acme/report:1.4"}) || statuses != len(want.DiffIDs) {
		t.Fatalf("result %+v, %d layer messages", res, statuses)
	}
	got, err := dst.ImageFacts(ctx, "acme/report:1.4")
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got.DiffIDs, want.DiffIDs) || got.Platform() != want.Platform() {
		t.Fatalf("target facts %v %s, want %v %s", got.DiffIDs, got.Platform(), want.DiffIDs, want.Platform())
	}
	if got.ID == want.ID {
		t.Fatal("the fake's two stores were expected to name the build differently")
	}
	if reqs := dstSrv.LoadRequests(); len(reqs) != 1 || !strings.Contains(reqs[0].Platform, "amd64") {
		t.Fatalf("load requests %+v", reqs)
	}
}

// TestLoadImageReadOnly: read-only refuses a load without reading the
// archive or sending anything.
func TestLoadImageReadOnly(t *testing.T) {
	srv := fakeengine.New()
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
	r := &countingReader{}
	if _, err := eng.LoadImage(ctxT(t), r, Platform{}, nil); !errors.Is(err, ErrReadOnly) {
		t.Fatalf("err %v", err)
	}
	if r.n != 0 || len(srv.Requests()) != 0 {
		t.Fatalf("read-only read %d bytes and sent %v", r.n, srv.Requests())
	}
}

type countingReader struct{ n int }

func (c *countingReader) Read(p []byte) (int, error) { c.n += len(p); return len(p), nil }
