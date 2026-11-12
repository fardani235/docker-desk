# Docker Desk

One screen for Docker engines — the one on this machine, or ones on others, over SSH or TCP;
one at a time in front of you, the rest watched in the background.
Not a table you scroll — a desk: the kinds of things
Docker has on the left, the objects of the kind you picked in the middle with their details
underneath, the engine's own event stream ticking by on the right, and the slow work you sent
off in the corner. All alive at once, all from the keyboard.

```
 Docker Desk  unix:///var/run/docker.sock  ctx default  Engine 29.8.2  on myhost  ● connected
 Daemons [◆1 local ●] │ 2 staging ● │ 3 prod-1 ✗                                       M picker  < > focus
┌─ 1 Kinds ────────┐┏━ ◆ 2 Containers (12) ━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━┓┌─ 4 Events ─────────── ● live ─┐
│▸ Containers    12│┃  NAME            IMAGE         STATE        CPU      MEM     ┃│06:51:17 ctr kill sig 15 web   │
│  Images        14│┃▸ shop-web-1      nginx:1.27    ● running   0.3%  12.1MiB     ┃│06:51:17 ctr die exit 0 web    │
│  Volumes        7│┃  shop-db-1       postgres:16   ● running   1.2%   140MiB     ┃│06:51:17 net disconnect shop_… │
│  Networks       7│┃  paused-batch    alpine:3.20   ‖ paused                      ┃│06:51:17 ctr stop       web    │
│  Compose        2│┃  shop-worker-1   shop/worker…  ○ exited                      ┃│                               │
│                  │┗━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━┛│                               │
│  ● 7 running     │┌─ 3 Details: shop-web-1 ──────── [Info] Logs Stats Inspect ─┐│                               │
│  ‖ 1 paused      ││Name     shop-web-1                                         │└───────────────────────────────┘
│  ○ 4 stopped     ││State    ● running   Up 3 hours                             │┌─ 5 Jobs ─────── ⠙ 1 running ─┐
│                  ││Ports      0.0.0.0:8080 → 80/tcp                            ││▸ ⠙ pull image redis:7     43% │
│                  ││Networks   shop_default  172.18.0.2  gw 172.18.0.1          ││  ✓ stop container web     2ms │
└──────────────────┘└────────────────────────────────────────────────────────────┘└───────────────────────────────┘
 ↑↓ move  / filter  ⏎ actions  s start  t stop  R restart  p pause  u unpause  K kill …   Tab pane  ? keys  q quit
```

Real captures of every screen are in [`docs/screens/`](docs/screens/).

## Install and launch

You need Go 1.26 or newer to build, and a Docker daemon to look at — local or remote.

```sh
make install      # puts docker-desk in $(go env GOPATH)/bin   (or: go install ./cmd/docker-desk)
docker-desk
```

Or without installing: `make build && ./bin/docker-desk`.

Docker Desk needs nothing else at run time — not even the `docker` CLI, on this machine or on
the remote one. The one exception: an `ssh://` daemon is reached through your `ssh` client.

