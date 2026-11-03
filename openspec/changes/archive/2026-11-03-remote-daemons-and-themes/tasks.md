## 1. Endpoint resolution

- [x] 1.1 Add `--host`/`-H` and `--context`/`-c` with the Docker CLI's precedence, refusing both together; verify with table-driven resolution tests (`TestResolve`) and through the launch sequence (`TestRefusals`, `TestCheckOverTCP`)
- [x] 1.2 Resolve TLS for `tcp://` endpoints: `DOCKER_TLS_VERIFY` / `DOCKER_TLS` / `DOCKER_CERT_PATH` for a host named directly, the context store's TLS files and `SkipTLSVerify` for a named context; verify with `TestResolveTLS`, including "verifying without a CA is an error" and "a context ignores the environment"
- [x] 1.3 Parse `unix://`, `ssh://[user@]host[:port][/path]` and `tcp://host[:port]`, refusing other schemes, passwords, and hosts or users that ssh would read as an option; verify with `TestTarget`
- [x] 1.4 Describe how an endpoint is reached (remote or not, the tag, whether it is insecure); verify with `TestLink`

## 2. TCP and TLS

- [x] 2.1 Give the engine a TLS client configuration (CA, client certificate, skip-verify) read once at construction; verify with `TestTLSMaterialIsCheckedAtConstruction`
- [x] 2.2 Let the fake daemon listen on loopback TCP, plain and with mutual TLS from a throwaway CA; verify with `TestOverTCPAndTLS` (requests, a long-lived stream, a mutation, the hijacked exec) and `TestTLSIsActuallyEnforced`
- [x] 2.3 Dial a TCP endpoint directly before the client is used and classify TCP and TLS failures; verify with `TestCheckTCP`, `TestClassifyTCP` and, end to end, `TestCheckOverTLS`
- [x] 2.4 Cross-check the context store against Docker itself: have the real `docker` CLI create a context with certificates and reach the fake through it, then require the desk to; verify with `TestContextWrittenByDockerItself`

## 3. SSH

- [x] 3.1 Start `ssh -N -L` to a private local socket, wait until it answers, and report a failure in ssh's own words; verify with `TestCommandLine`, `TestForwardsToTheRemoteSocket`, `TestOpenFailsInSSHsOwnWords`
- [x] 3.2 Supervise the tunnel: restart with a growing pause, always in batch mode, say why it is down, never outlive `Close`; verify with `TestSupervisorReconnects`, `TestSupervisorKeepsTryingAndSaysWhy`, `TestInteractiveOnlyForTheFirstConnection`, `TestCloseEndsSSHAndRemovesTheSocket`, `TestCloseWhileReconnecting`
- [x] 3.3 Pass ssh's messages to the terminal only until the tunnel is up; verify with `TestDetach` and under the race detector
- [x] 3.4 Explain a tunnel that is up while the remote socket cannot be opened by asking the remote machine, and list the possibilities when that cannot decide; verify with `TestExplain` and `TestRealSSHExplain`
- [x] 3.5 Run a throwaway unprivileged sshd for tests; verify the tunnel against it with the real ssh client: `TestRealSSH` (40 concurrent streams, a mutation, a shell, socket permissions), `TestRealSSHFailures`, `TestRealSSHReconnect`
- [x] 3.6 End ssh when the desk is killed outright (Linux); verify with `TestKilledDeskTakesItsSSHAlong`, confirmed to fail with the mechanism removed

## 4. Launch sequence

- [x] 4.1 Wire resolve → parse → open the way → ping → info → terminal for the three transports, with Ctrl+C handled until the desk owns the terminal; verify in process with `TestCheckLocalSocket`, `TestCheckOverTCP`, `TestCheckOverTLS`, `TestCheckOverSSH`, and on a pseudo-terminal with `TestCtrlCWhileSSHConnects`
- [x] 4.2 Show how the daemon is reached in `--check` and in every "not reachable" message; verify with the same tests and `TestDoesNotOpenWithoutADaemon`, `TestSSHFailuresDoNotOpenTheWorkspace`

## 5. The desk

- [x] 5.1 Show the remote tag in the header, every confirmation, form and picker, the help and the too-small screen, and nothing for a local socket; verify with `TestRemoteDaemonIsAnnouncedEverywhere`, `TestLocalSocketLooksAsItAlwaysDid`
- [x] 5.2 Never drop or truncate the endpoint's beginning, the connection state or the tag; verify with `TestRemoteTagIsNeverDropped`, `TestLongEndpointNeverHidesTheTag`
- [x] 5.3 Say why an SSH link is down, once per reason; verify with `TestFallenTunnelExplainsItself`

## 6. Themes

