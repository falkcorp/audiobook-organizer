<!-- file: docs/agent-tasks/todo-completion/missing-file-lane/TASK-117-add-the-review-rating-half-of-app-to-server-read.md -->
<!-- version: 1.0.0 -->
<!-- guid: 184029dc-6d0e-458e-b336-80ff9d68002d -->
<!-- last-edited: 2026-08-21 -->

# TASK-117 — Add the review/rating half of app-to-server reading-state sync (reading status half already exists) (TODO.md L8675)

**Priority:** P2 · **Effort:** M · **Recommended subagent:** Sonnet-class · missing-file-lane subagent · **Why:** extends an existing, well-understood merge-semantics endpoint with one more field; needs real-client verification before finalizing the shape · **Depends on:** none · **Wave:** 1

Source: `TODO.md` line 8675 as of commit 46628240 (later edits shift lines) — re-find it with `grep -n -F "**Reading status and review/rating must sync from " TODO.md` (line numbers drift; the grep is built from the line's own text). Scope file: `scope-12.json`.

## ⛔ START HERE (do this first, exactly)

```bash
# ⛔ START HERE — do not touch code before this block succeeds
REPO=/Users/jdfalk/repos/github.com/jdfalk/audiobook-organizer   # adjust to your clone
git -C "$REPO" fetch origin
git -C "$REPO" worktree add "$REPO/.worktrees/missing-file-lane-117-add-the-review-rating-half-of-app-to-server-read" -b agent/missing-file-lane-117-add-the-review-rating-half-of-app-to-server-read origin/main
cd "$REPO/.worktrees/missing-file-lane-117-add-the-review-rating-half-of-app-to-server-read"
git rebase origin/main
# Go task: do NOT run 'go work init .' — it breaks the build (ambiguous genproto imports)
```

(Protocol also in `docs/agent-tasks/ORCHESTRATION.md` — the inline block above is authoritative for this task.)

## Goal

Since ABS core has no first-class review object (per the item), inspect what AudioBooth and Absorb actually send for a rating before designing the field; add a Rating field to the MediaProgress/UserBookState model and its merge logic in progress.go mirroring the existing IsFinished pattern; verify round-trip — set on the app, persists, and comes BACK on the next sync, not just accepted once.

## Background (verify before editing)

- IsFinished is already fully modeled with merge semantics at progress.go L292-343 — reading status is done, per the TODO item's own 2026-08-14 update.
- Review status has ZERO existing field anywhere in this handler — genuinely unbuilt.
- The item explicitly warns: round-trip matters more than write-once — a rating that persists but never comes back reads as data loss to the user.

- **Re-verify these anchors before editing** — line numbers drift; a zero-hit grep means STOP and report:
  ```bash
  grep -n 'IsFinished' internal/server/handlers/abs/progress.go   # ≥6 hits including L58, L292, L305, L316, L326-327, L343 — IsFinished is already fully modeled with merge semantics
  grep -inw 'rating\|review' internal/server/handlers/abs/progress.go   # 0 hits — no rating/review field exists anywhere in the progress handler (whole-word match — a bare case-insensitive substring match on 'rating' false-positives on 'enumerating' elsewhere in the file)
  ```

### Reuse — don't invent

- Use `the IsFinished merge pattern to mirror for a new Rating field` in `internal/server/handlers/abs/progress.go` (verify: `grep -n 'req.IsFinished != nil' internal/server/handlers/abs/progress.go`) — do NOT write a parallel helper.

## Step-by-step

1. Check what real clients (AudioBooth, Absorb) actually send for a rating — search internal/syncapi/conformance's fixtures and any captured request logs before deciding the field shape.
2. Add a Rating field (type TBD by step 1 — likely *float64 or *int) to the MediaProgress/UserBookState struct.
3. Extend progress.go's request/merge logic (mirror the `if req.IsFinished != nil { incoming.IsFinished = *req.IsFinished }` pattern at ~L326-327) for the new Rating field.
4. Ensure the GET path that returns stored.IsFinished (L316) also returns the stored Rating, so a re-sync reads it back.
5. Extend an internal/syncapi/conformance fixture so field-presence is checked automatically, per this codebase's stated preference for the conformance harness over eyeballing JSON.

Then, always:
- Keep the change purely additive — do not touch adjacent code, do not reorder imports beyond the formatter, do not change signatures unless a step above says so explicitly.
- Bump the file header (`version` + `last-edited: 2026-08-21`) on every file you touch; keep existing guids. New files get a fresh guid (`uuidgen | tr A-Z a-z`).
- Add a changelog fragment `changelog.d/20260821_missing-file-lane_117.md` (NO file header; format per `changelog.d/README.md`: a `### Fixed|Changed|Added` heading, a `####` title, one paragraph).
- Do NOT edit `TODO.md` — the coordinator closes the source item in one commit per wave (every brief in a wave would otherwise collide on it). In your final report, state the exact `TODO.md` line text to check off. Never add new TODO items directly — use a `todo.d/` fragment (no header).

### Edge-case semantics (conservative defaults — treat unknown as unknown, never as disqualifying)

- A rating outside whatever range the client expects — decide reject vs clamp vs pass-through, and document the choice.
- If a client sends written-review text alongside a numeric rating, decide whether to store it or ignore it, and say so explicitly rather than silently dropping one half.

## Tests

- internal/server/handlers/abs/progress_test.go: TestMediaProgress_RatingRoundTrips — set a rating via the write endpoint, then GET the same progress record, assert the rating comes back unchanged.
- TestMediaProgress_RatingAbsentDoesNotClearIsFinished — anti-over-suppression: a request that sends IsFinished but no rating must not null out a previously-set rating.

Anti-over-suppression test: `TestMediaProgress_RatingAbsentDoesNotClearIsFinished` — a known-good input still passes with the new guard active.

## How to test

```bash
make ci
```
If `make ci` is too slow for iteration, first run `go build ./... && go vet ./<changed-pkg>/... && go test ./<changed-pkg>/... -count=1` (or `npm --prefix web test -- <file>` for web), then the full gate once before reporting done.

## Acceptance criteria

- [ ] go test ./internal/server/handlers/abs/... -run MediaProgress_Rating passes both cases.
- [ ] Anti-over-suppression test: `TestMediaProgress_RatingAbsentDoesNotClearIsFinished` — a known-good input still passes with the new guard active.
- [ ] Edge cases above hold (nil/empty/unknown never disqualify; a test asserts it where a filter/guard is added).
- [ ] Gate green: `make ci` exits 0; `go vet`/lint clean.
- [ ] File headers bumped on every changed file (`grep -n "last-edited: 2026-08-21" <file>` hits for each).
- [ ] Changelog fragment present: `test -f changelog.d/20260821_missing-file-lane_117.md`.

## Commit message

```
feat(missing-file-lane): Add the review/rating half of app-to-server reading-state sy (TODO L8675)

<why the change was needed; what it protects; what it deliberately does NOT change>

Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>
```

## Done

STOP — report done with exact counts (`COMPLETED: n — ...` / `REMAINING: n — ...` / `BLOCKED: n — ...`); the coordinator owns push/PR/merge. Do NOT run `git push`, `gh pr`, or any merge.

## Idempotency / Rollback

If the first acceptance check below already passes at HEAD (`go test ./internal/server/handlers/abs/... -run MediaProgress_Rating passes both cases.`), this task is already applied — run the acceptance checks instead of re-applying. Rollback = `git revert` the single commit; pre-existing behaviour is untouched (purely additive change).

## Coordinator notes

Verify against real clients, not just a spec guess — the item explicitly warns AudioBooth and Absorb differ in which endpoints they call.
