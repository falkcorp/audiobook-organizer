<!-- file: docs/agent-tasks/todo-completion/dedup/TASK-041-audit-remaining-we-use-the-wide-type-because-x-r.md -->
<!-- version: 1.0.0 -->
<!-- guid: aabc4a41-e662-46f0-95cb-b9e5a9249906 -->
<!-- last-edited: 2026-08-21 -->

# TASK-041 — Audit remaining 'we use the wide type because X requires it' justification comments -- one genuinely stale instance found (TODO.md L903)

**Priority:** P2 · **Effort:** S · **Recommended subagent:** Sonnet-class · dedup subagent · **Why:** single-file, single-parameter narrowing with a clear compiler-checkable target interface already defined; low risk · **Depends on:** none · **Wave:** 1

Source: `TODO.md` line 903 as of commit 46628240 (later edits shift lines) — re-find it with `grep -n -F "Audit existing \"we use the wide type because X req" TODO.md` (line numbers drift; the grep is built from the line's own text). Scope file: `scope-02.json`.

## ⛔ START HERE (do this first, exactly)

```bash
# ⛔ START HERE — do not touch code before this block succeeds
REPO=/Users/jdfalk/repos/github.com/jdfalk/audiobook-organizer   # adjust to your clone
git -C "$REPO" fetch origin
git -C "$REPO" worktree add "$REPO/.worktrees/dedup-041-audit-remaining-we-use-the-wide-type-because-x-r" -b agent/dedup-041-audit-remaining-we-use-the-wide-type-because-x-r origin/main
cd "$REPO/.worktrees/dedup-041-audit-remaining-we-use-the-wide-type-because-x-r"
git rebase origin/main
# Go task: do NOT run 'go work init .' — it breaks the build (ambiguous genproto imports)
```

(Protocol also in `docs/agent-tasks/ORCHESTRATION.md` — the inline block above is authoritative for this task.)

## Goal

Narrow CollectDuration's `tagStore Store` parameter (internal/dedup/collectors_metadata.go:147) to `tagStore database.BookTagSingletonStore`, since its only use is four EnsureSingletonBookTag calls (lines 245,248,261,264) which themselves only need that 3-method interface -- and correct the stale comment at line 51 that currently justifies the wide type with a claim about EnsureSingletonBookTag that is no longer true.

## Background (verify before editing)

- This is the third instance of the exact pattern CLAUDE.md's worked example describes (handlers.OrganizeStore, handlers.OperationsStore, both already fixed 2026-08-18) -- 'a comment explains why something must stay wide, verify the claim before believing it.'
- collectors_metadata.go:142-147: 'tagStore is the Store used for side-effect tag writes; it must be [comment continues, truncated in grep] ... may be nil -- side-effect tags silently skipped'.

- **Re-verify these anchors before editing** — line numbers drift; a zero-hit grep means STOP and report:
  ```bash
  grep -n 'func EnsureSingletonBookTag' internal/database/tag_helpers.go   # 1 hit L66, param type BookTagSingletonStore — EnsureSingletonBookTag actually takes the narrow BookTagSingletonStore, not the full Store
  grep -n 'type BookTagSingletonStore interface' internal/database/tag_helpers.go   # 1 hit L46 — BookTagSingletonStore is a 3-method interface
  grep -n 'tagStore Store' internal/dedup/collectors_metadata.go   # 1 hit L147 — CollectDuration's tagStore param is currently the wide dedup.Store
  grep -n 'type Store interface' internal/dedup/store.go   # 1 hit L102 — dedup.Store is an 8-entry composition (the 'whole surface')
  grep -n 'structural satisfaction requires the full' internal/server/handlers/operations/interfaces.go   # 1 hit L27, in a comment explaining it was narrowed by #2566 — operations/interfaces.go's comment already correctly documents its own historical narrowing (verified correct, no action needed)
  grep -n 'func SaveConfigToDatabase' internal/config/persistence.go   # 1 hit L1491, param `store database.SettingsStore` — system/interfaces.go's SettingsStore claim is verified accurate (no action needed)
  ```

### Reuse — don't invent

- Use `database.BookTagSingletonStore (the narrow interface to declare CollectDuration's tagStore param as, instead of dedup.Store)` in `internal/database/tag_helpers.go` (verify: `grep -n 'type BookTagSingletonStore interface' internal/database/tag_helpers.go`) — do NOT write a parallel helper.

## Step-by-step

1. Read internal/dedup/collectors_metadata.go:130-270 in full to confirm tagStore has no other use besides the four EnsureSingletonBookTag calls.
2. Change CollectDuration's signature (collectors_metadata.go:145-147) from `tagStore Store` to `tagStore database.BookTagSingletonStore` (database already imported per line ~43).
3. Update the doc comment at collectors_metadata.go:51 to remove the stale 'requires the full Store interface' claim and instead say EnsureSingletonBookTag needs database.BookTagSingletonStore.
4. Find and update every call site of CollectDuration to pass a value satisfying the narrower interface (a *database.PebbleStore or database.MockStore already does, structurally, since narrowing a parameter type never requires call-site changes when the passed value's concrete type already implements the wider interface's superset).

Then, always:
- Keep the change purely transform — do not touch adjacent code, do not reorder imports beyond the formatter, do not change signatures unless a step above says so explicitly.
- Bump the file header (`version` + `last-edited: 2026-08-21`) on every file you touch; keep existing guids. New files get a fresh guid (`uuidgen | tr A-Z a-z`).
- Add a changelog fragment `changelog.d/20260821_dedup_041.md` (NO file header; format per `changelog.d/README.md`: a `### Fixed|Changed|Added` heading, a `####` title, one paragraph).
- Do NOT edit `TODO.md` — the coordinator closes the source item in one commit per wave (every brief in a wave would otherwise collide on it). In your final report, state the exact `TODO.md` line text to check off. Never add new TODO items directly — use a `todo.d/` fragment (no header).

### Edge-case semantics (conservative defaults — treat unknown as unknown, never as disqualifying)

- A caller passing `nil` for tagStore (the documented 'side-effect tags silently skipped' path) must still compile and behave identically -- nil satisfies any interface type.

## Tests

- go build ./internal/dedup/... after the change is itself the test -- a structural interface narrowing has no new runtime behavior to unit-test; existing dedup collector tests must stay green.

Anti-over-suppression test: `N/A -- this is an interface-narrowing refactor, not a filter/guard.` — a known-good input still passes with the new guard active.

## How to test

```bash
go build ./... && go vet ./... && go test ./internal/dedup/... -count=1
```
Do NOT use `make ci` as the gate: it is red on `main` from 10 pre-existing staticcheck findings unrelated to this task. Run `staticcheck ./<changed-pkg>/...` and fix only findings in files you touched. A failing test in a package you did not change is not yours — report it, do not fix it.

## Acceptance criteria

- [ ] go build ./... succeeds.
- [ ] grep -n 'tagStore database.BookTagSingletonStore' internal/dedup/collectors_metadata.go returns 1 hit.
- [ ] Anti-over-suppression test: `N/A -- this is an interface-narrowing refactor, not a filter/guard.` — a known-good input still passes with the new guard active.
- [ ] Edge cases above hold (nil/empty/unknown never disqualify; a test asserts it where a filter/guard is added).
- [ ] Gate green: `go build ./... && go vet ./... && go test ./internal/dedup/... -count=1` exits 0; `go vet`/lint clean.
- [ ] File headers bumped on every changed file (`grep -n "last-edited: 2026-08-21" <file>` hits for each).
- [ ] Changelog fragment present: `test -f changelog.d/20260821_dedup_041.md`.

## Commit message

```
refactor(dedup): Audit remaining 'we use the wide type because X requires it' (TODO L903)

<why the change was needed; what it protects; what it deliberately does NOT change>

Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>
```

## Done

STOP — report done with exact counts (`COMPLETED: n — ...` / `REMAINING: n — ...` / `BLOCKED: n — ...`); the coordinator owns push/PR/merge. Do NOT run `git push`, `gh pr`, or any merge.

## Idempotency / Rollback

If the symbol already lives at its NEW location and is absent from the old one (re-run the re-verify greps above), the move is already done — run acceptance instead. Rollback = `git revert` the commit; behaviour at the old site is restored.

## Coordinator notes

Scope for this item is exactly the one stale comment found; the TODO's broader ask ('audit existing... comments across the codebase... grep for justification comments near database.Store / database.BookStore and re-verify each claim') was performed as part of this investigation (see verified_anchors) and turned up only this one additional instance beyond the two CLAUDE.md already documents as fixed.
