<!-- file: docs/agent-tasks/todo-completion/metadata/TASK-221-metafetch-collapse-duplicate-book-file-rows-by-c.md -->
<!-- version: 1.0.0 -->
<!-- guid: e6f42012-6429-48cf-976b-3a7c64c2c121 -->
<!-- last-edited: 2026-08-21 -->

# TASK-221 — metafetch: collapse duplicate book_file rows by cleaned path before write/rename (DUPROW-1)

**Priority:** P1 · **Effort:** S · **Recommended subagent:** Sonnet-class · metadata subagent · **Why:** One new pure helper plus four mechanical call-site insertions, but the keeper-choice rule is data-adjacent and must match the existing rankKeeper order. · **Depends on:** none · **Wave:** 1 · **REVIEW-CRITICAL (prod-data path): PR stays open for the owner; never weak-tier**

Source: `TODO.md` line 90030 as of commit 46628240 (later edits shift lines) — re-find it with `sed -n '90030p' TODO.md` (line numbers drift; the grep is built from the line's own text). Scope file: `scope-21.json`.

## ⛔ START HERE (do this first, exactly)

```bash
# ⛔ START HERE — do not touch code before this block succeeds
REPO=/Users/jdfalk/repos/github.com/jdfalk/audiobook-organizer   # adjust to your clone
git -C "$REPO" fetch origin
git -C "$REPO" worktree add "$REPO/.worktrees/metadata-221-metafetch-collapse-duplicate-book-file-rows-by-c" -b agent/metadata-221-metafetch-collapse-duplicate-book-file-rows-by-c origin/main
cd "$REPO/.worktrees/metadata-221-metafetch-collapse-duplicate-book-file-rows-by-c"
git rebase origin/main
# Go task: do NOT run 'go work init .' — it breaks the build (ambiguous genproto imports)
```

(Protocol also in `docs/agent-tasks/ORCHESTRATION.md` — the inline block above is authoritative for this task.)

## Goal

Add ONE unexported helper `dedupeBookFilesByPath(bookID string, files []database.BookFile) []database.BookFile` to internal/metafetch/helpers.go and call it immediately after every `GetBookFiles` load on the apply pipeline, so no path in a book is ever written, renamed, or renumbered twice in a single run. It collapses rows whose cleaned absolute path (`filepath.Clean(strings.TrimSpace(f.FilePath))`) is identical, keeping the best-evidenced row using the same preference order the repo already reviewed for `rankKeeper` (has AcoustIDFingerprint > has Duration > has FileHash > lexicographically smallest ID). Rows with an empty path are passed through untouched (they are another op's problem). When it collapses anything it logs exactly one WARN per call: `slog.Warn("duplicate book_file rows collapsed", "book_id", bookID, "rows", len(files), "distinct", len(out), "collapsed", len(files)-len(out))`. This is the fix for the 2026-08-21 prod incident where book 01KZR9GEH5ZQW9CV1EN130Y7C0 held 42 rows for 21 distinct paths, every file was tag-written twice, and the second rename pass failed with `stat rename source ...: no such file or directory` because the first pass had already moved the file.

## Background (verify before editing)

- The apply pipeline has FOUR independent book-file load sites, all of which must be fixed or the incident only half-goes-away: service_writeback.go:412 (generateSegmentTitles), :480 (runApplyPipeline), :730 (writeBackForBook), and service.go:524 (RunApplyPipelineRenameOnly). Verified: grep -n "GetBookFiles(" internal/metafetch/*.go | grep -v _test.go.
- generateSegmentTitles (service_writeback.go:411) is the silent corrupter: it sets bookFiles[i].TrackNumber = i+1 and bookFiles[i].TrackCount = totalTracks over the raw row list, so a 21-file book with 42 rows is stamped as a 42-track book in the database.
- writeBackForBook (service_writeback.go:730) filters only on `!bf.Missing` and the optional segmentFilter — nothing collapses by path — which is why every file logged 'wrote metadata back to' twice (service_writeback.go:822).
- runApplyPipeline (service_writeback.go:464) feeds its raw row list into newPathOrganizer(...).ComputeTargetPaths, which is what produced the prod log line 'file naming pattern does not distinguish... files=42' for a 21-file book (internal/organizer/pipeline.go:149).
- The repo already has a reviewed, data-loss-critical keeper order in internal/plugins/maintenance/dedupe_book_file_rows.go:91 (rankKeeper). It is unexported and in another package, so it cannot be imported; matching the order by hand is required and the drift risk is recorded in notes.

- **Re-verify these anchors before editing** — line numbers drift; a zero-hit grep means STOP and report:
  ```bash
  grep -n "func (mfs \*Service) runApplyPipeline" internal/metafetch/service_writeback.go   # 1 hit at L464 (the GetBookFiles call is at L480) — runApplyPipeline loads book files at service_writeback.go:480 and never dedupes them
  grep -n "func (mfs \*Service) generateSegmentTitles" internal/metafetch/service_writeback.go   # 1 hit at L411 — generateSegmentTitles loads book files at service_writeback.go:412 and assigns TrackNumber=i+1 / TrackCount=len(rows), so duplicated rows corrupt track numbering
  grep -n "bookFiles, bfErr := mfs.db.GetBookFiles(book.ID)" internal/metafetch/service_writeback.go   # 1 hit at L730 — writeBackForBook loads book files at service_writeback.go:730 into activeFiles, which is what writes tags once per row
  grep -n "slog.Info(\"wrote metadata back to\"" internal/metafetch/service_writeback.go   # 2 hits at L822 (multi-file branch) and L870 (single-file branch) — the prod symptom 'wrote metadata back to' logged twice per file comes from the multi-file write loop at service_writeback.go:822
  grep -n "func (mfs \*Service) RunApplyPipelineRenameOnly" internal/metafetch/service.go   # 1 hit at L513 — RunApplyPipelineRenameOnly is a fourth apply-pipeline entry point that loads book files (service.go:524)
  grep -n "^func stripSubtitle" internal/metafetch/helpers.go   # 1 hit at L86 — the package already has an unexported-helpers file to host the new function
  grep -n "func TestGenerateSegmentTitles" internal/metafetch/service_mock_test.go   # 1 hit at L1258 — generateSegmentTitles already has a mock-store test harness to extend
  ```

### Reuse — don't invent

- Use `database.MockStore{GetBookFilesFunc, UpdateBookFileFunc} + NewService(mock) test harness` in `internal/metafetch/service_mock_test.go` (verify: `grep -n "GetBookFilesFunc: func(bookID string)" internal/metafetch/service_mock_test.go`) — do NOT write a parallel helper.
- Use `rankKeeper — the repo's existing, data-loss-reviewed keeper preference order (fingerprint > duration > hash > smallest ID). UNEXPORTED in another package: copy the ORDER, do not import.` in `internal/plugins/maintenance/dedupe_book_file_rows.go` (verify: `grep -n "func rankKeeper" internal/plugins/maintenance/dedupe_book_file_rows.go`) — do NOT write a parallel helper.
- Use `makeTestAudio — real-file fixture builder if a end-to-end test is wanted` in `internal/metafetch/service_writeback_realfile_test.go` (verify: `grep -n "func makeTestAudio" internal/metafetch/service_writeback_realfile_test.go`) — do NOT write a parallel helper.

## Step-by-step

1. In internal/metafetch/helpers.go, add imports as needed (`path/filepath`, `log/slog`, `strings` — check which are already imported before adding) and append a new unexported function:

func dedupeBookFilesByPath(bookID string, files []database.BookFile) []database.BookFile {
	if len(files) < 2 {
		return files
	}
	better := func(cand, cur database.BookFile) bool {
		// SAME ORDER as maintenance.rankKeeper: a fingerprint costs a full-file
		// decode and cannot be guessed back, so it outranks everything.
		cf, uf := len(cand.AcoustIDFingerprint) > 0, len(cur.AcoustIDFingerprint) > 0
		if cf != uf { return cf }
		cd, ud := cand.Duration > 0, cur.Duration > 0
		if cd != ud { return cd }
		ch, uh := strings.TrimSpace(cand.FileHash) != "", strings.TrimSpace(cur.FileHash) != ""
		if ch != uh { return ch }
		return cand.ID < cur.ID
	}
	idx := make(map[string]int, len(files))
	out := make([]database.BookFile, 0, len(files))
	for _, f := range files {
		key := strings.TrimSpace(f.FilePath)
		if key == "" {
			// A pathless row is not a duplicate of anything; pass it through so
			// this helper never changes which rows exist, only how many twins do.
			out = append(out, f)
			continue
		}
		key = filepath.Clean(key)
		if at, seen := idx[key]; seen {
			if better(f, out[at]) { out[at] = f }
			continue
		}
		idx[key] = len(out)
		out = append(out, f)
	}
	if len(out) != len(files) {
		slog.Warn("duplicate book_file rows collapsed",
			"book_id", bookID, "rows", len(files), "distinct", len(out),
			"collapsed", len(files)-len(out))
	}
	return out
}

Add a doc comment above it explaining the prod incident (book 01KZR9GEH5ZQW9CV1EN130Y7C0, 42 rows / 21 paths, double tag write + rename ENOENT) and that the ORDER of `better` mirrors maintenance.rankKeeper.
2. In internal/metafetch/service_writeback.go, inside generateSegmentTitles: find the line `bookFiles, err := mfs.db.GetBookFiles(bookID)` at L412 and insert immediately after its error check: `bookFiles = dedupeBookFilesByPath(bookID, bookFiles)`. Do this BEFORE the `totalTracks` computation and the `for i := range bookFiles` loop so TrackCount is the distinct-path count.
3. In internal/metafetch/service_writeback.go, inside runApplyPipeline: after the `bookFiles, err := mfs.db.GetBookFiles(id)` block at L480 and its `if len(bookFiles) == 0 { return nil }` guard, insert `bookFiles = dedupeBookFilesByPath(id, bookFiles)`. It must come BEFORE the `newPathOrganizer(mfs.db).ComputeTargetPaths(book, bookFiles)` call at L493.
4. In internal/metafetch/service_writeback.go, inside writeBackForBook: at L730 `bookFiles, bfErr := mfs.db.GetBookFiles(book.ID)`, insert `bookFiles = dedupeBookFilesByPath(book.ID, bookFiles)` right after the `if bfErr != nil { bookFiles = nil }` block and BEFORE the `!bf.Missing` filter that builds activeFiles.
5. In internal/metafetch/service.go, inside RunApplyPipelineRenameOnly (starts L513): after `bookFiles, err := mfs.db.GetBookFiles(id)` at L524 and its error return, insert `bookFiles = dedupeBookFilesByPath(id, bookFiles)`. Place it BEFORE the `if len(bookFiles) == 0 && book.FilePath != ""` virtual-entry branch so the virtual-entry logic is unaffected.
6. Bump the `// version:` header on internal/metafetch/helpers.go, internal/metafetch/service_writeback.go and internal/metafetch/service.go (minor bump, e.g. 1.2.0 -> 1.3.0) and set `// last-edited:` to today's date on each.
7. Add a changelog fragment under changelog.d/ describing the fix (NO file/version/guid header in the fragment — fragments are header-exempt).

Then, always:
- Keep the change purely additive — do not touch adjacent code, do not reorder imports beyond the formatter, do not change signatures unless a step above says so explicitly.
- Bump the file header (`version` + `last-edited: 2026-08-21`) on every file you touch; keep existing guids. New files get a fresh guid (`uuidgen | tr A-Z a-z`).
- Add a changelog fragment `changelog.d/20260821_metadata_221.md` (NO file header; format per `changelog.d/README.md`: a `### Fixed|Changed|Added` heading, a `####` title, one paragraph).
- Do NOT edit `TODO.md` — the coordinator closes the source item in one commit per wave (every brief in a wave would otherwise collide on it). In your final report, state the exact `TODO.md` line text to check off. Never add new TODO items directly — use a `todo.d/` fragment (no header).

### Edge-case semantics (conservative defaults — treat unknown as unknown, never as disqualifying)

- len(files) < 2 → return the input slice unchanged and log nothing.
- FilePath empty or whitespace-only → the row is NOT a dedupe candidate; append it verbatim. Two empty-path rows both survive. internal/organizer/pipeline.go:101 already drops them downstream.
- All rows identical in every ranking field → the lexicographically smallest ID wins, so a dry run and a later run pick the same survivor.
- Rows flagged Missing are NOT special-cased here: a Missing twin of a present row still collapses (the present row wins on Duration/hash/fingerprint or on ID). Do not add a Missing filter — that is planTargetPaths' job and duplicating it here would change totalTracks semantics.
- The helper returns a NEW slice; callers must reassign (`bookFiles = dedupeBookFilesByPath(...)`). It must not mutate the input slice in place, because runApplyPipeline later builds bfMap over the returned slice and writes bf.FilePath back to the DB.
- Case sensitivity: compare paths byte-exactly after Clean/TrimSpace. Do NOT lowercase — the library lives on a case-sensitive NAS mount and two case-different paths are two real files.

## Tests

- internal/metafetch/service_writeback_duprows_test.go (NEW file, needs the standard 4-line // file/version/guid/last-edited header): `TestDedupeBookFilesByPath_CollapsesExactDuplicates` — table test on the pure helper. Case 'prod shape': 42 rows built as 21 paths x 2 → asserts len(out)==21 and that the returned paths are the 21 distinct ones in first-seen order.
- internal/metafetch/service_writeback_duprows_test.go: `TestDedupeBookFilesByPath_KeepsTheFingerprintedTwin` — two rows, same FilePath, the SECOND carrying AcoustIDFingerprint: []byte{0x01}; asserts the surviving row's ID is the fingerprinted one. This is the data-loss assertion.
- internal/metafetch/service_writeback_duprows_test.go: `TestDedupeBookFilesByPath_KeeperOrderMatchesRankKeeper` — three sub-cases mirroring dedupe_book_file_rows_test.go: fingerprint beats duration; duration beats nothing; on a full tie the lexicographically smallest ID wins (assert determinism by running the helper twice on freshly-built equal inputs and comparing the surviving IDs).
- internal/metafetch/service_writeback_duprows_test.go: `TestDedupeBookFilesByPath_NormalizesPathBeforeComparing` — rows with '/a/b/1.mp3' and '/a/./b/1.mp3' and '/a/b/1.mp3 ' (trailing space) collapse to one.
- internal/metafetch/service_writeback_duprows_test.go: ANTI-OVER-SUPPRESSION — `TestDedupeBookFilesByPath_DistinctPathsAllSurvive`: 21 rows with 21 different paths → asserts len(out)==21 and that the slice is returned with every original ID present. Without this a helper that returned only the first row would pass every other test.
- internal/metafetch/service_writeback_duprows_test.go: `TestDedupeBookFilesByPath_EmptyPathRowsPassThrough` — two rows with FilePath "" plus one real path → asserts all three survive (empty paths are not duplicates of each other).
- internal/metafetch/service_mock_test.go: extend TestGenerateSegmentTitles with sub-test `duplicate_rows_do_not_inflate_track_count` — GetBookFilesFunc returns 4 rows across 2 distinct paths; asserts UpdateBookFileFunc was called exactly 2 times and every captured file has TrackCount == 2 (NOT 4). This is the regression that reproduces the prod 'files=42' symptom at the DB level.

Anti-over-suppression test: `TestDedupeBookFilesByPath_DistinctPathsAllSurvive` — a known-good input still passes with the new guard active.

## How to test

```bash
go build ./... && go vet ./... && go test ./internal/metafetch/... -count=1
```
Do NOT use `make ci` as the gate: it is red on `main` from 10 pre-existing staticcheck findings unrelated to this task. Run `staticcheck ./<changed-pkg>/...` and fix only findings in files you touched. A failing test in a package you did not change is not yours — report it, do not fix it.

## Acceptance criteria

- [ ] `go build ./... && go vet ./... && go test ./internal/metafetch/... -count=1` exits 0.
- [ ] `grep -c "dedupeBookFilesByPath(" internal/metafetch/service_writeback.go internal/metafetch/service.go internal/metafetch/helpers.go` reports 3, 1 and 1 respectively (3 call sites in service_writeback.go, 1 in service.go, 1 definition in helpers.go).
- [ ] `grep -n "duplicate book_file rows collapsed" internal/metafetch/helpers.go` returns exactly 1 hit.
- [ ] `go test ./internal/metafetch/... -run 'DedupeBookFilesByPath|GenerateSegmentTitles' -count=1 -v` shows every listed test PASS, including TestDedupeBookFilesByPath_DistinctPathsAllSurvive.
- [ ] Anti-over-suppression test: `TestDedupeBookFilesByPath_DistinctPathsAllSurvive` — a known-good input still passes with the new guard active.
- [ ] Edge cases above hold (nil/empty/unknown never disqualify; a test asserts it where a filter/guard is added).
- [ ] Gate green: `go build ./... && go vet ./... && go test ./internal/metafetch/... -count=1` exits 0; `go vet`/lint clean.
- [ ] File headers bumped on every changed file (`grep -n "last-edited: 2026-08-21" <file>` hits for each).
- [ ] Changelog fragment present: `test -f changelog.d/20260821_metadata_221.md`.

## Commit message

```
feat(metadata): metafetch: collapse duplicate book_file rows by cleaned path (DUPROW-1)

<why the change was needed; what it protects; what it deliberately does NOT change>

Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>
```

## Done

STOP — report done with exact counts (`COMPLETED: n — ...` / `REMAINING: n — ...` / `BLOCKED: n — ...`); the coordinator owns push/PR/merge. Do NOT run `git push`, `gh pr`, or any merge.

## Idempotency / Rollback

**This task touches persisted data, files on disk, or an apply path. `git revert` does NOT restore data.** Mandatory: (1) the op/endpoint defaults to dry-run / `apply=false` and prints what it WOULD change; (2) every mutation is journaled through the existing undo ledger (`CreateOperationChange` — verify: `grep -rn "func.*CreateOperationChange" internal/database/*.go`) so `internal/undo` can replay it — a mutation without a journal row is a defect; (3) acceptance includes a test that applies on a fixture and then undoes via `internal/undo` and asserts the fixture is byte-identical; (4) the apply path refuses to start while a `library.scan` operation is running or queued (check the registry for an active scan before mutating — a running scan clobbers applied metadata). Idempotency: re-running in dry-run must report 0 pending changes after a successful apply. Rollback of the CODE = `git revert`; rollback of the DATA = the undo ledger, which is why (2) is not optional. PR stays open for the owner — the coordinator never admin-merges it.

If this presence check already passes at HEAD — ``grep -c "dedupeBookFilesByPath(" internal/metafetch/service_writeback.go internal/metafetch/service.go internal/metafetch/helpers.go` reports 3, 1 and 1 respectively (3 call sites in service_writeback.go, 1 in service.go, 1 definition in helpers.go).` — this task is already applied — run the acceptance checks instead of re-applying. Rollback = `git revert` the single commit; pre-existing behaviour is untouched (purely additive change).

## Coordinator notes

internal/metafetch/service_writeback_duprows_test.go is a NEW file (needs the 4-line version header; only changelog.d/ and todo.d/ fragments are header-exempt). DRIFT RISK worth stating in the helper's doc comment: the keeper order is duplicated from maintenance.rankKeeper (internal/plugins/maintenance/dedupe_book_file_rows.go:91) because that function is unexported in another package; if either order changes the two must be changed together. Part 2 of this todo_line (organizer planTargetPaths) is independent and can land in either order — this part alone fixes the reported incident.
