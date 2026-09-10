<!-- file: docs/agent-tasks/todo-completion-2026-09/database/TASK-302-purge-empty-authors-delete-guard-book-scan-uses.md -->
<!-- version: 1.7.0 -->
<!-- guid: 3b515dcd-13e2-4fbd-a72e-ffe9e5e3f660 -->
<!-- last-edited: 2026-09-10 -->

# TASK-302 — purge-empty-authors delete-guard book scan uses a narrower byte-range bound than the sibling backfill already fixed for the identical bug (DB-01)

> **Status 2026-09-10:** 🆕 NEW — Wave 3 audit finding `DB-01` (audit_database_operations.json) · adversarial re-check 2026-09-10: **CONFIRMED**
> **Design fit 2026-09-10 (`audiobook-organizer:expert`, `state/final/design_fit_rows_*.json`): FITS** — author_bookref.go:317-327 still scans book:0..book:; the sibling fix shipped in pebble_store_versiongroup_backfill.go for the identical bound shape.
**Priority:** P1 · **Effort:** S · **Recommended subagent:** Opus-class · database subagent · **Depends on:** none · **Wave:** per ../orchestration.md (collision-aware) · **REVIEW-CRITICAL (prod-data path): PR stays open for the owner; never weak-tier**

Source: Wave 3 audit finding `DB-01` (audit_database_operations.json) · adversarial re-check 2026-09-10: **CONFIRMED**. Verified at HEAD `42d187168` on 2026-09-10; line numbers drift — re-verify with the greps below before editing.

## ⛔ START HERE (do this first, exactly)

```bash
# ⛔ START HERE — do not touch code before this block succeeds
REPO=/Users/jdfalk/repos/github.com/jdfalk/audiobook-organizer   # the primary checkout (same path convention as every carried brief)
git -C "$REPO" fetch origin
git -C "$REPO" worktree add "$REPO/.worktrees/database-302" -b agent/database-302-purge-empty-authors-delete-guard-book-sc origin/main
cd "$REPO/.worktrees/database-302"
git rebase origin/main
# Go task: do NOT run 'go work init .' — it breaks the build (ambiguous genproto imports)
```

(Protocol in `../ORCHESTRATION.md` and `docs/agent-tasks/ORCHESTRATION.md` — the inline block above is authoritative for this task.)

## Goal

Widen both iterator bounds in author_bookref.go's pass 2 to the true prefix range (`[]byte("book:")`..`prefixUpperBound([]byte("book:"))`, mirroring pebble_store_versiongroup_backfill.go's v3 fix); the existing one-colon structural filter already discriminates primary rows from secondary indexes across the wider range, so no other logic changes. Then run a one-off audit query for any live `book:` key whose first byte after the colon is not `0-9`.

Why it matters: CountAuthorReferences backs the delete-author / purge-empty-authors safety guard. If any book row's ID does not start with a Crockford-ULID digit (0-7) or a plain digit (0-9) -- a caller-supplied ID from an importer, migration, or restore path, which the code's own comment says is possible -- pass 2 silently misses that book's legacy AuthorID reference. That undercounts the author's references, and an author who is credited ONLY through that legacy field on that one book becomes wrongly deletable, permanently losing the author record (and any editorial data attached to it) with no error surfaced. This is the exact failure class already proven real enough to force a mandatory one-time re-backfill in the sibling file.

## Background (verify before editing)

- Pass 2 of CountAuthorReferences (the safety scan that decides whether an author is deletable) opens `snap.NewIter(&pebble.IterOptions{LowerBound: []byte("book:0"), UpperBound: []byte("book:;")})` (lines 324-327), justified by a comment at lines 308-323 admitting: 'a caller-supplied book ID starting outside that range would be invisible to pass 2, losing its LEGACY AuthorID reference... Whether any such row exists on the live library has NOT been measured.' The exact same bound shape (`book:0`..`book:;`) was identified as a real bug and FIXED in the sibling file internal/database/pebble_store_versiongroup_backfill.go (sentinel bumped v2->v3 on 2026-08-23, see lines 36-49 and 112-124 there): 'a letter-leading ID... would have been silently invisible to the v2 scan, with no error surfaced anywhere,' replaced with the true prefix range `book:`..`book;`. author_bookref.go was never updated to match.
- Anchor: `internal/database/author_bookref.go:325` (audit `DB-01`, confidence high, severity high).
- Related tracking: Not tracked as an action item anywhere seen -- the residual risk is only documented in a code comment in author_bookref.go itself ('RESIDUAL, recorded rather than guessed'), which does not cross-reference the sibling file's fix.
- **Adversarial re-check (2026-09-10, `state/final/adversarial_top11.json`): CONFIRMED** — author_bookref.go:324-327 bounds book:0..book:; admit only digits after the colon; sibling pebble_store_versiongroup_backfill.go v2->v3 fixed the identical shape; CreateBook (pebble_store.go:2418-2426) accepts caller-supplied non-ULID ids unconditionally.
  - Blast radius: CountAuthorReferences (delete-author / purge-empty-authors guard). Existing strings.Count(key, ':')!=1 filter keeps secondary indexes out, so widening is safe per the sibling precedent.
  - Existing tests to extend: internal/database/author_bookref_test.go
  - Standing-ban contact: none; author deletion has known open issues (CreateAuthor racy) — extra review care

