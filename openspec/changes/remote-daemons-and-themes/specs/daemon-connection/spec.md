## MODIFIED Requirements

### Requirement: Single daemon from the current Docker context
The program SHALL talk to exactly one daemon for its whole lifetime, chosen with the same
precedence as the Docker CLI: the `--context` flag, then the `--host` flag, then the
`DOCKER_HOST` environment variable, then the `DOCKER_CONTEXT` environment variable, then
`currentContext` in the Docker CLI configuration file, then the default context. Giving both
`--host` and `--context` SHALL be refused. The default context SHALL mean what it means to the
Docker CLI: the endpoint in `DOCKER_HOST` when that is set, otherwise the local socket
`unix:///var/run/docker.sock` — or `tcp://localhost:2376` when `DOCKER_TLS_VERIFY` or
`DOCKER_TLS` is set and no endpoint is named anywhere. The program SHALL NOT offer any way to
switch daemon while running.

#### Scenario: No configuration at all
- **WHEN** no flag is given, neither `DOCKER_HOST` nor `DOCKER_CONTEXT` is set and the Docker configuration names no current context
- **THEN** the program uses `unix:///var/run/docker.sock` and reports the context as `default`

#### Scenario: DOCKER_HOST is set
- **WHEN** `DOCKER_HOST` is set and no flag is given
- **THEN** the program uses that endpoint regardless of `DOCKER_CONTEXT` and of any configured context

#### Scenario: A named context is current
- **WHEN** the Docker configuration names a current context and nothing with higher precedence is given
- **THEN** the program uses that context's stored endpoint and reports that context's name

#### Scenario: The named context does not exist
- **WHEN** the selected context name has no stored metadata
- **THEN** the program exits with a non-zero status and a message naming the missing context and what selected it, without opening the workspace

#### Scenario: --host names the daemon
- **WHEN** the program is started with `--host URL` (or `-H URL`)
- **THEN** it uses that endpoint, whatever `DOCKER_HOST` and the configured context say, and reports that the flag chose it

#### Scenario: --context names the context
- **WHEN** the program is started with `--context NAME` (or `-c NAME`)
- **THEN** it uses that context's endpoint even if `DOCKER_HOST` is set, and reports that the flag chose it

#### Scenario: Both flags
- **WHEN** the program is started with both `--host` and `--context`
- **THEN** it exits with a non-zero status saying the two cannot be used together, without opening the workspace

#### Scenario: The default context with DOCKER_HOST set
- **WHEN** the program is started with `--context default` while `DOCKER_HOST` names an endpoint
- **THEN** it uses the endpoint in `DOCKER_HOST`, as `docker --context default` does, and says that both chose it

#### Scenario: TLS asked for and no endpoint named
- **WHEN** `DOCKER_TLS_VERIFY` is set, and no flag, `DOCKER_HOST` or context names an endpoint
- **THEN** it uses `tcp://localhost:2376` with TLS, as the Docker CLI does, and says why

### Requirement: Unreachable daemon is reported before the workspace opens
The program SHALL verify that the daemon answers before taking over the terminal. If it does
not answer within a bounded time, the program SHALL print a plain message that names the
endpoint, how it was chosen, how it is reached and the reason, exit with a non-zero status, and
SHALL NOT open the workspace. The reason SHALL identify the actual failure where it can be
known, and SHALL list the possibilities rather than name one when it cannot.

#### Scenario: Socket does not exist
- **WHEN** the endpoint is a local socket whose file does not exist
- **THEN** the program prints that the Docker daemon is not reachable, the endpoint, and that the socket does not exist, then exits non-zero without drawing the workspace

#### Scenario: Permission denied
- **WHEN** the local socket exists but the user may not open it
- **THEN** the message names the endpoint and states that permission was denied

#### Scenario: Daemon does not answer
- **WHEN** the socket accepts no connection or the daemon does not answer within the time limit
- **THEN** the message names the endpoint and the failure, and the program exits non-zero without drawing the workspace

#### Scenario: SSH cannot log in
- **WHEN** the endpoint is `ssh://` and the ssh client fails to connect or authenticate
- **THEN** the message carries what ssh itself reported (for example permission denied, host key verification failed, connection refused) and the program exits non-zero without drawing the workspace

#### Scenario: No ssh client
- **WHEN** the endpoint is `ssh://` and no ssh client program can be started
- **THEN** the message says the ssh program was not found and how another one can be named

#### Scenario: SSH connects but the daemon's socket cannot be opened
- **WHEN** the SSH connection is established but the remote socket cannot be reached
- **THEN** the message says which it is when that can be determined — no socket at that path, not a socket, the remote user may not open it, or it lies in a directory the remote user may not look into — and otherwise lists the remaining possibilities, including an SSH server that forbids forwarding to sockets

