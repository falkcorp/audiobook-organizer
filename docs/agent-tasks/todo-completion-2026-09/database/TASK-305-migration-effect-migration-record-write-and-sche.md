<!-- file: docs/agent-tasks/todo-completion-2026-09/database/TASK-305-migration-effect-migration-record-write-and-sche.md -->
<!-- version: 1.6.0 -->
<!-- guid: 6d850ea9-8048-4027-b7cc-35d61684a9e6 -->
<!-- last-edited: 2026-09-10 -->

# TASK-305 — Migration effect, migration-record write, and schema-version write are three separate, unbatched Pebble writes -- a crash between them replays a non-idempotent migration (DB-02)

> **Status 2026-09-10:** 🆕 NEW — Wave 3 audit finding `DB-02` (audit_database_operations.json) · adversarial re-check 2026-09-10: **CONFIRMED**

**Priority:** P2 · **Effort:** M · **Recommended subagent:** Opus-class · database subagent · **Depends on:** none · **Wave:** per ../orchestration.md (collision-aware) · **REVIEW-CRITICAL (prod-data path): PR stays open for the owner; never weak-tier**

Source: Wave 3 audit finding `DB-02` (audit_database_operations.json) · adversarial re-check 2026-09-10: **CONFIRMED**. Verified at HEAD `42d187168` on 2026-09-10; line numbers drift — re-verify with the greps below before editing.

## ⛔ START HERE (do this first, exactly)

```bash
# ⛔ START HERE — do not touch code before this block succeeds
REPO=/Users/jdfalk/repos/github.com/jdfalk/audiobook-organizer   # the primary checkout (same path convention as every carried brief)
git -C "$REPO" fetch origin
git -C "$REPO" worktree add "$REPO/.worktrees/database-305" -b agent/database-305-migration-effect-migration-record-write origin/main
cd "$REPO/.worktrees/database-305"
git rebase origin/main
# Go task: do NOT run 'go work init .' — it breaks the build (ambiguous genproto imports)
```

(Protocol in `../ORCHESTRATION.md` and `docs/agent-tasks/ORCHESTRATION.md` — the inline block above is authoritative for this task.)

## Goal

Either (a) require every future Pebble `Up` function to be provably idempotent (document and lint-check it, as pebble_store_versiongroup_backfill.go already models), or (b) fold the migration-record write and the version write into the same batch the migration itself uses, and require `Up` implementations to accept and write into that batch instead of committing independently.

Why it matters: If the process crashes or is killed between `m.Up(store)` succeeding and `setVersion` succeeding, `getCurrentVersion` on the next boot still reports the pre-migration version, so `RunMigrations` re-runs the SAME migration's `Up` function. Every currently-registered migration happens to be a no-op for PebbleStore ('SQLite-only migration; no-op for PebbleStore', e.g. lines 594-598, 601-605, 608-612), so this is latent today, but the pattern is the general mechanism any future Pebble-side migration will inherit, and nothing in the framework enforces or even flags idempotency on `Up`.

## Background (verify before editing)

- RunMigrations (lines 451-469) does, per pending migration: `m.Up(store)` (runs the migration's side effects), then `recordMigration(store, m)` (a separate `store.SetUserPreference` write), then `setVersion(store, m.Version)` (a third separate `SetUserPreference` write). None of the three is wrapped in one atomic batch/transaction.
- Anchor: `internal/database/migrations.go:451` (audit `DB-02`, confidence medium, severity medium).
- **Adversarial re-check (2026-09-10, `state/final/adversarial_top11.json`): CONFIRMED** — RunMigrations loop: m.Up(store), recordMigration (SetUserPreference), setVersion — three separate writes, no batch.
  - Blast radius: migrations_extra_test.go; store.go:1380, testutil/integration.go:74.
  - Existing tests to extend: internal/database/migrations_extra_test.go
  - Standing-ban contact: none
  - Note: Latent today (every registered Up is a Pebble no-op). Option (b) shared batch means changing ~60 Up signatures — larger than Effort M; prefer option (a) idempotency requirement + lint.

- **Re-verify these anchors before editing** — a zero-hit grep means STOP and report:
  ```bash
  test -e internal/database/migrations.go   # the file the finding is anchored to still exists (-e: a directory anchor is valid too)
  sed -n '445,475p' internal/database/migrations.go   # expect the code described under Background (drifted lines: re-find by the quoted text)
  ```

## Step-by-step

1. Re-run the re-verify anchors; read the surrounding function end-to-end and confirm the finding still holds at HEAD (if it does not, STOP and report).
2. Implement the fix described under Goal in `internal/database/migrations.go` (and any sibling that shares the same shape — grep for the pattern before assuming there is one copy).
3. Write the regression test first (it must fail against the pre-fix code), then make it pass.
4. Run the gate; add the changelog fragment; bump headers; report with exact counts.

Then, always:
- Keep the change purely on-target — do not touch adjacent code, do not "clean up while you're in there".
- Bump the file header (`version` + `last-edited: 2026-09-10`) on every file you touch; keep existing guids. New files get a fresh guid (`uuidgen | tr A-Z a-z`).
- Add a changelog fragment `changelog.d/20260910_database_305.md` (NO file header; format per `changelog.d/README.md`: a `### Fixed|Changed|Added` heading, a `####` title, one paragraph).
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
- [ ] Changelog fragment present: `test -f changelog.d/20260910_database_305.md`.

## Commit message

```
fix(database): Migration effect, migration-record write, and schema-version write are (DB-02)

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
