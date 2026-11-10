# Tasks

## 1. The parser and the comparison

- [ ] 1.1 Add `go.yaml.in/yaml/v3` to `go.mod`; run `go mod tidy` and verify `go list -m go.yaml.in/yaml/v3` prints v3.0.6 and `go build ./...` still passes
- [ ] 1.2 Add `internal/composefile` with `Parse([]byte) (File, error)` decoding `name` and `services[name]{image, profiles}` into typed strings, refusing a document with a `!` tag, more than one document, or a decode error; verify with unit tests including a fixture shaped like `/home/ridwan/workspace/gitlab/compose.yaml` (template image, 20-line block scalar containing a flow map) and a malformed file that must fail rather than return a partial `File`
- [ ] 1.3 Add `Read(path string) ([]byte, error)` to `internal/composefile`: `os.Stat` first, refuse anything that is not a regular file or exceeds 1 MiB, and verify each refusal — directory, FIFO, oversized file, missing file — is a distinct error a test can name
- [ ] 1.4 Add a `recover` net around `Parse` and a fuzz or mutation test that calls `Parse` without it, so a fault the checks miss fails the test rather than being caught; verify the test fails when the net is removed
- [ ] 1.5 Add `Compare(File, []Actual) Result` with verdicts match, differs, declared only, present only, not compared; verify with table tests for a literal image, a `${` template, a service declaring `profiles`, a `oneoff` container, and a service declared in two files where the last declaration wins

## 2. Snapshot, plan and flags

- [ ] 2.1 Add a `composefile.Actual` projection to `internal/plan` built from the snapshot: service name, container name, image reference, normalised reference, `Created`, `config-hash`, excluding `oneoff` containers; verify with a test over the seeded `shop` project
- [ ] 2.2 Add `NoComposeFiles bool` to `ui.Options` and a `--no-compose-files` flag in `cmd/docker-desk`, text mirroring `--no-registry`, and verify `docker-desk --help` lists it and a unit test threads it into `Options`
- [ ] 2.3 Record the new flag and the read in `internal/engine/types.go`'s label constants — `LabelComposeConfigHash` and `LabelComposeOneOff` — and verify a test reads both from an inspect fixture

## 3. The Declared tab

- [ ] 3.1 Add `tabDeclared` to `details.go`'s tab list (`KindCompose: {tabInfo, tabDeclared}`) and render the header only: project, containers, working directory, every config path, whether the daemon is local, and the file's write time against the newest container's creation; verify a behaviour test sees `nothing has been read` and that no reader was called before `Enter`
- [ ] 3.2 Render the comparison once a result exists: rows ordered declared only, differs, present only, not compared, match, then by name, with the counts in the header and the two container times when the file is newer; verify a behaviour test sees each verdict from a fixture file
- [ ] 3.3 Wire `Enter` on the tab to a job (`compare with the compose file`) that reads, parses and compares, and `c` to cancel; verify a UI test asserts one job starts on the first `Enter`, none on a second before it finishes, and that the job lines never contain a byte of the file
- [ ] 3.4 Render each refusal as one sentence and no rows: not found, not a regular file, over 1 MiB, unparseable, remote transport, `--no-compose-files`; verify one UI test per cause
- [ ] 3.5 Add "compare with the compose file" to the Compose action menu so the tab is reachable without knowing `]`, and verify the menu test lists exactly stop, remove and compare

## 4. Sandbox and end-to-end

- [ ] 4.1 Add a `-compose-dir` flag to `cmd/fake-dockerd`, write fixture files there in `SeedDemo` and point the seeded labels at them: `shop` with a template image, a profiled service and a service that has no container; `blog` whose file is deliberately absent; verify `make sandbox` shows both a populated comparison and the "cannot be compared" refusal
- [ ] 4.2 Extend `internal/e2e` with a scripted session that opens `shop` › Compose › Declared, asks for the comparison, sees the template row not compared and the missing service, and exports the screens; verify `make e2e` regenerates them under `docs/screens`

## 5. The promise, in the documents and the UI

- [ ] 5.1 Replace the sentence in `internal/ui/details.go` that promises compose files are never read with what the tab now does, and verify no Go file still contains "never reads or runs compose files"
- [ ] 5.2 Rewrite README "Compose, honestly" and the "no compose files" line in "What it does not do", stating: one file, on request, on this machine, never executed, never interpolated, never rendered; verify the new section names each refusal the tab can produce
- [ ] 5.3 Add the round-fourteen decision to `docs/DECISIONS.md` that supersedes D15 — same option table, the failure-mode argument for `go.yaml.in/yaml/v3`, the hostile-label mitigation, and the `--no-compose-files` flag — and verify D15 carries a pointer to it
- [ ] 5.4 Update the Keys section of `README.md` and `internal/ui/view.go` help for the new tab and its `Enter`/`c`, and verify `?` in the desk shows them

## 6. Integration

- [ ] 6.1 Run `make test`, `make race` and `make vet` and confirm all pass
- [ ] 6.2 Run the desk read-only against the real daemon, open `gitlab` › Compose › Declared and confirm the service set matches, the templated image is reported as not compared, and the file's write time is four seconds before the container's creation; record the outcome in the change
- [ ] 6.3 Confirm no mutating request and no file write is possible from the tab: `make real-readonly-check` plus a grep that `internal/composefile` opens files only for reading

## Workflow follow-up

- Sync the `compose-projects` delta into the main spec and archive the change once the review requirements are satisfied.