#### Scenario: SSH connects and the daemon does not answer
- **WHEN** the SSH connection is established and nothing answers on the remote socket within the time limit
- **THEN** the message says that nothing answered, and does not present it as a refusal by the SSH server

#### Scenario: A fault in the client's own set-up
- **WHEN** a certificate file named by the configuration exists but cannot be used
- **THEN** the message names the file and the fault, and does not say that the daemon is unreachable

#### Scenario: TCP port refuses or the name does not resolve
- **WHEN** the endpoint is `tcp://` and the connection is refused, the host name cannot be resolved, or nothing answers in time
- **THEN** the message names the address and which of those happened

#### Scenario: TLS mismatch
- **WHEN** TLS is configured but the port does not speak TLS, or the port requires TLS and none is configured
- **THEN** the message says which side is missing TLS and how to supply or drop it

#### Scenario: Certificate problem
- **WHEN** the daemon's certificate is not signed by the configured authority or is not valid for the host name, or the daemon refuses or requires a client certificate
- **THEN** the message says which of those it is

#### Scenario: Interrupted while connecting
- **WHEN** the operator presses Ctrl+C before the workspace has opened, including while ssh is waiting for an answer
- **THEN** the program exits with a non-zero status, the ssh process it started is ended, and the workspace is not opened

#### Scenario: The message is safe to print
- **WHEN** the endpoint or context name contains control characters
- **THEN** the program refuses it without writing those characters to the terminal

### Requirement: Daemon identity is always visible
While the workspace is open it SHALL permanently display the daemon endpoint as the operator
named it, the context name, and the engine's name and version, in every layout and behind no
keystroke. When the daemon is not reached through a local socket, the workspace SHALL
additionally display, next to the endpoint, how it is reached. Neither the connection state
nor that indication SHALL be dropped or cut off at any supported terminal size; of the endpoint
itself, the beginning and the end SHALL always be shown in the header and the whole of it in
every dialog and in the help. Error messages SHALL name the daemon by that endpoint and never
by an internal address used to reach it.

#### Scenario: Normal operation
- **WHEN** the workspace is open
- **THEN** the endpoint, context name and engine version are visible on screen

#### Scenario: A dialog is open
- **WHEN** a confirmation or any other overlay is displayed
- **THEN** the endpoint remains visible, and a confirmation for a mutation additionally repeats the endpoint in its own text

#### Scenario: SSH endpoint
- **WHEN** the endpoint is `ssh://` to another machine
- **THEN** the header shows the `ssh://` endpoint — not the local end of the tunnel — and a tag saying it is remote and reached over ssh

#### Scenario: Endpoint longer than the header or the dialog
- **WHEN** the endpoint is too long for the header, or for a dialog's width
- **THEN** in the header the middle of the endpoint gives way — its beginning and its end stay, as do the connection state and the tags; in a dialog the endpoint continues on the next line and the context and the tag are shown in full

#### Scenario: Long context name
- **WHEN** the context name is too long to share a dialog line with the tag
- **THEN** each is given a line of its own and neither is cut off

#### Scenario: A confirmation on the smallest terminal
- **WHEN** a confirmation is opened at 80 columns by 24 rows, on any endpoint, with more to say than fits
- **THEN** what is acted on, the daemon, the prompt, the input and the keys stay on screen, and the effects, warnings and listed objects between them scroll

#### Scenario: Errors while the SSH link is down
- **WHEN** a request fails on an `ssh://` endpoint
- **THEN** the message names the `ssh://` endpoint and not the local end of the tunnel

### Requirement: Connection loss is visible and recoverable
If the connection to the daemon is lost while the workspace is open, the program SHALL show a
disconnected state next to the endpoint, keep the last known data on screen marked as stale,
keep retrying, and resume live updates when the daemon answers again. When the daemon is
reached through SSH and it is the SSH connection that failed, the program SHALL additionally
say so and why, and SHALL re-establish the connection without asking anything on the terminal.

#### Scenario: Daemon stops while the desk is open
- **WHEN** the event stream ends or requests start failing with connection errors
- **THEN** the header shows that the daemon is disconnected and the desk keeps responding to keys

#### Scenario: Daemon comes back
- **WHEN** the daemon becomes reachable again
- **THEN** the disconnected indication clears, lists are refreshed and the event stream resumes without restarting the program

#### Scenario: The SSH connection drops
- **WHEN** the ssh connection carrying an `ssh://` endpoint ends while the workspace is open
- **THEN** the header shows the disconnected state, the status line says the SSH link is down and gives ssh's reason, and the desk keeps responding to keys

#### Scenario: The SSH connection can be re-established
- **WHEN** the remote machine accepts connections again and logging in needs no answer from the operator
- **THEN** the connection is re-established and live updates resume without restarting the program

