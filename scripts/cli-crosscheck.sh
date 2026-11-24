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
FAKE2_PID=""

cleanup() {
  [ -n "$FAKE_PID" ] && kill "$FAKE_PID" 2>/dev/null || true
  [ -n "$FAKE2_PID" ] && kill "$FAKE2_PID" 2>/dev/null || true
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
expect_eq "dangling images" "$(d images -f dangling=true -q | wc -l | tr -d ' ')" "3"
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

echo "== disk usage (the Space view's sources)"
df_out="$(d system df --format '{{.Type}} {{.TotalCount}} {{.Active}} {{.Size}} {{.Reclaimable}}')"
expect_contains "system df images" "$df_out" "Images 19 11 3.562GB 1.505GB (42%)"
expect_contains "system df containers" "$df_out" "Containers 13 8 120.5MB 37.51MB (31%)"
expect_contains "system df volumes" "$df_out" "Local Volumes 7 4 2.443GB 1.872GB (76%)"
expect_contains "system df build cache" "$df_out" "Build Cache 4 0 511.7MB 511.7MB"
expect_contains "system df -v shared/unique" "$(d system df -v | grep 'shop/worker' | tr -s ' ')" "220MB 131MB 88.7MB 1"
expect_contains "system df -v no unique" "$(d system df -v | grep '^python' | tr -s ' ')" "131MB 131MB 0B 0"
expect_contains "ps -s writable layer" "$(d ps -a -s --format '{{.Names}} {{.Size}}')" "shop-api-1 18.4MB (virtual 236MB)"
expect_eq "history: CMD made no layer" "$(d history --format '{{.Size}}' shop/worker:dev | head -1)" "0B"
expect_eq "history: layer sizes" "$(d history --format '{{.Size}}' shop/worker:dev | tail -1)" "74.8MB"

echo "== diff (the Files view's container side)"
# The fake's change list for shop-web-1 (fakeengine.DemoChanges), as the
# real CLI prints it: one line per path, the kind's letter first.
want_diff="C /etc
C /etc/nginx
C /etc/nginx/nginx.conf
C /run
A /run/nginx.pid
C /tmp
D /tmp/seed
C /var
C /var/cache
C /var/cache/nginx
A /var/cache/nginx/client_temp
A /var/cache/nginx/client_temp/00000001
C /var/lib
C /var/lib/nginx
A /var/lib/nginx/state.db"
expect_eq "diff shop-web-1: every kind, in the daemon's order" "$(d diff shop-web-1)" "$want_diff"
expect_eq "diff of a container that wrote nothing" "$(d diff redis-cache)" ""
expect_fails "diff of an unknown container" "No such container" d diff ghost

echo "== cp (the Files view's copy out and copy in)"
# The real CLI against the fake's archive endpoints: GET for out, HEAD and
# PUT for in. The desk names exact destinations (W3); the CLI nests into an
# existing directory, so every destination here is a path that does not
# exist yet, which is where the two agree.
CP="$DIR/cp"; mkdir -p "$CP"
d cp shop-web-1:/etc/nginx/nginx.conf "$CP/nginx.conf" 2>/dev/null
expect_contains "cp out: a file" "$(cat "$CP/nginx.conf")" "worker_processes 4;"
d cp shop-web-1:/etc/nginx "$CP/nginx-out" 2>/dev/null
expect_eq "cp out: a directory, to exactly the path named" "$(cd "$CP/nginx-out" && find . | sort | tr '\n' ' ')" ". ./conf.d ./conf.d/default.conf ./current.conf ./nginx.conf "
expect_eq "cp out: a symlink inside is a link" "$(readlink "$CP/nginx-out/current.conf")" "nginx.conf"
d cp shop-web-1:/bin/sh "$CP/sh-link" 2>/dev/null
expect_eq "cp out: a symlink is copied as the link" "$(readlink "$CP/sh-link")" "/usr/bin/bash"
expect_eq "cp out: the mode is kept" "$(stat -c %a "$CP/nginx-out/nginx.conf")" "644"
expect_fails "cp out: a missing path" "Could not find the file /nope in container shop-web-1" d cp shop-web-1:/nope "$CP/nope"
printf 'listen 8081;\n' > "$CP/app.conf"; chmod 600 "$CP/app.conf"
d cp "$CP/app.conf" shop-web-1:/etc/nginx/app.conf 2>/dev/null
expect_eq "cp in: a file, at the path named" "$(d cp shop-web-1:/etc/nginx/app.conf - 2>/dev/null | tar -xO)" "listen 8081;"
mkdir -p "$CP/assets/js"; printf 'x' > "$CP/assets/js/app.js"; ln -s js/app.js "$CP/assets/main.js"
d cp "$CP/assets" shop-web-1:/srv/assets 2>/dev/null
expect_eq "cp in: a directory, with a link as a link" "$(d cp shop-web-1:/srv/assets - 2>/dev/null | tar -t | sort | tr '\n' ' ')" "assets/ assets/js/ assets/js/app.js assets/main.js "
expect_fails "cp in: a missing parent" "Could not find the file /srv/none" d cp "$CP/app.conf" shop-web-1:/srv/none/app.conf
mkdir -p "$CP/f"; printf 'x' > "$CP/f/nginx"
expect_fails "cp in: a file over a directory is refused" "cannot overwrite directory" d cp "$CP/f/nginx" shop-web-1:/etc/

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

echo "== create (the desk's create form sends the same request)"
made="$(d create --name cross-made -p 127.0.0.1:18080:80 -p 53:53/udp -e LOG_LEVEL=info -e EMPTY= \
  --mount type=volume,src=cross-cache,dst=/var/cache --mount type=bind,src=/srv/data,dst=/data,readonly \
  -v cross-legacy:/legacy:ro --restart on-failure:3 nginx:1.27 sh -c 'sleep 1000')"
expect_eq "create returns the full ID" "${#made}" "64"
expect_eq "create leaves it created" "$(d inspect --format '{{.State.Status}}' cross-made)" "created"
expect_eq "create: command" "$(d inspect --format '{{json .Config.Cmd}}' cross-made)" '["sh","-c","sleep 1000"]'
expect_contains "create: environment" "$(d inspect --format '{{json .Config.Env}}' cross-made)" '"LOG_LEVEL=info","EMPTY="'
expect_eq "create: restart policy" "$(d inspect --format '{{.HostConfig.RestartPolicy.Name}}:{{.HostConfig.RestartPolicy.MaximumRetryCount}}' cross-made)" "on-failure:3"
expect_contains "create: port binding" "$(d inspect --format '{{json .HostConfig.PortBindings}}' cross-made)" '"80/tcp":[{"HostIp":"127.0.0.1","HostPort":"18080"}]'
expect_contains "create: udp port" "$(d inspect --format '{{json .HostConfig.PortBindings}}' cross-made)" '"53/udp"'
mounts="$(d inspect --format '{{range .Mounts}}{{.Type}}:{{.Source}}:{{.Destination}}:{{.RW}}:{{.Mode}} {{end}}' cross-made)"
expect_contains "create: named volume mount" "$mounts" "volume:/var/lib/fake-docker/volumes/cross-cache/_data:/var/cache:true:z"
expect_contains "create: read-only bind mount" "$mounts" "bind:/srv/data:/data:false:"
expect_contains "create: -v volume, read-only" "$mounts" "/legacy:false:ro"
expect_contains "create made the named volume" "$(d volume ls --format '{{.Name}}')" "cross-cache"
expect_fails "create: name taken" "is already in use" d create --name cross-made nginx:1.27
expect_fails "create: relative mount target" "absolute" d create --mount type=volume,src=x1,dst=rel nginx:1.27
d start cross-made >/dev/null
expect_eq "start runs the created container" "$(d inspect --format '{{.State.Status}}' cross-made)" "running"
expect_contains "ps shows the published port" "$(d ps --format '{{.Names}} {{.Ports}}' --filter name=cross-made)" "127.0.0.1:18080->80/tcp"
auto="$(d create nginx:1.27)"
expect_contains "create without a name: the daemon names it" "$(d inspect --format '{{.Name}}' "$auto")" "_"
pulled="$(d create --name cross-pulled hello-world 2>&1)"
expect_contains "create of a missing image: the CLI pulls after the 404" "$pulled" "Unable to find image 'hello-world:latest' locally"
expect_eq "…and then creates" "$(d inspect --format '{{.State.Status}}' cross-pulled)" "created"
expect_fails "create of an unpullable image" "pull access denied" d create nosuch/image:1
d rm -f cross-made >/dev/null 2>&1 || { d stop cross-made >/dev/null; d rm cross-made >/dev/null; }
d rm cross-pulled "$auto" >/dev/null

echo "== re-create: docker create with the clone fields, docker rename"
# What a re-create sends and what the clone reads back: the real CLI creates
# a container with every carried field, and inspect must report each one.
d network create cross-front >/dev/null
d network create cross-back >/dev/null
d create --name cross-clone \
  --entrypoint /bin/sh -e LOG_LEVEL=info -e TOKEN=crosscheck-sentinel \
  --label com.docker.compose.project=cross --label tier=web \
  -p 127.0.0.1:18081:80 --expose 9000 \
  --mount type=bind,src=/srv/conf,dst=/etc/web,readonly --mount type=volume,src=cross-data,dst=/data \
  --network name=cross-front,alias=web --network cross-back \
  --health-cmd probe --health-interval 30s --health-retries 3 \
  --restart on-failure:3 --memory 512m --cpu-quota 50000 --pids-limit 200 --shm-size 128m \
  --stop-signal SIGQUIT --stop-timeout 30 --workdir /srv --user 1000 --hostname cross-host --init -t -i \
  nginx:1.27 -c serve >/dev/null
ins() { d inspect --format "$1" cross-clone; }
expect_eq "clone: entrypoint" "$(ins '{{json .Config.Entrypoint}}')" '["/bin/sh"]'
expect_eq "clone: command" "$(ins '{{json .Config.Cmd}}')" '["-c","serve"]'
# Some CLI versions send -e entries in no fixed order: check each one.
expect_contains "clone: environment, LOG_LEVEL" "$(ins '{{json .Config.Env}}')" '"LOG_LEVEL=info"'
expect_contains "clone: environment, values included" "$(ins '{{json .Config.Env}}')" '"TOKEN=crosscheck-sentinel"'
expect_eq "clone: labels" "$(ins '{{index .Config.Labels "com.docker.compose.project"}} {{index .Config.Labels "tier"}}')" "cross web"
expect_eq "clone: working dir, user, hostname" "$(ins '{{.Config.WorkingDir}} {{.Config.User}} {{.Config.Hostname}}')" "/srv 1000 cross-host"
expect_eq "clone: tty, open stdin, init" "$(ins '{{.Config.Tty}} {{.Config.OpenStdin}} {{.HostConfig.Init}}')" "true true true"
expect_eq "clone: health check" "$(ins '{{json .Config.Healthcheck.Test}} {{.Config.Healthcheck.Interval}} {{.Config.Healthcheck.Retries}}')" '["CMD-SHELL","probe"] 30s 3'
expect_eq "clone: stop signal and timeout" "$(ins '{{.Config.StopSignal}} {{.Config.StopTimeout}}')" "SIGQUIT 30"
expect_contains "clone: fixed host port" "$(ins '{{json .HostConfig.PortBindings}}')" '"80/tcp":[{"HostIp":"127.0.0.1","HostPort":"18081"}]'
expect_contains "clone: exposed-only port is exposed" "$(ins '{{json .Config.ExposedPorts}}')" '"9000/tcp"'
if ins '{{json .HostConfig.PortBindings}}' | grep -q 9000; then bad "clone: exposed-only port is not published"; else ok "clone: exposed-only port is not published"; fi
mounts="$(ins '{{range .Mounts}}{{.Type}}:{{.Name}}:{{.Source}}:{{.Destination}}:{{.RW}}:{{.Mode}} {{end}}')"
expect_contains "clone: read-only bind" "$mounts" "bind::/srv/conf:/etc/web:false:"
expect_contains "clone: named volume" "$mounts" "volume:cross-data:/var/lib/fake-docker/volumes/cross-data/_data:/data:true:z"
expect_eq "clone: network mode" "$(ins '{{.HostConfig.NetworkMode}}')" "cross-front"
expect_eq "clone: networks with aliases" "$(ins '{{range $k, $v := .NetworkSettings.Networks}}{{$k}}={{join $v.Aliases ","}} {{end}}')" "cross-back= cross-front=web "
expect_eq "clone: restart" "$(ins '{{.HostConfig.RestartPolicy.Name}}:{{.HostConfig.RestartPolicy.MaximumRetryCount}}')" "on-failure:3"
expect_eq "clone: limits" "$(ins '{{.HostConfig.Memory}} {{.HostConfig.CpuQuota}} {{.HostConfig.PidsLimit}} {{.HostConfig.ShmSize}}')" "536870912 50000 200 134217728"
expect_eq "clone: daemon defaults reported" "$(ins '{{.HostConfig.Runtime}} {{.HostConfig.LogConfig.Type}}')" "runc json-file"
d rename cross-clone cross-clone-previous-1
expect_eq "rename: the name changes" "$(d inspect --format '{{.Name}}' cross-clone-previous-1)" "/cross-clone-previous-1"
expect_fails "rename: the old name is free" "no such object" d inspect cross-clone
expect_fails "rename: a name in use" "is already in use" d rename cross-clone-previous-1 shop-web-1
expect_fails "rename: an invalid name" "Invalid container name" d rename cross-clone-previous-1 'bad/name'
d rm cross-clone-previous-1 >/dev/null
d network rm cross-front cross-back >/dev/null

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

echo "== builder prune (the Space view's b sends the same request)"
# A second fake, seeded with an in-use, an internal and a frontend record
# besides the demo's four unused ones, so the two scopes differ. The legacy
# builder path is forced: it is the CLI's own client for POST /build/prune,
# which is exactly the request the desk sends. (buildx's docker driver would
# talk BuildKit's gRPC instead, which the fake does not serve.)
SOCK2="$DIR/fake-build.sock"
"$DIR/fake-dockerd" -socket "$SOCK2" -build-cache-scopes >"$DIR/fake-build.log" 2>&1 &
FAKE2_PID=$!
for _ in $(seq 1 50); do [ -S "$SOCK2" ] && break; sleep 0.1; done
d2() { DOCKER_BUILDKIT=0 docker -H "unix://$SOCK2" "$@"; }
case "$(d2 version --format '{{.Server.Version}}')" in
  *-fake) ;;
  *) kill "$FAKE2_PID" 2>/dev/null; echo "ABORT: second socket is not the fake. Nothing was changed."; exit 2 ;;
