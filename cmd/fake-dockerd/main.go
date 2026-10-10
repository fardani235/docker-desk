// Command fake-dockerd runs the in-memory fake Docker Engine on a unix
// socket. It is the sandbox for trying Docker Desk's destructive actions
// without a real daemon:
//
//	fake-dockerd -socket /tmp/docker-desk-sandbox.sock &
//	DOCKER_HOST=unix:///tmp/docker-desk-sandbox.sock docker-desk
//
// The real docker CLI works against it too, which is how the fake itself is
// validated.
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
	empty := flag.Bool("empty", false, "start with no containers, images, volumes or custom networks")
	flag.Parse()

	srv := fakeengine.New()
	if !*empty {
		srv.SeedDemo()
	}
	if err := srv.Start(*socket); err != nil {
		fmt.Fprintf(os.Stderr, "fake-dockerd: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("fake Docker engine listening on unix://%s\n", *socket)
	fmt.Printf("  DOCKER_HOST=unix://%s docker-desk\n", *socket)
	fmt.Println("Nothing here is real. Ctrl+C to stop.")

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt, syscall.SIGTERM)
	<-sig
	srv.Close()
}
