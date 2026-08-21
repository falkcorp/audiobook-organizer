<!-- file: docs/agent-tasks/todo-completion/web/TASK-169-play-the-first-2-minutes-of-part-1-s-audio-direc.md -->
<!-- version: 1.0.0 -->
<!-- guid: a1cbfefc-f209-4c65-9fb6-5097369191da -->
<!-- last-edited: 2026-08-21 -->

# TASK-169 — Play the first ~2 minutes of part 1's audio directly from the review metadata panel, reusing the existing bounded audio-sample endpoint (REVIEW-PREVIEW)

**Priority:** P2 · **Effort:** M · **Recommended subagent:** Sonnet-class · web subagent · **Why:** Mostly UI wiring against an already-bounded, already-proven endpoint, but requires a real backend fix (resolving part 1 via GetBookFiles instead of the currently-wrong book.FilePath directory path) plus the frontend control -- moderate, not trivial. · **Depends on:** none · **Wave:** 1

Source: `TODO.md` line 2238 as of commit 46628240 (later edits shift lines) — re-find it with `grep -n -F "**REVIEW-PREVIEW**" TODO.md` (line numbers drift; the grep is built from the line's own text). Scope file: `scope-17-rescope.json`.

## ⛔ START HERE (do this first, exactly)

```bash
# ⛔ START HERE — do not touch code before this block succeeds
REPO=/Users/jdfalk/repos/github.com/jdfalk/audiobook-organizer   # adjust to your clone
git -C "$REPO" fetch origin
git -C "$REPO" worktree add "$REPO/.worktrees/web-169-play-the-first-2-minutes-of-part-1-s-audio-direc" -b agent/web-169-play-the-first-2-minutes-of-part-1-s-audio-direc origin/main
cd "$REPO/.worktrees/web-169-play-the-first-2-minutes-of-part-1-s-audio-direc"
git rebase origin/main
npm ci --prefix web   # frontend task: Playwright/vitest must come from the worktree
```

(Protocol also in `docs/agent-tasks/ORCHESTRATION.md` — the inline block above is authoritative for this task.)

## Goal

Add a 'preview' control to web/src/components/review/MetadataPanel.tsx that streams the first ~2 minutes of audio from part 1 of the book via the existing /api/v1/audiobooks/:id/sample endpoint, reusing AudioSampleCompare.tsx's URL-building pattern. Fix internal/server/audio_sample.go's handleAudioSample to resolve part 1's actual file path via GetBookFiles(bookID) (sorted by TrackNumber, first entry) instead of passing book.FilePath directly -- book.FilePath is a directory, not a file, per the scanner (verified above), so the endpoint likely mis-serves or fails outright for any multi-file book today.

## Background (verify before editing)

- AudioSampleCompare.tsx already proves the /sample endpoint pattern end-to-end for a DIFFERENT feature (comparing two candidate books) -- this item reuses the same endpoint for a different caller (the review metadata panel) and a different, narrower duration (~2 minutes vs AudioSampleCompare's 30-second CLIP_DURATION).
- The endpoint's existing server-side cap (audio.SampleMaxDuration=60s, enforced inside ExtractSample regardless of client-requested duration) already satisfies the item's 'never stream unbounded, cap independent of client request' requirement -- no new capping logic is needed, only correct file resolution for multi-part books.
- context.WithTimeout(c.Request.Context(), 120) at audio_sample.go:47 passes a bare int (120) as the timeout Duration, which Go interprets as 120 NANOSECONDS, not 120 seconds -- likely a pre-existing latent bug unrelated to this item's scope; flagged in notes for the coordinator's awareness, not to be fixed under this task per the 'stay on target' scope discipline.

- **Re-verify these anchors before editing** — line numbers drift; a zero-hit grep means STOP and report:
  ```bash
  grep -n 'SampleMaxDuration\|func ExtractSample' internal/audio/sample.go   # 2 hits: SampleMaxDuration = 60 constant, and the ExtractSample function — a bounded audio-sample endpoint already exists, hard-capped server-side (not client-request-dependent)
  grep -n 'sample?start=' web/src/components/AudioSampleCompare.tsx   # 1 hit ~L46 — the endpoint is already consumed by an existing frontend component (precedent for the new preview control)
  grep -n 'FilePath: book.FilePath' internal/server/audio_sample.go   # 1 hit ~L42 — the endpoint passes book.FilePath directly to ffmpeg -- a per-book field, not per-file
  grep -n 'dbBook.FilePath = dirPath' internal/scanner/scanner.go   # 1 hit ~L1699 — book.FilePath is set to a DIRECTORY path by the scanner, not a specific audio file -- the multi-file resolution gap the item worried about
  grep -n 'func (s \*PebbleStore) GetBookFiles' internal/database/pebble_store_bookfiles.go   # 1 hit ~L397 — GetBookFiles exists to enumerate a book's individual files, the reuse target for resolving 'part 1'
  grep -n 'MetadataSearchDialog' web/src/components/review/MetadataPanel.tsx   # ≥1 hit — the review metadata panel (the 'chooser' referenced by the item) is a real, distinct component
  ```

### Reuse — don't invent

- Use `the existing bounded audio-sample endpoint and its buildSampleUrl-style client helper from AudioSampleCompare.tsx` in `internal/server/audio_sample.go` (verify: `grep -n 'func (s \*Server) handleAudioSample' internal/server/audio_sample.go`) — do NOT write a parallel helper.
- Use `GetBookFiles for resolving which file is part 1 of a multi-file book, ordered by TrackNumber` in `internal/database/pebble_store_bookfiles.go` (verify: `grep -n 'func (s \*PebbleStore) GetBookFiles' internal/database/pebble_store_bookfiles.go`) — do NOT write a parallel helper.

## Step-by-step

1. In internal/server/audio_sample.go's handleAudioSample, replace `FilePath: book.FilePath` with a resolution step: call `s.Ops().GetBookFiles(book.ID)` (or the equivalent store method available on Server), sort by TrackNumber (nil-safe), and use the first entry's FilePath as req.FilePath -- falling back to book.FilePath only if GetBookFiles returns empty (single-file/legacy books where FilePath might coincidentally be a file).
2. Add a query param or keep duration capped via the existing `duration` param defaulting differently for this caller if a distinct default is wanted (~120s per the item vs AudioSampleCompare's 30s default) -- confirm audio.SampleMaxDuration=60s is NOT a hard blocker for a 2-minute preview (currently it IS: 60s < 120s), so raise SampleMaxDuration or add a separate, still-server-enforced cap specific to this longer preview use case.
3. Add a preview button/control to web/src/components/review/MetadataPanel.tsx that, on click, plays via the endpoint from steps 1-2, targeting part 1's file specifically.
4. Add internal/server/audio_sample_test.go covering the new file-resolution behavior.

Then, always:
- Keep the change purely additive — do not touch adjacent code, do not reorder imports beyond the formatter, do not change signatures unless a step above says so explicitly.
- Bump the file header (`version` + `last-edited: 2026-08-21`) on every file you touch; keep existing guids. New files get a fresh guid (`uuidgen | tr A-Z a-z`).
- Add a changelog fragment `changelog.d/20260821_web_169.md` (NO file header; format per `changelog.d/README.md`: a `### Fixed|Changed|Added` heading, a `####` title, one paragraph).
- Do NOT edit `TODO.md` — the coordinator closes the source item in one commit per wave (every brief in a wave would otherwise collide on it). In your final report, state the exact `TODO.md` line text to check off. Never add new TODO items directly — use a `todo.d/` fragment (no header).

### Edge-case semantics (conservative defaults — treat unknown as unknown, never as disqualifying)

- A book with zero files or a corrupted/unreadable part-1 file should show a clear 'preview unavailable' state, not a silent failure or a crash.
- audio.SampleMaxDuration (currently 60s) is BELOW this item's requested ~2-minute (120s) preview length -- this needs an explicit decision (raise the constant globally, or add a second, still-server-enforced cap for this specific caller) before the frontend can request the full 2 minutes.

## Tests

- internal/server/audio_sample_test.go: a multi-file book's sample request resolves to part 1's file (lowest TrackNumber), not book.FilePath (the directory), when they differ -- needs a fixture book with multiple book_file rows.
- internal/server/audio_sample_test.go: request more than the cap via a large `duration` query param, assert the response is still capped at the server-enforced maximum, not the client-requested value.
- Frontend: preview control renders in MetadataPanel.tsx and triggers a request scoped to the book's ID.

Anti-over-suppression: N/A

## How to test

```bash
make ci && npm --prefix web run lint && npm --prefix web test
```
If `make ci` is too slow for iteration, first run `go build ./... && go vet ./<changed-pkg>/... && go test ./<changed-pkg>/... -count=1` (or `npm --prefix web test -- <file>` for web), then the full gate once before reporting done.

## Acceptance criteria

- [ ] Manual: on a multi-part book in the review panel, click preview and confirm audio plays from the START of part 1, not a directory-path failure or silent no-op.
- [ ] Anti-over-suppression: N/A
- [ ] Edge cases above hold (nil/empty/unknown never disqualify; a test asserts it where a filter/guard is added).
- [ ] Gate green: `make ci && npm --prefix web run lint && npm --prefix web test` exits 0; `go vet`/lint clean.
- [ ] File headers bumped on every changed file (`grep -n "last-edited: 2026-08-21" <file>` hits for each).
- [ ] Changelog fragment present: `test -f changelog.d/20260821_web_169.md`.

## Commit message

```
feat(web): Play the first ~2 minutes of part 1's audio directly from th (REVIEW-PREVIEW)

<why the change was needed; what it protects; what it deliberately does NOT change>

Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>
```

## Done

STOP — report done with exact counts (`COMPLETED: n — ...` / `REMAINING: n — ...` / `BLOCKED: n — ...`); the coordinator owns push/PR/merge. Do NOT run `git push`, `gh pr`, or any merge.

## Idempotency / Rollback

If the first acceptance check below already passes at HEAD (`Manual: on a multi-part book in the review panel, click preview and confirm audio plays from the START of part 1, not a directory-path failure or silent no-op.`), this task is already applied — run the acceptance checks instead of re-applying. Rollback = `git revert` the single commit; pre-existing behaviour is untouched (purely additive change).

## Coordinator notes

The unbounded-read-caused-OOM caution the original item raised is ALREADY satisfied by the existing endpoint's ffmpeg-based extraction (server-side cap independent of client Range headers) -- the real remaining gap this rescope found is narrower and more concrete than 'build a new endpoint': (a) book.FilePath is a directory, not a file, so multi-part resolution is currently broken/untested, and (b) the existing 60s cap is below this item's 120s target and needs an explicit bump or a second cap. Also flagging, out of scope for this item: audio_sample.go:47's `context.WithTimeout(ctx, 120)` passes a bare int interpreted as 120 NANOSECONDS rather than 120 seconds -- a likely pre-existing bug in the reused endpoint, worth its own ticket.
