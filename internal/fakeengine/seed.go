package fakeengine

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/fardani235/docker-desk/internal/forensics/fixture"
)

// composeLabels returns the labels compose puts on a container. The paths
// point into ComposeDir when it is set, and at a machine that is not this
// one otherwise.
func (s *Server) composeLabels(project, service string) map[string]string {
	dir := "/home/dev/src/" + project
	if s.ComposeDir != "" {
		dir = filepath.Join(s.ComposeDir, project)
	}
	return map[string]string{
		"com.docker.compose.project":              project,
		"com.docker.compose.service":              service,
		"com.docker.compose.project.working_dir":  dir,
		"com.docker.compose.project.config_files": filepath.Join(dir, "compose.yaml"),
		"com.docker.compose.container-number":     "1",
		"com.docker.compose.config-hash":          configHash(project, service),
		"com.docker.compose.oneoff":               "False",
	}
}

// configHash stands in for compose's fingerprint of a service's
// configuration: 64 hex digits, stable per service.
func configHash(project, service string) string {
	sum := sha256.Sum256([]byte(project + "/" + service))
	return hex.EncodeToString(sum[:])
}

// SeedDemo fills the fake with a realistic small machine: two compose
// projects, standalone containers in every state, dangling and unused images,
// unused volumes and networks. It is what `make sandbox` shows.
func (s *Server) SeedDemo() {
	s.Name = "sandbox"
	if s.ComposeDir != "" {
		if err := s.writeComposeFiles(); err != nil {
			panic(fmt.Sprintf("fakeengine: writing the demo compose files: %v", err))
		}
	}

	// -- compose project "shop": running ------------------------------------
	s.AddVolume("shop_pgdata", map[string]string{"com.docker.compose.project": "shop", "com.docker.compose.volume": "pgdata"})
	s.AddVolume("shop_uploads", map[string]string{"com.docker.compose.project": "shop", "com.docker.compose.volume": "uploads"})
	s.AddContainer(ContainerSpec{
		Name: "shop-web-1", Image: "nginx:1.27", Labels: s.composeLabels("shop", "web"),
		Networks: []string{"shop_default"}, Ports: []PortSpec{{Private: 80, Public: 8080, Proto: "tcp"}},
		Cmd:     []string{"nginx", "-g", "daemon off;"},
		Logs:    []string{"/docker-entrypoint.sh: Configuration complete; ready for start up", "nginx/1.27.0 started"},
		LogRate: 2, Age: 3 * time.Hour,
	})
	s.AddContainer(ContainerSpec{
		Name: "shop-api-1", Image: "shop/api:dev", Labels: s.composeLabels("shop", "api"),
		Networks: []string{"shop_default"}, Volumes: []string{"shop_uploads"},
		Ports:   []PortSpec{{Private: 3000, Public: 3000, Proto: "tcp"}},
		Cmd:     []string{"node", "server.js"},
		Logs:    []string{"listening on :3000"},
		LogRate: 5, Age: 3 * time.Hour,
	})
	s.AddContainer(ContainerSpec{
		Name: "shop-db-1", Image: "postgres:16", Labels: s.composeLabels("shop", "db"),
		Networks: []string{"shop_default"}, Volumes: []string{"shop_pgdata"},
		Ports:  []PortSpec{{Private: 5432, Proto: "tcp"}},
		Cmd:    []string{"postgres"},
		Health: "healthy",
		Logs:   []string{"database system is ready to accept connections"},
		Age:    3 * time.Hour, StopDelay: 1500 * time.Millisecond,
	})
	s.AddContainer(ContainerSpec{
		Name: "shop-worker-1", Image: "shop/worker:dev", Labels: s.composeLabels("shop", "worker"),
		Networks: []string{"shop_default"}, State: "exited", ExitCode: 1,
		Cmd:  []string{"python", "worker.py"},
		Logs: []string{"connecting to queue…", "Traceback (most recent call last):", "ConnectionRefusedError: [Errno 111] Connection refused"},
		Age:  3 * time.Hour,
	})

	// -- compose project "blog": stopped ------------------------------------
	// Its compose file is deliberately absent, even with ComposeDir set: the
	// Declared tab's "cannot be compared" is shown, not assumed.
	s.AddVolume("blog_db", map[string]string{"com.docker.compose.project": "blog", "com.docker.compose.volume": "db"})
	s.AddContainer(ContainerSpec{
		Name: "blog-app-1", Image: "ghost:5", Labels: s.composeLabels("blog", "app"),
		Networks: []string{"blog_default"}, State: "exited", Cmd: []string{"node", "current/index.js"},
		Logs: []string{"Ghost is shutting down"}, Age: 30 * time.Hour,
	})
	s.AddContainer(ContainerSpec{
		Name: "blog-db-1", Image: "mysql:8", Labels: s.composeLabels("blog", "db"),
		Networks: []string{"blog_default"}, Volumes: []string{"blog_db"}, State: "exited", Cmd: []string{"mysqld"},
		Logs: []string{"mysqld: Shutdown complete"}, Age: 30 * time.Hour,
	})

	// -- standalone containers ----------------------------------------------
	s.AddContainer(ContainerSpec{
		Name: "redis-cache", Image: "redis:7", Networks: []string{"bridge", "staging"},
		Ports: []PortSpec{{Private: 6379, Public: 6379, Proto: "tcp"}}, Cmd: []string{"redis-server"},
		Logs: []string{"Ready to accept connections tcp"}, Age: 26 * time.Hour,
	})
	s.AddContainer(ContainerSpec{
		Name: "noisy-logger", Image: "alpine:3.20", Cmd: []string{"sh", "-c", "yes"},
		LogRate: 20000, Age: 12 * time.Minute,
	})
	s.AddContainer(ContainerSpec{
		Name: "paused-batch", Image: "alpine:3.20", State: "paused", Tty: true, Cmd: []string{"sh"},
		Logs: []string{"batch 41 done", "batch 42 started"}, Age: 50 * time.Minute,
	})
	s.AddContainer(ContainerSpec{
		Name: "old-migration", Image: "shop/api:dev", State: "exited", Cmd: []string{"npm", "run", "migrate"},
		Privileged:       true,
		AnonymousVolumes: 1,
		Logs:             []string{"running 12 migrations", "done"}, Age: 72 * time.Hour,
	})
	s.AddContainer(ContainerSpec{
		Name: "distroless-svc", Image: "gcr.io/distroless/static:nonroot", NoShell: true, Cmd: []string{"/svc"},
		Logs: []string{"svc up"}, LogRate: 1, Age: 5 * time.Hour,
	})
	s.AddContainer(ContainerSpec{
		Name: "slow-stopper", Image: "busybox:1.36", StopDelay: 4 * time.Second, Cmd: []string{"sleep", "infinity"},
		Age: 40 * time.Minute,
	})

	// -- things nothing uses --------------------------------------------------
	s.AddImage(410_000_000, 9*24*time.Hour)    // dangling
	s.AddImage(1_250_000_000, 21*24*time.Hour) // dangling
	s.AddImage(131_000_000, 14*24*time.Hour, "python:3.12-slim")
	s.AddImage(154_000_000, 40*24*time.Hour, "node:22-alpine", "node:lts-alpine")
	s.AddVolume("old-backups", nil)
	s.AddAnonymousVolume("leftover-1")
	s.AddAnonymousVolume("leftover-2")
	s.AddNetwork("orphan-net", nil)

	s.seedDisk()

	// acme/report:1.4 has two builds: report-job was created on the first,
	// and the tag has since moved to the second, which keeps the base,
	// adds two layers and changes the config (Containers › Changes).
	s.SeedMovedTag(ContainerSpec{
		Name: "report-job", Image: "acme/report:1.4", State: "exited", Cmd: []string{"python3", "-m", "report"},
		Logs: []string{"report for 2026-11-03 written"}, Age: 20 * time.Hour,
	}, fixture.ChangesReport("acme/report:1.4", false), fixture.ChangesReport("acme/report:1.4", true),
		[]int64{74_800_000, 31_400_000, 1_250_000}, []int64{74_800_000, 31_400_000, 1_250_000, 6_900_000, 2_100}, 12*24*time.Hour)

	// shop/api:dev is the image with something to find: a credential
	// deleted one layer after it was added, a token in a build argument,
	// package lists cleaned up too late, setuid and capability bits, and
	// binaries with and without hardening (Images › Findings).
	s.SetImageArchive("shop/api:dev", fixture.OCI(fixture.Demo()).Archive)
	// python:3.12-slim carries an SBOM made from another build: its
	// package view (Findings, then i) has every kind of disagreement.
	s.SetImageArchive("python:3.12-slim", fixture.OCI(fixture.DemoPackages("python:3.12-slim")).Archive)
	// nginx:1.27 is the image the Files view has most to draw in — a
	// replaced file, a whiteout, an opaque directory, links, devices and a
	// hostile name — in the classic layout; shop-web-1, made from it, has
	// written one of every change `docker diff` can list (F, then Enter).
	s.SetImageArchive("nginx:1.27", fixture.Legacy(fixture.DemoFiles("nginx:1.27")).Archive)
	s.SetChanges("shop-web-1", DemoChanges()...)
}

