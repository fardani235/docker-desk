package engine

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/fardani235/docker-desk/internal/fakeengine"
)

// tagPosts are the tag requests the fake received, as method, path, query.
func tagPosts(srv *fakeengine.Server) []fakeengine.Request {
	var out []fakeengine.Request
	for _, r := range srv.Mutations() {
		if strings.HasSuffix(r.Path, "/tag") {
			out = append(out, r)
		}
	}
	return out
}

func TestTagImageSendsOneRequestByID(t *testing.T) {
	var id string
	srv, eng := startFake(t, func(s *fakeengine.Server) {
		id = s.AddImage(1000, 0, "acme/api:3.29.0")
		s.AddImage(2000, 0, "other:1")
	})
	ctx := ctxT(t)
	srv.ResetRequests()

	if err := eng.TagImage(ctx, "sha256:"+id, "registry.example:5000/acme/api:stable"); err != nil {
		t.Fatal(err)
	}
	muts := srv.Mutations()
	if len(muts) != 1 {
		t.Fatalf("a tag is exactly one mutating request, got %v", muts)
	}
	r := muts[0]
	if r.Method != http.MethodPost || r.Path != "/images/sha256:"+id+"/tag" {
		t.Fatalf("request = %s, want POST /images/sha256:<id>/tag (the source by ID)", r)
	}
	q, err := url.ParseQuery(r.Query)
	if err != nil {
		t.Fatal(err)
	}
	if q.Get("repo") != "registry.example:5000/acme/api" || q.Get("tag") != "stable" || len(q) != 2 {
		t.Fatalf("query = %q, want exactly repo and tag (no force, nothing else)", r.Query)
	}
	if got := srv.ImageRefs(); !slices.Contains(got, "registry.example:5000/acme/api:stable") || !slices.Contains(got, "acme/api:3.29.0") {
		t.Fatalf("refs after the tag = %v", got)
	}
	d, err := eng.InspectImage(ctx, "registry.example:5000/acme/api:stable")
	if err != nil || d.ID != "sha256:"+id {
		t.Fatalf("the new tag names %q (%v), want the source image", d.ID, err)
	}
	tr := srv.TagRequests()
	if len(tr) != 1 || tr[0].Source != "sha256:"+id || tr[0].ImageID != id || tr[0].MovedFrom != "" {
		t.Fatalf("the fake's tag log = %+v", tr)
	}
	// Nothing was asked of a registry: no pull, push or distribution request.
	for _, r := range srv.Requests() {
		if strings.Contains(r.Path, "/distribution/") || strings.HasSuffix(r.Path, "/push") || r.Path == "/images/create" || r.Path == "/auth" {
			t.Errorf("a tag made a registry request: %s", r)
		}
	}
}

func TestTagImageDefaultsToLatestAndMoves(t *testing.T) {
	var a, b string
	srv, eng := startFake(t, func(s *fakeengine.Server) {
		a = s.AddImage(1000, 0, "acme/api:1")
		b = s.AddImage(2000, 0, "acme/api:latest", "acme/api:2")
	})
	ctx := ctxT(t)
	srv.ResetRequests()

	// No tag means :latest, and the daemon moves a tag another image holds.
	if err := eng.TagImage(ctx, "sha256:"+a, "acme/api"); err != nil {
		t.Fatal(err)
	}
	posts := tagPosts(srv)
	if len(posts) != 1 {
		t.Fatalf("requests = %v", posts)
	}
	if q, _ := url.ParseQuery(posts[0].Query); q.Get("repo") != "docker.io/acme/api" || q.Get("tag") != "latest" {
		t.Fatalf("query = %q, want repo=docker.io/acme/api tag=latest", posts[0].Query)
	}
	d, err := eng.InspectImage(ctx, "acme/api:latest")
	if err != nil || d.ID != "sha256:"+a {
		t.Fatalf("acme/api:latest names %q (%v), want image a", d.ID, err)
	}
	// The image that lost the tag keeps its data and its other reference.
	old, err := eng.InspectImage(ctx, "sha256:"+b)
	if err != nil || !slices.Equal(old.Refs(), []string{"acme/api:2"}) {
		t.Fatalf("image b after the move: %v (%v)", old.Refs(), err)
	}
	if tr := srv.TagRequests(); len(tr) != 1 || tr[0].MovedFrom != b {
		t.Fatalf("the fake's tag log = %+v", tr)
	}
	// Tagging what is already there changes nothing and is not an error.
	if err := eng.TagImage(ctx, "sha256:"+a, "acme/api:latest"); err != nil {
		t.Fatal(err)
	}
	if now, _ := eng.InspectImage(ctx, "sha256:"+a); !slices.Equal(now.Refs(), []string{"acme/api:1", "acme/api:latest"}) {
		t.Fatalf("refs after tagging twice = %v", now.Refs())
	}
}

