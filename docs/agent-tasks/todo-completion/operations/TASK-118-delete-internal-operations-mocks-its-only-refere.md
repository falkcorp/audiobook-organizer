<!-- file: docs/agent-tasks/todo-completion/operations/TASK-118-delete-internal-operations-mocks-its-only-refere.md -->
<!-- version: 1.0.0 -->
<!-- guid: e77f8da8-a227-4174-a7a5-5c31a6eb74df -->
<!-- last-edited: 2026-08-21 -->

# TASK-118 — Delete internal/operations/mocks — its only referencer is dead, permanently-untagged, currently-broken test code (TODO.md L4743)

**Priority:** P2 · **Effort:** S · **Recommended subagent:** Sonnet-class · operations subagent · **Why:** The deletion itself is mechanical, but deciding what to do with the one (broken, dead) referencer requires reading server_import_file_mocks_test.go closely enough to confirm nothing salvageable is being thrown away — not a pure delete-and-done. · **Depends on:** none · **Wave:** 1

Source: `TODO.md` line 4743 as of commit 46628240 (later edits shift lines) — re-find it with `grep -n -F "`internal/operations/mocks` — 206 generated lines," TODO.md` (line numbers drift; the grep is built from the line's own text). Scope file: `scope-09.json`.

## ⛔ START HERE (do this first, exactly)

```bash
# ⛔ START HERE — do not touch code before this block succeeds
REPO=/Users/jdfalk/repos/github.com/jdfalk/audiobook-organizer   # adjust to your clone
git -C "$REPO" fetch origin
git -C "$REPO" worktree add "$REPO/.worktrees/operations-118-delete-internal-operations-mocks-its-only-refere" -b agent/operations-118-delete-internal-operations-mocks-its-only-refere origin/main
cd "$REPO/.worktrees/operations-118-delete-internal-operations-mocks-its-only-refere"
git rebase origin/main
# Go task: do NOT run 'go work init .' — it breaks the build (ambiguous genproto imports)
```

(Protocol also in `docs/agent-tasks/ORCHESTRATION.md` — the inline block above is authoritative for this task.)

## Goal

Delete internal/operations/mocks/ and its .mockery.yaml entry, and delete internal/server/server_import_file_mocks_test.go — the latter is dead code from before commit ceb125ef removed the v1 queue it tests, is gated behind a build tag CI never sets, and does not even compile under that tag today.

## Background (verify before editing)

- internal/server/handlers/operations/handler_test.go imports a DIFFERENT package (internal/server/handlers/operations/mocks, pkgname operationsmocks — .mockery.yaml:247-251) which is unrelated and must NOT be touched.
- TestImportFile_WithMockMetadata_CreateAuthorAndBook (the test inside server_import_file_mocks_test.go) may cover real, still-relevant behavior (import-file-with-AI-metadata) that has no coverage elsewhere — before deleting, grep for a differently-named test covering the same handler to confirm nothing unique is lost, or port the useful assertions into an existing, currently-compiling test file instead of a blind delete.

- **Re-verify these anchors before editing** — line numbers drift; a zero-hit grep means STOP and report:
  ```bash
  wc -l internal/operations/mocks/*.go   # 206 mock_progress_reporter.go — dir contents
  grep -rln "operations/mocks\"" --include="*.go" .    # 2 hits: internal/server/server_import_file_mocks_test.go and (a different, unrelated package) internal/server/handlers/operations/mocks — confirm the second is a DIFFERENT import path (internal/server/handlers/operations/mocks, not internal/operations/mocks) before concluding — the only outside importer
  grep -n "go:build mocks" internal/server/server_import_file_mocks_test.go   # 1 hit at L6 — the importer is gated behind a build tag
  grep -rn "tags mocks\|tags=mocks\|build mocks" Makefile .github/workflows/*.yml   # 0 hits — the mocks build tag is never invoked by Makefile or CI — that build tag is never referenced in Makefile or CI workflows
  go vet -tags mocks ./internal/server/... 2>&1 | grep -n 'undefined: queuemocks.NewMockQueue'   # 1 hit — vet: internal/server/server_import_file_mocks_test.go:98:26: undefined: queuemocks.NewMockQueue — the file is currently broken even under its own tag
  git log --oneline --all --grep="delete v1 queue"   # 1 hit: ceb125ef — the removal commit that orphaned it
  ```

### Reuse — don't invent

- No existing helper identified; do not invent new constants for concepts that already have a name — grep first.

## Step-by-step

1. Read internal/server/server_import_file_mocks_test.go in full to see what TestImportFile_WithMockMetadata_CreateAuthorAndBook (and any sibling tests in the file) actually assert.
2. Check whether an existing, currently-built test file already covers the same import-file-with-metadata path without the `mocks` build tag: `grep -rln "ImportFile" internal/server/*_test.go | grep -v server_import_file_mocks_test.go`.
3. If coverage is duplicated elsewhere: delete internal/server/server_import_file_mocks_test.go outright (`git rm`).
4. If NOT duplicated: port the test's assertions into a new or existing non-tag-gated test file, replacing the dead `queuemocks.NewMockQueue(t)` dependency with whatever the current queue abstraction is (find it via `grep -rn "type.*Queue interface" internal/operations/`), then delete the old file.
5. Delete internal/operations/mocks/mock_progress_reporter.go and its directory.
6. In .mockery.yaml, remove the `internal/operations:` block's `ProgressReporter:` entry (~L25-31) — check first whether ProgressReporter is used unmocked elsewhere (`grep -rln "ProgressReporter" --include="*.go" internal/operations/`) so removing the mock doesn't strand a still-needed interface without a double.
7. Run `go build -tags mocks ./... ` and `go vet -tags mocks ./...` to confirm the whole `mocks`-tagged build is clean after the deletions (not just the untagged default build).

Then, always:
- Keep the change purely removal — do not touch adjacent code, do not reorder imports beyond the formatter, do not change signatures unless a step above says so explicitly.
- Bump the file header (`version` + `last-edited: 2026-08-21`) on every file you touch; keep existing guids. New files get a fresh guid (`uuidgen | tr A-Z a-z`).
- Add a changelog fragment `changelog.d/20260821_operations_118.md` (NO file header; format per `changelog.d/README.md`: a `### Fixed|Changed|Added` heading, a `####` title, one paragraph).
- Do NOT edit `TODO.md` — the coordinator closes the source item in one commit per wave (every brief in a wave would otherwise collide on it). In your final report, state the exact `TODO.md` line text to check off. Never add new TODO items directly — use a `todo.d/` fragment (no header).

### Edge-case semantics (conservative defaults — treat unknown as unknown, never as disqualifying)

- internal/server/scan_edge_cases_test.go and other *_test.go files may ALSO carry `//go:build mocks` — grep -rln "go:build mocks" --include="*_test.go" . before finishing, to make sure this class of dead-and-broken-under-its-own-tag test isn't larger than this one file.

## Tests

- If porting: the ported test must compile and pass under the DEFAULT build (no `mocks` tag) so it actually runs in CI, unlike its predecessor.

Anti-over-suppression: N/A

## How to test

```bash
go build ./... && go vet ./... && go test ./internal/operations/mocks/... ./internal/server/... -count=1
```
Do NOT use `make ci` as the gate: it is red on `main` from 10 pre-existing staticcheck findings unrelated to this task. Run `staticcheck ./<changed-pkg>/...` and fix only findings in files you touched. A failing test in a package you did not change is not yours — report it, do not fix it.

## Acceptance criteria

- [ ] `
- [ ] t
- [ ] e
- [ ] s
- [ ] t
- [ ]  
- [ ] -
- [ ] d
- [ ]  
- [ ] i
- [ ] n
- [ ] t
- [ ] e
- [ ] r
- [ ] n
- [ ] a
- [ ] l
- [ ] /
- [ ] o
- [ ] p
- [ ] e
- [ ] r
- [ ] a
- [ ] t
- [ ] i
- [ ] o
- [ ] n
- [ ] s
- [ ] /
- [ ] m
- [ ] o
- [ ] c
- [ ] k
- [ ] s
- [ ] `
- [ ]  
- [ ] r
- [ ] e
- [ ] t
- [ ] u
- [ ] r
- [ ] n
- [ ] s
- [ ]  
- [ ] n
- [ ] o
- [ ] n
- [ ] -
- [ ] z
- [ ] e
- [ ] r
- [ ] o
- [ ] ;
- [ ]  
- [ ] g
- [ ] o
- [ ]  
- [ ] b
- [ ] u
- [ ] i
- [ ] l
- [ ] d
- [ ]  
- [ ] .
- [ ] /
- [ ] .
- [ ] .
- [ ] .
- [ ]  
- [ ] e
- [ ] x
- [ ] i
- [ ] t
- [ ] s
- [ ]  
- [ ] 0
- [ ] ;
- [ ]  
- [ ] g
- [ ] o
- [ ]  
- [ ] v
- [ ] e
- [ ] t
- [ ]  
- [ ] -
- [ ] t
- [ ] a
- [ ] g
- [ ] s
- [ ]  
- [ ] m
- [ ] o
- [ ] c
- [ ] k
- [ ] s
- [ ]  
- [ ] .
- [ ] /
- [ ] .
- [ ] .
- [ ] .
- [ ]  
- [ ] e
- [ ] x
- [ ] i
- [ ] t
- [ ] s
- [ ]  
- [ ] 0
- [ ] ;
- [ ]  
- [ ] `
- [ ] g
- [ ] r
- [ ] e
- [ ] p
- [ ]  
- [ ] -
- [ ] r
- [ ] n
- [ ]  
- [ ] '
- [ ] q
- [ ] u
- [ ] e
- [ ] u
- [ ] e
- [ ] m
- [ ] o
- [ ] c
- [ ] k
- [ ] s
- [ ] '
- [ ]  
- [ ] -
- [ ] -
- [ ] i
- [ ] n
- [ ] c
- [ ] l
- [ ] u
- [ ] d
- [ ] e
- [ ] =
- [ ] '
- [ ] *
- [ ] .
- [ ] g
- [ ] o
- [ ] '
- [ ]  
- [ ] .
- [ ] `
- [ ]  
- [ ] r
- [ ] e
- [ ] t
- [ ] u
- [ ] r
- [ ] n
- [ ] s
- [ ]  
- [ ] 0
- [ ]  
- [ ] h
- [ ] i
- [ ] t
- [ ] s
- [ ] ;
- [ ]  
- [ ] `
- [ ] g
- [ ] r
- [ ] e
- [ ] p
- [ ]  
- [ ] -
- [ ] n
- [ ]  
- [ ] '
- [ ] P
- [ ] r
- [ ] o
- [ ] g
- [ ] r
- [ ] e
- [ ] s
- [ ] s
- [ ] R
- [ ] e
- [ ] p
- [ ] o
- [ ] r
- [ ] t
- [ ] e
- [ ] r
- [ ] '
- [ ]  
- [ ] .
- [ ] m
- [ ] o
- [ ] c
- [ ] k
- [ ] e
- [ ] r
- [ ] y
- [ ] .
- [ ] y
- [ ] a
- [ ] m
- [ ] l
- [ ] `
- [ ]  
- [ ] r
- [ ] e
- [ ] t
- [ ] u
- [ ] r
- [ ] n
- [ ] s
- [ ]  
- [ ] 0
- [ ]  
- [ ] h
- [ ] i
- [ ] t
- [ ] s
- [ ] .
- [ ] Anti-over-suppression: N/A
- [ ] Edge cases above hold (nil/empty/unknown never disqualify; a test asserts it where a filter/guard is added).
- [ ] Gate green: `go build ./... && go vet ./... && go test ./internal/operations/mocks/... ./internal/server/... -count=1` exits 0; `go vet`/lint clean.
- [ ] File headers bumped on every changed file (`grep -n "last-edited: 2026-08-21" <file>` hits for each).
- [ ] Changelog fragment present: `test -f changelog.d/20260821_operations_118.md`.

## Commit message

```
refactor(operations): Delete internal/operations/mocks — its only referencer is de (TODO L4743)

<why the change was needed; what it protects; what it deliberately does NOT change>

Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>
```

## Done

STOP — report done with exact counts (`COMPLETED: n — ...` / `REMAINING: n — ...` / `BLOCKED: n — ...`); the coordinator owns push/PR/merge. Do NOT run `git push`, `gh pr`, or any merge.

## Idempotency / Rollback

If the removed symbol/file is already ABSENT at HEAD (re-run the re-verify greps above: zero hits = already removed) AND the replacement is present, the removal is already done — run acceptance instead. Rollback = `git revert` the commit to restore the file + its call sites; no data or schema is touched.

## Coordinator notes

The item as literally written ('effectively unreferenced') undersells what was found: the sole referencer isn't just unreferenced-in-spirit, it's dead code that doesn't even compile under the only tag that would include it. Worth a wider grep (see edge_cases) for siblings before closing this out as a one-file fix.
