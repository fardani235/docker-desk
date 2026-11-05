# Tasks

## 1. Reads

- [x] 1.1 `ImageFacts` (inspect, history), environment names only; read-only coverage list.
- [x] 1.2 Fake daemon: per-image config, fixtures shared by inspect, history and archive.

## 2. Comparison

- [x] 2.1 Pair and refusals: digest, image ID, unresolved, same image, platform.
- [x] 2.2 Lineage and instruction mapping; config difference.
- [x] 2.3 Content: packages by ecosystem, findings by identity; unreadable layers refuse.

## 3. The tab

- [x] 3.1 Containers › Changes after Info; at most four requests on opening; no archive before Enter.
- [x] 3.2 Enter as a cancellable job reusing Findings' cache; c cancels; second open instant.
- [x] 3.3 Refusal text and the restart/re-create line in every state.

## 4. Tests and documentation

- [x] 4.1 Unit, UI (80×24, 120 columns, every theme, read-only, no environment value anywhere) and e2e tests.
- [x] 4.2 Demo moved tag and `-changes` seed; screen captures 33 and 34.
- [x] 4.3 README, DECISIONS (round eight), TESTING, help; `make test`, `make race`.