- [x] 6.1 Build every style from a palette; ship `dark` (the original colours), `light`, `ansi`, six named palettes and `mono`; verify with `TestDefaultThemeIsTheOriginalPalette`, `TestThemeByName`
- [x] 6.2 Select with `--theme` / `DOCKER_DESK_THEME`, defaulting by terminal background, refusing unknown names; verify with `TestThemeIsAcceptedEitherWay`, `TestRefusals`, `TestHelp`
- [x] 6.3 Cycle with `T`; verify with `TestThemeKeyCycles` (a letter in the filter, nothing under an overlay, bound to no action, no request sent)
- [x] 6.4 Prove a theme changes colour only, and that no colour means no colour; verify with `TestAThemeChangesColourAndNothingElse`, `TestHighlightIsVisibleInEveryTheme`, `TestThemesWithoutColour`
- [x] 6.5 Check every palette for contrast against the background it is meant for and adjust the colours that fail; verify with `TestThemesAreLegible`

## 7. Proof

- [x] 7.1 Extend the test guard to TCP addresses and SSH tunnels; verify with `TestGuardAllowsOnlyTheFakesOwnListeners`, `TestGuardRefusesTunnelsToRealSockets`
- [x] 7.2 Drive the real binary on a pseudo-terminal over each transport — act, watch the answer, open a shell, lose and regain the connection, quit, leave nothing behind; verify with `TestRemoteOverTCP`, `TestRemoteOverTLS`, `TestRemoteOverSSH`
- [x] 7.3 Prove that ssh's question is asked before the desk opens and never under it; verify with `TestSSHAsksOnTheTerminalBeforeTheDesk`
- [x] 7.4 Drive the themes through the real binary; verify with `TestThemes`
- [x] 7.5 Have the new ways in reviewed independently against the safety rules; verify each finding is fixed with a test or kept with a recorded reason (docs/DECISIONS.md, R13)

## 8. Review and hardening

- [x] 8.1 Give a non-interactive ssh no controlling terminal and no graphical prompt, so that a jump host's ssh or a ProxyCommand cannot ask under the desk; verify with `TestBatchRunsHaveNoTerminal` and the reviewer's scenario, `TestJumpHostNeverAsksUnderTheDesk`, confirmed to fail without the fix
- [x] 8.2 Hold the user, host, port and socket path of an SSH endpoint — and the tunnel's local path — to characters that mean nothing to ssh, a shell or a terminal; verify with `TestCheckDestination`, `TestUnsafeDestinationNeverReachesSSH`, the added cases of `TestTarget` (confirmed to fail under the first version's rule), and `TestResolve` for control characters
- [x] 8.3 Lay a confirmation out so that it always fits: identity and prompt fixed, the middle scrolling; verify with `TestConfirmationsFitTheSmallestTerminal`, confirmed to fail under the old layout
- [x] 8.4 Keep both ends of a long endpoint in the header and never cut the context or the tag in a dialog; verify with `TestRemoteTagIsNeverDropped` (connected and disconnected), `TestLongContextNeverHidesTheTag`
- [x] 8.5 Bound the exec attach by its context and give the TLS transport a handshake timeout; verify with `TestAttachIsBoundedOverTLS`, confirmed not to return without the fix
- [x] 8.6 Follow the CLI where the first version did not: `--context default` and `DOCKER_HOST`, `tcp://localhost:2376` when TLS is asked for and nothing is named, port 2375 with TLS, TLS for a context storing any TLS file; verify with `TestResolve`, `TestResolveTLS`, `TestTarget`, `TestDefaultContextFollowsTheCLI`, and by running `docker` itself against closed ports
- [x] 8.7 Correct the explanations that blamed the wrong cause (a silent daemon, a hidden directory, a local bind failure, TLS hints for a context, the set-up headline) and name the endpoint instead of the tunnel's socket in errors; verify with `TestCheckOverSSH`, `TestRealSSHExplain`, `TestHintsFitTheFailure`, `TestClassifyTCP`, `TestCheckOverTLS`, `TestErrorsNameTheDaemonNotTheTunnel`
- [x] 8.8 Keep ssh in the foreground (`ForkAfterAuthentication=no`, dropped for a client that predates it) and report one that went to the background; verify with `TestSSHThatPredatesAnOption`, `TestSSHThatGoesToTheBackground`
- [x] 8.9 Clean up on a hang-up, during launch and while the desk is open; verify with `TestHangUpCleansUp`
- [x] 8.10 Close the gaps in the tests themselves: a Ctrl+C stand-in that ignores the terminal's interrupt, a leftover check that asserts, `DOCKER_DESK_REQUIRE_SSHD`, a harness environment that names no real daemon, a guard whose temporary directory cannot be redefined; verify with `TestCtrlCWhileSSHConnects`, `TestLaunchLeavesNothingBehind`, and a full run with `DOCKER_DESK_REQUIRE_SSHD=1`

## 9. Finish

- [x] 9.1 Update `README.md` (endpoints, remote daemons, failure messages, themes, keys, limits); verify the runnable commands were run as written
- [x] 9.2 Record every decision with its alternatives and cost in `docs/DECISIONS.md` (R1–R14, T1–T5), the build in `docs/JOURNAL.md`, and the proof and its limits in `docs/TESTING.md`
- [x] 9.3 Final gate: `go vet ./...`, `go test ./...`, `go test -race ./...`, `make build`, `make crosscheck`, and `openspec validate remote-daemons-and-themes --strict`
