// Command fake-dockerd runs the in-memory fake Docker Engine on a unix
// socket. It is the sandbox for trying Docker Desk's destructive actions
// without a real daemon:
//
//	fake-dockerd -socket /tmp/docker-desk-sandbox.sock &
//	DOCKER_HOST=unix:///tmp/docker-desk-sandbox.sock docker-desk
//
// The real docker CLI works against it too, which is how the fake itself is
// validated.
//
// With -tcp it listens on a TCP address instead, to try the desk on a daemon
// that is not a local socket:
//
//	fake-dockerd -tcp 127.0.0.1:2375 &
//	docker-desk --host tcp://127.0.0.1:2375
//
// With -registry it also serves a registry, in plain HTTP on a loopback
// address ("127.0.0.1:0" picks a free port), holding a repository worth
// browsing with B, and seeds the daemon with three images of it:
//
//	fake-dockerd -registry 127.0.0.1:0
//
// With -compose-dir it writes the demo's compose files there and points the
// seeded projects' labels at them, so Compose › Declared has something to
// compare (shop) and something it cannot (blog, whose file is absent):
//
//	fake-dockerd -compose-dir /tmp/docker-desk-sandbox-compose
package main

import (
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/fardani235/docker-desk/internal/fakeengine"
	"github.com/fardani235/docker-desk/internal/fakeregistry"
)

func main() {
	socket := flag.String("socket", "/tmp/docker-desk-sandbox.sock", "unix socket to listen on")
	tcp := flag.String("tcp", "", "listen on this TCP address (host:port, without TLS) instead of the unix socket")
	empty := flag.Bool("empty", false, "start with no containers, images, volumes or custom networks")
	changes := flag.Bool("changes", false, "also seed a container for every case the Changes tab has nothing to compare")
	regAddr := flag.String("registry", "", "also serve a registry on this loopback address (127.0.0.1:0 picks a port), with a repository to browse")
	composeDir := flag.String("compose-dir", "", "write the demo's compose files into this directory and point the seeded labels at them")
	flag.Parse()

	srv := fakeengine.New()
	srv.ComposeDir = *composeDir
	if !*empty {
		srv.SeedDemo()
		// Images › Upstream: a moved, a current and a gone tag on a
		// registry that never resolves, so the daemon answers.
		srv.SeedUpstream()
	}
	if *changes {
		srv.SeedChanges()
	}
	var reg *fakeregistry.Registry
	repo := ""
	if *regAddr != "" {
		r, err := fakeregistry.Listen(*regAddr)
		if err != nil {
			fmt.Fprintf(os.Stderr, "fake-dockerd: registry: %v\n", err)
			os.Exit(1)
		}
		reg = r
		repo = srv.SeedRepository(reg)
	}
	var err error
	if *tcp != "" {
		err = srv.StartTCP(*tcp)
	} else {
		err = srv.Start(*socket)
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "fake-dockerd: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("fake Docker engine listening on %s\n", srv.Host())
	fmt.Printf("  docker-desk --host %s\n", srv.Host())
	if *composeDir != "" && !*empty {
		fmt.Printf("compose files in %s (shop has one; blog's is absent on purpose)\n", *composeDir)
	}
	if reg != nil {
		fmt.Printf("fake registry listening on %s (plain HTTP, loopback)\n", reg.Host())
		fmt.Printf("  B in the desk on Images › %s:3.28.2 browses %s\n", repo, repo)
	}
	fmt.Println("Nothing here is real. Ctrl+C to stop.")

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt, syscall.SIGTERM)
	<-sig
	srv.Close()
	if reg != nil {
		reg.Close()
	}
}