#### Scenario: Re-establishing would need an answer
- **WHEN** logging in again would require a passphrase, a password or a host-key confirmation
- **THEN** nothing is asked on the terminal; the desk stays disconnected, says why, and remains usable for looking at the last known data and for quitting

## REMOVED Requirements

### Requirement: Local daemons only
**Reason**: The owner asked for the desk to reach remote daemons over SSH and other protocols.
The requirement existed to keep a context left pointing at a remote host from causing changes
on the wrong machine; that risk is now addressed by announcing a remote daemon as remote
wherever the daemon is named (see "A remote daemon is announced as remote").
**Migration**: A context or `DOCKER_HOST` that points at `ssh://` or `tcp://` no longer stops
the program — it connects. To keep working on the local daemon, select it explicitly
(`docker context use default`, `--context default`, or unset `DOCKER_HOST`). `--read-only`
remains the way to look at any daemon with a guarantee that nothing is changed.

## ADDED Requirements

### Requirement: Supported endpoint transports
The program SHALL accept endpoints of the forms `unix://PATH`,
`ssh://[USER@]HOST[:PORT][/PATH]` and `tcp://HOST[:PORT]`, and SHALL refuse any other form at
launch with a message naming the endpoint and the supported forms. An `ssh://` endpoint without
a path SHALL mean the socket `/var/run/docker.sock` on the remote machine. A `tcp://` endpoint
without a port SHALL mean port 2375, with or without TLS, as it does to the Docker CLI, and the
port in use SHALL be shown by `--check` and in the help. An endpoint SHALL be refused when it
contains a password or control characters. The user, host, port and socket path of an `ssh://`
endpoint SHALL be refused unless they consist of characters that mean nothing to the ssh
client, to a shell or to a terminal: in particular a host or user that could be read as an
option, and a socket path containing `$`, `%`, `:`, `\` or whitespace, which the ssh client
would expand or split.

#### Scenario: Unsupported scheme
- **WHEN** the resolved endpoint uses `npipe://`, `fd://`, `http://`, `https://` or any other scheme
- **THEN** the program exits with a non-zero status, prints the endpoint and the supported schemes, and does not open the workspace

#### Scenario: SSH endpoint naming the socket
- **WHEN** the endpoint is `ssh://user@host/run/user/1000/docker.sock`
- **THEN** the program reaches the socket at `/run/user/1000/docker.sock` on that host

#### Scenario: Host that looks like an option
- **WHEN** the endpoint's host or user begins with `-`
- **THEN** the program refuses it at launch and starts no ssh process

#### Scenario: Password in the URL
- **WHEN** the endpoint is `ssh://user:password@host`
- **THEN** the program refuses it at launch and says passwords in the URL are not supported

#### Scenario: Socket path the ssh client would expand
- **WHEN** the endpoint is `ssh://host/${SOME_VARIABLE}` or carries a `%` token in its path, plainly or percent-encoded
- **THEN** the program refuses it at launch, starts no ssh process, and nothing from the environment is sent anywhere

#### Scenario: TCP endpoint without a port, with TLS
- **WHEN** the endpoint is `tcp://host` and TLS is configured
- **THEN** the program dials port 2375, where the Docker CLI dials, and shows that port

### Requirement: SSH endpoints go through the operator's ssh client
For an `ssh://` endpoint the program SHALL use the ssh client program installed on the
operator's machine, so that the operator's SSH configuration, keys, agent and known hosts apply.
It SHALL carry every connection to the daemon over a single SSH connection. Anything ssh needs
to ask the operator SHALL be asked before the workspace takes over the terminal and never
after — neither by the ssh process the program starts nor by any process that one starts, such
as the connection to a jump host. The local end of the connection SHALL be usable only by the
operator, and SHALL be removed, and the ssh process ended, when the program exits, including
when its terminal is closed.

#### Scenario: Key with a passphrase
- **WHEN** the endpoint is `ssh://` and logging in requires a passphrase, with the program started from a terminal
- **THEN** ssh asks for it on the ordinary screen, and the workspace opens only after it has been answered

#### Scenario: Not started from a terminal
- **WHEN** the endpoint is `ssh://` and the program is not attached to a terminal (for example `--check` in a script)
- **THEN** ssh is run so that it fails instead of waiting for an answer

#### Scenario: Many live streams
- **WHEN** the workspace follows logs, the event stream and statistics for dozens of containers over an `ssh://` endpoint
- **THEN** all of them are carried by one ssh process and one authenticated connection

#### Scenario: Quit
- **WHEN** the operator quits the workspace opened on an `ssh://` endpoint
- **THEN** no ssh process started by the program is left running and its local socket and directory are gone

#### Scenario: The terminal is closed
- **WHEN** the program receives a hang-up while the workspace is open on an `ssh://` endpoint
- **THEN** it ends as if it had been quit: no ssh process, socket or directory is left

