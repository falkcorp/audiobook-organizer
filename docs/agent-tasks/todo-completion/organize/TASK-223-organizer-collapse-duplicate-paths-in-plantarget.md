<!-- file: docs/agent-tasks/todo-completion/organize/TASK-223-organizer-collapse-duplicate-paths-in-plantarget.md -->
<!-- version: 1.1.0 -->
<!-- guid: 70aa3243-811c-4c94-bb19-ce15b72ab714 -->
<!-- last-edited: 2026-09-02 -->

# TASK-223 — organizer: collapse duplicate paths in planTargetPaths so totalTracks counts files, not rows (DUPROW-1)

> **Status 2026-09-02:** ✅ DONE — PR #2689 merged 2026-08-22 (ba869ab36).

**Priority:** P1 · **Effort:** S · **Recommended subagent:** Sonnet-class · organize subagent · **Why:** Small edit, but it changes totalTracks and therefore the track numbers every organize run produces — the blast radius is every book, so the test must pin the numbering, not just the absence of a crash. · **Depends on:** none · **Wave:** 1 · **REVIEW-CRITICAL (prod-data path): PR stays open for the owner; never weak-tier**

Source: `TODO.md` line 90030 as of commit 46628240 (later edits shift lines) — re-find it with `sed -n '90030p' TODO.md` (line numbers drift; the grep is built from the line's own text). Scope file: `scope-21.json`.

## ⛔ START HERE (do this first, exactly)

```bash
# ⛔ START HERE — do not touch code before this block succeeds
REPO=/Users/jdfalk/repos/github.com/jdfalk/audiobook-organizer   # adjust to your clone
git -C "$REPO" fetch origin
git -C "$REPO" worktree add "$REPO/.worktrees/organize-223-organizer-collapse-duplicate-paths-in-plantarget" -b agent/organize-223-organizer-collapse-duplicate-paths-in-plantarget origin/main
cd "$REPO/.worktrees/organize-223-organizer-collapse-duplicate-paths-in-plantarget"
git rebase origin/main
# Go task: do NOT run 'go work init .' — it breaks the build (ambiguous genproto imports)
```

(Protocol also in `docs/agent-tasks/ORCHESTRATION.md` — the inline block above is authoritative for this task.)

## Goal

Extend the existing row-set normalization inside `planTargetPaths` (internal/organizer/pipeline.go:78) so that, alongside dropping empty-FilePath rows, it also collapses rows whose `filepath.Clean(strings.TrimSpace(FilePath))` is identical, keeping the FIRST such row encountered after the existing sort-stable ordering. Doing it here rather than in each caller is the point: the function's own doc comment (L83-95) says the row set is normalized HERE rather than trusting callers to agree, and this is the same class of bug that comment describes. After the change a book with 42 rows over 21 distinct paths plans 21 entries, `totalTracks` is 21, and the collision/track-suffix branch at L148 is not tripped.

## Background (verify before editing)

- planTargetPaths is the ONE shared planner: ComputeTargetPaths (L54) and OrganizeBookDirectory both go through it, so a fix here covers the organize path as well as the metafetch apply path.
- `totalTracks := len(sorted)` (L130) is deliberately documented (L124-129) to count Missing rows so a partially-missing book keeps stable track numbers. Collapsing DUPLICATE PATHS does not touch that rule — a duplicate is not a separate track — but it does change the number for any book that currently has duplicate rows, which is the intended correction.
- planPass (L166) emits one FileRenameEntry per row, keyed by SegmentID. Two rows sharing a SourcePath therefore yield two entries with the same source; RenameFiles moves the file on the first and fails `stat rename source ...: no such file or directory` on the second. That is the exact prod failure of 2026-08-21.
- The collision branch at L148 re-plans with forceTrackSuffix when two rows land on the same target. With duplicate rows it fires spuriously — the prod log 'files=42' for a 21-file book — and renames every file of the book to add a suffix it did not need.

- **Re-verify these anchors before editing** — line numbers drift; a zero-hit grep means STOP and report:
  ```bash
  grep -n "func planTargetPaths" internal/organizer/pipeline.go   # 1 hit at L78 — planTargetPaths is the shared normalizer for all three plan callers and already filters empty-path rows
  grep -n "totalTracks := len(sorted)" internal/organizer/pipeline.go   # 1 hit at L130 — totalTracks is a raw row count, which is what made a 21-file book plan as a 42-track book
  grep -n "file naming pattern does not distinguish" internal/organizer/pipeline.go   # 1 hit at L149 — the prod warning 'file naming pattern does not distinguish ... files=42' is emitted from the collision branch here
  grep -n "func planPass" internal/organizer/pipeline.go   # 1 hit at L166 — planPass keys its output on SegmentID, so two rows with the same SourcePath produce two rename entries — the second stats a source the first already moved
  grep -rln "planTargetPaths" internal/organizer/*_test.go   # 3 files: path_builder_characterization_test.go, organizer_regression_test.go, unit_test.go — existing characterization tests already exercise planTargetPaths and are the pattern to follow
  ```

### Reuse — don't invent

- Use `the existing row-set normalization block (empty-FilePath drop) at pipeline.go:99-105 — extend it, do not add a second normalization pass` in `internal/organizer/pipeline.go` (verify: `grep -n "if f.FilePath == \"\" {" internal/organizer/pipeline.go`) — do NOT write a parallel helper.
- Use `path_builder_characterization_test.go — existing table-test harness for planTargetPaths` in `internal/organizer/path_builder_characterization_test.go` (verify: `grep -n "planTargetPaths" internal/organizer/path_builder_characterization_test.go`) — do NOT write a parallel helper.

## Step-by-step

1. In internal/organizer/pipeline.go, locate the normalization loop at L99-105 (`sorted := make(...)` / `for _, f := range files { if f.FilePath == "" { continue }; sorted = append(sorted, f) }`).
2. Replace that loop body so it also skips a row whose cleaned path was already appended. Keep the existing empty-path drop first:

	sorted := make([]database.BookFile, 0, len(files))
	seenPath := make(map[string]struct{}, len(files))
	dupes := 0
	for _, f := range files {
		if f.FilePath == "" {
			continue
		}
		key := filepath.Clean(strings.TrimSpace(f.FilePath))
		if _, ok := seenPath[key]; ok {
			// Duplicate book_file rows for ONE path. Planning both gives two
			// entries with the same SourcePath: the first rename moves the file
			// and the second fails ENOENT. It also inflates totalTracks, which
			// renumbered a 21-file book as 42 tracks in production on 2026-08-21.
			dupes++
			continue
		}
		seenPath[key] = struct{}{}
		sorted = append(sorted, f)
	}
	if dupes > 0 {
		slog.Warn("duplicate book_file paths collapsed while planning target paths",
			"title", vars.Title, "rows", len(files), "distinct", len(sorted), "collapsed", dupes)
	}
3. Confirm `strings` is already imported in pipeline.go (it is — check with `grep -n '"strings"' internal/organizer/pipeline.go`); `filepath` and `slog` are already imported (lines 11-12 of the import block).
4. IMPORTANT ORDERING NOTE to put in a comment: the dedupe runs BEFORE the `sort.Slice` at L109, so 'first wins' means first in the caller's order, not first by track number. Do not move it after the sort — the sort is not stable with respect to equal (TrackNumber, FilePath) pairs and moving it would make the survivor non-deterministic.
5. Bump the `// version:` header on internal/organizer/pipeline.go (1.1.0 -> 1.2.0) and set `// last-edited:` to today's date.
6. Add a changelog fragment under changelog.d/ (no file header in the fragment).

Then, always:
- Keep the change purely transform — do not touch adjacent code, do not reorder imports beyond the formatter, do not change signatures unless a step above says so explicitly.
- Bump the file header (`version` + `last-edited: 2026-08-21`) on every file you touch; keep existing guids. New files get a fresh guid (`uuidgen | tr A-Z a-z`).
- Add a changelog fragment `changelog.d/20260821_organize_223.md` (NO file header; format per `changelog.d/README.md`: a `### Fixed|Changed|Added` heading, a `####` title, one paragraph).
- Do NOT edit `TODO.md` — the coordinator closes the source item in one commit per wave (every brief in a wave would otherwise collide on it). In your final report, state the exact `TODO.md` line text to check off. Never add new TODO items directly — use a `todo.d/` fragment (no header).

### Edge-case semantics (conservative defaults — treat unknown as unknown, never as disqualifying)

- Empty FilePath: keep the EXISTING behaviour — dropped outright, and never entered into seenPath (so two empty rows do not collapse into a phantom entry).
- A duplicate pair where one row is Missing and the other is not: first-wins means the Missing one can survive if it comes first. That is acceptable here because planPass skips Missing rows (L178) and totalTracks still counts it — same as any other Missing row. Do NOT add a not-Missing preference: it would change totalTracks semantics that pipeline.go:124-129 deliberately fixes.
- Trailing-slash / './' variants of the same path must collapse (filepath.Clean handles both). Case differences must NOT collapse — the library is on a case-sensitive mount.
- len(files)==0 or rootDir=="" → the existing early return at L79-81 still applies and no warning is logged.
- A book whose rows are ALL duplicates of one path (e.g. 6 rows, 1 path) plans exactly 1 entry with totalTracks==1, so the pattern gets no track suffix — correct, it is a single-file book.

## Tests

- internal/organizer/plan_dedupe_test.go (NEW file, needs the 4-line version header): `TestPlanTargetPaths_DuplicateRowsCollapseToDistinctFiles` — build 42 rows as 21 distinct paths x 2 (TrackNumber 0 on every row so numbering is position-derived), call planTargetPaths with a file pattern that has NO {track} placeholder (e.g. "{title} - {author}", the live prod pattern shape). Assert: len(entries)==21, and the produced filenames end '- 01' .. '- 21' (NOT '- 01' .. '- 42'). This pins totalTracks==21.
- internal/organizer/plan_dedupe_test.go: `TestPlanTargetPaths_NoDuplicateSourcePathIsPlannedTwice` — over the same 42-row input, collect entry.SourcePath into a map and assert every source appears exactly once. This is the direct assertion for the prod ENOENT.
- internal/organizer/plan_dedupe_test.go: ANTI-OVER-SUPPRESSION — `TestPlanTargetPaths_DistinctRowsAreAllPlanned`: 21 rows with 21 distinct paths and no duplicates → assert len(planTargetPaths(...))==21 and every input path appears as a SourcePath. Without this a change that returned only the first row would pass the two tests above.
- internal/organizer/plan_dedupe_test.go: `TestPlanTargetPaths_MissingRowsStillCountTowardTotalTracks` — 12 rows, 11 flagged Missing, all distinct paths → assert the single planned entry is numbered '- 07' if its TrackNumber is 7, proving the documented Missing-counting rule (pipeline.go:124-129) survives this change. Run the existing suite too: `go test ./internal/organizer/... -run PlanTargetPaths -count=1`.
- Re-run the existing characterization suite unchanged: `go test ./internal/organizer/... -count=1` — path_builder_characterization_test.go and organizer_regression_test.go both drive planTargetPaths and must stay green; if one fails, its fixture has duplicate paths and the EXPECTATION is what changed, so read it before editing it.

Anti-over-suppression test: `TestPlanTargetPaths_DistinctRowsAreAllPlanned` — a known-good input still passes with the new guard active.

## How to test

```bash
go build ./... && go vet ./... && go test ./internal/organizer/... -count=1
```
Do NOT use `make ci` as the gate: it is red on `main` from 10 pre-existing staticcheck findings unrelated to this task. Run `staticcheck ./<changed-pkg>/...` and fix only findings in files you touched. A failing test in a package you did not change is not yours — report it, do not fix it.

## Acceptance criteria

- [ ] `go build ./... && go vet ./... && go test ./internal/organizer/... -count=1` exits 0.
- [ ] `grep -n "duplicate book_file paths collapsed" internal/organizer/pipeline.go` returns exactly 1 hit.
- [ ] `go test ./internal/organizer/... -run 'PlanTargetPaths' -count=1 -v` shows TestPlanTargetPaths_DuplicateRowsCollapseToDistinctFiles, TestPlanTargetPaths_NoDuplicateSourcePathIsPlannedTwice and TestPlanTargetPaths_DistinctRowsAreAllPlanned all PASS.
- [ ] Anti-over-suppression test: `TestPlanTargetPaths_DistinctRowsAreAllPlanned` — a known-good input still passes with the new guard active.
- [ ] Edge cases above hold (nil/empty/unknown never disqualify; a test asserts it where a filter/guard is added).
- [ ] Gate green: `go build ./... && go vet ./... && go test ./internal/organizer/... -count=1` exits 0; `go vet`/lint clean.
- [ ] File headers bumped on every changed file (`grep -n "last-edited: 2026-08-21" <file>` hits for each).
- [ ] Changelog fragment present: `test -f changelog.d/20260821_organize_223.md`.

## Commit message

```
refactor(organize): organizer: collapse duplicate paths in planTargetPaths so to (DUPROW-1)

<why the change was needed; what it protects; what it deliberately does NOT change>

Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>
```

## Done

STOP — report done with exact counts (`COMPLETED: n — ...` / `REMAINING: n — ...` / `BLOCKED: n — ...`); the coordinator owns push/PR/merge. Do NOT run `git push`, `gh pr`, or any merge.

## Idempotency / Rollback

**This task touches persisted data, files on disk, or an apply path. `git revert` does NOT restore data.** Mandatory: (1) the op/endpoint defaults to dry-run / `apply=false` and prints what it WOULD change; (2) every mutation is journaled through the existing undo ledger (`CreateOperationChange` — verify: `grep -rn "func.*CreateOperationChange" internal/database/*.go`) so `internal/undo` can replay it — a mutation without a journal row is a defect; (3) acceptance includes a test that applies on a fixture and then undoes via `internal/undo` and asserts the fixture is byte-identical; (4) the apply path refuses to start while a `library.scan` operation is running or queued (check the registry for an active scan before mutating — a running scan clobbers applied metadata). Idempotency: re-running in dry-run must report 0 pending changes after a successful apply. Rollback of the CODE = `git revert`; rollback of the DATA = the undo ledger, which is why (2) is not optional. PR stays open for the owner — the coordinator never admin-merges it.

If the symbol already lives at its NEW location and is absent from the old one (re-run the re-verify greps above), the move is already done — run acceptance instead. Rollback = `git revert` the commit; behaviour at the old site is restored.

## Coordinator notes

internal/organizer/plan_dedupe_test.go is a NEW file (needs the 4-line version header). This is part 2 of todo_line 90030 and is INDEPENDENT of part 1 — no ordering dependency, but both touch the same incident so the coordinator should not run them in the same worktree (part 1 edits internal/metafetch only, part 2 edits internal/organizer only; no file overlap). Blast radius: this changes track numbering for any book that currently holds duplicate rows, so it should land alongside or after the maintenance repair op (todo_line 90032) rather than being treated as cosmetic.