func TestTagImageMissingSource(t *testing.T) {
	srv, eng := startFake(t, func(s *fakeengine.Server) { s.AddImage(1000, 0, "acme/api:1") })
	ctx := ctxT(t)
	gone := "sha256:" + strings.Repeat("ab", 32)
	err := eng.TagImage(ctx, gone, "acme/api:2")
	if !IsNotFound(err) {
		t.Fatalf("a missing source must be the daemon's 404, got %v", err)
	}
	if !strings.Contains(err.Error(), "No such image") {
		t.Errorf("the daemon's message is lost: %v", err)
	}
	if got := srv.ImageRefs(); !slices.Equal(got, []string{"acme/api:1"}) {
		t.Fatalf("a failed tag changed the store: %v", got)
	}
	if len(srv.TagRequests()) != 0 {
		t.Fatal("the fake recorded a tag it refused")
	}
}

func TestTagImageRefusesBadRequestsBeforeSending(t *testing.T) {
	var id string
	srv, eng := startFake(t, func(s *fakeengine.Server) { id = "sha256:" + s.AddImage(1000, 0, "acme/api:1") })
	ctx := ctxT(t)
	srv.ResetRequests()
	digest := "acme/api@sha256:" + strings.Repeat("0", 64)
	cases := []struct{ name, source, target string }{
		{"empty target", id, ""},
		{"empty source", "", "acme/api:2"},
		{"digest target", id, digest},
		{"tag and digest target", id, "acme/api:2@sha256:" + strings.Repeat("0", 64)},
		{"upper-case repository", id, "Acme/API:2"},
		{"space in target", id, "acme/api :2"},
		{"newline in target", id, "acme/api:2\n"},
		{"bad tag", id, "acme/api:-x"},
		{"path traversal in source", "../containers/x", "acme/api:2"},
		{"query in source", id + "?force=1", "acme/api:2"},
		{"extra path in source", id + "/json", "acme/api:2"},
		{"space in source", "acme/api:1 ", "acme/api:2"},
	}
	for _, c := range cases {
		err := eng.TagImage(ctx, c.source, c.target)
		if err == nil || !strings.Contains(err.Error(), "nothing was sent") {
			t.Errorf("%s: err = %v, want a refusal that says nothing was sent", c.name, err)
		}
		if errors.Is(err, ErrReadOnly) {
			t.Errorf("%s: refused as read-only", c.name)
		}
	}
	if got := srv.Requests(); len(got) != 0 {
		t.Fatalf("refused tags reached the daemon: %v", got)
	}
}

func TestTagImageReadOnly(t *testing.T) {
	srv := fakeengine.New()
	id := "sha256:" + srv.AddImage(1000, 0, "acme/api:1")
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
	srv.ResetRequests()
	if err := eng.TagImage(ctxT(t), id, "acme/api:2"); !errors.Is(err, ErrReadOnly) {
		t.Fatalf("read-only tag: %v", err)
	}
	// The wrapper aside, the request hook refuses the POST itself.
	if err := eng.(*readOnlyEngine).Engine.TagImage(ctxT(t), id, "acme/api:2"); !errors.Is(err, ErrReadOnly) {
		t.Fatalf("read-only tag past the wrapper: %v", err)
	}
	if got := srv.Mutations(); len(got) != 0 {
		t.Fatalf("read-only mode sent %v", got)
	}
	if got := srv.ImageRefs(); !slices.Equal(got, []string{"acme/api:1"}) {
		t.Fatalf("refs = %v", got)
	}
}

// The fake emits the daemon's image tag event: the image's ID, and the new
// reference as its name.
func TestTagImageEvent(t *testing.T) {
	var id string
	srv, eng := startFake(t, func(s *fakeengine.Server) { id = s.AddImage(1000, 0, "acme/api:1") })
	ctx, cancel := context.WithCancel(ctxT(t))
	defer cancel()

	got := make(chan Event, 16)
	go func() { _ = eng.Events(ctx, time.Time{}, func(e Event) { got <- e }) }()
	waitFor(t, "event subscription", func() bool { return srv.EventSubscribers() == 1 })

	// The source by reference works too, and a registry host with a port
	// is part of the name, not a tag.
	if err := eng.TagImage(ctx, "acme/api:1", "localhost:5000/team/api"); err != nil {
		t.Fatal(err)
	}
	select {
	case e := <-got:
		if e.Type != "image" || e.Action != "tag" || e.ID != "sha256:"+id || e.Attributes["name"] != "localhost:5000/team/api:latest" {
			t.Fatalf("event = %+v, want the image tag event naming localhost:5000/team/api:latest", e)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("no tag event")
	}
}
