<!-- file: docs/agent-tasks/todo-completion/audiobooks/TASK-003-fix-the-author-path-post-filter-to-treat-nil-isp.md -->
<!-- version: 1.0.0 -->
<!-- guid: f9027e9e-22c8-4c95-8b68-e576bcc924c0 -->
<!-- last-edited: 2026-08-21 -->

# TASK-003 — Fix the author-path post-filter to treat nil IsPrimaryVersion as primary, matching storage's default (TODO.md L3884)

**Priority:** P1 · **Effort:** S · **Recommended subagent:** Sonnet-class · audiobooks subagent · **Why:** One-line fix but on a prod-data-shaped read path with subtle nil semantics -- worth a careful reviewer, not pure mechanical. · **Depends on:** TASK-002 · **Wave:** 4 · **REVIEW-CRITICAL (prod-data path): PR stays open for the owner; never weak-tier**

Source: `TODO.md` line 3884 as of commit 46628240 (later edits shift lines) — re-find it with `grep -n -F "Decide the single meaning of a nil `IsPrimaryVersi" TODO.md` (line numbers drift; the grep is built from the line's own text). Scope file: `scope-06.json`.

## ⛔ START HERE (do this first, exactly)

```bash
# ⛔ START HERE — do not touch code before this block succeeds
REPO=/Users/jdfalk/repos/github.com/jdfalk/audiobook-organizer   # adjust to your clone
git -C "$REPO" fetch origin
git -C "$REPO" worktree add "$REPO/.worktrees/audiobooks-003-fix-the-author-path-post-filter-to-treat-nil-isp" -b agent/audiobooks-003-fix-the-author-path-post-filter-to-treat-nil-isp origin/main
cd "$REPO/.worktrees/audiobooks-003-fix-the-author-path-post-filter-to-treat-nil-isp"
git rebase origin/main
# Go task: do NOT run 'go work init .' — it breaks the build (ambiguous genproto imports)
```

(Protocol also in `docs/agent-tasks/ORCHESTRATION.md` — the inline block above is authoritative for this task.)

## Goal

Change internal/audiobooks/service_query.go line 346 so a nil IsPrimaryVersion is treated as primary (true), matching the storage-layer convention already used by pebble_store.go:953 and the memdb index default, so the author path and library path classify the same book identically.

## Background (verify before editing)

- GetBooksByAuthorIDCore (both the memdb and Pebble-scan implementations) already excludes explicitly-false rows before returning, per its own doc comment: 'Non-primary versions are duplicates of a book already in the list, so it excludes them' (internal/database/memdb_reads.go:510-511). So an explicitly-false book like 01KNDB8NWHXV2DKRQESBA9SDRA (author 42623) never reaches this post-filter at all under ANY is_primary_version query value -- confirmed by the TODO's own measurement (author_id=42623&is_primary_version=false -> 0 rows).
- A nil-flagged book DOES survive into `books` (nil counts as primary at the getter level) and is then handed to the buggy post-filter, which misclassifies it as non-primary when is_primary_version=false is requested -- explaining the TODO's measured author_id=38542&is_primary_version=false -> 1 row, is_primary_version: null.

- **Re-verify these anchors before editing** — line numbers drift; a zero-hit grep means STOP and report:
  ```bash
  grep -n "bPrimary := b.IsPrimaryVersion" internal/audiobooks/service_query.go   # 1 hit L346 — The author-path post-filter treats nil as false
  grep -n "eff := book.IsPrimaryVersion == nil" internal/database/pebble_store.go   # 1 hit L953 — Storage's PebbleStore filter treats nil as true (primary)
  grep -n "Default: true" internal/database/memdb_schema.go   # 1 hit L165 — memdb IsPrimaryVersion index defaults nil to true
  grep -n "booksCore, err = svc.store.GetBooksByAuthorIDCore" internal/audiobooks/service_query.go   # 1 hit L156 — authorID branch calls GetBooksByAuthorIDCore, which already excludes explicit-false rows before the post-filter runs
  ```

### Reuse — don't invent

- No existing helper identified; do not invent new constants for concepts that already have a name — grep first.

## Step-by-step

1. Before any edit, run `grep -n "bPrimary := b.IsPrimaryVersion" internal/audiobooks/service_query.go`.
2. If it shows `== nil || *b.IsPrimaryVersion` at L346 (i.e. TASK-002 already landed this exact change), this task is a no-op for the code change -- do NOT re-edit the line. Skip to the comment-only step.
3. If it still shows `!= nil && *b.IsPrimaryVersion` at L346, apply the change: `bPrimary := b.IsPrimaryVersion == nil || *b.IsPrimaryVersion`.
4. In either case, add the explanatory comment above line 346: '// nil counts as primary, matching pebble_store.go's filter and the memdb index default (memdb_schema.go Default: true) -- see TODO.md is_primary_version investigation.' if not already present.
5. Bump the file's version header and last-edited date.
6. Add the changelog fragment and TODO.md close-out as before.

