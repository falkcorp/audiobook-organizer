<!-- file: docs/agent-tasks/todo-completion-2026-09/database/TASK-315-the-real-pebbledb-corrupted-organize-path-repair.md -->
<!-- version: 1.7.0 -->
<!-- guid: 7d6228c2-048e-4af7-8ae4-24a7a9316eb7 -->
<!-- last-edited: 2026-09-10 -->

# TASK-315 — The real PebbleDB corrupted-organize-path repair (migration014UpPebble) is written but never wired to migration014Up, so it has never run against the production backend (DB-03)

> **Status 2026-09-10:** 🆕 NEW — Wave 3 audit finding `DB-03` (audit_database_operations.json) · adversarial re-check 2026-09-10: **CONFIRMED**

**Priority:** P2 · **Effort:** S · **Recommended subagent:** Haiku-class · database subagent · **Depends on:** none · **Wave:** per ../orchestration.md (collision-aware) 

Source: Wave 3 audit finding `DB-03` (audit_database_operations.json) · adversarial re-check 2026-09-10: **CONFIRMED**. Verified at HEAD `42d187168` on 2026-09-10; line numbers drift — re-verify with the greps below before editing.

## ⛔ START HERE (do this first, exactly)

```bash
# ⛔ START HERE — do not touch code before this block succeeds
REPO=/Users/jdfalk/repos/github.com/jdfalk/audiobook-organizer   # the primary checkout (same path convention as every carried brief)
git -C "$REPO" fetch origin
git -C "$REPO" worktree add "$REPO/.worktrees/database-315" -b agent/database-315-the-real-pebbledb-corrupted-organize-pat origin/main
cd "$REPO/.worktrees/database-315"
git rebase origin/main
# Go task: do NOT run 'go work init .' — it breaks the build (ambiguous genproto imports)
```

(Protocol in `../ORCHESTRATION.md` and `docs/agent-tasks/ORCHESTRATION.md` — the inline block above is authoritative for this task.)

## Goal

Either dispatch migration014UpPebble from migration014Up via AsPebbleStore(store) (the same pattern migration007Up already uses at lines 577-581), or -- if the condition is believed to no longer exist in the live library -- run a one-off read-only audit to confirm zero `book:` rows contain `{` in FilePath, then delete the dead function and its lint suppression.

Why it matters: Books whose FilePath was written before the leftover-placeholder guard was added to expandPattern (i.e. paths that literally contain `{series}` or `{author}`) are never flagged for review on the production (Pebble) database -- the one-time cleanup this migration exists for silently never executes. Any such rows remain silently corrupted/unresolvable indefinitely.

## Background (verify before editing)

- migration014Up (lines 628-636) is a hardcoded no-op with the comment 'SQLite-only migration; no-op for PebbleStore.' The actual repair logic lives in migration014UpPebble (lines 638-670), which scans every book with `GetAllBooksCore(0, 0)`, flags any book whose FilePath still contains a literal `{` (an unresolved `{series}`/`{author}` placeholder) by setting `LibraryState = "needs_review"`, and is marked `//lint:ignore U1000 kept: real Pebble corrupted-path migration logic not yet dispatched from the migration014Up no-op stub (follow-up wire-up, 2026-07-12)`. grep across internal/database confirms migration014UpPebble is called from nowhere -- it is dead code sitting behind a lint suppression for roughly two months as of this audit (2026-09-10).
- Anchor: `internal/database/migrations.go:632` (audit `DB-03`, confidence high, severity medium).
- Related tracking: The code comment itself flags it as a pending 'follow-up wire-up' from 2026-07-12, but nothing in TODO.md was checked for a matching entry by this audit -- worth reconciling rather than re-filing if one already exists.
- **Adversarial re-check (2026-09-10, `state/final/adversarial_top11.json`): CONFIRMED** — migration014Up is a hardcoded no-op; migration014UpPebble fully implemented (flags FilePath containing '{' as LibraryState=needs_review), carries lint:ignore U1000 'not yet dispatched (2026-07-12)'. migration007Up shows the AsPebbleStore dispatch pattern. Untracked in TODO.md.
  - Blast radius: migrations.go; runs once at startup; touches every book row with '{' in FilePath.
  - Existing tests to extend: internal/database/migrations_extra_test.go
  - Standing-ban contact: WRITES book.LibraryState — the field behind two live landmines (fix-library-states ban, scanner revert). Different value ('needs_review'), one-time, targeted; prefer the audit-first alternative (confirm zero live rows have '{', delete the dead code).

