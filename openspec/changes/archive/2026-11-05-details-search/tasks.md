# Tasks

## 1. Pure logic

- [x] 1.1 `internal/search`: case-insensitive find on stripped text; marking ranges in styled text.
- [x] 1.2 `internal/keypath`: key path per line of indented JSON.

## 2. The desk

- [x] 2.1 `textView` find: memoised matches, row map, pending seek, highlighting.
- [x] 2.2 The bar, keys, Esc ladder, `n`/`N`, clearing on a new object; nothing leaks while typing.
- [x] 2.3 Logs filter: incremental memo, follow and scroll-back in the filtered set, status row.

## 3. Tests and documentation

- [x] 3.1 Unit and UI tests: offsets, wrapped seek, wrap-around, filter and follow, stripped
  matching, Esc ladder, no leak, contrast and attributes in every theme.
- [x] 3.2 README keys, `?` overlay, DECISIONS round ten, JOURNAL; `go build`, `go vet`, `go test`.
