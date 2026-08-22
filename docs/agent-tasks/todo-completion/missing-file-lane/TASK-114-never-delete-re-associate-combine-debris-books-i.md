<!-- file: docs/agent-tasks/todo-completion/missing-file-lane/TASK-114-never-delete-re-associate-combine-debris-books-i.md -->
<!-- version: 1.0.0 -->
<!-- guid: ddc1fafe-6681-49f3-85e2-06183e0da806 -->
<!-- last-edited: 2026-08-21 -->

# TASK-114 — Never delete — re-associate: combine debris books into a template match by duration, then version-group (TODO.md L8943)

**Priority:** P1 · **Effort:** L · **Recommended subagent:** Opus-class · missing-file-lane subagent · **Why:** novel duration-based template-matching logic against a prod data path with hard never-delete constraints and messy real debris (partial coverage, internally-redundant files) — the highest-complexity, highest-risk item in this scope · **Depends on:** none · **Wave:** 1 · **REVIEW-CRITICAL (prod-data path): PR stays open for the owner; never weak-tier**

Source: `TODO.md` line 8943 as of commit 46628240 (later edits shift lines) — re-find it with `grep -n -F "**Never delete — re-associate (duplicate resolutio" TODO.md` (line numbers drift; the grep is built from the line's own text). Scope file: `scope-12.json`.

## ⛔ START HERE (do this first, exactly)

```bash
# ⛔ START HERE — do not touch code before this block succeeds
REPO=/Users/jdfalk/repos/github.com/jdfalk/audiobook-organizer   # adjust to your clone
git -C "$REPO" fetch origin
git -C "$REPO" worktree add "$REPO/.worktrees/missing-file-lane-114-never-delete-re-associate-combine-debris-books-i" -b agent/missing-file-lane-114-never-delete-re-associate-combine-debris-books-i origin/main
cd "$REPO/.worktrees/missing-file-lane-114-never-delete-re-associate-combine-debris-books-i"
git rebase origin/main
# Go task: do NOT run 'go work init .' — it breaks the build (ambiguous genproto imports)
```

(Protocol also in `docs/agent-tasks/ORCHESTRATION.md` — the inline block above is authoritative for this task.)

## Goal

Build the combine-by-template repair for redundant/debris book groups (the Successors class): (1) detect that a group's tracks map onto a better-assembled sibling book's track list; (2) combine the debris into ONE book using that sibling's track list as a TEMPLATE, matching debris files to template slots by DURATION rather than guessing boundaries from filenames; (3) version-group the result, primary = most complete book, ties broken by earliest ULID (reusing pickPrimary's convention); NEVER delete a book row — this op only re-associates/combines.

## Background (verify before editing)

- block_hash (DoNotImport) already exists as the ONLY current suppression mechanism for a re-appearing deleted-row, and it is a dead end (audio becomes permanently unrecoverable) — this op exists specifically to give the pipeline a real alternative to that dead end.
- The Successors debris example (cited by this item and by L8890) was 11 rows / 17 files covering 12 of 13 tracks with 5 internally-redundant files — the template-matching logic MUST handle partial coverage and internal redundancy as first-class cases, not edge cases discovered later.
- missing_file_repoint.go's apply=false-default + full-report-before-any-write + explicit collision handling is the closest in-repo precedent for 'match by measured evidence, never guess from names, report everything including the rejected rows' — follow that shape.

- **Re-verify these anchors before editing** — line numbers drift; a zero-hit grep means STOP and report:
  ```bash
  grep -rln 'Successors\|combine_by_template\|CombineByTemplate' internal --include='*.go'   # 0 hits — no combine-by-template / Successors-class code exists yet (a loose 'combine.into.one' regex false-positives on the unrelated anthology-detection action-label text in fs_regroup_shape.go/_test.go and regroup_shattered_ai_test.go)
  grep -rn 'DoNotImport\|block_hash' internal/database/*.go | head -3   # ≥1 hit — the standing never-delete rule and the DoNotImport/block_hash suppression it's weighed against already exist
  ```

