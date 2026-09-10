<!-- file: docs/agent-tasks/todo-completion-2026-09/database/TASK-326-dual-write-activity-migration-the-secondary-sqli.md -->
<!-- version: 1.0.0 -->
<!-- guid: 9a9adb87-d630-4fe4-838c-e7a0371d9487 -->
<!-- last-edited: 2026-09-10 -->

# TASK-326 — Dual-write activity migration: the secondary (SQLite) backend receives every write immediately but Prune/Summarize/CompactByDay run only against the ACTIVE backend until the read flip, so the secondary accumulates unbounded rows for the entire migration window (OPS-02)

> **Status 2026-09-10:** 🆕 NEW — Wave 3 audit finding `OPS-02` (audit_database_operations.json)

**Priority:** P2 · **Effort:** S · **Recommended subagent:** Haiku-class · database subagent · **Depends on:** none · **Wave:** 1 

Source: Wave 3 audit finding `OPS-02` (audit_database_operations.json). Verified at HEAD `42d187168` on 2026-09-10; line numbers drift — re-verify with the greps below before editing.

## ⛔ START HERE (do this first, exactly)

```bash
# ⛔ START HERE — do not touch code before this block succeeds
REPO=/path/to/audiobook-organizer   # adjust to your clone
git -C "$REPO" fetch origin
git -C "$REPO" worktree add "$REPO/.worktrees/database-326" -b agent/database-326-dual-write-activity-migration-the-second origin/main
cd "$REPO/.worktrees/database-326"
git rebase origin/main
# Go task: do NOT run 'go work init .' — it breaks the build (ambiguous genproto imports)
```

(Protocol in `../ORCHESTRATION.md` and `docs/agent-tasks/ORCHESTRATION.md` — the inline block above is authoritative for this task.)

## Goal

Either also run Prune/CompactByDay against the secondary during the dual-write window (bounded, so it cannot race the backfill's own reads), or add an explicit alarm/metric on secondary row count vs. wall-clock time in dual-write mode so an abnormally long migration window is visible before it becomes a storage problem.

Why it matters: Every activity event the running server records lands in the secondary (SQLite) store from the moment dual-write starts, but nothing prunes, compacts, or digests it until the backfill completes and SetReadSecondary(true) is called (internal/activity/sql_migration.go:125). If the backfill/parity-verification phase runs for an extended period (it is explicitly a live, no-downtime, potentially multi-day cutover per the package doc), the secondary's live-traffic table grows without bound for that whole window with no compaction, which can turn the eventual flip into a bigger-than-necessary table and a slower first CompactByDay/RepairActivityIndexes pass.

## Background (verify before editing)

- Record (lines 114-131) writes to BOTH `m.primary` and `m.secondary` unconditionally on every call. But Summarize (161-163), Prune (165-167), CompactByDay (169-171), RecompactDigests (173-175) and RepairActivityIndexes (177-179) are all routed through `m.active()` only (line 95-100: active() returns primary until `readSecondary` flips true). The package doc (lines 16-28) states this is deliberate ('after the flip... never Pebble's unbounded, timeout-prone path'), but the mirror direction is unguarded: before the flip, the secondary is a write-only sink with zero maintenance run against it.
- Anchor: `internal/database/sql_activity_migrating_store.go:161` (audit `OPS-02`, confidence medium, severity medium).
- Related tracking: Project memory notes 'Activity SQLite is ON... Dual-write (read_secondary=false)' as the state at least as of the most recent note in scope, which is consistent with this window being live in prod right now, but no task was found addressing secondary-side growth specifically.

- **Re-verify these anchors before editing** — a zero-hit grep means STOP and report:
  ```bash
  test -f internal/database/sql_activity_migrating_store.go   # the file the finding is anchored to still exists
  sed -n '155,167p' internal/database/sql_activity_migrating_store.go   # expect the code described under Background (drifted lines: re-find by the quoted text)
  ```

## Step-by-step

1. Re-run the re-verify anchors; read the surrounding function end-to-end and confirm the finding still holds at HEAD (if it does not, STOP and report).
2. Implement the fix described under Goal in `internal/database/sql_activity_migrating_store.go` (and any sibling that shares the same shape — grep for the pattern before assuming there is one copy).
3. Write the regression test first (it must fail against the pre-fix code), then make it pass.
4. Run the gate; add the changelog fragment; bump headers; report with exact counts.

Then, always:
- Keep the change purely on-target — do not touch adjacent code, do not "clean up while you're in there".
- Bump the file header (`version` + `last-edited: 2026-09-10`) on every file you touch; keep existing guids. New files get a fresh guid (`uuidgen | tr A-Z a-z`).
- Add a changelog fragment `changelog.d/20260910_database_326.md` (NO file header; format per `changelog.d/README.md`: a `### Fixed|Changed|Added` heading, a `####` title, one paragraph).
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
- [ ] Changelog fragment present: `test -f changelog.d/20260910_database_326.md`.

## Commit message

```
fix(database): Dual-write activity migration: the secondary (SQLite) backend receives (OPS-02)

<why the change was needed; what it protects; what it deliberately does NOT change>

Co-Authored-By: Claude Fable 5.1 <noreply@anthropic.com>
```

## Done

STOP — report done with exact counts (`COMPLETED: n — ...` / `REMAINING: n — ...` / `BLOCKED: n — ...`); the coordinator owns push/PR/merge. Do NOT run `git push`, `gh pr`, or any merge.

## Idempotency / Rollback

Pure code change: rollback = `git revert` the commit. If the re-verify greps show the fix already present, run acceptance instead of re-implementing.

## Coordinator notes

Standard lane: coordinator may admin-merge on a green gate.
