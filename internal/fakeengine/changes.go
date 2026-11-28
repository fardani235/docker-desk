package fakeengine

import (
	"time"

	"github.com/fardani235/docker-desk/internal/forensics/fixture"
)

// ImageConfig is what inspect reports of an image's config.
type ImageConfig struct {
	Architecture, Variant string
	Cmd, Entrypoint       []string
	User, WorkingDir      string
	Env                   []string
	ExposedPorts          []string // "8000/tcp"
	Healthcheck           []string
}

var defaultImageConfig = ImageConfig{
	Architecture: "amd64",
	Cmd:          []string{"/bin/app"},
	Env:          []string{"PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin"},
	WorkingDir:   "/",
}

func (img *Image) configLocked() ImageConfig {
	if img.config != nil {
		return *img.config
	}
	return defaultImageConfig
}

// SetImageFixture makes the image a reference or ID resolves to be the
// fixture, everywhere the daemon describes it: its archive (OCI layout),
// its config and labels in inspect, its layers' diff IDs and its history.
// sizes, when given, are the layers' sizes as the daemon reports them (the
// fixture's own files are a few bytes); the image's size is their sum.
func (s *Server) SetImageFixture(ref string, f fixture.Image, sizes ...int64) bool {
	built := fixture.OCI(f).Archive
	s.mu.Lock()
	defer s.mu.Unlock()
	img := s.findImageLocked(ref)
	if img == nil {
		return false
	}
	img.archive = built
	arch := f.Architecture
	if arch == "" {
		arch = "amd64"
	}
	img.config = &ImageConfig{
		Architecture: arch, Variant: f.Variant,
		Cmd: f.Cmd, Entrypoint: f.Entrypoint, User: f.User, WorkingDir: f.WorkingDir,
		Env: f.Env, ExposedPorts: f.ExposedPorts, Healthcheck: f.Healthcheck,
	}
	img.Labels = f.Labels
	// Not nil: an image of no layers has none, not the two a size-only
	// image is given (layersLocked).
	img.layers = []Layer{}
	img.Size = 0
	for i, l := range f.Layers {
		size := l.ContentSize()
		if i < len(sizes) {
			size = sizes[i]
		}
		img.layers = append(img.layers, Layer{Name: l.DiffID(), Size: size, CreatedBy: l.CreatedBy, DiffID: l.DiffID()})
		img.Size += size
	}
	// A fixture with a history of its own is reported with it: the
	// entries that made no layer too, each layer-making entry with its
	// layer's size. One that does not bind to the layers is reported as
	// it is, with no size at all.
	img.history = nil
	if len(f.History) > 0 {
		made := 0
		for _, h := range f.History {
			if !h.EmptyLayer {
				made++
			}
		}
		next := 0
		for _, h := range f.History {
			step := HistoryStep{CreatedBy: h.CreatedBy}
			if !h.EmptyLayer && made == len(img.layers) {
				step.Size = img.layers[next].Size
				next++
			}
			img.history = append(img.history, step)
		}
	}
	return true
}

// Untag removes a reference from every image, leaving the image itself.
func (s *Server) Untag(ref string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	norm, ok := normalizeRef(ref)
	if !ok {
		return
	}
	for _, img := range s.images {
		img.RepoTags = removeString(img.RepoTags, norm)
	}
}

// SeedMovedTag builds two variants of one tag: the container is created on
// the first, then the tag moves to the second, as a rebuild or re-pull
// does. It returns the IDs of the image the container runs and of the one
// the tag names now.
func (s *Server) SeedMovedTag(spec ContainerSpec, old, now fixture.Image, oldSizes, newSizes []int64, age time.Duration) (oldID, newID string) {
	oldID = s.AddImage(0, age, spec.Image)
	s.SetImageFixture(oldID, old, oldSizes...)
	s.AddContainer(spec)
	newID = s.MoveTag(spec.Image, 0)
	s.SetImageFixture(newID, now, newSizes...)
	return oldID, newID
}

// LayersApp and LayersBase are the two images SeedLayers adds.
const (
	LayersApp  = "acme/steps:1"
	LayersBase = "acme/steps-base:1"
)