### Reuse — don't invent

- Use `missing_file_repoint.go's report-before-write, TSV-per-row, apply=false-default pattern (directly analogous: this op also decides matches by duration and must never delete)` in `internal/plugins/maintenance/missing_file_repoint.go` (verify: `grep -n 'Apply bool' internal/plugins/maintenance/missing_file_repoint.go`) — do NOT write a parallel helper.
- Use `ApplyDuplicateOf / pickPrimary's version-grouping conventions` in `internal/plugins/maintenance/regroup_apply.go` (verify: `grep -n 'func pickPrimary\|func ApplyDuplicateOf' internal/plugins/maintenance/regroup_apply.go`) — do NOT write a parallel helper.

## Step-by-step

1. Find the 'group's tracks map onto a better-assembled sibling' detector. Do NOT invent one: use the existing candidate grouping in internal/plugins/dedup (enumerate with `grep -rn '^func ' internal/plugins/dedup/*.go` and name the chosen function in your report before writing code). If no suitable scorer exists, STOP and report - do not write a new similarity scorer inside this op.
2. Build the template-matching function: given the template's ordered track durations and the debris group's file durations, assign each debris file to the template slot whose duration it matches within a tolerance; files with no matching slot are reported as gaps, not silently dropped; multiple debris files matching one slot are reported as a collision needing a documented tie-break rule.
3. On apply=true only: MOVE the debris books' book_file rows onto the template book with the store's book-file reassignment call and leave every debris BOOK row in place (empty but present). Do NOT call merge.Service.CombineBooks or anything behind regroup_apply.go's bookCombiner - internal/merge/serialize.go:15 documents CombineBooks as hard-deleting shells, which violates the never-delete rule this op exists to honor.
4. Version-group the result: primary = most complete book, ties broken by earliest ULID, reusing pickPrimary's exact convention (internal/plugins/maintenance/regroup_apply.go:391) rather than reimplementing tie-breaking.
5. NEVER call a row-delete function anywhere in this file - mirror missing_file_repoint.go's structural (not just documented) never-delete guarantee, and assert it with TestCombineByTemplate_NeverDeletesARow.
6. Write a full per-group report (every debris file's outcome: matched-slot / gap / collision) before any apply-mode write, mirroring missing_file_repoint.go's report-before-summary ordering.
7. Default apply=false; register the op in internal/plugins/maintenance/plugin.go.

Then, always:
- Keep the change purely additive — do not touch adjacent code, do not reorder imports beyond the formatter, do not change signatures unless a step above says so explicitly.
- Bump the file header (`version` + `last-edited: 2026-08-21`) on every file you touch; keep existing guids. New files get a fresh guid (`uuidgen | tr A-Z a-z`).
- Add a changelog fragment `changelog.d/20260821_missing-file-lane_114.md` (NO file header; format per `changelog.d/README.md`: a `### Fixed|Changed|Added` heading, a `####` title, one paragraph).
- Do NOT edit `TODO.md` — the coordinator closes the source item in one commit per wave (every brief in a wave would otherwise collide on it). In your final report, state the exact `TODO.md` line text to check off. Never add new TODO items directly — use a `todo.d/` fragment (no header).

### Edge-case semantics (conservative defaults — treat unknown as unknown, never as disqualifying)

- A debris group with NO usable template candidate (no sibling book found) must report insufficient-evidence / no-candidate, not error out or skip silently.
- A template book that is ITSELF later found to be debris (nested bad data) is out of scope for this op — document that assumption rather than trying to detect it here.

## Tests

- TestCombineByTemplate_MatchesByDurationNotFilename — a debris file whose NAME suggests the wrong track but whose DURATION matches a different template slot is assigned by duration, proving filename guessing isn't happening.
- TestCombineByTemplate_ReportsGapsForMissingTracks — a debris group covering only 12 of 13 template slots reports the 13th as a gap, does not silently proceed as if complete.
- TestCombineByTemplate_ReportsRedundantFilesAsCollision — two debris files both matching one template slot are both reported, not silently deduped by picking one arbitrarily.
- TestCombineByTemplate_NeverDeletesARow — apply=true run against a fixture asserts the store's delete function is called zero times across the whole op, proving the never-delete guarantee structurally.
- TestCombineByTemplate_VersionGroupsWithEarliestULIDPrimary — the combined result's primary matches pickPrimary's own selection for the same ID set.

Anti-over-suppression test: `TestCombineByTemplate_NeverDeletesARow` — a known-good input still passes with the new guard active.

## How to test

```bash
go build ./... && go vet ./... && go test ./internal/plugins/maintenance/... -count=1
```
Do NOT use `make ci` as the gate: it is red on `main` from 10 pre-existing staticcheck findings unrelated to this task. Run `staticcheck ./<changed-pkg>/...` and fix only findings in files you touched. A failing test in a package you did not change is not yours — report it, do not fix it.

## Acceptance criteria

- [ ] go test ./internal/plugins/maintenance/... -run CombineByTemplate passes all five cases.
- [ ] Running against a fixture modeling the Successors shape (11 rows/17 files, 12 of 13 tracks, 5 redundant) in dry-run produces a report with exactly one gap row and five collision-flagged files, zero writes.
- [ ] Anti-over-suppression test: `TestCombineByTemplate_NeverDeletesARow` — a known-good input still passes with the new guard active.
- [ ] Edge cases above hold (nil/empty/unknown never disqualify; a test asserts it where a filter/guard is added).
- [ ] Gate green: `go build ./... && go vet ./... && go test ./internal/plugins/maintenance/... -count=1` exits 0; `go vet`/lint clean.
- [ ] File headers bumped on every changed file (`grep -n "last-edited: 2026-08-21" <file>` hits for each).
- [ ] Changelog fragment present: `test -f changelog.d/20260821_missing-file-lane_114.md`.

## Commit message

```
feat(missing-file-lane): Never delete — re-associate: combine debris books into a tem (TODO L8943)

<why the change was needed; what it protects; what it deliberately does NOT change>

Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>
```

## Done

STOP — report done with exact counts (`COMPLETED: n — ...` / `REMAINING: n — ...` / `BLOCKED: n — ...`); the coordinator owns push/PR/merge. Do NOT run `git push`, `gh pr`, or any merge.

## Idempotency / Rollback

**This task touches persisted data, files on disk, or an apply path. `git revert` does NOT restore data.** Mandatory: (1) the op/endpoint defaults to dry-run / `apply=false` and prints what it WOULD change; (2) every mutation is journaled through the existing undo ledger (`CreateOperationChange` — verify: `grep -rn "func.*CreateOperationChange" internal/database/*.go`) so `internal/undo` can replay it — a mutation without a journal row is a defect; (3) acceptance includes a test that applies on a fixture and then undoes via `internal/undo` and asserts the fixture is byte-identical; (4) the apply path refuses to start while a `library.scan` operation is running or queued (check the registry for an active scan before mutating — a running scan clobbers applied metadata). Idempotency: re-running in dry-run must report 0 pending changes after a successful apply. Rollback of the CODE = `git revert`; rollback of the DATA = the undo ledger, which is why (2) is not optional. PR stays open for the owner — the coordinator never admin-merges it.

If this presence check already passes at HEAD — `the artifact this task adds is present: re-run grep -rln 'Successors\|combine_by_template\|CombineByTemplate' internal --include='*.go'` — this task is already applied — run the acceptance checks instead of re-applying. Rollback = `git revert` the single commit; pre-existing behaviour is untouched (purely additive change).

## Coordinator notes

This is the umbrella 'never-delete-re-associate' track referenced by L8245 (3 real duplicate-shaped multidisc holds usable as future test fixtures, though those specifically default to 'separate', not this combine path), L8094 (the ~180 bracketed-number shattered-series books are input candidates for this exact mechanism once built), and L8890 part 2 (the First Aid roster's identical sub-bullet — do not build it twice, this is the canonical spec).
