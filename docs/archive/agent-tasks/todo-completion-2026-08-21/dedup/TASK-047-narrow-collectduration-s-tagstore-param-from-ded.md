<!-- file: docs/agent-tasks/todo-completion/dedup/TASK-047-narrow-collectduration-s-tagstore-param-from-ded.md -->
<!-- version: 1.1.0 -->
<!-- guid: b0482e86-a7f2-48c7-87b1-6aa1808ad589 -->
<!-- last-edited: 2026-09-02 -->

# TASK-047 — Narrow CollectDuration's tagStore param from dedup.Store to database.BookTagSingletonStore (TODO.md L4719)

> **Status 2026-09-02:** ✅ DONE — PR #2728 merged 2026-08-22 (987aaa454).

**Priority:** P2 · **Effort:** S · **Recommended subagent:** Haiku-class · dedup subagent · **Why:** Single parameter type change plus a doc-comment fix; both existing call sites already satisfy the narrower type with no changes needed. · **Depends on:** none · **Wave:** 2

Source: `TODO.md` line 4719 as of commit 46628240 (later edits shift lines) — re-find it with `grep -n -F "`internal/dedup/collectors_metadata.go:51` — \"`dat" TODO.md` (line numbers drift; the grep is built from the line's own text). Scope file: `scope-09.json`.

## ⛔ START HERE (do this first, exactly)

```bash
# ⛔ START HERE — do not touch code before this block succeeds
REPO=/Users/jdfalk/repos/github.com/jdfalk/audiobook-organizer   # adjust to your clone
git -C "$REPO" fetch origin
git -C "$REPO" worktree add "$REPO/.worktrees/dedup-047-narrow-collectduration-s-tagstore-param-from-ded" -b agent/dedup-047-narrow-collectduration-s-tagstore-param-from-ded origin/main
cd "$REPO/.worktrees/dedup-047-narrow-collectduration-s-tagstore-param-from-ded"
git rebase origin/main
# Go task: do NOT run 'go work init .' — it breaks the build (ambiguous genproto imports)
```

(Protocol also in `docs/agent-tasks/ORCHESTRATION.md` — the inline block above is authoritative for this task.)

## Goal

Change CollectDuration's `tagStore Store` parameter to `tagStore database.BookTagSingletonStore`, and fix the stale doc comment at collectors_metadata.go:47-51 which claims EnsureSingletonBookTag 'requires the full Store interface' when it only needs 3 methods.

## Background (verify before editing)

- This mirrors the exact pattern CLAUDE.md's worked example describes: a comment justifying a wide parameter that was true when written but is now stale because the callee (EnsureSingletonBookTag) was independently narrowed.
- No caller needs to change: rescore.go:228 already passes `nil` (untyped nil satisfies any interface) and engine.go:562 passes `de.bookStore`, whose static type dedup.Store already embeds database.BookTagSingletonStore by name.

- **Re-verify these anchors before editing** — line numbers drift; a zero-hit grep means STOP and report:
  ```bash
  grep -n "func CollectDuration" -A 5 internal/dedup/collectors_metadata.go   # 1 hit ~L145, param `tagStore Store` — CollectDuration's tagStore param is typed dedup.Store
  grep -n "func EnsureSingletonBookTag" internal/database/tag_helpers.go   # 1 hit ~L66: `func EnsureSingletonBookTag(store BookTagSingletonStore, ...)` — EnsureSingletonBookTag actually requires only BookTagSingletonStore (3 methods)
  grep -n "type BookTagSingletonStore interface" -A 4 internal/database/tag_helpers.go   # 1 hit ~L46, 3 method lines — BookTagSingletonStore is exactly 3 methods
  grep -n "database.BookTagSingletonStore" internal/dedup/store.go   # 1 hit inside dedupForwardedStores — dedup.Store already embeds database.BookTagSingletonStore by name (so narrowing the param does not require touching callers)
  grep -n "CollectDuration(" internal/dedup/rescore.go internal/dedup/engine.go   # 2 hits: rescore.go:228 passes nil, engine.go:562 passes de.bookStore — the only two callers pass either nil or de.bookStore (dedup.Store), both of which satisfy the narrower type
  ```

### Reuse — don't invent

- Use `database.BookTagSingletonStore` in `internal/database/tag_helpers.go` (verify: `grep -n "type BookTagSingletonStore interface" internal/database/tag_helpers.go`) — do NOT write a parallel helper.

## Step-by-step

