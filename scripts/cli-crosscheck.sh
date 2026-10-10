#!/usr/bin/env bash
# Cross-check the fake Docker engine with the REAL docker CLI.
#
# The desk's tests run against internal/fakeengine. A fake written next to the
# code it tests can agree with that code and still be wrong, so this script
# drives the fake with Docker's own client and asserts on what comes back.
#
# Safety: every docker invocation goes through d(), which passes an explicit
# -H pointing at a socket this script created in a private temp directory, and
# the script aborts before any mutation unless the server on that socket
# identifies itself as the fake. The real daemon is never contacted.
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
DIR="$(mktemp -d /tmp/docker-desk-crosscheck.XXXXXX)"
SOCK="$DIR/fake.sock"
HOST="unix://$SOCK"
FAKE_PID=""

cleanup() {
  [ -n "$FAKE_PID" ] && kill "$FAKE_PID" 2>/dev/null || true
  rm -rf "$DIR"
}
trap cleanup EXIT

# Never inherit a daemon selection from the caller's environment.
unset DOCKER_HOST DOCKER_CONTEXT

d() { docker -H "$HOST" "$@"; }

pass=0
fail=0
ok()   { pass=$((pass+1)); printf '  ok    %s\n' "$1"; }
bad()  { fail=$((fail+1)); printf '  FAIL  %s\n        %s\n' "$1" "${2:-}"; }
expect_eq()       { if [ "$2" = "$3" ]; then ok "$1"; else bad "$1" "want [$3] got [$2]"; fi; }
expect_contains() { if printf '%s' "$2" | grep -qF -- "$3"; then ok "$1"; else bad "$1" "want substring [$3] in [$2]"; fi; }
expect_fails()    { local name="$1" want="$2"; shift 2; local out; if out="$("$@" 2>&1)"; then bad "$name" "command succeeded: $out"; else expect_contains "$name" "$out" "$want"; fi; }

(cd "$ROOT" && go build -o "$DIR/fake-dockerd" ./cmd/fake-dockerd)
"$DIR/fake-dockerd" -socket "$SOCK" >"$DIR/fake.log" 2>&1 &
FAKE_PID=$!
for _ in $(seq 1 50); do [ -S "$SOCK" ] && break; sleep 0.1; done

echo "== identity"
ver="$(d version --format '{{.Server.Version}}')"
case "$ver" in
  *-fake) ok "server identifies as the fake ($ver)" ;;
  *) echo "ABORT: $HOST answered as '$ver', not the fake engine. Nothing was changed."; exit 2 ;;
esac
expect_eq "info name" "$(d info --format '{{.Name}}')" "sandbox"
expect_eq "api version negotiated" "$(d version --format '{{.Server.APIVersion}}')" "1.56"

echo "== listing"
expect_contains "ps shows running" "$(d ps --format '{{.Names}}')" "shop-web-1"
if d ps --format '{{.Names}}' | grep -q shop-worker-1; then bad "ps hides exited"; else ok "ps hides exited"; fi
expect_contains "ps -a shows exited" "$(d ps -a --format '{{.Names}} {{.Status}}')" "shop-worker-1 Exited (1)"
expect_contains "ps label filter" "$(d ps -a --filter label=com.docker.compose.project=blog --format '{{.Names}}' | sort | tr '\n' ' ')" "blog-app-1 blog-db-1"
expect_contains "ports rendered" "$(d ps --format '{{.Names}} {{.Ports}}')" "0.0.0.0:8080->80/tcp"
expect_contains "images" "$(d images --format '{{.Repository}}:{{.Tag}}')" "nginx:1.27"
expect_eq "dangling images" "$(d images -f dangling=true -q | wc -l | tr -d ' ')" "2"
expect_contains "volume ls" "$(d volume ls --format '{{.Name}}')" "shop_pgdata"
expect_contains "dangling volumes" "$(d volume ls -f dangling=true --format '{{.Name}}')" "old-backups"
expect_contains "network ls" "$(d network ls --format '{{.Name}} {{.Driver}}')" "shop_default bridge"

echo "== inspect"
expect_eq "inspect state" "$(d inspect --format '{{.State.Status}}' shop-db-1)" "running"
expect_eq "inspect health" "$(d inspect --format '{{.State.Health.Status}}' shop-db-1)" "healthy"
expect_contains "inspect network ip" "$(d inspect --format '{{range .NetworkSettings.Networks}}{{.IPAddress}}{{end}}' shop-web-1)" "172."
expect_eq "inspect mount" "$(d inspect --format '{{range .Mounts}}{{.Name}}{{end}}' shop-db-1)" "shop_pgdata"
expect_eq "network inspect containers" "$(d network inspect --format '{{len .Containers}}' shop_default)" "3"
expect_eq "volume inspect" "$(d volume inspect --format '{{.Driver}}' shop_pgdata)" "local"
expect_contains "image inspect" "$(d image inspect --format '{{.Os}}/{{.Architecture}}' nginx:1.27)" "linux/amd64"
expect_fails "inspect missing" "no such object" d inspect definitely-not-here