| Command | What it does |
| --- | --- |
| `docker-desk` | open the desk on the daemon your current Docker context points at |
| `docker-desk -H ssh://ops@prod-1` | open it on that daemon instead (`--host`; any `unix://`, `ssh://` or `tcp://` URL) |
| `docker-desk -c staging` | open it on the daemon of that Docker context (`--context`) |
| `docker-desk --read-only` | look, don't touch: every action that changes something, and the shell, is disabled; anything but `GET`/`HEAD` is refused before it is sent — on every daemon of the session, including those added later |
| `docker-desk --no-compose-files` | never read a compose file: Compose › Declared compares nothing and says which flag forbade it (`--read-only` alone still allows the comparison, which only reads) |
| `docker-desk --check` | say which daemon would be used, how it is reached and whether it answers, then exit |
| `docker-desk --report ~/py.spdx.json --report-format spdx --report-object python:3.12-slim` | analyse one image as Images › Findings does, write the report, exit — no terminal needed (see [Export](#export--evidence-to-a-file)) |
| `docker-desk --theme nord` | pick a colour theme (see [Themes](#themes)) |
| `docker-desk --version`, `--help` | |
| `make sandbox` | start a **fake** engine full of containers and open the desk on it — the place to try `remove` and `prune` with nothing at stake |

### Which daemon?

The desk opens on one daemon, decided the way the Docker CLI decides it: `--context`,
then `--host`, then `DOCKER_HOST`, then `DOCKER_CONTEXT`, then the context chosen with
`docker context use`, then `unix:///var/run/docker.sock`. (`--host` and `--context` together
are refused. As in the CLI, the context named `default` *is* `DOCKER_HOST` when that is set.)
More can be added while it runs, from the picker — see
[Several daemons](#several-daemons-in-one-session). The focused daemon's endpoint is on the top
line of the screen at all times, and again inside every confirmation, so you know the blast
radius before you touch anything.

| Endpoint | Reached how |
| --- | --- |
| `unix:///path/to/docker.sock` | a socket on this machine |
| `ssh://[user@]host[:port][/path]` | the daemon's socket on another machine, through your `ssh` client |
| `tcp://host[:port]` | the Engine API over TCP — with TLS when configured, otherwise **not encrypted**; port 2375 if none is given |

Anything else (`npipe://`, `fd://`, `http://`) is refused at launch.

`docker-desk --check` shows what would be used without opening anything. (The remote examples
in this README are illustrations: the wording is the program's, the hosts and versions are
made up.)

```
endpoint   ssh://ops@prod-1
context    prod
chosen by  --context flag
via        an SSH tunnel to /var/run/docker.sock on prod-1
engine     28.1.1 on prod-1 (API 1.49, linux/x86_64)
mode       read-write (every change still needs an in-app confirmation)
```

### A daemon on another machine

**It says so.** A daemon that is not this machine's own socket carries a tag next to the
endpoint — in the header, where it is never dropped however narrow the terminal, and inside
every confirmation, form and picker:

| Tag | Meaning |
| --- | --- |
| `[REMOTE ssh]` | another machine, through SSH |
| `[REMOTE tls]` | another machine, TLS, the daemon's certificate verified |
| `[REMOTE tls, UNVERIFIED]` | TLS, but anyone could be answering — shown in the colour of danger |
| `[REMOTE tcp, NO TLS]` | not encrypted, and open to whoever reaches the port — also in that colour |

(Without `REMOTE` when the address is `localhost` or a loopback address.)

**Over SSH.** `ssh://ops@prod-1` means: the daemon behind `/var/run/docker.sock` on `prod-1`,
logged in as `ops`. A daemon listening elsewhere is named by its path, as in the Docker CLI —
a rootless daemon, for instance: `ssh://ops@prod-1/run/user/1000/docker.sock`.

- It is your `ssh`: `~/.ssh/config` (aliases, jump hosts, per-host keys), your agent and your
  `known_hosts` all apply. If `ssh ops@prod-1` works, so does the desk. To use another program
  or extra options: `DOCKER_DESK_SSH="ssh -F $HOME/.ssh/work_config"`.
- Whatever ssh has to ask — a key's passphrase, a password, an unknown host key — it asks on
  the ordinary screen, *before* the desk opens. Nothing is ever asked afterwards, by ssh or by
  the ssh it starts for a jump host: a reconnection is given no terminal to ask on.
- The user, host and socket path in the URL may contain only letters, digits and a few marks
  (`. _ -`, and `/ @ + , =` in a path). Anything else — ssh would expand a `$` or a `%` in the
  path, for one — is refused; put unusual names in `~/.ssh/config` under a host alias and use
  the alias.
- One connection carries everything: lists, logs, the event stream, a statistics stream per
  container, the shell. If it drops, the header says `✗ DISCONNECTED`, the status line says
  why the link is down, and the desk reconnects by itself when it can do so without a question.
  A login that needs a typed passphrase or password cannot be repeated that way — use an agent,
  or restart the desk.
- The remote machine needs Docker and an SSH server that allows forwarding to unix sockets
  (OpenSSH does by default). It does **not** need the `docker` CLI. On this point the desk
  differs from `docker -H ssh://…`, which needs the CLI there but no forwarding; see
  [DECISIONS](docs/DECISIONS.md#r2--ssh-one-forwarded-socket-not-docker-system-dial-stdio-per-connection)
  for why.

**Over TCP.** TLS is used exactly when the Docker CLI would use it:

```sh
# an endpoint named directly: certificates from the environment
DOCKER_TLS_VERIFY=1 DOCKER_CERT_PATH=~/certs/prod docker-desk -H tcp://prod-1:2376

# a context that stores its certificates
docker context create prod --docker "host=tcp://prod-1:2376,ca=ca.pem,cert=cert.pem,key=key.pem"
docker-desk -c prod
```

`DOCKER_CERT_PATH` holds `ca.pem`, and `cert.pem` with `key.pem` if the daemon wants a client
certificate; it defaults to `~/.docker`. `DOCKER_TLS=1` alone encrypts without verifying the
daemon. With none of these the connection is plain TCP: allowed, as in the CLI, and labelled.

Two of the CLI's rules that surprise, and that the desk follows so that it never looks at a
different daemon than `docker` does: a `tcp://` URL without a port means **2375 even with
TLS** (name `:2376` yourself; `--check` shows the port in use), and with `DOCKER_TLS_VERIFY`
set and no endpoint named anywhere, the daemon is `tcp://localhost:2376`.

### When the daemon cannot be reached

The desk tells you plainly — which endpoint, chosen by what, reached how, and why — and never
opens a workspace that would only fail at everything:

```
docker-desk: the Docker daemon is not reachable.

  endpoint   unix:///var/run/docker.sock
  context    default
  chosen by  Docker default (no context configured)
  reason     the socket /var/run/docker.sock does not exist
  hint       Is the Docker daemon installed and running? (e.g. `sudo systemctl start docker`)

The workspace was not opened.
```

For a remote daemon the reason is as specific as it can be made:

```
docker-desk: the Docker daemon is not reachable.

  endpoint   ssh://ops@prod-1
  context    default
  chosen by  --host flag
  via        an SSH tunnel to /var/run/docker.sock on prod-1
  reason     the remote user ops may not open /var/run/docker.sock on prod-1
  hint       The socket usually belongs to the `docker` group: add the remote user to it and log in again.

The workspace was not opened.
```

Likewise: ssh's own words when it cannot log in; no socket at that path; a port that refuses;
a name that does not resolve; TLS asked of a port that does not speak it, and the reverse; a
certificate from the wrong authority; a client certificate missing or refused.

If the daemon goes away while the desk is open — or the network between you and it does — the
header says `✗ DISCONNECTED — retrying`, the last known data stays on screen marked as stale,
the desk keeps answering keys, and everything resumes by itself when the daemon is back.

### Several daemons in one session

The daemon the desk opened on is the first member of a **session**; `M` opens the **picker**,
which lists the session's daemons and every context in this machine's Docker configuration
(`$DOCKER_CONFIG`, or `~/.docker`) — the same names and endpoints as `docker context ls`, each
endpoint in full with how it is reached (`[REMOTE ssh]`, `[tls]`, …). `Enter` on a context
adds its daemon; on a daemon of the session, it focuses it. A context that cannot be used — an
`npipe://` endpoint, unreadable metadata — is listed with the reason and cannot be added. A
daemon is one member however it is named: two contexts that reach the same socket, host and
port (and, for TLS, the same certificates) are one daemon, and the picker says so.

**One daemon is focused.** The panes, every action, the Events history, statistics and logs are
the focused daemon's: an action always has that one target, named in its confirmation. `>` and
`<` move the focus through the connected daemons in order; the line under the header — the
**strip** — lists every member, marks the focused one with `◆`, and shows each one's state
(`●` connected, `✗` disconnected, `…` connecting, `ro` read-only). When focus moves, the
previous daemon's lists, details and selection leave the screen at once, its statistics and log
streams are closed, and the panes say `Loading…` until the newly focused daemon answers; an
answer still on its way from the previous daemon is dropped, never shown.

**The others are watched, not shown.** A daemon that is not focused keeps one event stream and a
ping, nothing else. What matters from it is a **notice** in the status line, with its name: a
container that exited (`staging: container shop-web-1 exited (code 137)`), the daemon going away
or coming back. Its events are recorded in its own history — focus it and the Events pane shows
them — and never in the focused daemon's.

**A daemon added from the picker never asks.** It is reached exactly as at launch, with the same
checks and time bound, but nothing is asked on the terminal: an ssh login that would need a
passphrase, a password or a host-key confirmation fails, and the daemon is shown disconnected
with ssh's own words. (Load the key into your agent, or accept the host key once with `ssh`,
and it connects on its next try.) A daemon that is down when added is kept, shown disconnected
with the reason, and retried every 1 to 15 seconds; the rest of the session is unaffected.

**Removing a daemon** — `d` on it in the picker, then `y` — is allowed for any member, the launch
daemon too. It is refused while a job runs on that daemon, and the picker names the job: let it
finish, or cancel it in the Jobs pane. Removal closes the daemon's streams and connection and
ends the ssh process started for it; nothing on the daemon changes. When the focused daemon
goes, focus moves to another connected one and the status line says which. When the last one
goes, the desk stays open on an empty state that says no daemon is connected: no action key
does anything there, `M` adds one, `q` quits.

**Jobs belong to their daemon.** A pull keeps running on its daemon when you focus another, and
the Jobs pane lists it with that daemon's name (`pull image redis:7 @staging`); its outcome is
announced the same way.

**Read-only is one flag for the whole session.** With `--read-only` every daemon refuses
changes, including those added later; the picker says so before you add one, and the strip marks
each `ro`.

## The desk

| Pane | Shows |
| --- | --- |
| **1 Kinds** | Containers, Images, Volumes, Networks, Compose — with live counts. Moving the highlight is enough; the list follows. |
| **2 List** | The objects of that kind. Containers show live CPU and memory. The highlight follows the *object* when the list changes underneath it. |
| **3 Details** | The highlighted object: **Info** (readable summary), **Inspect** (the daemon's full JSON; `/` finds a key or value and names its path), for containers **Changes** (what the image its tag names now would bring; see below), **Logs** (live follow; `/` filters), **Stats** (CPU, memory, network, disk — with history) and **Processes** (a snapshot of `docker top`; see below), for images **Findings** (what the image's own bytes hold; see below) and **Upstream** (whether the tag names another build today; see below), and for compose projects **Declared** (what the project's compose file says now against what runs; see [Compose, honestly](#compose-honestly)). |
| **4 Events** | The engine's event stream as it happens. It is the answer to everything you do — and to everything anyone else does to this daemon. |
| **5 Jobs** | Every change you asked for, running in the background: progress while it runs, the daemon's answer when it ends. |
| **Space** (`S`) | Not a pane: a full-screen account of the disk — the daemon's totals, unique and shared bytes per image, what a removal frees. See [Space](#space--where-the-bytes-went-and-what-removing-something-frees). |
| **Repository** (`B`) | Not a pane: a full-screen list of a repository's tags from its registry — what each names, whether that build is already here, and a pull of one build by its digest. See [Repository](#repository--a-registrys-tags-and-one-build-by-its-digest). |

The focused pane has a heavy border and a `◆` in its title (it does not rely on colour;
`NO_COLOR` is respected, whatever the theme). Wide terminals (≥ 120 columns) get three columns; from 80×24 up you
still get all five panes in a tighter arrangement. Smaller than that, the desk says so and
accepts no key but `q` — nothing can be acted on that is not on screen.

### Image findings

Images › **Findings** reads the image's own archive — the stream `docker save` writes, asked
for with a GET (`/images/get?names=…&platform=…`) — and reports what is inside it, joined with
what the daemon runs from it now. Nothing is written to disk while it reads (`w` exports the
report, when you ask); every buffer is bounded.

```
 26 findings · by layer · 4 layers · 79.0kB · oci · linux/amd64
 live: 2 container(s): ○ old-migration exited privileged · ● shop-api-1 running 3000→3000/tcp
   --  certain supply.sbom          SBOM present: SPDX-2.3, 12 packages
   L2  certain layer.cache          /var/lib/apt/lists  2 files, 74.0kB
 ▸ L2  certain secret.deleted       /root/.aws/credentials  AWS access key ID: AKIA****…
 ───────────────────────────────────────────────────────────── 15 of 26 ─
 why      Added in layer 2 and deleted in layer 3, yet still in the image bytes:
          anyone with the image can read layer 2.
 evidence AWS access key ID: AKIA************ (20 chars)   (v reveals)
 source   image · certain · layer 2
 by       COPY . /root
 unknown  whether the value is a live credential, a test fixture or revoked
```

Every finding names its rule, its **source** (`image`: the archive's bytes; `daemon`: the
daemon's answers; `desk`: a join of the two), a **certainty** (`certain`, `likely`, `hint`),
the layer, the path, masked evidence, why it is reported, the build instruction that made the
layer (only when the image's history maps onto its layers exactly), and what the evidence
cannot tell. There is no severity score.

| Group | Rules |
| --- | --- |
| config | runs as root · built for another platform · nothing to run, a bare shell, or a program not in the image · no (or disabled) health check |
| layer | one layer holding most of the bytes · a layer adding very many files · build junk (`.git`, editor litter, files in `/tmp`, downloaded archives, Dockerfiles) · package caches left behind — or removed a layer too late |
| filesystem | setuid · setgid · world-writable files and directories without a sticky bit · file capabilities |
| secrets | in the final filesystem · deleted in a later layer (**still in the image bytes**) · replaced in a later layer · passed as a build argument · set by ENV |
| binaries | not PIE · no or partial RELRO · no stack protector · UPX-packed · Go toolchain version and modules |
| supply | SBOM present · installed OS packages the SBOM does not declare · declared OS packages not installed, or at another version (hint) · provenance present · no attestations · created from a tag, not a digest · the tag moved since the container was created · no registry digest |
| live | a privileged container · host network · ports published on every interface · secrets readable in a running container |

The live half — which containers use the image, their state, ports, privileged flag and
network mode — is recomputed whenever the pane is drawn. The analysis runs as a job ("analyse
image", with bytes read and layers found); the report is kept by image ID and platform, so
opening the tab again is instant. On a remote daemon an image over 500 MB waits for Enter.
Read-only mode works unchanged: everything is a GET.

Secrets are masked everywhere. `v` reveals the one under the cursor; it hides again when the
cursor moves. A secret's value never goes into a job, an error, a log or a file.

### Changes — what re-creating a container would bring

The desk already says when a tag has moved: an image finding (`supply.tag-moved`) notes that a
container runs bytes its tag no longer names. Containers › **Changes**, the tab after Info,
says what the difference *is*, so that you know what a re-deploy brings before you do it.

The pair needs no history: a container keeps its image alive, so the image it runs is still
here (untagged if need be), and the image the tag names now is whichever local image carries
that tag. Drawn at once, from four requests at most — an inspect and a history of each image;
the reference the container was created from comes from the inspect the Details pane already
made:

- **identities**: both IDs, ages and sizes, and the size difference;
- **layers**, from both images' ordered diff IDs: *base intact: all N layers shared, M added*,
  or where the two stacks part — each layer outside the shared part with its size and the
  build instruction that made it (build-argument values left out, as in Findings). When the
  history does not map onto the layers, the instruction is *unknown*, with the reason;
- **config**: command, entrypoint, user, working directory, exposed ports, health check,
  labels, and environment variable **names** — added or removed. Values are dropped where the
  daemon's answer is decoded; they are never read, so a variable whose value changed under the
  same name cannot be seen, and the tab says so.

`Enter` then compares the contents, as a job (*compare images*) that reads both archives the
way Findings does — streamed from `GET /images/get`, read once, nothing written to disk — and
lists packages added, removed and at another version (dpkg, apk, rpm) and archive findings
that appeared or disappeared. A report Findings already made is used, not read again, and
the reports the job makes are Findings' too, so a second open is instant. `c` cancels.
Nothing is read before `Enter`.

When there is nothing to compare, the tab says why: the container was created from a digest
or an image ID (nothing can move); its tag no longer resolves here (the desk does not pull to
find out); the tag still names the image it runs; the two images are for different platforms;
or — once the archives are read — a layer of either could not be read. It also says, always:
**restarting a container keeps the image it runs; only re-creating it takes the new one —
which this desk does not do.** There is no update button, and no score.

### Image packages — the declared list against the installed one

`i` on the Findings tab opens the image's ingredient list, full screen: what its SBOM declares,
against what its own package databases say is installed. Both come from the analysis already
made — nothing is requested, and the archive is not read again.

```
┏━ ◆ Packages — what the SBOM declares, against what the image's databases say is installed ━━━━━━━━ python:3.12-slim ━┓
┃ by name · showing disagreements · 5 of 73 rows                                                                       ┃
┃   SBOM       SPDX-2.3 · 70 packages · attestation ed4a7151c323                                                       ┃
┃   installed  dpkg · /var/lib/dpkg/status (layer 4) · 66 packages                                                     ┃
┃              no apk or rpm database in any layer                                                                     ┃
┃   rows       62 both · 1 version differs · 1 declared only · 3 undeclared · 6 not checkable                          ┃
┃                                                                                                                      ┃
┃   NAME                              VERSION                   LICENCE                   TYPE    LAYER  STATE         ┃
┃   curl                              7.88.1-10+deb12u8         —                         deb     L4     undeclared    ┃
┃ ▸ libcurl4                          7.88.1-10+deb12u8         —                         deb     L4     undeclared    ┃
┃   libgnutls30                       3.7.9-2+deb12u3           LGPL-2.1-or-later         deb     --     declared only ┃
┃   libssl3                           3.0.15-1~deb12u1          Apache-2.0                deb     L2     differs       ┃
┃   netcat-openbsd                    1.219-1                   —                         deb     L4     undeclared    ┃
┃                                                                                                                      ┃
┃ ─────────────────────────────────────────────────────────────────────────────────────────────────────────── 2 of 5 ─ ┃
┃ state      installed, and the SBOM does not list it                                                                  ┃
┃ installed  version 7.88.1-10+deb12u8 · amd64 · listed by /var/lib/dpkg/status                                        ┃
┃ added by   layer 4 · RUN apt-get update && apt-get install -y curl netcat-openbsd && rm -rf /var/lib/apt/lists/*     ┃
```

- **Declared**: the SBOM's packages — name, version, licence, purl — from SPDX `packages[]` or
  CycloneDX `components[]`, read as the archive streams, so an SBOM far above the 4 MiB bound for
  JSON documents is still listed. At most 20,000 are kept; the true total is always shown.
- **Installed**: the image's own databases, read in the layer walk and dropped once parsed —
  dpkg's `/var/lib/dpkg/status` and `status.d/*`, apk's `/lib/apk/db/installed`, rpm's
  `rpmdb.sqlite` (in `/var/lib/rpm` or `/usr/lib/sysimage/rpm`), which the desk reads with its
  own read-only SQLite reader. Which copy counts is decided by the final filesystem — whiteouts,
  opaque directories, a directory replaced by a link, layers that cannot be read. Each package is
  attributed to the layer that introduced it, by diffing the database across layers, with the
  build instruction where the history maps.
- **The join** is within one ecosystem: `pkg:deb` against dpkg, `pkg:apk` against apk, `pkg:rpm`
  against rpm. A row is **both**, **differs** (version), **declared only**, **undeclared**, or
  **not checkable** — every other ecosystem, a package without a purl, and anything whose other
  side cannot be read, with the reason on the row. Nothing is called undeclared on the strength
  of a list that was cut short.
- **Unknowns are said.** A zstd layer, a deleted database, a damaged or Berkeley DB rpm database,
  a non-empty write-ahead log: what is installed is *unknown*, with the reason, in the header —
  never an empty list. Bounds: 20,000 declared, 10,000 installed, 64 MiB for an rpm database,
  8 MiB for a text database; when one is hit, the header says so.

Installed OS packages the SBOM does not list become one `supply.undeclared` finding, naming them;
declared OS packages that are not installed, or are at another version, become one
`supply.sbom-drift` hint. In the sandbox, `python:3.12-slim` has every kind of disagreement.

### Upstream — what the tag names today

Images › **Upstream** asks the tag's registry whether it still names this image's build. Before
it asks, the tab names the host and whether a credential from your Docker configuration is
involved (`asks ghcr.io with the credential from credsStore desktop`); the check runs as a job,
and its answer is kept for the session with its age. `r` (while the tab is shown) asks again;
`c` cancels. Nothing polls, and a list refresh asks nothing.

```
Moved: the tag names a different build than this image.
Local build:    2026-10-01 00:00 UTC (config created)
Upstream build: 2026-10-05 00:00 UTC (config created)
The upstream build is later, by 4d.
The times are what each build's producer wrote in its config, not a fact.
First 11 of 13 layers shared; 2 upstream layers differ (local has 13).
```

The verdict is **current**, **moved**, **gone**, **unknown** (with the reason: refused,
unreachable, unverified certificate, quota, a credential helper that failed, a platform no longer
published) or **not applicable** (built here, untagged, pinned to a digest). It never says newer
or stale: build times are the producers' claims — a reproducible build carries the epoch, a
rebuild can be dated before what it replaced — so a direction is given only when both times are
usable and differ. The comparison uses the digest the daemon recorded when the image was pulled
(`RepoDigests`), never the image ID, which on the classic image store is a different digest.

Credentials come from `$DOCKER_CONFIG`, or `~/.docker` — `credHelpers`, `credsStore`, `auths` —
under the key the Docker CLI uses (`https://index.docker.io/v1/` for Docker Hub). They are never
shown, logged or written. When the desk cannot reach the registry (name, TLS, timeout, quota, an
unreadable answer) it asks the daemon's `GET /distribution/{name}/json` with the same credential;
that answer says *via the daemon* and has no build time. A refusal is an answer, and is not
retried through the daemon.

`U` (Images) checks every tagged image, one at a time, as one job, and stops at a quota refusal,
saying when it resets — Docker Hub's anonymous quota is shared with `docker pull`. The list's
**UPSTREAM** column shows the answers (`moved +4d`, `current`, `gone`); it appears once a
check has answered, is filled only by checks, and is the first column to go when the list is
narrow. `--no-registry` forbids every
registry request, by the desk or through the daemon; `--read-only` does not, as the check only
reads. Plain HTTP is used for loopback registries only; there is no private-CA option yet.

### Repository — a registry's tags, and one build by its digest

`B`, from any pane, covers the desk with the tags a registry lists for one repository. On an
image it fills the name line with that image's repository and asks at once; with no image it
opens on an empty name line (`e` edits it again later — every key is text until Enter asks or
Esc steps out). Opening sends one request, `GET /v2/<repository>/tags/list?n=100`, and nothing
else; `Tab` asks for the next page the registry links to (on the same host and path only), up to
1000 tags held. Tags are in **text order**, numbers by value (`3.9` before `3.10`) — never by
recency, which a tag list does not carry; the header says so. `/` filters the tags held.

```
repository 127.0.0.1:5000/acme/api
asks       127.0.0.1:5000 anonymously · 100 tags held · Tab asks for the next page · listed 4s ago
  TAG           DIGEST               PLATFORMS             HERE                           BUILT
  3.27.0        sha256:73f0ebe515b3  amd64 arm64/v8        same build as stable           2026-06-15 amd64
  3.28          sha256:a58c0cb3fff6  amd64 arm64/v8        here, the tag moved upstream   2026-10-20 amd64
▸ 3.28.2        sha256:5343c588b2a1  amd64 arm64/v8        here                           2026-09-21 amd64
  3.29.0-rc.1   sha256:ffdcd7c49f8f  no amd64 · arm64/v8   -                              no build here
```

When the highlight rests on a row, one manifest request says what the tag names — its digest,
and an index's platforms — and the answer is kept for the session with its age; moving back to
the row asks nothing. **HERE** compares that digest with the `RepoDigests` of the images of the
same repository on this daemon — never an image ID: `here`, `here, the tag moved upstream` (you
have the tag, as another build), `same build as <tag>` (you have this build, under another tag),
or `-`. A tag whose index has no build for the daemon's architecture says so on the row, and its
pull is refused. `d` reads the build times of the rows on screen as one job (up to three
requests a row, for the daemon's platform; each time names the platform whose config it came
from), stops at a quota refusal and keeps what it read; `c` cancels it. `r` forgets what was
read about the repository and asks again. Nothing polls.

`Enter` opens a pull confirmation for exactly `name:tag@sha256:<digest>`: the full reference is
shown, and it says the daemon fetches the **digest, not the tag** — if the tag moves meanwhile,
you still get the build you looked at. You confirm by typing the tag (`3.28.3`), not the
digest. The Engine API carries only the digest to the daemon, which records the image as
`name@sha256:…`; a local `name:tag` is neither created nor moved, and the dialog says so. The
same confirmation is what `P` opens on Images › Upstream when it shows **moved**.

A `401` is reported as what it is: *the registry answered 401 - it does not have this
repository, or this credential is not allowed to see it* — registries such as Docker Hub answer
both cases the same way. Credentials are the Upstream tab's, from your Docker configuration.
`--no-registry`: the view does not open, and says why. `--read-only`: it browses; the pull is
refused like every other change.

### Space — where the bytes went, and what removing something frees

`S`, from any pane, covers the desk with an account of what this engine holds on disk; `S`
or Esc puts the desk back exactly as it was. The totals are the daemon's own
(`GET /system/df`), printed the way `docker system df` prints them, so they read the same:

```
 TYPE            TOTAL  ACTIVE      SIZE  RECLAIMABLE
 Images             14      10   3.276GB  1.326GB (40%)
 Containers         12       8   107.1MB  24.11MB (22%)
 Local Volumes       7       4   2.443GB  1.872GB (76%)
 Build Cache         4       0   511.7MB  511.7MB
 daemon (GET /system/df) · 08:12:48, 0s ago
```

Below them, every image with its size, its **unique** size (what only it holds) and its
**shared** size, as the daemon reports them; then the shared bytes, **each listed once**,
against the exact set of images that hold them; the containers; the local volumes; and the
build cache. For the highlighted image, two numbers:

```
 ▸ shop/worker:dev              219.7MB   88.7MB    131MB   1 ctr
 ───────────────────────────────────────────────────────── lines 8–19 of 71 ─
 alone      frees 88.7MB — unique; 131MB shared stay · daemon, 0s ago
 together   with the 7 image(s) sharing its layers: frees 2.468GB · computed
            from layer sizes, 0s ago
 removal    used by 1 container(s): refused until they are removed — the
            alone figure is what it frees then
```

- **Which numbers are whose.** Every figure says where it came from and how old it is:
  `daemon` (the account), `computed from layer sizes` (the daemon's per-layer sizes from each
  image's history, matched to its layers' chain IDs and checked against the daemon's own
  totals for it), `computed writable layer`, or `archive`. A figure taken before a refresh
  that found the engine changed is marked **STALE**; `r` reads the account again. Nothing is
  estimated: where a figure cannot be known exactly it says "at least" or "not known", and
  why. On a daemon older than API 1.52, which states no totals, the view says the totals were
  summed by the client library.
- **Nothing total is called freeable.** Removing an image frees its unique bytes; the layers
  it shares stay for the images that share them. "Together" is the image and every image
  holding one of its layers. Reclaimable is the daemon's estimate; with the containerd image
  store the bytes go when its garbage collector runs.
- **Writable layers are measured only when you ask** — `m`, as a cancellable job
  (`GET /containers/json?size=1`): the daemon walks every container's filesystem for it,
  which is slow on a busy engine and costly over a remote link. No list refresh asks.
- **Analysed images** (Images › Findings) get their archive's size by layer and the largest
  build instruction, from the report already kept. No archive is read again.
- **Enter** goes to the highlighted image, container or volume in its own kind.
- **Reclaim is the desk's own dialogs, nothing else:** `d` opens the usual removal
  confirmation for the highlighted object, `X` the usual prune preview. Every removal and
  prune confirmation — from here or from the list — now has a **frees** line with the bytes
  it would free, its source and its age ("not measured" before the first account). There is
  no new way to delete anything, Docker's prune endpoints are not called, nothing is forced.
  In read-only mode every figure is there and `d`, `X` are not.

The opening account is itself a job ("measure disk usage"), cancellable with `c`: the daemon
walks container and volume filesystems to answer it too.

### Files — what is inside an image, and what a container wrote

`F`, on the highlighted image or container and from any pane, covers the desk with the
image's filesystem: its layers merged into one tree, each path with its type, size, mode and
the layer — and that layer's build instruction — that last wrote it. `F` or Esc puts the desk
back exactly as it was.

```
   NAME                                      SIZE  TYPE      LAYER  CHANGE
   - etc/                                    258B  dir 5            changed
     - nginx/                                171B  dir 3            changed
       + conf.d/                              50B  dir 1
 ▸       nginx.conf                           88B  file      L2     changed
         traversal.conf                       33B  file      L3
       os-release                             55B  file      L1
 ────────────────────────────────────────────────────────────────── 7 of 24 ─
 path       /etc/nginx/nginx.conf
 type       regular file · mode 0644 (-rw-r--r--) · uid 0 gid 0 · modified
            2024-01-02 03:04:05 UTC
 size       88B, the image's: the container changed this file, and its own
            size is not read
 layer      2 of 3 · RUN apt-get update && apt-get install -y nginx=1.27.2 &&
            rm -rf /var/lib/apt/lists/* && apt-get clean
 change     changed by the container since its image
```

**Two sources, and only one of them is expensive.**

- **An image's tree** comes from its archive — the stream `docker save` writes
  (`GET /images/get`) — read once per session, as one job, and only when you press `Enter`.
  Until then the view shows what the desk already knows (the image, its platform, its layer
  count and the daemon's size for it) and says *nothing has been read*. The job reads tar
  headers only: names, types, sizes, modes, owners and times. **No file's contents are read,
  shown or searched.** `c` cancels it; closing the view cancels it too. A cancelled or failed
  read keeps nothing and says why. The last two images read are kept, so reopening is
  instant and the archive is not read again.
- **A container's files** are its image's tree with the daemon's own change list laid over it
  — `GET /containers/{id}/changes`, what `docker diff` prints — read when the view opens and
  again on `r`, which reads nothing else. Paths the container **added** are marked and have no
  size (the tree never reads a container's archive; only a copy does), a directory it added cannot be opened
  and says its contents are not read, a **changed** file keeps the image's size and says so,
  and a **deleted** path is struck through with the image's size and layer. Opened before the
  image is read, the view lists the changes alone, without sizes, and says the image's files
  have not been read and that `Enter` reads them. With no changes it says *this container has
  written nothing since its image*. The list's age is shown ("read 12s ago"); the image's tree
  never goes stale, since an image's archive cannot change.

**The tree is the merged filesystem.** A path several layers wrote appears once, with the last
writer's size and mode; a path a later layer removed is not drawn, and the header counts how
many were; an opaque directory shows only what its own and later layers put in it. Symlinks
show their target and are never followed; a hard link is one entry naming another; devices,
FIFOs and sockets are typed and never opened. A directory's size is computed from the files it
holds, says so, shows how many files those are (`dir 12`), and carries no layer (several layers
can write one). A file's layer is marked `L3?` when a layer above it could not be read: that layer
may have replaced or removed it, and the detail says which and why. The daemon's size for the
image and the tree's own total are shown side by side as **different measurements, not a
contradiction**: layers overlap, and the daemon counts what a later layer replaced or removed.

**Bounds, stated.** At most **200,000 entries**, 4 KiB a path (Linux's `PATH_MAX`) and 48 MB of
path text are kept. The header says how many were indexed of how many were seen
("200,000 entries indexed of 812,431 seen") and which bound dropped the rest; while the index
is cut, the filter says it may miss a path the image holds. A **zstd-compressed layer cannot be
read** — the desk carries no zstd decoder — and the header names it: no row is drawn, and no
path is said to be absent, on its account.

**Moving.** `↑` `↓` `PgUp` `PgDn` `g` `G` move; `→` or `Enter` opens a directory, `←` closes it
or goes to the one it is in; `s` orders by name or by size (largest first, directories by
their files); `/` filters by path, case ignored, showing every match with the directories that
hold it and counting the matches among the paths searched. `Esc` clears the filter and keeps
the cursor on its path. Long names are cut on the left, so the end of a name stays visible.

**Names are hostile.** Whoever built the image chose them. Every name is escaped before it is
drawn or written — a newline as `\n`, other control characters, `DEL` and invalid UTF-8 as
`\xNN`, a backslash doubled — so a name cannot colour the screen, move the cursor or forge a
row of the listing. A name shaped like `../../etc/passwd` is a name in the tree; no path is
ever opened, resolved or joined to the machine's filesystem.

**Copying, out and in.** On a container, `o` and `i` copy the highlighted path between the
container and this machine — the only time the desk reads or writes a file's contents, and only
the path asked for, streamed and never drawn. On an image both keys say copies are between a
container and this machine, and open nothing.

- **The destination is the path named.** In both directions the copy is written *at* the path
  you give, never inside it: `/etc/nginx` copied out to `~/nginx-copy` makes `~/nginx-copy` hold
  `conf.d/` and `nginx.conf`; a local `a.conf` copied in to `/etc/app/a.conf` is that file, not
  `/etc/app/a.conf/a.conf`. This is a deliberate difference from `docker cp`, which nests a
  source inside a directory that already exists. The destination's parent must already exist —
  no folder is created on either side — and both forms say so.
- **`o` copy the highlighted path out to a path on this machine.** The form names the container
  path and asks for a local one; `~` is your home folder and a relative path is taken from the
  current folder, and the form shows the absolute path it will write before you press Enter. A
  local path that already exists — file, folder or link — is refused, and so is one whose parent
  does not exist or is not a folder; nothing is read from the daemon then. Enter starts one job:
  the daemon's archive (`GET /containers/{id}/archive`, what `docker cp` reads) is streamed and
  written there — a file, or a directory with everything beneath it. Modes are kept; symlinks are
  recreated as links and never followed; devices, FIFOs and sockets are skipped and named in the
  job's output. No entry of the archive can land outside the path named: an absolute name, a
  `..` or a path beneath one of its own symlinks stops the copy. If the copy fails or is
  cancelled (`c` in the Jobs pane), everything it wrote is removed and the job says what. It
  only reads the daemon, so it works in read-only mode, and its job says nothing was changed.
- **`i` copy a local path in to the highlighted path.** The form asks for the local source and
  the container destination, filled in with the highlighted path. Enter walks the source (off
  the screen's thread; Esc stops it) and refuses a source that does not exist, cannot be read,
  is `/` or is itself a device or FIFO. The confirmation then names the daemon, the container,
  the absolute source with its files, directories, links and bytes, the destination, and that
  files already there with the same names are **replaced** (others are left alone; a directory
  is never replaced by a file, nor a file by a directory). Only `y` copies — Enter, `n` and Esc
  send nothing. The source is streamed as a tar (`PUT /containers/{id}/archive` at the
  destination's parent), never held in memory; the job reports what it sent. When it ends the
  view reads the change list again, so the copied paths appear as added or changed — with the
  view's usual rules: no size for what the container added, the image's size for a changed
  file. Read-only mode refuses `i` before any form opens.
- **Ownership and times.** Contents and permission bits are copied; ownership is not. What you
  copy out belongs to you (set-id and sticky bits are not applied, times are not restored);
  what you copy in gets the daemon's default owner — root, as with `docker cp` without `-a` —
  and the modification time the source had.

The copy-in form and its confirmation, captured:
[`docs/screens/56-copy-in-form.txt`](docs/screens/56-copy-in-form.txt) and
[`docs/screens/57-copy-in-confirm.txt`](docs/screens/57-copy-in-confirm.txt).

**Export.** `w` writes the listing through the evidence dialog below — text (one path per line,
tab-separated) or JSON — with every path's type, size, mode and layer and, for a container,
what it did to it; names escaped as on screen. Before the archive is read there is no index,
and nothing is written.

### Export — evidence to a file

`w` writes what the desk has already worked out to **one file on this machine**: in Images ›
Findings (Details pane focused), in the Packages view, and in the Space view. It is the only
thing in the program that writes a file, and it asks first.

The dialog offers a format and a path, prefilled as
`~/docker-desk-<kind>-<object>-<YYYYMMDD>.<ext>` — `~/docker-desk-findings-python-3.12-slim-20261109.json`.
`Tab` changes the format (the extension follows it); the path can be edited. `Enter` shows the
**absolute** path that will be written (`~` expanded to your home folder), the format, the size
in bytes and the mode; only `y` writes it — a `y` typed in the dialog's first quarter of a second
is not consent — and `n` or `Esc` goes back to the path.

| Format | Holds | Offered in |
| --- | --- | --- |
| `json` | the desk's own schema (`docker-desk/image-report/v1`, `docker-desk/space-report/v1`): everything computed — config (environment variable *names* only), layers, attestations, every finding with its source, certainty, layer, instruction and unknown, the notes, the SBOM side, each package database, and every joined package row with its state | Findings, Packages, Space |
| `spdx` | SPDX 2.3 JSON: the packages only | Findings, Packages (prefilled there) |
| `cyclonedx` | CycloneDX 1.5 JSON: the packages only | Findings, Packages |
| `text` | Findings: one tab-separated line per finding, grouped by group. Space: the totals, every image and every shared group, one line each | Findings, Packages, Space |

The rules every export follows:

- **Consent and the file.** One path, confirmed. The file is created with mode `0600`. A path
  that already holds a file is **refused** — the TUI never replaces one; the command line does
  only with `--force` — and so is a folder, or a folder that does not exist (none is created).
  The bytes go to a hidden temporary file beside the target and take the target's name only
  when all of them are written, so a failure leaves **no partial file** under either name.
- **Nothing else moves.** No request is sent for an export — the document is collected from
  what the view already holds — nothing goes over the network, and the daemon is not touched,
  so `w` works in `--read-only` mode. The file is written in the background: a failed export
  never holds up the desk, and the status line says why in one sentence (`Not exported: cannot
  write /srv/x.json: the folder /srv cannot be written to (permission denied).`).
- **Deterministic.** Every list is sorted by the serializer itself — packages by name, then
  version; findings by group, then layer, rule and path — never in map order. The same object
  exports to the same bytes, from the TUI or from `--report`. SPDX's required creation time is
  therefore fixed at `1970-01-01T00:00:00Z`, and says why; the CycloneDX serial number is a
  name-based UUID of the content.
- **What is left out.** No timestamps, no host paths (paths in a report are paths inside the
  image), no user names, no daemon address or name, no container IDs (containers are named);
  image IDs and digests stay, because they say which object this is. A secret's **value** is
  never written: a finding says its evidence, masked, and that it had a value.
- **Nothing is filled in.** A package that is not checkable — npm, pip, Go modules, no purl, a
  database that cannot be read — keeps that state and its reason in every format (an SPDX
  package's `comment`, a CycloneDX component's `docker-desk:` properties). A version neither
  side states is left out, never guessed; the SBOM's purl is written only beside the version it
  names; a licence is given as its source wrote it, and SPDX's `licenseDeclared` is
  `NOASSERTION`, because the desk does not rewrite free text into an SPDX expression.
- **Space** exports totals (as the daemon states them), each image with what removing it frees
  alone and together with the images sharing its layers, and the shared groups. Every figure
  carries `exact`, `at least` or `unknown`, its source, and the reason when it is not exact.
  Writable layers are included only once `m` has measured them; a figure taken before the engine
  changed is marked stale.

**Headless.** `--report PATH --report-format json|spdx|cyclonedx|text --report-object REF`
connects as the desk would, asks exactly what the view asks (inspect, the archive once, the
containers using it; or the account and every image's layers for `--report-object :space`),
writes the file and exits: `0` when it is written, `1` when it is not (with nothing left at the
path), `2` for flags that do not fit together. `--report-format` defaults to `json`; `:space` is
written as `json` or `text` (no image reference begins with a colon). `--force` replaces an
existing file. It needs no terminal, and works with `--read-only`.

```sh
docker-desk --read-only --report ~/api.txt --report-format text --report-object shop/api:dev
docker-desk --report ~/space.json --report-object :space
```

### Themes

| Theme | |
| --- | --- |
| `auto` | the default: `dark`, or `light` when your terminal says its background is light |
| `dark` | the original palette |
| `light` | darker hues that stay readable on a light background |
| `ansi` | only your terminal's own 16 colours, so it follows whatever scheme the terminal has |
| `dracula`, `nord`, `gruvbox`, `solarized-dark`, `solarized-light`, `catppuccin` | for terminals already using that scheme |
| `mono` | no colour at all |

Choose one with `--theme NAME` or `DOCKER_DESK_THEME=NAME`. Inside the desk, `T` switches to
the next one so that you can pick by looking; the status line tells you its name and how to
keep it (nothing is saved for you). A theme changes colours and nothing else: every pane,
border, glyph and word is the same in all of them, red still means "destructive" or "failed",
and the desk never paints your terminal's background.

## Keys

Press `?` inside the desk for the same list.

**Everywhere**

| Key | |
| --- | --- |
| `Tab` / `Shift+Tab` | next / previous pane |
| `1` `2` `3` `4` `5` | focus Kinds / List / Details / Events / Jobs |
| `[` `]` | previous / next Details tab |
| `↑` `↓` or `k` `j`, `PgUp` `PgDn`, `g` `G` | move / page / first / last |
| `/` | in the List: filter the list as you type (Enter keeps, Esc undoes). In the Details pane: search — see below |
| `Space` | in the List: select / unselect the highlighted row (Containers, Images, Volumes, Networks) |
| `a` | in the List: select every row the filter shows |
| `Esc` | in the List: clear the selection; again, clear the filter; again, back to Kinds |
| `Enter` | in the List: a menu of what you can do to the highlighted object, or to the selection |
| `r` | refresh every list now; also takes a new Processes snapshot (now if the tab is shown, else on the next visit), and asks upstream again if Images › Upstream is shown |
| `U` | Images: check every tagged image upstream, one at a time, as one job (see [Upstream](#upstream--what-the-tag-names-today)) |
| `S` | Space: where the bytes went, and what removing something frees (see above) |
| `F` | Files: the highlighted image's merged tree, or a container's changes over it (see [Files](#files--what-is-inside-an-image-and-what-a-container-wrote)) |
| `B` | Repository: a registry's tags for the highlighted image's repository, and a pull by digest (see [Repository](#repository--a-registrys-tags-and-one-build-by-its-digest)) |
| `M` | the daemon picker: this session's daemons and this machine's Docker contexts (see [Several daemons](#several-daemons-in-one-session)) |
| `>` `<` | focus the next / previous connected daemon |
| `T` | next colour theme |
| `?` | help |
| `q`, `Ctrl+C` | quit (asks first if jobs are running) |

**Acting on things** (List or Details pane focused)

| Kind | Keys |
| --- | --- |
| Containers | `n` new (create and start, pulling the image first if it is missing) · `s` start · `t` stop · `R` restart · `p` pause · `u` unpause · `K` kill · `d` remove · `e` shell |
| Images | `P` pull by name (on Upstream showing *moved*: the build the tag names now, by digest) · `d` remove · `X` prune |
| Volumes | `n` new · `d` remove · `X` prune |
| Networks | `n` new · `d` remove · `X` prune · `c` connect a container · `D` disconnect a container |
| Compose | `t` stop the whole project · `d` remove the whole project · (menu) compare with the compose file — a read, allowed in read-only mode |

**Acting on a selection.** Mark rows with `Space` (or `a` for everything the filter shows):
they get a `✓` in the gutter and the title says how many, e.g. `2 Containers (12) - 3 selected`.
The selection belongs to its kind and follows its objects across refreshes; an object that
disappears leaves it. While a kind has a selection, `s` `t` `R` `p` `u` `K` `d` on Containers
and `d` on Images, Volumes and Networks apply to every selected object instead of the
highlighted one. `e` shell, `c` connect and `D` disconnect work on one object only and refuse
(nothing is sent); `X` prune chooses its own objects and refuses too; `P` pull and `n` new
(on Containers, Volumes and Networks) are unaffected. The selection is cleared when the job starts, and kept if you decline. `Space`
still pages in the Details pane; in the Events pane it holds the view.

**Creating a container** (`n` on Containers, also in the action menu as *new…*). One form,
seven fields: the image (required; no tag means `:latest`), a name (empty lets the daemon choose
one), published ports, environment variables, mounts, the restart policy and the command (empty
keeps the image's). `Tab` / `Shift+Tab` move between fields, `↑` `↓` between a field's entries,
`Ctrl+N` adds an entry and `Ctrl+X` removes the focused one, `←` `→` choose the restart policy
(`on-failure` takes an optional retry limit on the row below). One syntax per entry:

| Field | Entry | Examples |
| --- | --- | --- |
| Ports | `[host-address:][host-port:]container-port[/tcp\|udp]` | `8080:80` · `127.0.0.1:5432:5432` · `53:53/udp` · `[::1]:8080:80` · `80` (the daemon picks the host port) |
| Environment | `NAME=value` — the value is everything after the first `=` | `LOG_LEVEL=info` |
| Mounts | `source:destination[:ro]` — a source starting with `/` is a host path, anything else a named volume | `/srv/data:/data:ro` · `cache:/var/cache` |

The form is the confirmation: under the fields it restates everything that will be sent — the
daemon, the exact image reference and whether it is already here or *will be pulled first*, the
name, every port, environment entry and mount, the policy and the command — and that the
container will be created *and started*. Enter is the only key that sends anything, and not
before the form has been on screen long enough to read; Esc sends nothing. A value that does not
parse, a name already in use or a host port given twice is reported under the form with the
field at fault focused, and nothing is sent. Enter builds the plan from a fresh look at the
daemon and starts one job: pull (only if the image is missing — exactly that reference, never
every tag), create, start. A step whose predecessor failed is not attempted and the job says so;
a container created but not started is named as such. The new container appears in the list from
the daemon's own events. Environment values are shown in the form and sent in the request, and
appear nowhere else — not in the job, its output or the status line. Read-only mode refuses `n`
before any form opens. The container joins the daemon's default network; anything the seven
fields do not cover (networks, user, limits, a TTY, …) is not offered. The form, captured:
[`docs/screens/54-create-form.txt`](docs/screens/54-create-form.txt) and, at 80×24,
[`docs/screens/55-create-form-80x24.txt`](docs/screens/55-create-form-80x24.txt).

**In the daemon picker** (`M`): `↑` `↓` move · `Enter` focus the daemon, or add the context ·
`d` remove the daemon (then `y` to confirm, `n` to keep it) · `r` read the contexts again ·
`Esc`, `q` or `M` close.

**Details**: `←` `→` switch tab · `f` pause/resume following (Logs) · `w` toggle wrapping (not Processes).

**Search in the Details pane** (`/`, on any tab). The bar opens in the pane's last row (on Logs,
in its status row) and takes no row once closed. Each tab keeps its own query until another
object is highlighted.

| Key | Info, Inspect, Processes, Changes: **find** | Logs: **filter** |
| --- | --- | --- |
| `/` | open the bar; matches are marked as you type and counted: `find: q  3 of 9` | open the bar; lines that do not match are hidden as you type |
| `Enter` | keep the query, close the input, jump to the current match | keep the filter |
| `n` `N` | next / previous match, wrapping at either end | — |
| `Esc` | clear the query; again, back to the List | clear the filter; again, back to the List |

Find hides nothing: every match is bold and underlined (in the accent colour; the current one
reversed too; with no colour at all, `›…‹` and `»…«`), and on **Inspect** the bar names the key
of the current match — `at State.Health.Status`, array elements as `[n]`. The Logs filter
follows the newest *matching* line, scrolls back through matching lines only, and counts them
against what the desk holds: `● following · 12 of 5000 match`, or `no match in the 5000 lines
held`. That is all it can see — the last 5000 lines at most, starting from the last 1000 when
the stream opened; older output on the daemon is **not** searched. Matching ignores case and
colour codes and takes the query literally (no patterns). While a search is open `n` and `N`
step through matches, so Volumes' and Networks' `n` (new) waits until `Esc`. Typing in the bar
can never trigger an action: every key is text until Enter or Esc. Searching sends nothing.
Stats and Findings are not text; on them the bar says there is nothing to search.

**Processes** (Containers): the daemon's default process listing (`GET /containers/{id}/top`,
no `ps` arguments), one row per process, columns exactly as the daemon names them. It is a
snapshot, not a live view: taken when the tab is opened, when the highlight moves to another
container, and on `r` — never on a timer; its capture time is in the status row, which says
*stale* while the daemon cannot be reached. A stopped, paused or exited container says that
processes are available only for running containers. Long values are cut to the pane width;
the movement keys scroll the rows. Command lines are shown as the daemon reports them,
arguments included — nothing is masked, so a secret passed on a command line is visible here,
as it is to `docker top`; process rows are never written to jobs, the status line or files.
**Changes** (Containers): `↑` `↓` scroll · `Enter` compare the contents of both images (a
job; nothing is read before it) · `c` cancel the comparison.
**Declared** (Compose): `↑` `↓` scroll · `Enter` read the project's compose file and compare it
with what runs (a job; nothing is opened before it; again to read it again) · `c` cancel the
read. Also reached from the project's action menu: *compare with the compose file*.
**Findings** (Images): `↑` `↓` move between findings · `o` order by layer / group / certainty ·
`v` reveal the highlighted secret · `i` the image's packages · `w` export… · `c` cancel the analysis ·
`Enter` analyse when asked first or after a failure.
**Packages** (`i` on Findings): `↑` `↓` move · `/` search name, version, licence and purl ·
`o` order by name / state / layer / licence · `s` show all, the disagreements, or one state ·
`S` show all · `w` export… (SPDX first) · `Esc` clear the search, then close.
**Space** (`S`): `↑` `↓` move between images, containers and volumes · `Enter` go to the
highlighted one · `r` read the account again · `m` measure writable layers · `c` cancel a
measurement · `w` export… · `d` remove… · `X` prune… · `S` or `Esc` close.
**Repository** (`B`): `↑` `↓` `PgUp` `PgDn` move between tags · `Tab` next page · `/` filter ·
`e` edit the repository name · `d` build times of the rows on screen · `c` cancel them · `Enter`
pull the highlighted build by its digest · `r` ask again · `Esc` step out of a line, then clear
the filter, then close (`B` closes too).
**Events**: `Space` (or `f`) holds the view: the pane shows a still copy of the history
(1000 events) with a cursor on the newest, and its title reads `‖ held +N` as new events keep
being recorded and lists keep refreshing underneath. `↑` `PgUp` `g` hold a live view too. The
movement keys move the cursor; `↓` past the newest, `G`, `End` or `Space` follow again. `Enter`
on a held event goes to its object — the container (exec events too), the image reference it
names, the volume, the network (connect and disconnect too) — in its own list, clearing a filter
that hides it; an object not listed yet is waited for two seconds, and a marker or a daemon,
plugin or swarm event says there is nothing to go to. Going there changes nothing and works
read-only. **Jobs**: `Enter` read a job's full output · `c` cancel a running job · `x` clear
finished ones.

### The shell

`e` on a running container shows the command it will run — by default
`sh -c 'command -v bash >/dev/null 2>&1 && exec bash || exec sh'`, which you can edit — and on
Enter hands your terminal to that process: a proper session, raw keys, resizes forwarded. When
it exits (or the container stops) you are back at the desk exactly where you left it. If the
container has no shell, you are back immediately with the daemon's reason. If the daemon ever
stops answering in the middle of a session, **Ctrl-] three times** ends it locally and brings
the desk back.

## The safety model

Docker Desk changes and deletes real things, so it is deliberate by design rather than by your
being careful.

- **No single keystroke changes anything.** Every action key opens a dialog that names the
  exact object (name and ID), the exact effect in plain words, and the daemon it will be sent
  to. Nothing has been sent at that point.
- **A daemon on another machine is called remote where you decide.** The dialog's "daemon"
  line carries the same tag as the header — `[REMOTE ssh]`, `[REMOTE tcp, NO TLS]` — and is
  never cut short to fit. This is all that stands between a context left pointing at production
  and a change made there; `--read-only` is the stronger guard when you only mean to look.
- **Confirmation scales with what cannot be undone.**
  - start, stop, restart, pause, unpause, kill, connect, disconnect, stop project → press `y`.
    Enter does nothing.
  - anything that deletes — remove, prune, remove project → type the word shown (`remove` or
    `prune`) and press Enter.
  - pull, new volume, new network, shell → you type the name yourself; Enter submits.
  - copy into a container (`i` in Files) → the confirmation names the source, its size, the
    destination and that same-named files there are replaced; press `y`. Copying *out* changes
    nothing on the daemon: Enter on its form writes only the one local path it shows, never
    one that exists, and is allowed in read-only mode.
  - new container → the form restates the whole configuration and any pull it needs; Enter
    submits, never before the form has been on screen long enough to read.
  - **a selection** (`Space` / `a` in the list), whatever the action → type the verb *and the
    count* shown, e.g. `stop 3` or `remove 7`, and press Enter. One key never consents to
    many objects, and the number is what you have to have read.
- **A selection shows every object, and every object it leaves out.** The dialog lists each
  object that will be acted on — one request each, never forced — and under **Not included**
  each selected object the single-object rules would not allow, with the reason: a running
  container you asked to remove, a volume, image or network something uses, a container that
  is already stopped, one that has vanished. Nothing is left out silently, and nothing
  refused is sent. If one request fails the rest still run, and the job reports how many
  failed (`1 of 3 failed`) with the daemon's message for each.
- **Only what you press after reading counts.** A dialog ignores `y`, the typed word and Enter
  for its first quarter of a second, and if you press anything else while a confirmation is
  still being prepared, that action is dropped instead of popping up under your fingers. Keys
  typed ahead — or typed for the filter — can never consent to something you have not seen.
  Esc always works at once.
- **A confirmation acts on what it showed.** The object is captured by its daemon ID when the
  dialog opens. If the list reshuffles underneath, the dialog still means the same object.
  Things addressed by a *name* — an image tag, a volume — are re-checked just before the
  request: if the tag has moved to another image or the volume was recreated, nothing is
  removed.
- **Nothing is ever forced.** If the thing is in use, the dialog says by what and offers no way
  to proceed: a running container must be stopped first; an image, volume or network that any
  container references — *running or stopped* — is in use. If something becomes in use after
  you confirm, the daemon refuses and its message is shown.
- **A prune shows exactly what it will destroy, and destroys exactly that.** The dialog lists
  every object; `Tab` switches between the default scope (dangling images / anonymous volumes)
  and "all unused" and recomputes the list. After you confirm, each listed object is deleted
  individually and without force. Docker's own prune endpoints are never called, so nothing
  that appeared after the preview can be swept up with it.
- **Errors never end the session.** A daemon error is shown — in the pane it concerns, in the
  status line, and kept with the job — and the desk stays usable.

Two deliberate differences from the Docker CLI, both on the cautious side:

- `X` on Networks keeps networks used only by *stopped* containers. `docker network prune`
  deletes them.
- "Remove project" removes the project's containers and nothing else. It names the networks
  and volumes that carry the project's label and leaves them where they are. This is not
  `compose down`.

## Compose, honestly

A "project" is every container carrying the same `com.docker.compose.project` label — the one
Docker Compose already puts on them. You can stop the group or remove the group. There is no
`up`, `down`, `build`, `pull` or scaling, and no compose binary is ever run.

The file is read for one thing: **Declared**, the project's second Details tab, sets what the
file says now against the containers that run. It reads **one file, on request, on this
machine; never executed, never interpolated, never rendered**:

- **On request.** Highlighting a project, refreshing, or opening the tab shows only what the
  labels say — project, containers, working directory, every config path, whether the daemon is
  local, the newest container's creation — and the words *nothing has been read*. No file is
  opened, or even stat'ed, before `Enter`. `Enter` reads, as a job, each file the
  `config_files` label names, in the order it names them.
- **On this machine.** Only when the daemon is reached over a local Unix socket. Over `ssh://`
  or `tcp://` the path belongs to the machine that ran compose, and a file at the same path here
  may be another file: the tab says so and reads nothing.
- **Bounded.** A regular file, at most 1 MiB, at an absolute path. It is parsed and discarded;
  the desk keeps the project's `name:` and, per service, its `image:` and `profiles:`.
- **Never rendered.** No byte of the file is shown, logged or written, except as part of the
  comparison (a service name, a declared image). The labels a path comes from can be set by an
  image, so the read is built to disclose nothing else.
- **Never interpolated.** An image with `$` in it (`app:${TAG:-latest}`) is a template: the row
  says so and is not compared. `.env` is not read and defaults are not applied.

Rows, disagreements first: **declared only** (a service no container carries), **differs** (a
literal image that is not the one running: tag, digest or repository), **present only** (a
container the file does not declare), **not compared** (with the reason), **match**. A service
declaring `profiles` is never called missing — no label records which profiles were enabled;
containers left by `docker compose run` (`oneoff`) are not compared; a service that builds its
image or inherits one through `extends` is not compared; a file with `include:` does not call a
container undeclared. Scale, ports, volumes, environment, command and health checks are not
compared at all. Each container's recorded `com.docker.compose.config-hash` is shown as
evidence and never recomputed. With several files, service names are their union and an image
comes from the last file that declares one.

The file is compared **as it is now**: nothing records what it said when the containers were
created. Its write time is reported against the newest container's creation as a fact
("written 4s before these were created"); when it is later, both times are shown and the tab
says that alone does not make it wrong.

When nothing is compared, the tab says why in one sentence and shows no rows:

| Refusal | When |
| --- | --- |
| `Not compared: --no-compose-files forbids reading compose files.` | the desk was started with `--no-compose-files` |
| `Not compared: the daemon is reached over ssh` (or `tcp`) `…` | the daemon is not a local Unix socket |
| `Not compared: the labels record no config file.` | the containers carry no `config_files` label |
| `Cannot be compared: there is no file at … on this machine.` | not found |
| `Cannot be compared: … is not a regular file.` | a directory, device, pipe or socket |
| `Cannot be compared: … is larger than 1 MiB, …` | over 1 MiB |
| `Cannot be compared: … is not a compose file the desk can read: …` | unparseable: not YAML, more than one document, an explicit tag such as `!reset`, a field of the wrong shape (the reason names a line, never its content) |
| `Cannot be compared: … cannot be opened: …` | permission denied, or another error from the system |
| `Cannot be compared: the recorded path … is not absolute.` | a relative path in the label |

`--no-compose-files` removes the whole class, as `--no-registry` removes registry requests;
`--read-only` does not, since the comparison only reads.

## What it does not do

No view across daemons: one is focused at a time, and nothing acts on two at once — no copying
images or volumes between them. No per-daemon read-only flag, and nothing about the session is
saved for the next run. No Windows named pipes. No Swarm, no compose
lifecycle — a compose file is read only for Declared, on request, and never run or
interpolated (see [Compose, honestly](#compose-honestly)) — no image builds or pushes, no Kubernetes. It creates containers only from its seven-field form
(no networks, users, limits or TTY; no build, no compose `up`), never reconfigures one, and does
copies files in or out only from the Files view, one path at a time. The build cache is accounted for in Space but never removed: the desk
has no builder, and calls no prune endpoint.

Good to know:

- Under a log flood the Logs view is a window onto the newest 5,000 lines (it tells you how
  many older ones scrolled out); very long lines are split at 2 KiB.
- Live statistics are kept for at most 32 running containers at a time; the highlighted one is
  always included, the rest show `n/c`.
- The Info tab shows how many environment variables a container has, not their values; the
  Inspect tab shows everything.
- Quitting cancels running jobs. A request the daemon had already accepted may still complete.
- Each `ssh://` daemon of a session holds one `ssh` process of its own; there is no cap on how
  many daemons a session holds. The strip shows as many as fit at the terminal's width, always
  the focused one, and counts the rest (`‹2 more`, `3 more›`).
- Over SSH: a server whose `sshd_config` forbids forwarding (`AllowStreamLocalForwarding no`,
  `AllowTcpForwarding no`, `PermitOpen none`, `DisableForwarding yes`) cannot be used, and the
  desk says so at launch. The remote user's own Docker context is not consulted — name the
  socket in the URL if it is not `/var/run/docker.sock`.
- Over SSH, a dropped link is noticed within about half a minute and retried every 1 to 15
  seconds. While it is down the desk shows what it last knew.
- Closing the terminal window ends the desk as `q` does: its `ssh` and the tunnel's socket go
  with it. If the desk is killed outright (`kill -9`) it cannot clean up: on Linux its `ssh` is
  still ended with it, on macOS it keeps running until the link drops or you end it, and an
  empty `docker-desk-ssh-…` directory stays in the temporary directory. The tunnel's socket is
  usable only by you in every case.
- Over TCP, `HTTP_PROXY`/`HTTPS_PROXY` apply, as they do for the Docker CLI — except to the
  shell, whose connection the client library makes directly.
- At 80×24 a confirmation with much to say scrolls its middle — the effects, the warnings, the
  list — and keeps what it acts on, the daemon, the prompt and the input on screen.
- TLS client keys protected by a passphrase are not supported (nor by the Docker CLI).
- Space attributes shared bytes only where an image's history maps onto its layers and its
  layer sizes add up to the daemon's size for it. With the containerd image store (Docker
  29's default) they do not: the daemon counts each layer's compressed content as well as its
  unpacked files, and the history gives only the second. Checked read-only against this
  machine's Docker 29: the history mapped for all 18 images and added up for none. There the
  view still shows every daemon figure, which images share layers, and each image's "alone";
  "together" and a prune's "frees" are then "at least" the members' unique sizes, and say
  why. In the classic store, removing an image can also delete untagged parent images; their
  layers are part of its shared size and are not claimed as freed.
- Changes compares the image a container runs with the image its reference names *here*: it
  never asks a registry. No file-level difference is made — a layer's build instruction says
  what it changed. Environment variables are compared by name only.
- Findings do not consult a vulnerability database: Go module versions are listed, not judged.
  zstd-compressed layers cannot be walked (the standard library has no decoder) and are
  reported as unreadable. Stack canaries are judged only for dynamically linked executables.
- Repository (`B`): no search, no `_catalog`, nothing from `hub.docker.com` — one named
  repository at a time, from its registry's `tags/list`. At most 1000 tags are held, the first
  in the registry's own order; the order shown is text order, never date. A registry that pages
  without a `Link` header shows its first page only. Build times are read only on `d`, for the
  rows on screen. A pinned pull leaves no local tag (the Engine API sends the digest in place of
  it); nothing retags, deletes or pushes a tag.
- Files (`F`): the tree never reads a container's archive or a file's contents, and there is no
  file-level difference. A container's side is the daemon's change list over its image's tree:
  a changed file's own size and what an added directory holds are not read. No file is
  previewed, and nothing is compared between a container and its image beyond what the daemon
  lists. The index keeps at most 200,000 entries; zstd layers are named as unread.
- Copies (`o`, `i`): one path between one container and this machine — not between containers
  or daemons, no globs, no local file browser, no copy from an image. An existing local path is
  never replaced and there is no option to force it. Ownership, times (on copy out), extended
  attributes and ACLs are not preserved; hard links are sent as separate files.
- Packages: only dpkg, apk and rpm databases are read as the installed side — not npm, pip or
  Go modules, whose SBOM entries are "not checkable". rpm is read from `rpmdb.sqlite` only (rpm
  4.16 and later); an older Berkeley DB or SUSE's NDB database is reported as unknown, as is one
  with a non-empty write-ahead log. With zstd layers, the installed side is unknown and the SBOM
  is still listed. Only the first SBOM attestation of an image is compared.

## Trying it safely

```sh
make sandbox
```

starts `fake-dockerd` — an in-memory engine that speaks the real Docker API, seeded with two
compose projects (`shop`'s compose file is written to `/tmp/docker-desk-sandbox-compose`, with
one of every Declared row; `blog`'s is absent on purpose, so the refusal shows too), containers in every state, a container that logs 20,000 lines a second, one
without a shell, one that takes four seconds to stop, dangling images, unused volumes, an
orphan network, `report-job`, whose tag `acme/report:1.4` has since moved to a newer build
(Containers › Changes), and three images on `registry.sandbox.invalid` — a name that never
resolves, so Images › Upstream falls back to the fake daemon, which says `acme/web:2.3` moved,
`acme/cron:1` is current and `acme/legacy:0.9` is gone, with no network at all — plus a
registry of its own, in plain HTTP on a free loopback port, holding `acme/api` (105 tags,
arm64-only `3.29.0-rc.1` among them) and three local images of it, so `B` on
`127.0.0.1:<port>/acme/api:3.28.2` shows `here`, `here, the tag moved upstream` and
`same build as stable`, and `Enter` on `3.28.3` pulls it by digest — plus, for Files (`F`),
`nginx:1.27`, whose archive has a deep tree, a file a later layer replaces, a whiteout, an opaque
directory, a symlink, a hard link, a device, a FIFO, a traversal-shaped name and a **hostile name**
(an ANSI colour, a newline and a byte that is not UTF-8, drawn escaped), and `shop-web-1`, made
from it, which has written **the four change cases** — a file added, a file changed, a file
deleted and a directory added (with an entry beneath it the desk does not read) — and opens the
desk on it. The header shows the sandbox socket. Nothing there
is real; break whatever you like. (The real `docker` CLI works against it too:
`DOCKER_HOST=unix:///tmp/docker-desk-sandbox.sock docker ps`.)

`./bin/fake-dockerd -changes` adds a container for every case Changes has nothing to compare
(pinned to a digest, created from an ID, a tag that no longer resolves, another platform, an
unreadable layer) and one rebuilt on another base.

To see how a daemon that is not a local socket is presented, without having one:

```sh
make build
./bin/fake-dockerd -tcp 127.0.0.1:2375 &
./bin/docker-desk -H tcp://127.0.0.1:2375      # header: [tcp, NO TLS]
```

## How it was built, and why

| | |
| --- | --- |
| [`docs/DECISIONS.md`](docs/DECISIONS.md) | every design decision, the alternatives, and the reason — including the ones forced by reality during the build |
| [`docs/JOURNAL.md`](docs/JOURNAL.md) | the build in order: what was made, checked, found and changed |
| [`docs/TESTING.md`](docs/TESTING.md) | how it is proven without touching real containers, the measurements, and what is *not* proven |
| [`openspec/changes/archive/2026-11-03-docker-desk-workspace/`](openspec/changes/archive/2026-11-03-docker-desk-workspace/) | the spec it was built to: proposal, eleven capability specs, design, tasks |
| [`openspec/changes/archive/2026-11-03-remote-daemons-and-themes/`](openspec/changes/archive/2026-11-03-remote-daemons-and-themes/) | the second change — remote daemons and themes: proposal, spec deltas, design, tasks |
| [`openspec/changes/archive/2026-11-05-disk-space/`](openspec/changes/archive/2026-11-05-disk-space/) | the fifth change — the Space view: proposal, spec, design, tasks |
| [`openspec/changes/archive/2026-11-05-details-search/`](openspec/changes/archive/2026-11-05-details-search/) | the tenth change — search in the Details pane: proposal, spec, design, tasks |
| [`openspec/changes/archive/2026-11-09-repository-browser/`](openspec/changes/archive/2026-11-09-repository-browser/) | the twelfth change — the repository browser and the pinned pull: proposal, spec and delta, design, tasks |

One thing from TESTING.md belongs here too: by design, **no mutating request from this program
had ever been sent to a real Docker daemon when it was handed over** — the build was required
not to touch real containers. Its requests are encoded by Docker's own client library and were
exercised against a fake engine that Docker's own CLI was used to validate. The first `stop`
you confirm on a real container will be the first one ever. `make sandbox` and `--read-only`
exist so that you can decide when that is.

The same holds for remote daemons, and more so: SSH, TLS and TCP were each driven end to end
with real implementations — your machine's `ssh` against a real `sshd`, real certificates, a
context store written by the real `docker` CLI — but the daemon at the far end was always the
fake one, on this machine. **The desk has never been connected to a Docker daemon on another
computer.** `docker-desk -H ssh://… --check`, then `--read-only`, is the careful way to make
that first contact.

## Layout of the code

```
cmd/docker-desk        the program: resolve the daemon, open the way to it, check it, then open the desk
cmd/fake-dockerd       the sandbox engine
internal/dockerctx     which daemon is meant, how it is reached (socket, SSH, TCP/TLS); why it cannot be
internal/sshtunnel     the one supervised ssh process that carries an ssh:// daemon's connections
internal/session       the session's daemons: which is focused, adding (never asking), removing, notices
internal/engine        the only door to the daemon (Docker's client library behind a narrow interface)
internal/plan          what a mutation will do, decided by pure code before anything is sent
internal/space         which bytes images share, and what removing a set of them frees (pure)
internal/changes       what differs between the image a container runs and the one its tag names (pure)
internal/registry      the registry client: references, credentials, challenges, tags/list, manifests
internal/freshness     whether a local image's tag names another build upstream (Images › Upstream)
internal/repository    the repository browser's tags, join, session cache and build-time batch
internal/forensics     what an image's own archive holds, read as one stream: findings, the SBOM's
                       packages, the installed dpkg/apk/rpm databases (with its own SQLite reader)
internal/report        evidence files: pure serializers (json, SPDX 2.3, CycloneDX 1.5, text) of plain
                       structs, and the one function that writes a file (0600, never a partial one)
internal/report/collect  what a view or --report has computed, as report's structs: the one conversion
internal/jobs          background jobs and their ledger
internal/live          bounded buffers and stream supervision for logs, stats and events
internal/shell         the interactive exec session and the terminal hand-off
internal/ui            the desk: panes, keys, dialogs
internal/fakeengine    an in-memory Docker Engine speaking the real HTTP API (tests, sandbox)
internal/sshtest       a throwaway sshd on a loopback port (tests)
internal/testguard     the switch that keeps tests away from real daemons
internal/ptyscreen     runs a program on a pseudo-terminal with an emulated screen (tests)
internal/e2e           the real binary, driven like a person would
scripts/               cross-check of the fake engine with the real docker CLI
```

```sh
make test         # everything, against the fake engine only
                  #   (DOCKER_DESK_REQUIRE_SSHD=1 make test — fail instead of skip where sshd cannot run)
make race         # the same under the race detector
make crosscheck   # the real docker CLI against the fake engine
make help         # the rest
```