- **Re-verify these anchors before editing** — a zero-hit grep means STOP and report:
  ```bash
  test -e internal/database/author_bookref.go   # the file the finding is anchored to still exists (-e: a directory anchor is valid too)
  sed -n '30,55p' internal/database/author_bookref.go   # expect the code described under Background (drifted lines: re-find by the quoted text)
  sed -n '106,130p' internal/database/author_bookref.go   # expect the code described under Background (drifted lines: re-find by the quoted text)
  sed -n '302,333p' internal/database/author_bookref.go   # expect the code described under Background (drifted lines: re-find by the quoted text)
  ```

## Step-by-step

1. Re-run the re-verify anchors; read the surrounding function end-to-end and confirm the finding still holds at HEAD (if it does not, STOP and report).
2. Implement the fix described under Goal in `internal/database/author_bookref.go` (and any sibling that shares the same shape — grep for the pattern before assuming there is one copy).
3. Write the regression test first (it must fail against the pre-fix code), then make it pass.
4. Run the gate; add the changelog fragment; bump headers; report with exact counts.

Then, always:
- Keep the change purely on-target — do not touch adjacent code, do not "clean up while you're in there".
- Bump the file header (`version` + `last-edited: 2026-09-10`) on every file you touch; keep existing guids. New files get a fresh guid (`uuidgen | tr A-Z a-z`).
- Add a changelog fragment `changelog.d/20260910_database_302.md` (NO file header; format per `changelog.d/README.md`: a `### Fixed|Changed|Added` heading, a `####` title, one paragraph).
- Do NOT edit `TODO.md` — the coordinator closes the source item in one commit per wave. In your final report, state the exact `TODO.md` line text (or `todo.d` fragment) to check off.

## Tests

- A regression test that reproduces the defect described in Background and fails on the pre-fix code.
- Existing package tests stay green (`-count=1`).
- ONLY if the fix adds or changes a write/apply/repair path (see Idempotency / Rollback): a test proving the dry-run / guard path writes nothing (fail-closed on error). A pure code change (lock, bound, check, propagated error) does not need this — do not add a dry-run surface to satisfy it.

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
- [ ] Changelog fragment present: `test -f changelog.d/20260910_database_302.md`.

## Commit message

```
fix(database): purge-empty-authors delete-guard book scan uses a narrower byte-range  (DB-01)

<why the change was needed; what it protects; what it deliberately does NOT change>

Co-Authored-By: Claude Fable 5.1 <noreply@anthropic.com>
```

## Done

STOP — report done with exact counts (`COMPLETED: n — ...` / `REMAINING: n — ...` / `BLOCKED: n — ...`); the coordinator owns push/PR/merge. Do NOT run `git push`, `gh pr`, or any merge.

## Idempotency / Rollback

Decide this FIRST and write the answer in your report: **does the fix add or change a path that writes, moves, or deletes persisted data or files** (an apply/repair/delete/migration path)?

- **NO** — the fix is a lock, a bound, a check, an error propagated, a header, a config value: pure code change. Rollback = `git revert` the commit. Already-done check = the re-verify anchors above show the new code (add the exact `grep -n '<new symbol or string>' <file>` you used to your report). Do NOT invent a dry-run/`apply` parameter that the Goal did not ask for.
- **YES** — **`git revert` does NOT restore data.** Mandatory: the op/endpoint defaults to dry-run / `apply=false` and prints what it WOULD change; the apply path journals enough to undo; a test proves the dry-run writes nothing; the PR is held for the owner.

## Coordinator notes

review_critical=true: prod-data path per CLAUDE.md's review-critical definition — hold the PR for the owner.