Then, always:
- Keep the change purely transform — do not touch adjacent code, do not reorder imports beyond the formatter, do not change signatures unless a step above says so explicitly.
- Bump the file header (`version` + `last-edited: 2026-08-21`) on every file you touch; keep existing guids. New files get a fresh guid (`uuidgen | tr A-Z a-z`).
- Add a changelog fragment `changelog.d/20260821_audiobooks_003.md` (NO file header; format per `changelog.d/README.md`: a `### Fixed|Changed|Added` heading, a `####` title, one paragraph).
- Do NOT edit `TODO.md` — the coordinator closes the source item in one commit per wave (every brief in a wave would otherwise collide on it). In your final report, state the exact `TODO.md` line text to check off. Never add new TODO items directly — use a `todo.d/` fragment (no header).

### Edge-case semantics (conservative defaults — treat unknown as unknown, never as disqualifying)

- A book with IsPrimaryVersion explicitly false never reaches this filter (excluded earlier by GetBooksByAuthorIDCore) -- this fix does not change that behavior, and should not attempt to: L3893 (needs_design) is where 'should the author listing expose non-primary books at all' gets decided.

## Tests

- See L3889 in this scope -- the conformance test added there (fixture with nil/true/false rows) is the correctness proof for this fix; do not duplicate a narrower test here.

Anti-over-suppression: N/A

## How to test

```bash
go build ./... && go vet ./... && go test ./internal/audiobooks/... -count=1
```
Do NOT use `make ci` as the gate: it is red on `main` from 10 pre-existing staticcheck findings unrelated to this task. Run `staticcheck ./<changed-pkg>/...` and fix only findings in files you touched. A failing test in a package you did not change is not yours — report it, do not fix it.

## Acceptance criteria

- [ ] grep -n 'bPrimary := b.IsPrimaryVersion == nil' internal/audiobooks/service_query.go returns 1 hit.
- [ ] The L3889 conformance test (once added) passes: go test ./internal/audiobooks/... -run IsPrimaryVersion -v exits 0.
- [ ] Anti-over-suppression: N/A
- [ ] Edge cases above hold (nil/empty/unknown never disqualify; a test asserts it where a filter/guard is added).
- [ ] Gate green: `go build ./... && go vet ./... && go test ./internal/audiobooks/... -count=1` exits 0; `go vet`/lint clean.
- [ ] File headers bumped on every changed file (`grep -n "last-edited: 2026-08-21" <file>` hits for each).
- [ ] Changelog fragment present: `test -f changelog.d/20260821_audiobooks_003.md`.

## Commit message

```
fix(audiobooks): Fix the author-path post-filter to treat nil IsPrimaryVersio (TODO L3884)

<why the change was needed; what it protects; what it deliberately does NOT change>

Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>
```

## Done

STOP — report done with exact counts (`COMPLETED: n — ...` / `REMAINING: n — ...` / `BLOCKED: n — ...`); the coordinator owns push/PR/merge. Do NOT run `git push`, `gh pr`, or any merge.

## Idempotency / Rollback

**This task touches persisted data, files on disk, or an apply path. `git revert` does NOT restore data.** Mandatory: (1) the op/endpoint defaults to dry-run / `apply=false` and prints what it WOULD change; (2) every mutation is journaled through the existing undo ledger (`CreateOperationChange` — verify: `grep -rn "func.*CreateOperationChange" internal/database/*.go`) so `internal/undo` can replay it — a mutation without a journal row is a defect; (3) acceptance includes a test that applies on a fixture and then undoes via `internal/undo` and asserts the fixture is byte-identical; (4) the apply path refuses to start while a `library.scan` operation is running or queued (check the registry for an active scan before mutating — a running scan clobbers applied metadata). Idempotency: re-running in dry-run must report 0 pending changes after a successful apply. Rollback of the CODE = `git revert`; rollback of the DATA = the undo ledger, which is why (2) is not optional. PR stays open for the owner — the coordinator never admin-merges it.

If the symbol already lives at its NEW location and is absent from the old one (re-run the re-verify greps above), the move is already done — run acceptance instead. Rollback = `git revert` the commit; behaviour at the old site is restored.

## Coordinator notes

This is a narrow, surgical fix per the owner's own guidance in the TODO text ('Default: true is already the storage answer, so the post-filter is the side that should change'). Do not also change GetBooksByAuthorIDCore's exclusion behavior in the same task -- that's the separate, undecided question in L3893.
