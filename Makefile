BIN      := bin
VERSION  ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS  := -s -w -X main.version=$(VERSION)
SANDBOX_SOCKET ?= /tmp/docker-desk-sandbox.sock

.PHONY: all build install test race vet crosscheck e2e real-readonly-check sandbox clean help

all: build

help: ## list targets
	@grep -E '^[a-z0-9-]+:.*##' $(MAKEFILE_LIST) | sed -E 's/:.*## /\t/' | sort

build: ## build bin/docker-desk and bin/fake-dockerd
	go build -ldflags '$(LDFLAGS)' -o $(BIN)/docker-desk ./cmd/docker-desk
	go build -o $(BIN)/fake-dockerd ./cmd/fake-dockerd

install: ## install docker-desk into GOBIN, or GOPATH/bin (already on PATH if you use Go)
	go install -ldflags '$(LDFLAGS)' ./cmd/docker-desk

test: ## unit, engine, UI and end-to-end tests (fake daemon only; never the real one)
	go test ./...

race: ## the same with the race detector
	go test -race ./...

vet: ## go vet
	go vet ./...

crosscheck: ## drive the fake daemon with the real docker CLI and assert on the results
	./scripts/cli-crosscheck.sh

e2e: build ## scripted pseudo-terminal session against the fake daemon; screens go to docs/screens
	go test ./internal/e2e/ -count=1 -update-screens

real-readonly-check: ## read-only decode check against the REAL daemon (GET/HEAD only, enforced)
	DOCKER_DESK_REAL_HOST=$${DOCKER_HOST:-unix:///var/run/docker.sock} \
		go test ./internal/engine/ -run TestRealDaemonReadOnly -count=1 -v

sandbox: build ## start the fake daemon and open the desk on it — nothing real is touched
	@$(BIN)/fake-dockerd -socket $(SANDBOX_SOCKET) >/dev/null & fake=$$!; \
	for i in 1 2 3 4 5 6 7 8 9 10; do [ -S $(SANDBOX_SOCKET) ] && break; sleep 0.1; done; \
	DOCKER_HOST=unix://$(SANDBOX_SOCKET) $(BIN)/docker-desk; status=$$?; \
	kill $$fake 2>/dev/null; wait $$fake 2>/dev/null; exit $$status

clean: ## remove bin/
	rm -rf $(BIN)
