<!-- file: docs/agent-tasks/todo-completion/maintenance/TASK-068-build-a-report-only-counter-for-book-filepath-co.md -->
<!-- version: 1.0.0 -->
<!-- guid: 2321cc46-d0bc-44dc-a7d7-1edec8a0bd79 -->
<!-- last-edited: 2026-08-21 -->

# TASK-068 — Build a REPORT-ONLY counter for Book.FilePath collisions (rows sharing the same path across different books) (TODO.md L670)

**Priority:** P2 · **Effort:** S · **Recommended subagent:** Sonnet-class · maintenance subagent · **Why:** small self-contained report op but must use a bounded worker pool / sharded map per the repo's whole-library concurrency mandate, not a naive single-threaded scan · **Depends on:** none · **Wave:** 1

Source: `TODO.md` line 670 as of commit 46628240 (later edits shift lines) — re-find it with `grep -n -F "**`Book.FilePath` is NOT unique — 1,264 values are" TODO.md` (line numbers drift; the grep is built from the line's own text). Scope file: `scope-02.json`.

## ⛔ START HERE (do this first, exactly)

```bash
# ⛔ START HERE — do not touch code before this block succeeds
REPO=/Users/jdfalk/repos/github.com/jdfalk/audiobook-organizer   # adjust to your clone
git -C "$REPO" fetch origin
git -C "$REPO" worktree add "$REPO/.worktrees/maintenance-068-build-a-report-only-counter-for-book-filepath-co" -b agent/maintenance-068-build-a-report-only-counter-for-book-filepath-co origin/main
cd "$REPO/.worktrees/maintenance-068-build-a-report-only-counter-for-book-filepath-co"
git rebase origin/main
# Go task: do NOT run 'go work init .' — it breaks the build (ambiguous genproto imports)
```

(Protocol also in `docs/agent-tasks/ORCHESTRATION.md` — the inline block above is authoritative for this task.)

## Goal

Build a new REPORT-ONLY maintenance op (no writes, matches the pattern of missing-file-audit.go) that enumerates every book's FilePath, buckets by exact string match, and reports how many books share a FilePath with another book -- reproducing and keeping current the 1,264-values/4,353-of-63,870-rows/6.8% figure so it can be re-run before any future op extends Book.FilePath-derived logic into a WRITE path.

## Background (verify before editing)

- TODO.md L670-L169: 1,264 Book.FilePath values are shared by more than one book row (4,353 of 63,870 rows, 6.8%); today it happens not to bite the chapters-backfill fallback (0 of 88 sampled recoverable rows are among the 4,353) but that is a property of today's data, not a guarantee.
- The item explicitly demands: 're-run the collision count before extending the fallback to any op that WRITES a book row' -- this directly gates L642's extension of missing-file-repoint to use Book.FilePath as a repoint source.

- **Re-verify these anchors before editing** — line numbers drift; a zero-hit grep means STOP and report:
  ```bash
  grep -rn 'FilePathCollision\|CollisionCount\|filepath_collision' --include="*.go" .   # 0 hits — no existing FilePath-collision counter exists anywhere in the codebase (a loose 'FilePath.*collision|duplicate.*FilePath' regex false-positives on unrelated duplicate-detection code in organizer.go, scanner.go, reconcile.go, and missing_file_repoint.go's unrelated 'collision' repoint bucket)
  grep -n 'GetAllBooksCore(limit int, offset int)' internal/plugins/maintenance/deps.go   # 1 hit — GetAllBooksCore is the existing narrow whole-library enumeration method to reuse
  ```

### Reuse — don't invent

- Use `opsBookReader.GetAllBooksCore paginated enumeration` in `internal/plugins/maintenance/deps.go` (verify: `grep -n 'GetAllBooksCore' internal/plugins/maintenance/deps.go`) — do NOT write a parallel helper.
- Use `registry.RunItems bounded worker pool (CLAUDE.md concurrency mandate for whole-library loops)` in `internal/operations/registry/run_items.go` (verify: `grep -n 'func RunItems' internal/operations/registry/run_items.go`) — do NOT write a parallel helper.
- Use `sdk.OperationDef / dry-run-by-default pattern from missing_file_audit.go` in `internal/plugins/maintenance/missing_file_audit.go` (verify: `grep -n 'ID:          "maintenance.missing-file-audit"' internal/plugins/maintenance/missing_file_audit.go`) — do NOT write a parallel helper.

## Step-by-step

1. Create internal/plugins/maintenance/filepath_collision_report.go modeled on missing_file_audit.go's structure (header comment, sdk.OperationDef with a new ID e.g. 'maintenance.filepath-collision-report', Capabilities: []sdk.Capability{sdk.CapLibraryRead}, no CapLibraryWrite since this never writes).
2. Enumerate books via GetAllBooksCore in pages (or ListBookIDs + GetBookByID batches), building a map[string][]string (FilePath -> []bookID) sharded across N workers via registry.RunItems, then merge shard maps under a single mutex only at the reduce step (per CLAUDE.md's pairwise/sharded-loop concurrency mandate).
3. Report: total books, total distinct FilePath values with >1 owner, total rows affected, and (capped, e.g. first 50) example collision groups with their book IDs and the shared path, for manual follow-up.
4. Wire the op into the maintenance plugin's registration list (grep for where other ops are registered, e.g. plugin.go's list of *Def() calls) and add the file header per file-headers.md.

Then, always:
- Keep the change purely additive — do not touch adjacent code, do not reorder imports beyond the formatter, do not change signatures unless a step above says so explicitly.
- Bump the file header (`version` + `last-edited: 2026-08-21`) on every file you touch; keep existing guids. New files get a fresh guid (`uuidgen | tr A-Z a-z`).
- Add a changelog fragment `changelog.d/20260821_maintenance_068.md` (NO file header; format per `changelog.d/README.md`: a `### Fixed|Changed|Added` heading, a `####` title, one paragraph).
- Do NOT edit `TODO.md` — the coordinator closes the source item in one commit per wave (every brief in a wave would otherwise collide on it). In your final report, state the exact `TODO.md` line text to check off. Never add new TODO items directly — use a `todo.d/` fragment (no header).

### Edge-case semantics (conservative defaults — treat unknown as unknown, never as disqualifying)

- Empty FilePath (blank string) should not itself count as a 'collision' bucket -- exclude rows with FilePath == "" from the map, or bucket them separately and call it out distinctly in the report.
- Soft-deleted books: decide (and document) whether ListSoftDeletedBooks rows participate in the collision count -- likely excluded, since a soft-deleted book's path collision with a live book is a different risk profile.

## Tests

- internal/plugins/maintenance/filepath_collision_report_test.go: TestFilePathCollisionReport_DetectsSharedPath -- 3 books, 2 sharing an identical FilePath, 1 unique; report counts collisionGroups=1, affectedRows=2.
- TestFilePathCollisionReport_NoCollisions_ReportsZero -- all-unique FilePath fixture reports 0 (anti-over-suppression: proves the detector doesn't fire on clean data).

Anti-over-suppression test: `TestFilePathCollisionReport_NoCollisions_ReportsZero` — a known-good input still passes with the new guard active.

## How to test

```bash
go build ./... && go vet ./... && go test ./internal/plugins/maintenance/... -count=1
```
Do NOT use `make ci` as the gate: it is red on `main` from 10 pre-existing staticcheck findings unrelated to this task. Run `staticcheck ./<changed-pkg>/...` and fix only findings in files you touched. A failing test in a package you did not change is not yours — report it, do not fix it.

## Acceptance criteria

- [ ] go test ./internal/plugins/maintenance/... -run TestFilePathCollisionReport passes.
- [ ] A dry run against a full-library fixture reproduces a collision-rate consistent with the 6.8% figure order-of-magnitude (exact figure will drift as the library changes; the op's job is to make it re-derivable, not to hard-code 6.8%).
- [ ] Anti-over-suppression test: `TestFilePathCollisionReport_NoCollisions_ReportsZero` — a known-good input still passes with the new guard active.
- [ ] Edge cases above hold (nil/empty/unknown never disqualify; a test asserts it where a filter/guard is added).
- [ ] Gate green: `go build ./... && go vet ./... && go test ./internal/plugins/maintenance/... -count=1` exits 0; `go vet`/lint clean.
- [ ] File headers bumped on every changed file (`grep -n "last-edited: 2026-08-21" <file>` hits for each).
- [ ] Changelog fragment present: `test -f changelog.d/20260821_maintenance_068.md`.

## Commit message

```
feat(maintenance): Build a REPORT-ONLY counter for Book.FilePath collisions (ro (TODO L670)

<why the change was needed; what it protects; what it deliberately does NOT change>

Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>
```

## Done

STOP — report done with exact counts (`COMPLETED: n — ...` / `REMAINING: n — ...` / `BLOCKED: n — ...`); the coordinator owns push/PR/merge. Do NOT run `git push`, `gh pr`, or any merge.

## Idempotency / Rollback

If the first acceptance check below already passes at HEAD (`go test ./internal/plugins/maintenance/... -run TestFilePathCollisionReport passes.`), this task is already applied — run the acceptance checks instead of re-applying. Rollback = `git revert` the single commit; pre-existing behaviour is untouched (purely additive change).

## Coordinator notes

This is a small, self-contained prerequisite for L642 and for L665's eventual authority decision -- keep it decoupled from those two rather than folding it into either PR, since it has independent value as a standing library-health report.