// DemoChanges is what shop-web-1 has written since its image: a file
// changed (nginx.conf), a file added (state.db) in a directory the image
// has, a file deleted (/tmp/seed) and a directory added
// (/var/cache/nginx/client_temp, with an entry the desk does not read), in
// the order the daemon lists them, parents with their own C.
func DemoChanges() []Change {
	return []Change{
		Changed("/etc"), Changed("/etc/nginx"), Changed("/etc/nginx/nginx.conf"),
		Changed("/run"), Added("/run/nginx.pid"),
		Changed("/tmp"), Deleted("/tmp/seed"),
		Changed("/var"), Changed("/var/cache"), Changed("/var/cache/nginx"),
		Added("/var/cache/nginx/client_temp"), Added("/var/cache/nginx/client_temp/00000001"),
		Changed("/var/lib"), Changed("/var/lib/nginx"), Added("/var/lib/nginx/state.db"),
	}
}

// seedDisk gives the demo's images the layers real ones have — most of them
// are built on one of two bases, and the application images on the language
// images — and its volumes, containers and build cache the sizes the Space
// view accounts for. Images seeded above with a size keep it.
func (s *Server) seedDisk() {
	L := func(name string, size int64, by string) Layer { return Layer{Name: name, Size: size, CreatedBy: by} }
	debian := L("debian:bookworm-slim", 74_800_000, "/bin/sh -c #(nop) ADD file:6e5c1b0e31a7 in / ")
	alpine := L("alpine:3.20", 7_800_000, "/bin/sh -c #(nop) ADD file:a71f6a2cd5d0 in / ")
	python := []Layer{debian,
		L("python/deps", 9_300_000, "RUN apt-get update && apt-get install -y ca-certificates netbase tzdata # buildkit"),
		L("python/3.12", 41_700_000, "RUN set -eux; ./configure --enable-optimizations; make install # buildkit"),
		L("python/pip", 5_200_000, "RUN pip install --upgrade pip setuptools wheel # buildkit"),
	}
	node := []Layer{alpine,
		L("node/22", 140_900_000, "RUN addgroup -g 1000 node && install node v22.11.0 # buildkit"),
		L("node/yarn", 5_300_000, "RUN apk add --no-cache yarn # buildkit"),
	}
	nodeDebian := []Layer{debian, L("node/20-bookworm", 128_400_000, "RUN install node v20.18.0 # buildkit")}
	with := func(base []Layer, more ...Layer) []Layer { return append(append([]Layer{}, base...), more...) }

	s.SetImageLayers("python:3.12-slim", python...)
	s.SetImageLayers("shop/worker:dev", with(python,
		L("worker/req", 1_200, "COPY requirements.txt . # buildkit"),
		L("worker/pip", 86_400_000, "RUN pip install -r requirements.txt # buildkit"),
		L("worker/app", 2_300_000, "COPY . /app # buildkit"))...)
	s.SetImageLayers("node:22-alpine", node...)
	s.SetImageLayers("shop/api:dev", with(node,
		L("api/pkg", 410_000, "COPY package*.json ./ # buildkit"),
		L("api/npm", 61_300_000, "RUN npm ci --omit=dev # buildkit"),
		L("api/app", 1_750_000, "COPY . . # buildkit"))...)
	s.SetImageLayers("alpine:3.20", alpine)
	s.SetImageLayers("nginx:1.27", debian,
		L("nginx/pkg", 112_900_000, "RUN apt-get install -y nginx=1.27.2 # buildkit"),
		L("nginx/entry", 4_620, "COPY docker-entrypoint.sh / # buildkit"))
	s.SetImageLayers("postgres:16", debian,
		L("postgres/pkg", 349_100_000, "RUN apt-get install -y postgresql-16 # buildkit"),
		L("postgres/entry", 12_300, "COPY docker-entrypoint.sh /usr/local/bin/ # buildkit"))
	s.SetImageLayers("redis:7", debian,
		L("redis/pkg", 41_200_000, "RUN apt-get install -y redis-server # buildkit"),
		L("redis/entry", 3_900, "COPY docker-entrypoint.sh /usr/local/bin/ # buildkit"))
	s.SetImageLayers("ghost:5", with(nodeDebian, L("ghost/5.101", 291_000_000, "RUN npm install -g ghost-cli && ghost install 5.101.3 # buildkit"))...)
	s.SetImageLayers("mysql:8",
		L("oraclelinux:9-slim", 118_000_000, "/bin/sh -c #(nop) ADD file:0f3a9e in / "),
		L("mysql/pkg", 466_000_000, "RUN microdnf install -y mysql-community-server-8.4.3 # buildkit"),
		L("mysql/entry", 7_900, "COPY docker-entrypoint.sh /usr/local/bin/ # buildkit"))
	s.SetImageLayers("busybox:1.36", L("busybox:1.36", 4_260_000, "/bin/sh -c #(nop) ADD file:b4c1 in / "))
	s.SetImageLayers("gcr.io/distroless/static:nonroot",
		L("distroless/static", 1_990_000, "bazel build //base:static"),
		L("distroless/nonroot", 1_200, "bazel build //base:nonroot"))

	// The two dangling images are earlier builds: of the worker, on the
	// same Python, and of the blog, on the same Node.
	s.mu.Lock()
	var dangling []*Image
	for _, img := range s.sortedImagesLocked() {
		if len(img.RepoTags) == 0 {
			dangling = append(dangling, img)
		}
	}
	s.mu.Unlock()
	for _, img := range dangling {
		switch img.Size {
		case 410_000_000:
			s.SetImageLayers(img.ID, with(python,
				L("worker/req@old", 1_100, "COPY requirements.txt . # buildkit"),
				L("worker/pip@old", 276_800_000, "RUN pip install -r requirements.txt # buildkit"),
				L("worker/app@old", 2_198_900, "COPY . /app # buildkit"))...)
		case 1_250_000_000:
			s.SetImageLayers(img.ID, with(nodeDebian,
				L("ghost/5.87", 1_046_800_000, "RUN npm install -g ghost-cli && ghost install 5.87.0 # buildkit"))...)
		}
	}

	for name, size := range map[string]int64{
		"shop_pgdata": 48_200_000, "shop_uploads": 312_000_000, "blog_db": 211_000_000,
		"old-backups": 1_820_000_000, stableID("anonvol", "leftover-1"): 52_000_000,
	} {
		s.SetVolumeSize(name, size)
	}
	for name, size := range map[string]int64{
		"shop-api-1": 18_400_000, "shop-db-1": 63_000, "shop-worker-1": 6_900_000,
		"old-migration": 2_100_000, "blog-db-1": 4_700_000,
	} {
		s.SetWritableSize(name, size)
	}
	day := 24 * time.Hour
	s.AddBuildCache(
		BuildRecord{ID: "kq3x9w2m7v4n8p1r5t6y0z2a", Type: "exec.cachemount", Description: "mount / from exec /bin/sh -c npm ci --omit=dev", Size: 233_000_000, LastUsed: time.Now().Add(-2 * day)},
		BuildRecord{ID: "u8c2d7e1f5g9h3j6k0l4m8n2", Type: "regular", Description: "[api 4/5] RUN npm ci --omit=dev", Size: 61_300_000, Shared: true, LastUsed: time.Now().Add(-2 * day)},
		BuildRecord{ID: "p0q4r8s2t6u0v4w8x2y6z0a4", Type: "source.local", Description: "local source for context", Size: 1_900_000, LastUsed: time.Now().Add(-2 * day)},
		BuildRecord{ID: "b5c9d3e7f1g5h9j3k7l1m5n9", Type: "regular", Description: "[worker 3/4] RUN pip install -r requirements.txt", Size: 276_800_000, LastUsed: time.Now().Add(-9 * day)},
	)
}

