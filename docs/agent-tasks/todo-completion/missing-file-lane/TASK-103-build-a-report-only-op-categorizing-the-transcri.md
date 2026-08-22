<!-- file: docs/agent-tasks/todo-completion/missing-file-lane/TASK-103-build-a-report-only-op-categorizing-the-transcri.md -->
<!-- version: 1.0.0 -->
<!-- guid: 26bea1fb-35cd-4a3b-9687-e9f970f40e75 -->
<!-- last-edited: 2026-08-21 -->

# TASK-103 — Build a report-only op categorizing the transcribe_status vs IntroTranscription drift (79.3% whisper_error-with-transcript sample) (TODO.md L8433)

**Priority:** P2 · **Effort:** M · **Recommended subagent:** Sonnet-class · missing-file-lane subagent · **Why:** a bounded-concurrency read-only scan + TSV report, following an established in-repo pattern (missing_file_repoint.go) closely enough that it's mostly adaptation, not novel design · **Depends on:** none · **Wave:** 1

Source: `TODO.md` line 8433 as of commit 46628240 (later edits shift lines) — re-find it with `grep -n -F "**Investigate: 79% of books with a stored transcri" TODO.md` (line numbers drift; the grep is built from the line's own text). Scope file: `scope-12.json`.

## ⛔ START HERE (do this first, exactly)

```bash
# ⛔ START HERE — do not touch code before this block succeeds
REPO=/Users/jdfalk/repos/github.com/jdfalk/audiobook-organizer   # adjust to your clone
git -C "$REPO" fetch origin
git -C "$REPO" worktree add "$REPO/.worktrees/missing-file-lane-103-build-a-report-only-op-categorizing-the-transcri" -b agent/missing-file-lane-103-build-a-report-only-op-categorizing-the-transcri origin/main
cd "$REPO/.worktrees/missing-file-lane-103-build-a-report-only-op-categorizing-the-transcri"
git rebase origin/main
# Go task: do NOT run 'go work init .' — it breaks the build (ambiguous genproto imports)
```

(Protocol also in `docs/agent-tasks/ORCHESTRATION.md` — the inline block above is authoritative for this task.)

## Goal

Add a report-only maintenance op that scans every book with a non-empty IntroTranscription, buckets it by TranscribeStatus, and within the whisper_error bucket compares TranscribeAttemptedAt vs IntroTranscribedAt to distinguish 'stale record of a historical outage' (attempted much later than transcribed) from 'currently failing pipeline' — computing the REAL full-library breakdown, since the 79.3% figure is only a 987-book sample. Makes NO writes to any book row.

## Background (verify before editing)

- The 79.3% figure is from a random-offset sample of 987 books with non-empty intro_transcription, not a full-library count.
- The item's own hypothesis (a re-attempted transcription after the original text was already saved keeps the old text but acquires a failure status) needs the actual field-write logic in intro_transcribe.go re-confirmed before citing any function name in code comments — the TODO item's cited 'applyOutcome' function name could not be grep-confirmed at HEAD by this scout; re-locate it before writing.
- This must use registry.RunItems with a bounded worker pool per CLAUDE.md's concurrency rule for whole-library loops.

- **Re-verify these anchors before editing** — line numbers drift; a zero-hit grep means STOP and report:
  ```bash
  grep -n 'IntroTranscription' internal/plugins/maintenance/intro_transcribe.go | head -5   # ≥3 hits (e.g. silenceSentinel, OnlyMissing comments referencing the field) — IntroTranscription is a real, actively-written field in the transcribe pipeline
  grep -rln 'transcribe.status.drift\|TranscribeStatusDrift' internal/plugins/maintenance   # 0 hits before this task — no existing maintenance op name suggests a status-vs-content drift audit
  ```

### Reuse — don't invent

- Use `registry.RunItems bounded worker pool (mandatory per CLAUDE.md for whole-library loops)` in `internal/operations/registry` (verify: `grep -rn 'func RunItems' internal/operations/registry/*.go`) — do NOT write a parallel helper.
- Use `writeRepointReport-style TSV writer pattern (report-before-summary ordering)` in `internal/plugins/maintenance/missing_file_repoint.go` (verify: `grep -n 'func writeRepointReport' internal/plugins/maintenance/missing_file_repoint.go`) — do NOT write a parallel helper.

## Step-by-step

1. Re-locate the outcome-application function in internal/plugins/maintenance/intro_transcribe.go (`grep -n '^func ' internal/plugins/maintenance/intro_transcribe.go`) and confirm which field-write is conditional on outcome type before citing any name in a comment. The TODO item's cited 'applyOutcome' does not exist at HEAD.
2. Add internal/plugins/maintenance/transcribe_status_drift_report.go modeled on missing_file_repoint.go: sdk.OperationDef with `Liveness: sdk.LivenessRunItems` and `Capabilities: []sdk.Capability{sdk.CapLibraryRead}` ONLY — this op never writes. (Both symbols verified in use at internal/plugins/acoustid/lsh_backfill.go:59,69.)
3. Load books with the store's GetAllBooksCore accessor — name it explicitly, because `grep -oE 'GetAllBooks[A-Za-z]*' internal/database/*.go` shows FOUR distinct bulk-book accessors (GetAllBooks, GetAllBooksCore, GetAllBooksFullFrom, plus their Func variants). BookCore carries IntroTranscription (internal/database/bookcore.go:140, copied at :257), so the core projection is sufficient; do not reach for GetAllBooksFullFrom. Filter to non-empty IntroTranscription.
4. Bucket by TranscribeStatus using the EXACT closed set documented at internal/database/store.go:346-352: "ok", "source_file_missing", "no_audio", "ffmpeg_error", "whisper_error", "empty", plus a nil/unset bucket and an "other" bucket for any future value. Do NOT invent an "unparsed" status — it does not exist.
5. Within the whisper_error bucket, sub-bucket on whether TranscribeAttemptedAt is more than 1h later than IntroTranscribedAt -> 'stale-drift' vs 'recent-failure'; a book with EITHER timestamp nil lands in 'unknown-timing' and is never compared against a zero time.Time.
6. Write a TSV report (bucket, book_id, transcribe_status, transcribed_at, attempted_at) before the summary, mirroring `writeRepointReport` (verified at internal/plugins/maintenance/missing_file_repoint.go:497), plus a JSON summary log line with bucket counts.
7. Register the op in internal/plugins/maintenance/plugin.go's op list next to `p.missingFileRepointDef()` (L64) and `p.probeDirectoryBooksDef()` (L101).

Then, always:
- Keep the change purely additive — do not touch adjacent code, do not reorder imports beyond the formatter, do not change signatures unless a step above says so explicitly.
- Bump the file header (`version` + `last-edited: 2026-08-21`) on every file you touch; keep existing guids. New files get a fresh guid (`uuidgen | tr A-Z a-z`).
- Add a changelog fragment `changelog.d/20260821_missing-file-lane_103.md` (NO file header; format per `changelog.d/README.md`: a `### Fixed|Changed|Added` heading, a `####` title, one paragraph).
- Do NOT edit `TODO.md` — the coordinator closes the source item in one commit per wave (every brief in a wave would otherwise collide on it). In your final report, state the exact `TODO.md` line text to check off. Never add new TODO items directly — use a `todo.d/` fragment (no header).

### Edge-case semantics (conservative defaults — treat unknown as unknown, never as disqualifying)

- A book with TranscribeAttemptedAt or IntroTranscribedAt unset (zero time.Time) must land in 'unknown-timing', never silently compared against a zero value as if it were real.

## Tests

- internal/plugins/maintenance/transcribe_status_drift_report_test.go: TestTranscribeStatusDrift_BucketsByStatus — fixture books with known TranscribeStatus/IntroTranscription/timestamps land in the expected buckets.
- TestTranscribeStatusDrift_EmptyTranscriptionExcluded — anti-over-suppression: a book with empty IntroTranscription must NOT appear in any bucket, proving the population filter is applied.

Anti-over-suppression test: `TestTranscribeStatusDrift_EmptyTranscriptionExcluded` — a known-good input still passes with the new guard active.

## How to test

```bash
go build ./... && go vet ./... && go test ./internal/plugins/maintenance/... -count=1
```
Do NOT use `make ci` as the gate: it is red on `main` from 10 pre-existing staticcheck findings unrelated to this task. Run `staticcheck ./<changed-pkg>/...` and fix only findings in files you touched. A failing test in a package you did not change is not yours — report it, do not fix it.

## Acceptance criteria

- [ ] go test ./internal/plugins/maintenance/... -run TranscribeStatusDrift passes.
- [ ] Running the op against a fixture DB with N qualifying books produces a TSV with exactly N data rows plus header.
- [ ] Anti-over-suppression test: `TestTranscribeStatusDrift_EmptyTranscriptionExcluded` — a known-good input still passes with the new guard active.
- [ ] Edge cases above hold (nil/empty/unknown never disqualify; a test asserts it where a filter/guard is added).
- [ ] Gate green: `go build ./... && go vet ./... && go test ./internal/plugins/maintenance/... -count=1` exits 0; `go vet`/lint clean.
- [ ] File headers bumped on every changed file (`grep -n "last-edited: 2026-08-21" <file>` hits for each).
- [ ] Changelog fragment present: `test -f changelog.d/20260821_missing-file-lane_103.md`.

## Commit message

```
feat(missing-file-lane): Build a report-only op categorizing the transcribe_status vs (TODO L8433)

<why the change was needed; what it protects; what it deliberately does NOT change>

Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>
```

## Done

STOP — report done with exact counts (`COMPLETED: n — ...` / `REMAINING: n — ...` / `BLOCKED: n — ...`); the coordinator owns push/PR/merge. Do NOT run `git push`, `gh pr`, or any merge.

## Idempotency / Rollback

If this presence check already passes at HEAD — `the artifact this task adds is present: re-run grep -n 'IntroTranscription' internal/plugins/maintenance/intro_transcribe.go | head -5` — this task is already applied — run the acceptance checks instead of re-applying. Rollback = `git revert` the single commit; pre-existing behaviour is untouched (purely additive change).

## Coordinator notes

Matches decision 13's 'categorizing REPORT op only' pattern for a sibling item (book_file rows with no bytes) — same shape, applied here to transcribe-status drift. Diagnostic-only; whether the tiered backfill's 'needs work' query needs fixing based on the findings is a follow-up, not part of this task.
