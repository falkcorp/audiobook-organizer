<!-- file: docs/agent-tasks/todo-completion-2026-09/database/TASK-334-digest-compaction-swallows-the-delete-error-for.md -->
<!-- version: 1.0.0 -->
<!-- guid: c7ec28a2-1be7-4eec-b202-d0e3553d2a2c -->
<!-- last-edited: 2026-09-10 -->

# TASK-334 — Digest compaction swallows the delete error for the pre-existing digest row, risking a duplicate digest entry on I/O failure (dead-code backend, low impact) (DB-04)

> **Status 2026-09-10:** 🆕 NEW — Wave 3 audit finding `DB-04` (audit_database_operations.json)

**Priority:** P3 · **Effort:** S · **Recommended subagent:** Haiku-class · database subagent · **Depends on:** none · **Wave:** 1 

Source: Wave 3 audit finding `DB-04` (audit_database_operations.json). Verified at HEAD `42d187168` on 2026-09-10; line numbers drift — re-verify with the greps below before editing.

## ⛔ START HERE (do this first, exactly)

```bash
# ⛔ START HERE — do not touch code before this block succeeds
REPO=/path/to/audiobook-organizer   # adjust to your clone
git -C "$REPO" fetch origin
git -C "$REPO" worktree add "$REPO/.worktrees/database-334" -b agent/database-334-digest-compaction-swallows-the-delete-er origin/main
cd "$REPO/.worktrees/database-334"
git rebase origin/main
# Go task: do NOT run 'go work init .' — it breaks the build (ambiguous genproto imports)
```

(Protocol in `../ORCHESTRATION.md` and `docs/agent-tasks/ORCHESTRATION.md` — the inline block above is authoritative for this task.)

## Goal

If NutsActivityStore is being kept as a rollback reference until removal, fix the swallowed error for correctness parity with the pattern used three lines below (check and propagate, or explicitly note why it's safe to ignore). If removal is imminent, this is moot and can be left for the removal PR.

Why it matters: Two digest rows for the same date would double-count that day's digest in any UI/summary that sums per-day digests. In practice this is low-impact: grep confirms `NewNutsActivityStore` has zero production call sites in this tree (only referenced in comments and `dual_write_activity_store.go`, which is itself marked 'UNUSED as of 2026-07-03 (TASK-22)... will be deleted... once the soak period has passed'), so this code path is dormant pending removal.

## Background (verify before editing)

- In the daily-digest compaction transaction (lines 608-630): `if existingKey != nil { _ = tx.Delete(actBucket("digest"), existingKey) }` discards the error entirely (not even filtering `IsKeyNotFound` the way the originals-delete loop three lines later does at line 623). If the delete fails for a real I/O reason, the code still proceeds to `tx.Put` the new digest at a freshly-minted ULID key (line 617), leaving the old digest row behind alongside the new one for the same day.
- Anchor: `internal/database/nuts_activity_store.go:614` (audit `DB-04`, confidence low, severity low).
- Related tracking: dual_write_activity_store.go's own header already tracks NutsActivityStore for deletion 'in the follow-up NutsDB-removal PR once the soak period has passed and an owner has greenlit removal' -- this finding is only relevant if that removal is delayed further.

- **Re-verify these anchors before editing** — a zero-hit grep means STOP and report:
  ```bash
  test -f internal/database/nuts_activity_store.go   # the file the finding is anchored to still exists
  sed -n '608,620p' internal/database/nuts_activity_store.go   # expect the code described under Background (drifted lines: re-find by the quoted text)
  ```

## Step-by-step

1. Re-run the re-verify anchors; read the surrounding function end-to-end and confirm the finding still holds at HEAD (if it does not, STOP and report).
2. Implement the fix described under Goal in `internal/database/nuts_activity_store.go` (and any sibling that shares the same shape — grep for the pattern before assuming there is one copy).
3. Write the regression test first (it must fail against the pre-fix code), then make it pass.
4. Run the gate; add the changelog fragment; bump headers; report with exact counts.

Then, always:
- Keep the change purely on-target — do not touch adjacent code, do not "clean up while you're in there".
- Bump the file header (`version` + `last-edited: 2026-09-10`) on every file you touch; keep existing guids. New files get a fresh guid (`uuidgen | tr A-Z a-z`).
- Add a changelog fragment `changelog.d/20260910_database_334.md` (NO file header; format per `changelog.d/README.md`: a `### Fixed|Changed|Added` heading, a `####` title, one paragraph).
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
- [ ] Changelog fragment present: `test -f changelog.d/20260910_database_334.md`.

## Commit message

```
fix(database): Digest compaction swallows the delete error for the pre-existing diges (DB-04)

<why the change was needed; what it protects; what it deliberately does NOT change>

Co-Authored-By: Claude Fable 5.1 <noreply@anthropic.com>
```

## Done

STOP — report done with exact counts (`COMPLETED: n — ...` / `REMAINING: n — ...` / `BLOCKED: n — ...`); the coordinator owns push/PR/merge. Do NOT run `git push`, `gh pr`, or any merge.

## Idempotency / Rollback

Pure code change: rollback = `git revert` the commit. If the re-verify greps show the fix already present, run acceptance instead of re-implementing.

## Coordinator notes

Standard lane: coordinator may admin-merge on a green gate.