// SandboxRegistry is the registry the demo's Upstream tags live on. It is
// under .invalid, which never resolves: the desk's own request fails at once,
// without touching the network, and the fake daemon answers in its place —
// the fallback, with no network at all.
const SandboxRegistry = "registry.sandbox.invalid"

// SeedUpstream gives Images › Upstream one tag that moved, one that is
// current and one that is gone. The sandbox (fake-dockerd) calls it after
// SeedDemo; it is separate so the demo's own figures, which tests pin, stay
// as they are.
func (s *Server) SeedUpstream() {
	moved := SandboxRegistry + "/acme/web:2.3"
	s.AddImage(88_000_000, 6*24*time.Hour, moved)
	s.SetUpstream(moved, Upstream{Digest: "sha256:" + stableID("upstream", "acme/web:2.3 rebuilt"),
		Platforms: []UpstreamPlatform{{OS: "linux", Architecture: "amd64"}, {OS: "linux", Architecture: "arm64", Variant: "v8"}}})
	current := SandboxRegistry + "/acme/cron:1"
	s.AddImage(23_000_000, 3*24*time.Hour, current)
	s.SetUpstream(current, Upstream{})
	gone := SandboxRegistry + "/acme/legacy:0.9"
	s.AddImage(61_000_000, 90*24*time.Hour, gone)
	s.SetUpstream(gone, Upstream{Gone: true})
}