1. Open internal/dedup/collectors_metadata.go.
2. Change the CollectDuration signature (~L145-150) from `tagStore Store,` to `tagStore database.BookTagSingletonStore,`.
3. Rewrite the doc comment above it (~L142-144, 'tagStore is the Store used for side-effect tag writes...') to state it takes the narrow BookTagSingletonStore, dropping the 'the engine passes its bookStore field which is a Store superset' phrasing in favor of noting dedup.Store already satisfies it.
4. The comment on DurationCollectorStore (~L44-51) that says 'database.EnsureSingletonBookTag (which requires the full Store interface) can be called without a type assertion' is now doubly stale (EnsureSingletonBookTag never required the full interface, and this narrowing removes the last reason to phrase it that way) — reword to state plainly that EnsureSingletonBookTag needs BookTagSingletonStore (3 methods), which tagStore's new narrow type provides directly.
5. Bump the file's version header and last-edited date.

Then, always:
- Keep the change purely transform — do not touch adjacent code, do not reorder imports beyond the formatter, do not change signatures unless a step above says so explicitly.
- Bump the file header (`version` + `last-edited: 2026-08-21`) on every file you touch; keep existing guids. New files get a fresh guid (`uuidgen | tr A-Z a-z`).
- Add a changelog fragment `changelog.d/20260821_dedup_047.md` (NO file header; format per `changelog.d/README.md`: a `### Fixed|Changed|Added` heading, a `####` title, one paragraph).
- Do NOT edit `TODO.md` — the coordinator closes the source item in one commit per wave (every brief in a wave would otherwise collide on it). In your final report, state the exact `TODO.md` line text to check off. Never add new TODO items directly — use a `todo.d/` fragment (no header).

### Edge-case semantics (conservative defaults — treat unknown as unknown, never as disqualifying)

- tagStore == nil (interface nil, the rescore.go call site): the existing `if tagStore != nil` guards at :244 and :260 must keep working identically against the narrower interface type — a nil database.BookTagSingletonStore compares equal to nil the same way a nil dedup.Store does.

## Tests

- No behavior changes, so existing tests (internal/dedup/collectors_metadata_test.go, if present, or engine_test.go's duration-signal tests) must keep passing unmodified — this is purely a static-type narrowing.
- If no direct unit test currently exercises CollectDuration with a fake tagStore, add one: TestCollectDuration_TagStoreAcceptsNarrowType — pass a minimal fake implementing only the 3 BookTagSingletonStore methods (not full dedup.Store) and assert it compiles and the tag side-effect fires.

Anti-over-suppression: N/A

## How to test

```bash
go build ./... && go vet ./... && go test ./internal/dedup/... -count=1
```
Do NOT use `make ci` as the gate: it is red on `main` from 10 pre-existing staticcheck findings unrelated to this task. Run `staticcheck ./<changed-pkg>/...` and fix only findings in files you touched. A failing test in a package you did not change is not yours — report it, do not fix it.

## Acceptance criteria

- [ ] go build ./internal/dedup/... exits 0.
- [ ] go vet ./internal/dedup/... exits 0.
- [ ] grep -n "tagStore database.BookTagSingletonStore" internal/dedup/collectors_metadata.go returns 1 hit.
- [ ] Anti-over-suppression: N/A
- [ ] Edge cases above hold (nil/empty/unknown never disqualify; a test asserts it where a filter/guard is added).
- [ ] Gate green: `go build ./... && go vet ./... && go test ./internal/dedup/... -count=1` exits 0; `go vet`/lint clean.
- [ ] File headers bumped on every changed file (`grep -n "last-edited: 2026-08-21" <file>` hits for each).
- [ ] Changelog fragment present: `test -f changelog.d/20260821_dedup_047.md`.

## Commit message

```
refactor(dedup): Narrow CollectDuration's tagStore param from dedup.Store to  (TODO L4719)

<why the change was needed; what it protects; what it deliberately does NOT change>

Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>
```

## Done

STOP — report done with exact counts (`COMPLETED: n — ...` / `REMAINING: n — ...` / `BLOCKED: n — ...`); the coordinator owns push/PR/merge. Do NOT run `git push`, `gh pr`, or any merge.

## Idempotency / Rollback

If the symbol already lives at its NEW location and is absent from the old one (re-run the re-verify greps above), the move is already done — run acceptance instead. Rollback = `git revert` the commit; behaviour at the old site is restored.

## Coordinator notes

Low-risk, purely additive-narrowing change; both call sites already satisfy the tighter type so no ripple beyond this one file.