echo "== lifecycle"
d stop redis-cache >/dev/null
expect_eq "stop -> exited" "$(d inspect --format '{{.State.Status}}' redis-cache)" "exited"
d start redis-cache >/dev/null
expect_eq "start -> running" "$(d inspect --format '{{.State.Status}}' redis-cache)" "running"
d pause redis-cache >/dev/null
expect_eq "pause -> paused" "$(d inspect --format '{{.State.Status}}' redis-cache)" "paused"
expect_fails "pause twice refused" "already paused" d pause redis-cache
d unpause redis-cache >/dev/null
expect_eq "unpause -> running" "$(d inspect --format '{{.State.Status}}' redis-cache)" "running"
d restart redis-cache >/dev/null
expect_eq "restart -> running" "$(d inspect --format '{{.State.Status}}' redis-cache)" "running"
d kill redis-cache >/dev/null
expect_eq "kill -> exit 137" "$(d inspect --format '{{.State.Status}} {{.State.ExitCode}}' redis-cache)" "exited 137"
expect_fails "kill stopped refused" "is not running" d kill redis-cache
expect_fails "rm running refused" "container is running" d rm shop-web-1
expect_eq "refused rm left it alone" "$(d inspect --format '{{.State.Status}}' shop-web-1)" "running"
d rm redis-cache >/dev/null
expect_fails "rm removed it" "no such object" d inspect redis-cache

echo "== events"
since="$(date +%s)"
sleep 1
d stop shop-web-1 >/dev/null
ev="$(d events --since "$since" --until "$(($(date +%s)+1))" --format '{{.Type}} {{.Action}} {{.Actor.Attributes.name}}')"
expect_contains "event kill" "$ev" "container kill shop-web-1"
expect_contains "event die" "$ev" "container die shop-web-1"
expect_contains "event stop" "$ev" "container stop shop-web-1"
expect_contains "event network disconnect" "$ev" "network disconnect shop_default"
d start shop-web-1 >/dev/null

echo "== logs and stats"
expect_contains "logs" "$(d logs shop-worker-1 2>&1)" "ConnectionRefusedError"
expect_eq "logs --tail" "$(d logs --tail 1 shop-worker-1 2>&1 | wc -l | tr -d ' ')" "1"
expect_contains "tty logs" "$(d logs paused-batch 2>&1)" "batch 42 started"
follow="$(timeout 2 docker -H "$HOST" logs -f --tail 0 shop-api-1 2>&1 || true)"
if [ "$(printf '%s\n' "$follow" | grep -c 'line ')" -ge 3 ]; then ok "logs -f streams new lines"; else bad "logs -f streams new lines" "$follow"; fi
stats="$(d stats --no-stream --format '{{.Name}} {{.CPUPerc}} {{.MemUsage}}' shop-api-1)"
expect_contains "stats name" "$stats" "shop-api-1"
if printf '%s' "$stats" | grep -Eq '[0-9]+\.[0-9]+% [0-9.]+[KMG]iB / 16GiB'; then ok "stats figures decode ($stats)"; else bad "stats figures decode" "$stats"; fi

echo "== exec"
expect_eq "exec command" "$(d exec shop-api-1 echo hello-from-fake)" "hello-from-fake"
if d exec shop-api-1 false; then bad "exec exit code"; else ok "exec exit code propagates"; fi
expect_fails "exec in stopped" "is not running" d exec shop-worker-1 echo hi
expect_fails "exec missing binary" "executable file not found" d exec shop-api-1 nonexistent-binary
expect_fails "exec without shell" "executable file not found" d exec distroless-svc sh
tty_out="$(printf 'echo typed-on-tty\nexit\n' | script -qec "docker -H $HOST exec -it shop-api-1 sh" /dev/null 2>&1 || true)"
expect_contains "exec -it interactive shell" "$tty_out" "typed-on-tty"

echo "== images"
pull="$(d pull hello-world 2>&1)"
expect_contains "pull defaults to :latest" "$pull" "hello-world:latest"
expect_contains "pull lists it" "$(d images --format '{{.Repository}}:{{.Tag}}')" "hello-world:latest"
expect_fails "pull missing" "pull access denied" d pull nosuch/image:1
expect_fails "rmi in use refused" "is using its referenced image" d rmi nginx:1.27
expect_contains "rmi one of two tags only untags" "$(d rmi node:lts-alpine)" "Untagged: node:lts-alpine"
expect_contains "other tag survives" "$(d images --format '{{.Repository}}:{{.Tag}}')" "node:22-alpine"
rm_out="$(d rmi hello-world)"
expect_contains "rmi deletes last tag" "$rm_out" "Deleted: sha256:"

echo "== volumes and networks"
d volume create cross-vol >/dev/null
expect_contains "volume create" "$(d volume ls --format '{{.Name}}')" "cross-vol"
d volume rm cross-vol >/dev/null
expect_fails "volume rm in use refused" "volume is in use" d volume rm shop_pgdata
d network create cross-net >/dev/null
expect_fails "network create duplicate refused" "already exists" d network create cross-net
d network connect cross-net shop-api-1
expect_contains "network connect" "$(d inspect --format '{{range $k, $v := .NetworkSettings.Networks}}{{$k}} {{end}}' shop-api-1)" "cross-net"
expect_fails "network rm with endpoints refused" "active endpoints" d network rm cross-net
d network disconnect cross-net shop-api-1
d network rm cross-net >/dev/null
expect_fails "predefined network rm refused" "pre-defined network" d network rm bridge

echo
echo "cross-check: $pass passed, $fail failed"
[ "$fail" -eq 0 ]
