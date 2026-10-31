<!-- file: docs/decisions/2026-10-30-deluge-swap-slot-dir.md -->
<!-- version: 1.0.0 -->
<!-- guid: df30686e-f82c-4757-8b73-c9c804b11036 -->
<!-- last-edited: 2026-10-30 -->

# Deluge swap notify: leaving version slot folder

## Decision log

- Chose: add `deluge.NotifyDelugeMoveStorageToDir(torrentHash, dir)`, which sends `dir` to
  Deluge unchanged. `NotifyDelugeMoveStorage` (file path) now delegates to it after
  `filepath.Dir`. The leaving-version call in `NotifyDelugeAfterVersionSwap` passes the slot
  folder to the dir variant.
- Rejected: passing a file path inside the slot (needs a file lookup that can be empty or stale).
- The resume handler reaches Deluge through `NotifyDelugeAfterVersionSwap`, so it is fixed by
  the same change; a server test pins it.

## Audit of NotifyDelugeMoveStorage callers (all pass a file path)

- `NotifyDelugeAfterOrganize` (internal/deluge/integration.go): newPath is the organized book file.
- `NotifyDelugeAfterVersionSwap`, entering version: bookFilePath is the active book file.
- `internal/server/deluge_integration.go` re-export: passes through; no production callers.
- Tests only: internal/server/deluge_integration_test.go.