esac
if ! d2 builder prune --help >/dev/null 2>&1; then
  echo "  skip  docker builder prune: this CLI has no builder command"
else
  expect_contains "system df before: 7 records, 1 active" "$(d2 system df --format '{{.Type}} {{.TotalCount}} {{.Active}}')" "Build Cache 7 1"
  out="$(d2 builder prune -f 2>&1)"
  expect_contains "builder prune: the daemon's reclaimed bytes" "$out" "Total reclaimed space: 573MB"
  expect_contains "builder prune: the daemon's deleted record" "$out" "kq3x9w2m7v4n8p1r5t6y0z2a"
  expect_contains "builder prune: in-use, internal and frontend kept" "$(d2 system df --format '{{.Type}} {{.TotalCount}} {{.Active}}')" "Build Cache 3 1"
  out="$(d2 builder prune -a -f 2>&1)"
  expect_contains "builder prune -a: internal removed" "$out" "intern000000000000000001"
  expect_contains "builder prune -a: frontend removed" "$out" "front0000000000000000001"
  if printf '%s' "$out" | grep -q inuse0000000000000000001; then bad "builder prune -a: in-use kept" "$out"; else ok "builder prune -a: in-use kept"; fi
  expect_contains "after -a only the in-use record is left" "$(d2 system df --format '{{.Type}} {{.TotalCount}} {{.Active}}')" "Build Cache 1 1"
fi
kill "$FAKE2_PID" 2>/dev/null || true

echo
echo "cross-check: $pass passed, $fail failed"
[ "$fail" -eq 0 ]
