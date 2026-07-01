<!-- file: docs/agent-tasks/provenance-hash-chain/ORCHESTRATION.md -->
<!-- version: 1.0.0 -->
<!-- guid: c362af66-2ee8-4998-9a52-f7264dc989be -->
<!-- last-edited: 2026-07-01 -->

# Orchestration — provenance-hash-chain

## Waves

There is exactly one wave. Both tasks are independent and run in parallel.

### Wave 1 (parallel — no shared files)

- **TASK-01** (`agent/hc-download-hash-field`, HASH-CHAIN-1) — touches `internal/database/store.go` (BookFile struct), `internal/database/pebble_store_mark_import.go` (Deluge import population), `internal/database/pebble_store.go` (read/write helpers), and a new API endpoint under `internal/server/handlers/audiobooks/`.
- **TASK-02** (`agent/hc-integrity-alert`, HASH-CHAIN-3) — touches `internal/plugins/maintenance/` (new file `integrity_check.go` + `plugin.go` registration).

These two tasks do not modify the same files or the same struct fields (T01 adds `DownloadHash`; T02 only *reads* `FileHash`/`OriginalFileHash`/`PostMetadataHash`, all of which already exist). There is a shared file risk only if both tasks touch `internal/database/store.go` or `internal/plugins/maintenance/plugin.go` — T01 does NOT touch `plugin.go`, and T02 does NOT touch `store.go`'s `BookFile` struct, so there is no overlap. Both branches can be created, worked, and merged in any order; whichever merges second should `git rebase origin/main` before opening its PR (standard hygiene, not a real conflict resolution).

## Dependency graph

```
TASK-01 (independent)
TASK-02 (independent)
```

No task depends on another. No serialization required. Merge order does not matter.

## Worker protocol (applies to every task)

1. Each task brief is self-contained — a worker should be able to execute it with zero other context beyond the repo checkout.
2. Workers MUST re-verify the file:line anchors in the "Background" section with the provided `grep` commands before editing — line numbers drift as the codebase changes.
3. Workers MUST run the exact build+test commands listed in "How to test" before opening a PR.
4. Workers MUST bump file version headers per `.standards/instructions/file-headers.md` on every file they touch (or create) — this is the last step in every task's "Step-by-step" section.
5. PR merge uses `gh pr merge <number> --rebase` (this repo is rebase/FF only, no squash), per the repo's `CLAUDE.md`.
6. Worktrees are removed after merge: `git worktree remove .worktrees/<branch-slug> && git worktree prune`.
