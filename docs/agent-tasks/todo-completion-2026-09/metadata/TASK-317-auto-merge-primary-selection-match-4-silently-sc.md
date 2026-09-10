<!-- file: docs/agent-tasks/todo-completion-2026-09/metadata/TASK-317-auto-merge-primary-selection-match-4-silently-sc.md -->
<!-- version: 1.7.0 -->
<!-- guid: 730ccffe-ac3f-4791-a5f4-d47d66e6d769 -->
<!-- last-edited: 2026-09-10 -->

# TASK-317 — Auto-merge primary-selection (MATCH-4) silently scores a book as having zero files when GetBookFiles errors, which can pick the wrong book as merge primary (SF-04)

> **Status 2026-09-10:** 🆕 NEW — Wave 3 audit finding `SF-04` (audit_silent_failures_pipeline.json)

**Priority:** P2 · **Effort:** S · **Recommended subagent:** Haiku-class · metadata subagent · **Depends on:** none · **Wave:** per ../orchestration.md (collision-aware) 

Source: Wave 3 audit finding `SF-04` (audit_silent_failures_pipeline.json). Verified at HEAD `42d187168` on 2026-09-10; line numbers drift — re-verify with the greps below before editing.

## ⛔ START HERE (do this first, exactly)

```bash
# ⛔ START HERE — do not touch code before this block succeeds
REPO=/Users/jdfalk/repos/github.com/jdfalk/audiobook-organizer   # the primary checkout (same path convention as every carried brief)
git -C "$REPO" fetch origin
git -C "$REPO" worktree add "$REPO/.worktrees/metadata-317" -b agent/metadata-317-auto-merge-primary-selection-match-4-sil origin/main
cd "$REPO/.worktrees/metadata-317"
git rebase origin/main
# Go task: do NOT run 'go work init .' — it breaks the build (ambiguous genproto imports)
```

(Protocol in `../ORCHESTRATION.md` and `docs/agent-tasks/ORCHESTRATION.md` — the inline block above is authoritative for this task.)

## Goal

Log the GetBookFiles error per candidate (mirroring the existing slog.Warn calls elsewhere in this function), and treat an error as "skip this candidate for primary selection this run" rather than silently equating it with zero files, or make the merge decision retry rather than default to zero.

Why it matters: This function automatically demotes every non-primary book to merged_into_book_id, which is a real, user-visible library mutation (the loser's rows stop being independently visible). A transient DB read error on the book that actually has the most files silently makes it lose the primary election to a book with fewer or no real files, and there is no log trail explaining why — an operator investigating a bad auto-merge has nothing to go on.

## Background (verify before editing)

- checkMetadataSourceHashDuplicates (service_apply.go:908-961) picks which of several same-metadata-hash books becomes the merge `primaryID` by comparing `len(files)` per candidate: `files, err := mfs.db.GetBookFiles(id); n := 0; if err == nil { n = len(files) }` (lines 934-938). On a GetBookFiles error the candidate is scored as having 0 files — the same score a book that legitimately has zero files gets — and the error is not logged at all (no slog.Warn anywhere in this loop, unlike the rest of the function which logs both the outer query failure at line 911 and every FlagMetadataHashDuplicate failure at line 956).
- Anchor: `internal/metafetch/service_apply.go:933` (audit `SF-04`, confidence medium, severity medium).

- **Re-verify these anchors before editing** — a zero-hit grep means STOP and report:
  ```bash
  test -e internal/metafetch/service_apply.go   # the file the finding is anchored to still exists (-e: a directory anchor is valid too)
  sed -n '902,967p' internal/metafetch/service_apply.go   # expect the code described under Background (drifted lines: re-find by the quoted text)
  ```

## Step-by-step

1. Re-run the re-verify anchors; read the surrounding function end-to-end and confirm the finding still holds at HEAD (if it does not, STOP and report).
2. Implement the fix described under Goal in `internal/metafetch/service_apply.go` (and any sibling that shares the same shape — grep for the pattern before assuming there is one copy).
3. Write the regression test first (it must fail against the pre-fix code), then make it pass.
4. Run the gate; add the changelog fragment; bump headers; report with exact counts.

Then, always:
- Keep the change purely on-target — do not touch adjacent code, do not "clean up while you're in there".
- Bump the file header (`version` + `last-edited: 2026-09-10`) on every file you touch; keep existing guids. New files get a fresh guid (`uuidgen | tr A-Z a-z`).
- Add a changelog fragment `changelog.d/20260910_metadata_317.md` (NO file header; format per `changelog.d/README.md`: a `### Fixed|Changed|Added` heading, a `####` title, one paragraph).
- Do NOT edit `TODO.md` — the coordinator closes the source item in one commit per wave. In your final report, state the exact `TODO.md` line text (or `todo.d` fragment) to check off.

## Tests

- A regression test that reproduces the defect described in Background and fails on the pre-fix code.
- Existing package tests stay green (`-count=1`).

## How to test

```bash
go build ./... && go vet ./... && go test ./internal/metafetch/... -count=1
```
Do NOT use `make ci` as the gate: it is red on `main` from pre-existing staticcheck findings unrelated to this task. Run `staticcheck ./<changed-pkg>/...` and fix only findings in files you touched.

## Acceptance criteria

- [ ] Every re-verify anchor above still hits (or the report says which moved and where).
- [ ] The tests listed above exist and pass; a regression test reproduces the original defect and fails on the pre-fix code.
- [ ] Gate green: the command in **How to test** exits 0; `go vet`/lint clean on touched packages.
- [ ] File headers bumped on every changed file (`grep -n "last-edited: 2026-09-10" <file>` hits for each).
- [ ] Changelog fragment present: `test -f changelog.d/20260910_metadata_317.md`.

## Commit message

```
fix(metadata): Auto-merge primary-selection (MATCH-4) silently scores a book as havin (SF-04)

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
