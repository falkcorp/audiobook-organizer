<!-- file: docs/agent-tasks/todo-completion/server-handlers/TASK-153-detect-multi-file-books-whose-synthesized-chapte.md -->
<!-- version: 1.0.0 -->
<!-- guid: c0815048-da90-451d-9061-46e8af7325be -->
<!-- last-edited: 2026-08-21 -->

# TASK-153 — Detect multi-file books whose synthesized chapter timeline stops short of Book.Duration (per-file BookFile.Duration missing or wrong) (TODO.md L685)

**Priority:** P2 · **Effort:** S · **Recommended subagent:** Sonnet-class · server-handlers subagent · **Why:** small, localized fix in one file's request-time code path plus a log-based detector; low risk since it only adds observability, not a behavior change to what's served · **Depends on:** none · **Wave:** 1

Source: `TODO.md` line 685 as of commit 46628240 (later edits shift lines) — re-find it with `grep -n -F "**Multi-file chapter synthesis produces a timeline" TODO.md` (line numbers drift; the grep is built from the line's own text). Scope file: `scope-02.json`.

## ⛔ START HERE (do this first, exactly)

```bash
# ⛔ START HERE — do not touch code before this block succeeds
REPO=/Users/jdfalk/repos/github.com/jdfalk/audiobook-organizer   # adjust to your clone
git -C "$REPO" fetch origin
git -C "$REPO" worktree add "$REPO/.worktrees/server-handlers-153-detect-multi-file-books-whose-synthesized-chapte" -b agent/server-handlers-153-detect-multi-file-books-whose-synthesized-chapte origin/main
cd "$REPO/.worktrees/server-handlers-153-detect-multi-file-books-whose-synthesized-chapte"
git rebase origin/main
# Go task: do NOT run 'go work init .' — it breaks the build (ambiguous genproto imports)
```

(Protocol also in `docs/agent-tasks/ORCHESTRATION.md` — the inline block above is authoritative for this task.)

## Goal

Add a divergence check inside loadChapters (mapper.go:243-265): after synthesizing the chapter list for a multi-file book with no persisted chapters, compare the last chapter's EndSec against view.DurationSec (the summed BookFile.Duration, already computed at mapper.go:188 and passed in as book-level context); when they diverge by more than a small tolerance, emit a slog.Warn naming the book ID and both durations so the underlying per-file Duration data-quality bug becomes visible in logs instead of only in ABS's rendered (silently wrong) chapter list. This is a detection/observability fix; correcting the underlying BookFile.Duration values themselves is a data-repair problem (likely via duration-reextract, subject to L680's tolerance-floor caveat) that this item does not attempt to solve.

## Background (verify before editing)

- TODO.md L685-L186: one of two 'Genesis' rows (1,189 files) serves 1,189 chapters ending at 32,636s against a 258,256s duration; its twin ends correctly at 258,256s -- same content, different per-file duration data, confirming the bug is in the STORED BookFile.Duration values on one row's segments, not in SynthesizeChapters' arithmetic itself (which is correct given correct inputs -- verified by reading timeline.go:54-70).
- loadChapters (mapper.go:243-265) has no access today to Book.Duration or view.DurationSec at the point it builds tracks -- it only receives bookID and []fileView, so the check needs either a signature change or to be hoisted into the caller (buildItemView at mapper.go:181-206, which already has both view.DurationSec and view.Chapters in scope after the loadChapters call at line 201).

- **Re-verify these anchors before editing** — line numbers drift; a zero-hit grep means STOP and report:
  ```bash
  grep -n 'func SynthesizeChapters' internal/audioutil/timeline.go   # 1 hit L54; body shows `end := start + t.DurationSec` with no validation — SynthesizeChapters has no guard against a zero or missing per-track duration
  grep -n 'DurationSec: f.DurationSec' internal/server/handlers/abs/mapper.go   # 1 hit L261 — loadChapters builds TrackInfo.DurationSec directly from BookFile.Duration with no cross-check against Book.Duration
  grep -n 'DurationSec: float64(f.Duration)' internal/server/handlers/abs/mapper.go   # 1 hit L217 — fileView.DurationSec is a direct cast of BookFile.Duration, no validation
  ```

### Reuse — don't invent

- Use `SynthesizeChapters / TrackInfo (the function to add a guard around, or a caller-side check before it)` in `internal/audioutil/timeline.go` (verify: `grep -n 'type TrackInfo struct' internal/audioutil/timeline.go`) — do NOT write a parallel helper.
- Use `view.DurationSec running total already computed by the caller loop (mapper.go:187-188) as the ground truth to compare against` in `internal/server/handlers/abs/mapper.go` (verify: `grep -n 'view.DurationSec += fv.DurationSec' internal/server/handlers/abs/mapper.go`) — do NOT write a parallel helper.

## Step-by-step

1. In internal/server/handlers/abs/mapper.go, after `view.Chapters = h.loadChapters(book.ID, view.Files)` (mapper.go:201), add a check: if len(view.Chapters) > 0, compute `lastEnd := view.Chapters[len(view.Chapters)-1].EndSec` and compare against `view.DurationSec` (already the summed BookFile.Duration from the loop at mapper.go:182-191).
2. If `view.DurationSec - lastEnd` exceeds a small tolerance (e.g. 5 seconds, matching the pattern in duration_reextract.go's durationAbsToleranceS), emit `slog.Warn("abs mapper: synthesized chapter timeline stops short of book duration", "book_id", book.ID, "chapters_end_sec", lastEnd, "book_duration_sec", view.DurationSec)`.
3. Do NOT change what is served to the client in this change -- this is detection-only, matching the project's decision pattern of shipping a detector before a fix (see decision #11's precedent: 'build a DETECTION-ONLY counter now; the fix itself is deferred').
4. Optionally (if reviewer wants it in the same PR): also log a per-file breakdown of which BookFile rows have Duration <= 0 among view.Files, to make root-causing a flagged book faster.

Then, always:
- Keep the change purely additive — do not touch adjacent code, do not reorder imports beyond the formatter, do not change signatures unless a step above says so explicitly.
- Bump the file header (`version` + `last-edited: 2026-08-21`) on every file you touch; keep existing guids. New files get a fresh guid (`uuidgen | tr A-Z a-z`).
- Add a changelog fragment `changelog.d/20260821_server-handlers_153.md` (NO file header; format per `changelog.d/README.md`: a `### Fixed|Changed|Added` heading, a `####` title, one paragraph).
- Do NOT edit `TODO.md` — the coordinator closes the source item in one commit per wave (every brief in a wave would otherwise collide on it). In your final report, state the exact `TODO.md` line text to check off. Never add new TODO items directly — use a `todo.d/` fragment (no header).

### Edge-case semantics (conservative defaults — treat unknown as unknown, never as disqualifying)

- A book with persisted (not synthesized) chapters (the `if h.chapters != nil` early-return branch at mapper.go:244-251) is out of scope for this check -- persisted chapters are trusted as-is; only the live-synthesis fallback path needs the guard.
- A single-file book (len(files)==1): SynthesizeChapters still runs but there is only one chapter equal to the one file's duration, so lastEnd == view.DurationSec by construction -- the check should be a no-op here, not a false positive.
- view.DurationSec == 0 (book with no BookFile rows, duration pulled from book.Duration per mapper.go:194-196): loadChapters returns nil chapters for zero files, so the len(view.Chapters) > 0 guard already skips this case.

## Tests

- internal/server/handlers/abs/mapper_test.go: TestLoadChapters_DivergentDurationLogsWarning -- a multi-file book fixture with one BookFile.Duration == 0 among several non-zero durations produces a synthesized chapter list whose last EndSec is well short of the summed intended duration; assert the warning fires (capture via a test slog handler) with the correct book_id and both duration values.
- TestLoadChapters_ConsistentDurations_NoWarning -- a fixture where all BookFile.Duration values are populated and consistent produces no warning (anti-over-suppression: proves the check doesn't fire on healthy data).

Anti-over-suppression test: `TestLoadChapters_ConsistentDurations_NoWarning` — a known-good input still passes with the new guard active.

## How to test

```bash
make ci
```
If `make ci` is too slow for iteration, first run `go build ./... && go vet ./<changed-pkg>/... && go test ./<changed-pkg>/... -count=1` (or `npm --prefix web test -- <file>` for web), then the full gate once before reporting done.

## Acceptance criteria

- [ ] go test ./internal/server/handlers/abs/... -run TestLoadChapters passes.
- [ ] grep -n 'synthesized chapter timeline stops short' internal/server/handlers/abs/mapper.go returns 1 hit after the change.
- [ ] Anti-over-suppression test: `TestLoadChapters_ConsistentDurations_NoWarning` — a known-good input still passes with the new guard active.
- [ ] Edge cases above hold (nil/empty/unknown never disqualify; a test asserts it where a filter/guard is added).
- [ ] Gate green: `make ci` exits 0; `go vet`/lint clean.
- [ ] File headers bumped on every changed file (`grep -n "last-edited: 2026-08-21" <file>` hits for each).
- [ ] Changelog fragment present: `test -f changelog.d/20260821_server-handlers_153.md`.

## Commit message

```
feat(server-handlers): Detect multi-file books whose synthesized chapter timeline s (TODO L685)

<why the change was needed; what it protects; what it deliberately does NOT change>

Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>
```

## Done

STOP — report done with exact counts (`COMPLETED: n — ...` / `REMAINING: n — ...` / `BLOCKED: n — ...`); the coordinator owns push/PR/merge. Do NOT run `git push`, `gh pr`, or any merge.

## Idempotency / Rollback

If the first acceptance check below already passes at HEAD (`go test ./internal/server/handlers/abs/... -run TestLoadChapters passes.`), this task is already applied — run the acceptance checks instead of re-applying. Rollback = `git revert` the single commit; pre-existing behaviour is untouched (purely additive change).

## Coordinator notes

This is intentionally scoped to detection/logging only, not to correcting the underlying BookFile.Duration values -- that correction is duration-reextract's job, and L680 already flags that duration-reextract's tolerance floor may not catch every case this detector would surface. Once this logs in production, it becomes new evidence for L680's blast-radius question.
