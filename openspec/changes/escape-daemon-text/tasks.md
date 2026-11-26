# Tasks

## 1. The rule, the fixture, and the line reset

- [ ] 1.1 Add a test-only hostile container to `internal/fakeengine` whose label, mount
      destination, compose project label, working directory, command and image tag each carry a
      different hostile value: SGR 8 (conceal), a newline, a C1 control, invalid UTF-8, a
      right-to-left override, and a plain colour sequence. Keep it out of the sandbox seed.
      Verify with a test that prints each field of the seeded container and finds its sequence
      in the engine's decoded result.
- [ ] 1.2 State the escaping rule in the package documentation of `internal/ui` and
      `internal/plan`: a value is escaped when it becomes drawn text, never when it becomes a
      request or a comparison; one layer escapes a value, once. Verify `go doc
      ./internal/ui` and `go doc ./internal/plan` print the rule.
- [ ] 1.3 Terminate every guarded frame line with SGR reset after guarding, so a colour or
      conceal sequence cannot carry into the next line. Verify a new test draws a line carrying
      `\x1b[8m` followed by a clean line and finds the clean line unchanged, and find that test
      failing before this task.
- [ ] 1.4 Keep the desk's own frames unchanged apart from the reset, and update
      `TestGuardLeavesTheDesksOwnFramesAlone` to assert the reset explicitly. Verify it passes
      and that no other frame test needed a change beyond the reset.

## 2. Details › Info, all five kinds

- [ ] 2.1 Escape the container Info fields — name, image, status, health, compose project and
      service, command, working directory, user, port host addresses, mount sources and
      destinations, labels and the daemon's error prose — escaping the value before applying
      its tone. Verify a new test highlights the hostile container and finds the literal escaped
      text in Details › Info at 120 and 80 columns, with no conceal sequence in the raw frame.
- [ ] 2.2 Escape the image Info fields — references, labels, the upstream registry and
      repository, and the platform line. Verify the same assertion over a hostile image tag and
      label.
- [ ] 2.3 Escape the volume and network Info fields — name, driver, mountpoint, labels,
      subnets, gateways and the attached container names. Verify the same assertion over the
      hostile volume and network labels.
- [ ] 2.4 Escape the compose Info fields — project name, working directory, service name and
      the recorded configuration paths. Verify the same assertion over the hostile project
      label.
- [ ] 2.5 Add one sentence to the README's guarantee for drawn names, naming Details, the
      confirmations, the Events pane and Findings. Verify the sentence matches the tests'
      wording and that the README's existing limit about the frame guard does not contradict it.

## 3. Events and Findings

- [ ] 3.1 Escape the Events pane's drawn fields — the action, its outcome (`exitCode`,
      `signal`), the object name, the network container and the marker — while `eventTone` and
      the column choices keep reading the raw values. Verify a hostile event is drawn escaped
      and is still toned as its raw action requires.
- [ ] 3.2 Escape a finding's path, evidence, reason, instruction and unknown text in the row
      and in the detail block. Verify with an archive fixture whose entry name carries an escape
      that the row and the detail block draw the escaped text, and that the existing secret
      masking still hides the value.

## 4. Plan display strings

- [ ] 4.1 Escape the step descriptions that skip it — container creation, connect, disconnect
      and the generic fallback — and escape the plan's target name, job target, effects,
      warnings, blockers, skipped entries and refusal messages built with `na(...)`. Verify
      plan tests assert the escaped text and that `executeRequest` still sends the raw
      identifiers and names.
- [ ] 4.2 Add the once-escaped assertion: a label containing a backslash is drawn with exactly
      two backslashes, not four, in Details and in a step description. Verify the test fails if
      the plan escapes a string the dialog already escaped.

## 5. Dialogs, pickers, status line and jobs

- [ ] 5.1 Draw the confirmation's step names and details from the plan's already-escaped
      strings without escaping them again, and escape the dialog's own ident line. Verify the
      frames for a container removal, a stop of several containers, a re-create, a copy-in and
      a prune over the hostile fixture.
- [ ] 5.2 Escape the picker rows and descriptions — connect and disconnect, the daemon picker,
      the image-copy destination list — and the forms that redraw operator input. Verify each
      picker frame over the hostile fixture, and the copy-in form after typing a path with an
      escape.
- [ ] 5.3 Escape every status-line message and job log line built from an object's name, and
      keep the job's own target and daemon names escaped once. Verify by driving a job over the
      hostile fixture and reading the status line and the job dialog.

## 6. Record and integration

- [ ] 6.1 Correct `docs/DECISIONS.md` AT16: what survives the guard is colour, attribute and
      conceal; it cannot move, invent or forge, and it can hide. Record this round's decisions
      beside it. Verify the entry names conceal, states the limit, and agrees with the README
      sentence added in 2.5.
- [ ] 6.2 Run the full check and verify it passes: `gofmt -l ./cmd ./internal`, `go vet ./...`,
      `go test ./...`, `make race`, `./scripts/cli-crosscheck.sh`, and `make e2e` with
      `docs/screens` unchanged.
- [ ] 6.3 Have the change reviewed independently against one threat model — a daemon of the
      session may be hostile — and verify every finding is fixed with a test or stated as a
      limit in DECISIONS.

## Workflow follow-up

- Archive the change after the review findings are resolved and the project's review
  requirements are satisfied.
- Verify the archived result, including that `openspec validate --all --strict` accepts the new
  `terminal-safety` spec's Purpose.