- **Re-verify these anchors before editing** — a zero-hit grep means STOP and report:
  ```bash
  test -e internal/database/migrations.go   # the file the finding is anchored to still exists (-e: a directory anchor is valid too)
  sed -n '622,676p' internal/database/migrations.go   # expect the code described under Background (drifted lines: re-find by the quoted text)
  ```

## Step-by-step

1. Re-run the re-verify anchors; read the surrounding function end-to-end and confirm the finding still holds at HEAD (if it does not, STOP and report).
2. Implement the fix described under Goal in `internal/database/migrations.go` (and any sibling that shares the same shape — grep for the pattern before assuming there is one copy).
3. Write the regression test first (it must fail against the pre-fix code), then make it pass.
4. Run the gate; add the changelog fragment; bump headers; report with exact counts.

Then, always:
- Keep the change purely on-target — do not touch adjacent code, do not "clean up while you're in there".
- Bump the file header (`version` + `last-edited: 2026-09-10`) on every file you touch; keep existing guids. New files get a fresh guid (`uuidgen | tr A-Z a-z`).
- Add a changelog fragment `changelog.d/20260910_database_315.md` (NO file header; format per `changelog.d/README.md`: a `### Fixed|Changed|Added` heading, a `####` title, one paragraph).
- Do NOT edit `TODO.md` — the coordinator closes the source item in one commit per wave. In your final report, state the exact `TODO.md` line text (or `todo.d` fragment) to check off.

## Tests

- A regression test that reproduces the defect described in Background and fails on the pre-fix code.
- Existing package tests stay green (`-count=1`).

## How to test

```bash
go build ./... && go vet ./... && go test ./internal/database/... -count=1
```
Do NOT use `make ci` as the gate: it is red on `main` from pre-existing staticcheck findings unrelated to this task. Run `staticcheck ./<changed-pkg>/...` and fix only findings in files you touched.

## Acceptance criteria

- [ ] Every re-verify anchor above still hits (or the report says which moved and where).
- [ ] The tests listed above exist and pass; a regression test reproduces the original defect and fails on the pre-fix code.
- [ ] Gate green: the command in **How to test** exits 0; `go vet`/lint clean on touched packages.
- [ ] File headers bumped on every changed file (`grep -n "last-edited: 2026-09-10" <file>` hits for each).
- [ ] Changelog fragment present: `test -f changelog.d/20260910_database_315.md`.

## Commit message

```
fix(database): The real PebbleDB corrupted-organize-path repair (migration014UpPebble (DB-03)

<why the change was needed; what it protects; what it deliberately does NOT change>

Co-Authored-By: Claude Fable 5.1 <noreply@anthropic.com>
```

## Done

STOP — report done with exact counts (`COMPLETED: n — ...` / `REMAINING: n — ...` / `BLOCKED: n — ...`); the coordinator owns push/PR/merge. Do NOT run `git push`, `gh pr`, or any merge.

## Idempotency / Rollback

Decide this FIRST and write the answer in your report: **does the fix add or change a path that writes, moves, or deletes persisted data or files** (an apply/repair/delete/migration path)?

- **NO** — the fix is a lock, a bound, a check, an error propagated, a header, a config value: pure code change. Rollback = `git revert` the commit. Already-done check = the re-verify anchors above show the new code (add the exact `grep -n '<new symbol or string>' <file>` you used to your report). Do NOT invent a dry-run/`apply` parameter that the Goal did not ask for.
- **YES** — stop and report before implementing: this brief was classified as a standard-lane code change, and a new write path needs the review-critical protocol (dry-run default, undo journal, owner hold).

## Coordinator notes

Standard lane: coordinator may admin-merge on a green gate.
