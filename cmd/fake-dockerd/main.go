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
package main

import (
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/fardani235/docker-desk/internal/fakeengine"
)

func main() {
	socket := flag.String("socket", "/tmp/docker-desk-sandbox.sock", "unix socket to listen on")
	tcp := flag.String("tcp", "", "listen on this TCP address (host:port, without TLS) instead of the unix socket")
	empty := flag.Bool("empty", false, "start with no containers, images, volumes or custom networks")
	flag.Parse()

	srv := fakeengine.New()
	if !*empty {
		srv.SeedDemo()
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
	fmt.Println("Nothing here is real. Ctrl+C to stop.")

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt, syscall.SIGTERM)
	<-sig
	srv.Close()
}