// ShopCompose is the demo's shop/compose.yaml. Against the seeded containers
// it has one of every row the Declared tab can show: a moved tag (web), a
// template image (api), a match (db), a service with no container (cache), a
// profiled service that is not missing (debug), and a running container the
// file does not declare (worker).
const ShopCompose = `# The sandbox's compose file for the "shop" project.
name: shop

x-defaults: &defaults
  restart: unless-stopped
  logging: { driver: json-file, options: { max-size: "10m" } }

services:
  web:
    <<: *defaults
    image: nginx:1.26
    ports: ["8080:80"]
  api:
    <<: *defaults
    image: shop/api:${API_TAG:-dev}
    environment:
      DATABASE_URL: postgres://shop@db/shop
  db:
    <<: *defaults
    image: postgres:16
    volumes: ["pgdata:/var/lib/postgresql/data"]
  cache:
    image: redis:7-alpine
  debug:
    image: busybox:1.36
    profiles: [debug]
    command: ["sleep", "infinity"]

volumes:
  pgdata: {}
  uploads: {}
`

// ShopComposeWritten is how long before seeding the shop file was written:
// after its containers (three hours old) were created.
const ShopComposeWritten = 20 * time.Minute

// writeComposeFiles writes the demo's compose files into ComposeDir. Only
// shop gets one; blog's is left absent (a file already there from an
// earlier run is renamed aside, never deleted).
func (s *Server) writeComposeFiles() error {
	dir := filepath.Join(s.ComposeDir, "shop")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	p := filepath.Join(dir, "compose.yaml")
	if err := os.WriteFile(p, []byte(ShopCompose), 0o644); err != nil {
		return err
	}
	when := time.Now().Add(-ShopComposeWritten)
	if err := os.Chtimes(p, when, when); err != nil {
		return err
	}
	blog := filepath.Join(s.ComposeDir, "blog", "compose.yaml")
	if _, err := os.Lstat(blog); err == nil {
		return os.Rename(blog, blog+".aside")
	}
	return nil
}
