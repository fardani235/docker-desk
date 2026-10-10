package fakeengine

import "time"

func compose(project, service string) map[string]string {
	return map[string]string{
		"com.docker.compose.project":              project,
		"com.docker.compose.service":              service,
		"com.docker.compose.project.working_dir":  "/home/dev/src/" + project,
		"com.docker.compose.project.config_files": "/home/dev/src/" + project + "/compose.yaml",
		"com.docker.compose.container-number":     "1",
	}
}

// SeedDemo fills the fake with a realistic small machine: two compose
// projects, standalone containers in every state, dangling and unused images,
// unused volumes and networks. It is what `make sandbox` shows.
func (s *Server) SeedDemo() {
	s.Name = "sandbox"

	// -- compose project "shop": running ------------------------------------
	s.AddVolume("shop_pgdata", map[string]string{"com.docker.compose.project": "shop", "com.docker.compose.volume": "pgdata"})
	s.AddVolume("shop_uploads", map[string]string{"com.docker.compose.project": "shop", "com.docker.compose.volume": "uploads"})
	s.AddContainer(ContainerSpec{
		Name: "shop-web-1", Image: "nginx:1.27", Labels: compose("shop", "web"),
		Networks: []string{"shop_default"}, Ports: []PortSpec{{Private: 80, Public: 8080, Proto: "tcp"}},
		Cmd:     []string{"nginx", "-g", "daemon off;"},
		Logs:    []string{"/docker-entrypoint.sh: Configuration complete; ready for start up", "nginx/1.27.0 started"},
		LogRate: 2, Age: 3 * time.Hour,
	})
	s.AddContainer(ContainerSpec{
		Name: "shop-api-1", Image: "shop/api:dev", Labels: compose("shop", "api"),
		Networks: []string{"shop_default"}, Volumes: []string{"shop_uploads"},
		Ports:   []PortSpec{{Private: 3000, Public: 3000, Proto: "tcp"}},
		Cmd:     []string{"node", "server.js"},
		Logs:    []string{"listening on :3000"},
		LogRate: 5, Age: 3 * time.Hour,
	})
	s.AddContainer(ContainerSpec{
		Name: "shop-db-1", Image: "postgres:16", Labels: compose("shop", "db"),
		Networks: []string{"shop_default"}, Volumes: []string{"shop_pgdata"},
		Ports:  []PortSpec{{Private: 5432, Proto: "tcp"}},
		Cmd:    []string{"postgres"},
		Health: "healthy",
		Logs:   []string{"database system is ready to accept connections"},
		Age:    3 * time.Hour, StopDelay: 1500 * time.Millisecond,
	})
	s.AddContainer(ContainerSpec{
		Name: "shop-worker-1", Image: "shop/worker:dev", Labels: compose("shop", "worker"),
		Networks: []string{"shop_default"}, State: "exited", ExitCode: 1,
		Cmd:  []string{"python", "worker.py"},
		Logs: []string{"connecting to queue…", "Traceback (most recent call last):", "ConnectionRefusedError: [Errno 111] Connection refused"},
		Age:  3 * time.Hour,
	})

	// -- compose project "blog": stopped ------------------------------------
	s.AddVolume("blog_db", map[string]string{"com.docker.compose.project": "blog", "com.docker.compose.volume": "db"})
	s.AddContainer(ContainerSpec{
		Name: "blog-app-1", Image: "ghost:5", Labels: compose("blog", "app"),
		Networks: []string{"blog_default"}, State: "exited", Cmd: []string{"node", "current/index.js"},
		Logs: []string{"Ghost is shutting down"}, Age: 30 * time.Hour,
	})
	s.AddContainer(ContainerSpec{
		Name: "blog-db-1", Image: "mysql:8", Labels: compose("blog", "db"),
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
}