#### Scenario: Reconnecting through a jump host
- **WHEN** the host is reached through a jump host whose login needs a typed answer, and the link drops after the workspace has opened
- **THEN** nothing is asked on the terminal, keys typed still reach the workspace, and the workspace says why the link is down

#### Scenario: A client configured to go to the background
- **WHEN** the operator's ssh configuration sends ssh to the background after logging in
- **THEN** the program keeps it in the foreground, or — where the client cannot be told to — reports that ssh went to the background instead of reporting an unexplained exit

#### Scenario: Another ssh program
- **WHEN** `DOCKER_DESK_SSH` names an ssh program and leading arguments
- **THEN** that program is run with those arguments in place of `ssh`

### Requirement: TCP endpoints use TLS as the Docker CLI does
For a `tcp://` endpoint named directly (by `--host` or `DOCKER_HOST`), the program SHALL use
TLS when `DOCKER_TLS_VERIFY` or `DOCKER_TLS` is set to a non-empty value: with the server's
certificate verified against `ca.pem` when `DOCKER_TLS_VERIFY` is set, and unverified when only
`DOCKER_TLS` is; certificates SHALL be looked for in `DOCKER_CERT_PATH`, or the Docker
configuration directory when it is unset, and `cert.pem` with `key.pem` SHALL be presented as
the client certificate when both exist. For a `tcp://` endpoint of a named context, the program
SHALL use TLS when the context stores any TLS material or sets `SkipTLSVerify`, using that
material and that setting, and SHALL ignore the TLS environment variables. Otherwise the
connection SHALL be plain TCP. The program SHALL never connect with weaker verification than
these rules give, and SHALL never connect without TLS where they give TLS.

#### Scenario: Verified TLS from the environment
- **WHEN** `DOCKER_HOST` is `tcp://…`, `DOCKER_TLS_VERIFY` is set and `DOCKER_CERT_PATH` holds `ca.pem`, `cert.pem` and `key.pem`
- **THEN** the program connects with TLS, verifies the daemon against that CA and presents that client certificate

#### Scenario: Verification asked for without a CA
- **WHEN** `DOCKER_TLS_VERIFY` is set and there is no `ca.pem` in the certificate directory
- **THEN** the program exits with a non-zero status naming the missing file, without connecting

#### Scenario: TLS from a context
- **WHEN** the selected context stores a CA and a client certificate for its `tcp://` endpoint
- **THEN** the program connects with TLS using them, whatever the TLS environment variables say

#### Scenario: A context written by the Docker CLI
- **WHEN** a context was created with `docker context create NAME --docker host=tcp://…,ca=…,cert=…,key=…`
- **THEN** the program reaches the same daemon through it as the Docker CLI does

#### Scenario: No TLS configured
- **WHEN** the endpoint is `tcp://` and neither the environment nor the context asks for TLS
- **THEN** the program connects without TLS and says, wherever it names the daemon, that the connection is not encrypted

#### Scenario: A context storing an incomplete key pair
- **WHEN** a context stores a client certificate without its key, and nothing else
- **THEN** the program uses TLS without a client certificate, as the Docker CLI does, and does not fall back to plain TCP

#### Scenario: A stalled TLS peer
- **WHEN** a TLS endpoint accepts a connection and then sends nothing, while a shell is being opened
- **THEN** the attempt is given up within the time allowed for starting a shell and the workspace returns with a message

### Requirement: A remote daemon is announced as remote
When the endpoint is not a local unix socket, the program SHALL show, in the header, in every
confirmation, form and picker, in the help and in the output of `--check`, how the daemon is
reached: over ssh, over TLS, over TLS without verifying the daemon, or over TCP without TLS. It
SHALL call the daemon remote unless the endpoint's address is this machine's loopback address.
A connection that is not encrypted or not verified SHALL be shown in the colour and wording
used for danger. A daemon reached through a local unix socket SHALL carry no such indication.

#### Scenario: Confirmation on a remote daemon
- **WHEN** a confirmation opens while the endpoint is `ssh://ops@prod-1`
- **THEN** its text names the endpoint and carries the tag `REMOTE ssh`

#### Scenario: Plain TCP
- **WHEN** the endpoint is `tcp://10.0.0.5:2375` without TLS
- **THEN** the header and every confirmation carry `REMOTE tcp, NO TLS`

#### Scenario: Loopback address
- **WHEN** the endpoint is `tcp://127.0.0.1:2375`
- **THEN** the tag names the transport and its safety without the word `REMOTE`

#### Scenario: Local socket
- **WHEN** the endpoint is `unix:///var/run/docker.sock`
- **THEN** the header and the dialogs look exactly as they did before this change

#### Scenario: Terminal too small
- **WHEN** the terminal is too small for the desk and the endpoint is remote
- **THEN** the message that replaces the desk still names the endpoint and carries the tag