// LayersFixtures are the two images of SeedLayers as fixtures: an
// application and the base it was built on. Their histories hold steps
// that made no layer (ENV, LABEL, CMD) beside those that did, and two of
// the layer-making steps (WORKDIR) made a layer that holds nothing — the
// same content, so the same diff ID, at the base's second depth and at the
// application's third and fifth. The application's RUN carries a sealed
// build argument whose value no view may show.
func LayersFixtures() (app, base fixture.Image) {
	file := func(path, body string) []fixture.File { return []fixture.File{{Path: path, Body: []byte(body)}} }
	baseLayer := fixture.Layer{CreatedBy: "/bin/sh -c #(nop) ADD file:51c0ffee in / ", Files: file("etc/os-release", "ID=steps\n")}
	empty := fixture.Layer{CreatedBy: "WORKDIR /app"}
	base = fixture.Image{
		OS: "linux", Architecture: "amd64", Cmd: []string{"/bin/sh"}, WorkingDir: "/app",
		Layers: []fixture.Layer{baseLayer, empty},
		History: []fixture.History{
			{CreatedBy: baseLayer.CreatedBy},
			{CreatedBy: "/bin/sh -c #(nop)  ENV LANG=C.UTF-8", EmptyLayer: true},
			{CreatedBy: "WORKDIR /app"},
			{CreatedBy: `/bin/sh -c #(nop)  CMD ["/bin/sh"]`, EmptyLayer: true},
		},
	}
	app = fixture.Image{
		OS: "linux", Architecture: "amd64", Cmd: []string{"/app/server"}, WorkingDir: "/srv",
		Layers: []fixture.Layer{
			baseLayer,
			{CreatedBy: "RUN make build", Files: file("usr/local/bin/tool", "#!/bin/sh\nexit 0\n")},
			empty,
			{CreatedBy: "COPY app /app", Files: file("app/server", "#!/bin/sh\nexec true\n")},
			empty,
		},
		History: []fixture.History{
			{CreatedBy: baseLayer.CreatedBy},
			{CreatedBy: "/bin/sh -c #(nop)  ENV LANG=C.UTF-8", EmptyLayer: true},
			{CreatedBy: "RUN |1 BUILD_TOKEN=tok-5ecr3t-value-0123456789 /bin/sh -c make build # buildkit"},
			{CreatedBy: "WORKDIR /app"},
			{CreatedBy: "COPY app /app # buildkit"},
			{CreatedBy: `/bin/sh -c #(nop)  LABEL org.opencontainers.image.title=steps`, EmptyLayer: true},
			{CreatedBy: "WORKDIR /srv"},
			{CreatedBy: `/bin/sh -c #(nop)  CMD ["/app/server"]`, EmptyLayer: true},
		},
	}
	return app, base
}

// SeedLayers adds what Images › Layers is about: an image whose history
// has config-only steps, and a second image holding one of its layers at
// the same depth and another at a different one. The daemon reports the
// two empty layers with size 0, so its history does not bind to the
// layers by counting; the archive's does. Like SeedChanges it is not part
// of SeedDemo: tests and `fake-dockerd -layers` add it.
func (s *Server) SeedLayers() (appID, baseID string) {
	app, base := LayersFixtures()
	day := 24 * time.Hour
	baseID = s.AddImage(0, 20*day, LayersBase)
	s.SetImageFixture(baseID, base, 28_400_000, 0)
	appID = s.AddImage(0, 2*day, LayersApp)
	s.SetImageFixture(appID, app, 28_400_000, 61_200_000, 0, 1_850_000, 0)
	return appID, baseID
}

// SeedChanges adds a container for every case in which the Changes tab has
// nothing to compare, and one whose new image rebuilt from another base.
// It is not part of SeedDemo, so the demo's lists stay as they are; tests
// and `fake-dockerd -changes` add it.
func (s *Server) SeedChanges() {
	day := 24 * time.Hour
	// Pinned to a digest.
	pinnedID := s.AddImage(0, 30*day, "acme/pinned:1")
	s.SetImageFixture(pinnedID, fixture.ChangesBase("acme/pinned:1"))
	s.AddContainer(ContainerSpec{Name: "pinned-svc", Image: "acme/pinned@sha256:" + pinnedID, Cmd: []string{"svc"}, Age: 2 * day})
	// Created from an image ID.
	s.AddContainer(ContainerSpec{Name: "by-id-svc", Image: "sha256:" + pinnedID, Cmd: []string{"svc"}, State: "exited", Age: 2 * day})
	// The tag no longer resolves here.
	goneID := s.AddImage(0, 40*day, "acme/gone:2")
	s.SetImageFixture(goneID, fixture.ChangesBase("acme/gone:2"))
	s.AddContainer(ContainerSpec{Name: "untagged-svc", Image: "acme/gone:2", Cmd: []string{"svc"}, State: "exited", Age: 3 * day})
	s.Untag("acme/gone:2")
	// The other image is for another platform.
	arm := fixture.ChangesReport("acme/edge:3", true)
	arm.Architecture, arm.Variant = "arm64", "v8"
	s.SeedMovedTag(ContainerSpec{Name: "edge-svc", Image: "acme/edge:3", Cmd: []string{"svc"}, State: "exited", Age: 4 * day},
		fixture.ChangesReport("acme/edge:3", false), arm, nil, nil, 10*day)
	// A layer of the new image cannot be read.
	z := fixture.ChangesReport("acme/zstd:4", true)
	z.Layers[len(z.Layers)-1].Zstd = true
	s.SeedMovedTag(ContainerSpec{Name: "zstd-svc", Image: "acme/zstd:4", Cmd: []string{"svc"}, State: "exited", Age: 4 * day},
		fixture.ChangesReport("acme/zstd:4", false), z, nil, nil, 10*day)
	// Rebuilt from another base: the stacks diverge after the first layer.
	s.SeedMovedTag(ContainerSpec{Name: "rebased-svc", Image: "acme/rebased:5", Cmd: []string{"svc"}, State: "exited", Age: 4 * day},
		fixture.ChangesReport("acme/rebased:5", false), fixture.ChangesRebased("acme/rebased:5"), nil, nil, 10*day)
}
