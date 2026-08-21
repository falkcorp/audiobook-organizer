<!-- file: docs/agent-tasks/todo-completion/database/TASK-024-replace-fragile-0x30-0x3a-only-book-0-book-bound.md -->
<!-- version: 1.0.0 -->
<!-- guid: f6e4da86-362d-4f63-ab9a-2390bbb82390 -->
<!-- last-edited: 2026-08-21 -->

# TASK-024 — Replace fragile [0x30-0x3A]-only book:0..book:; bounds in the version-group backfill with a real prefix scan (VGBACKFILL-BOUNDS-FRAGILE)

**Priority:** P2 · **Effort:** S · **Recommended subagent:** Sonnet-class · database subagent · **Why:** One-line-per-bound change in a well-commented, well-tested backfill function; low complexity but touches a production-repair code path so needs a careful re-run-safety check. · **Depends on:** none · **Wave:** 1

Source: `TODO.md` line 1945 as of commit 46628240 (later edits shift lines) — re-find it with `grep -n -F "**VGBACKFILL-BOUNDS-FRAGILE**" TODO.md` (line numbers drift; the grep is built from the line's own text). Scope file: `scope-03.json`.

## ⛔ START HERE (do this first, exactly)

```bash
# ⛔ START HERE — do not touch code before this block succeeds
REPO=/Users/jdfalk/repos/github.com/jdfalk/audiobook-organizer   # adjust to your clone
git -C "$REPO" fetch origin
git -C "$REPO" worktree add "$REPO/.worktrees/database-024-replace-fragile-0x30-0x3a-only-book-0-book-bound" -b agent/database-024-replace-fragile-0x30-0x3a-only-book-0-book-bound origin/main
cd "$REPO/.worktrees/database-024-replace-fragile-0x30-0x3a-only-book-0-book-bound"
git rebase origin/main
# Go task: do NOT run 'go work init .' — it breaks the build (ambiguous genproto imports)
```

(Protocol also in `docs/agent-tasks/ORCHESTRATION.md` — the inline block above is authoritative for this task.)

## Goal

Change the iterator bounds in BackfillVersionGroupIndex (internal/database/pebble_store_versiongroup_backfill.go:98-99) from the byte-range-limited `book:0`..`book:;` to a true prefix scan `book:`..`book;` (note: no colon on the upper bound — `;` is the byte immediately after `:` in ASCII, so `book;` is the correct exclusive upper bound for the `book:` prefix), so any book ID (not just ULIDs starting with a digit) is included in the scan, relying on the already-correct one-colon structural filter to reject secondary indexes.

## Background (verify before editing)

- This is a latent-correctness fix per the item's own framing, NOT the cause of any currently observed under-scan — every production book ID happens to be a ULID starting with a digit today.
- CreateBook mints a ULID only `if book.ID == ""`, so a caller supplying a letter-leading ID (e.g. via an import path, a test fixture, or a future ID scheme change) would silently never be indexed by this backfill, with no error surfaced anywhere.

- **Re-verify these anchors before editing** — line numbers drift; a zero-hit grep means STOP and report:
  ```bash
  grep -n 'LowerBound\|UpperBound' internal/database/pebble_store_versiongroup_backfill.go   # 2 hits at L98-99: []byte("book:0") / []byte("book:;") — the fragile byte-range bounds exist at this exact site
  grep -n 'strings.Count(key, ":")' internal/database/pebble_store_versiongroup_backfill.go   # 1 hit, `if strings.Count(key, \":\") != 1 { continue }` — the structural one-colon filter that makes the wider prefix safe already exists and does not depend on the byte range
  grep -n 'if book.ID == ""' internal/database/*.go   # 1 hit confirming the conditional ULID minting — CreateBook only mints a ULID conditionally, so a caller-supplied non-ULID ID is possible and would be silently invisible to the current scan
  ```

### Reuse — don't invent

- Use `the existing one-colon structural filter (already correctly discriminates primary rows from secondary indexes independent of byte range)` in `internal/database/pebble_store_versiongroup_backfill.go` (verify: `grep -n 'Only the primary' internal/database/pebble_store_versiongroup_backfill.go`) — do NOT write a parallel helper.

## Step-by-step

1. In internal/database/pebble_store_versiongroup_backfill.go, change L98-99 from `LowerBound: []byte("book:0"), UpperBound: []byte("book:;")` to `LowerBound: []byte("book:"), UpperBound: []byte("book;")`.
2. Bump the file's version header (currently 1.2.1) and last-edited date per the mandatory file-header rule.
3. Bump the sentinel from `versionGroupBackfillKey = "system:backfill:versiongroup_index_v2_done"` to a v3 key (`..._v3_done`), following the same v1->v2 bump pattern documented at L23-30, so every deployment (including ones that already completed v2 under the narrower bounds) re-runs once under the wider bounds. This is MANDATORY, not optional — the bound fix has no effect on already-existing letter-leading book IDs unless the one-time gate is forced to re-run.
4. Add or extend a unit test with a synthetic book ID starting with a letter (e.g. 'A01...') stored under `book:A01...`, asserting it is now included by the widened scan and correctly indexed if it has a VersionGroupID.

Then, always:
- Keep the change purely transform — do not touch adjacent code, do not reorder imports beyond the formatter, do not change signatures unless a step above says so explicitly.
- Bump the file header (`version` + `last-edited: 2026-08-21`) on every file you touch; keep existing guids. New files get a fresh guid (`uuidgen | tr A-Z a-z`).
- Add a changelog fragment `changelog.d/20260821_database_024.md` (NO file header; format per `changelog.d/README.md`: a `### Fixed|Changed|Added` heading, a `####` title, one paragraph).
- Do NOT edit `TODO.md` — the coordinator closes the source item in one commit per wave (every brief in a wave would otherwise collide on it). In your final report, state the exact `TODO.md` line text to check off. Never add new TODO items directly — use a `todo.d/` fragment (no header).

### Edge-case semantics (conservative defaults — treat unknown as unknown, never as disqualifying)

- If the sentinel is bumped to force a re-run (step 3), the re-run must still be idempotent per the file's own design (deterministic key→value writes) — confirm this holds for the wider scan too, not just re-verify the v1→v2 precedent.

## Tests

- internal/database/pebble_store_versiongroup_backfill_test.go (or wherever existing backfill tests live — `grep -rl BackfillVersionGroupIndex internal/database/*_test.go`) — new case: a book with a letter-leading ID and a non-empty VersionGroupID is included in the backfill's index output under the new bounds.
- Regression: existing ULID-based test cases must still pass unchanged, proving the widened bounds don't pick up unrelated secondary-index rows (the structural filter must still correctly reject them).

Anti-over-suppression: N/A

## How to test

```bash
go build ./... && go vet ./... && go test ./internal/database/... -count=1
```
Do NOT use `make ci` as the gate: it is red on `main` from 10 pre-existing staticcheck findings unrelated to this task. Run `staticcheck ./<changed-pkg>/...` and fix only findings in files you touched. A failing test in a package you did not change is not yours — report it, do not fix it.

## Acceptance criteria

- [ ] `go test ./internal/database/... -run BackfillVersionGroupIndex` passes including the new letter-ID case.
- [ ] `grep -n 'LowerBound: \[\]byte("book:"),' internal/database/pebble_store_versiongroup_backfill.go` confirms the widened bounds are in place.
- [ ] Anti-over-suppression: N/A
- [ ] Edge cases above hold (nil/empty/unknown never disqualify; a test asserts it where a filter/guard is added).
- [ ] Gate green: `make ci` exits 0; `go vet`/lint clean.
- [ ] File headers bumped on every changed file (`grep -n "last-edited: 2026-08-21" <file>` hits for each).
- [ ] Changelog fragment present: `test -f changelog.d/20260821_database_024.md`.
- [ ] Anti-over-suppression: N/A
- [ ] Edge cases above hold (nil/empty/unknown never disqualify; a test asserts it where a filter/guard is added).
- [ ] Gate green: `go build ./... && go vet ./... && go test ./internal/database/... -count=1` exits 0; `go vet`/lint clean.
- [ ] File headers bumped on every changed file (`grep -n "last-edited: 2026-08-21" <file>` hits for each).
- [ ] Changelog fragment present: `test -f changelog.d/20260821_database_024.md`.

## Commit message

```
fix(database): Replace fragile [0x30-0x3A]-only book:0..book:; bounds in th (VGBACKFILL-BOUNDS-FRAGILE)

<why the change was needed; what it protects; what it deliberately does NOT change>

Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>
```

## Done

STOP — report done with exact counts (`COMPLETED: n — ...` / `REMAINING: n — ...` / `BLOCKED: n — ...`); the coordinator owns push/PR/merge. Do NOT run `git push`, `gh pr`, or any merge.

## Idempotency / Rollback

If the symbol already lives at its NEW location and is absent from the old one (re-run the re-verify greps above), the move is already done — run acceptance instead. Rollback = `git revert` the commit; behaviour at the old site is restored.

## Coordinator notes

Genuinely low urgency per the item's own framing ('not the cause of any observed under-scan') — safe to schedule opportunistically.
