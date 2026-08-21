<!-- file: docs/agent-tasks/todo-completion/maintenance/TASK-078-extend-purge-empty-authors-report-to-categorize-.md -->
<!-- version: 1.0.0 -->
<!-- guid: 7ba2ef37-5589-4369-b6e6-0de41a59a90b -->
<!-- last-edited: 2026-08-21 -->

# TASK-078 — Extend purge-empty-authors' report to categorize the 822 zero-book-but-has-files authors (TODO.md L5275)

**Priority:** P2 · **Effort:** S · **Recommended subagent:** Sonnet-class · maintenance subagent · **Why:** Small, additive report extension reusing an existing op's structures, but needs a sensible per-author detail shape (file count, a sample file path or two, book_authors linkage check) for a human to actually make the decision from. · **Depends on:** none · **Wave:** 1

Source: `TODO.md` line 5275 as of commit 46628240 (later edits shift lines) — re-find it with `grep -n -F "**Decide what the 822 zero-book-but-has-files auth" TODO.md` (line numbers drift; the grep is built from the line's own text). Scope file: `scope-10.json`.

## ⛔ START HERE (do this first, exactly)

```bash
# ⛔ START HERE — do not touch code before this block succeeds
REPO=/Users/jdfalk/repos/github.com/jdfalk/audiobook-organizer   # adjust to your clone
git -C "$REPO" fetch origin
git -C "$REPO" worktree add "$REPO/.worktrees/maintenance-078-extend-purge-empty-authors-report-to-categorize-" -b agent/maintenance-078-extend-purge-empty-authors-report-to-categorize- origin/main
cd "$REPO/.worktrees/maintenance-078-extend-purge-empty-authors-report-to-categorize-"
git rebase origin/main
# Go task: do NOT run 'go work init .' — it breaks the build (ambiguous genproto imports)
```

(Protocol also in `docs/agent-tasks/ORCHESTRATION.md` — the inline block above is authoritative for this task.)

## Goal

Add a HeldBackSample field to emptyAuthorReport (internal/plugins/maintenance/author_purge_empty.go) that, capped at emptyAuthorSampleLimit entries, records enough detail per zero-book-but-has-files author for a human to judge whether it is a broken-junction book or genuine junk: author name, file count, and — if feasible from existing store methods — whether any book_authors:<bookID> array elsewhere in the library still references this author's ID (which would indicate a live but undercounted link rather than orphaned files). This is a report-only addition; do not change the delete/apply logic or the require_zero_files default.

## Background (verify before editing)

- docs context (TODO item L5275) measured 822 of 4,975 zero-book authors have a non-zero file count — 'A zero book count with files present looks more like a book that lost its junction entry than an empty author'.
- author_purge_empty.go already computes ZeroBooksWithFiles as a count via the same file-count pass that drives requireZeroFiles(), but the current Sample field (bounded by emptyAuthorSampleLimit=50, L33) only samples names, and per its own comment samples the ELIGIBLE-for-delete population, not this held-back one — so today a reviewer sees '822' with no way to eyeball who they are.
- This is the same pattern already applied elsewhere per owner decision: build a categorizing REPORT op rather than deciding the mutation policy blind (see decision #13 in SCOUT-INSTRUCTIONS.md, applied to a structurally similar 'held-back, undecided population' problem for book_file rows).

- **Re-verify these anchors before editing** — line numbers drift; a zero-hit grep means STOP and report:
  ```bash
  grep -n 'ZeroBooksWithFiles' internal/plugins/maintenance/author_purge_empty.go   # >=2 hits (struct field + summary usage) — the report already counts (but does not sample/categorize) the 822-shaped population
  grep -n 'RequireZeroFiles' internal/plugins/maintenance/author_purge_empty.go   # >=2 hits — the require_zero_files safety flag exists and is the reason this population is held back by default
  ```

### Reuse — don't invent

- Use `emptyAuthorReport / emptyAuthorSampleLimit (extend, do not replace)` in `internal/plugins/maintenance/author_purge_empty.go` (verify: `grep -n 'emptyAuthorSampleLimit' internal/plugins/maintenance/author_purge_empty.go`) — do NOT write a parallel helper.

## Step-by-step

1. In internal/plugins/maintenance/author_purge_empty.go, add a new field to emptyAuthorReport (near Sample, ~L76): `HeldBackSample []heldBackAuthor` where `type heldBackAuthor struct { Name string; FileCount int }` (add both near the report struct).
2. In the loop that currently computes ZeroBooksWithFiles (find it via the RequireZeroFiles/file-counts pass referenced at L148 'author file counts (needed for the require_zero_files guard)'), when an author is held back (zero books, non-zero files) and len(report.HeldBackSample) < emptyAuthorSampleLimit, append {Name: author.Name, FileCount: <that author's file count>} to HeldBackSample.
3. Update summary() (or add a second log line) to mention that HeldBackSample is populated whenever ZeroBooksWithFiles > 0, so a dry run surfaces actionable names instead of only a count.
4. No changes to Apply/RequireZeroFiles/apply logic — this is report-only, matching the existing op's report-only-by-default posture.

Then, always:
- Keep the change purely additive — do not touch adjacent code, do not reorder imports beyond the formatter, do not change signatures unless a step above says so explicitly.
- Bump the file header (`version` + `last-edited: 2026-08-21`) on every file you touch; keep existing guids. New files get a fresh guid (`uuidgen | tr A-Z a-z`).
- Add a changelog fragment `changelog.d/20260821_maintenance_078.md` (NO file header; format per `changelog.d/README.md`: a `### Fixed|Changed|Added` heading, a `####` title, one paragraph).
- Do NOT edit `TODO.md` — the coordinator closes the source item in one commit per wave (every brief in a wave would otherwise collide on it). In your final report, state the exact `TODO.md` line text to check off. Never add new TODO items directly — use a `todo.d/` fragment (no header).

### Edge-case semantics (conservative defaults — treat unknown as unknown, never as disqualifying)

- Zero held-back authors (ZeroBooksWithFiles == 0) — HeldBackSample stays nil/empty, no special-casing needed.
- An author appearing in HeldBackSample whose file count came from a stale/partial file-count pass (the existing pass already has this limitation for the ZeroBooksWithFiles count itself) — do not attempt to fix the underlying count's accuracy in this task, only surface what the existing pass already computes.

## Tests

- internal/plugins/maintenance/author_purge_empty_test.go — TestPurgeEmptyAuthors_HeldBackSample_Populated: construct a mock store with one zero-book author that has files and one zero-book author that has zero files; run the op dry-run; assert HeldBackSample contains exactly the first author's name and FileCount, and does NOT contain the second (unambiguous-junk) author.
- internal/plugins/maintenance/author_purge_empty_test.go — TestPurgeEmptyAuthors_HeldBackSample_CapAtLimit: construct more than emptyAuthorSampleLimit held-back authors, assert len(HeldBackSample) == emptyAuthorSampleLimit (the cap is respected, matching the existing Sample field's behavior) — this is the anti-over-suppression check: without a cap test, a future refactor could silently drop the cap and turn a report meant for human eyeballing into another 822-line dump.

Anti-over-suppression test: `TestPurgeEmptyAuthors_HeldBackSample_CapAtLimit` — a known-good input still passes with the new guard active.

## How to test

```bash
make ci
```
If `make ci` is too slow for iteration, first run `go build ./... && go vet ./<changed-pkg>/... && go test ./<changed-pkg>/... -count=1` (or `npm --prefix web test -- <file>` for web), then the full gate once before reporting done.

## Acceptance criteria

- [ ] go test ./internal/plugins/maintenance/... -run TestPurgeEmptyAuthors_HeldBack passes
- [ ] grep -n 'HeldBackSample' internal/plugins/maintenance/author_purge_empty.go returns hits in both the struct definition and the population loop
- [ ] Anti-over-suppression test: `TestPurgeEmptyAuthors_HeldBackSample_CapAtLimit` — a known-good input still passes with the new guard active.
- [ ] Edge cases above hold (nil/empty/unknown never disqualify; a test asserts it where a filter/guard is added).
- [ ] Gate green: `make ci` exits 0; `go vet`/lint clean.
- [ ] File headers bumped on every changed file (`grep -n "last-edited: 2026-08-21" <file>` hits for each).
- [ ] Changelog fragment present: `test -f changelog.d/20260821_maintenance_078.md`.

## Commit message

```
feat(maintenance): Extend purge-empty-authors' report to categorize the 822 zer (TODO L5275)

<why the change was needed; what it protects; what it deliberately does NOT change>

Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>
```

## Done

STOP — report done with exact counts (`COMPLETED: n — ...` / `REMAINING: n — ...` / `BLOCKED: n — ...`); the coordinator owns push/PR/merge. Do NOT run `git push`, `gh pr`, or any merge.

## Idempotency / Rollback

If the first acceptance check below already passes at HEAD (`go test ./internal/plugins/maintenance/... -run TestPurgeEmptyAuthors_HeldBack passes`), this task is already applied — run the acceptance checks instead of re-applying. Rollback = `git revert` the single commit; pre-existing behaviour is untouched (purely additive change).

## Coordinator notes

This does NOT decide what the 822 authors are — it only builds the tool a human needs to look at a sample and decide, matching the item's own framing ('Someone has to look at a sample and decide before that flag is ever flipped'). The flip itself stays a human call.
