<!-- file: docs/agent-tasks/todo-completion/missing-file-lane/TASK-114-build-chapters-backfill-from-a-near-exact-acoust.md -->
<!-- version: 1.0.0 -->
<!-- guid: 8ebe18c2-3aa9-4faf-b307-cf2079231afb -->
<!-- last-edited: 2026-08-21 -->

# TASK-114 — Build chapters backfill from a near-exact-acoustic-match duplicate (or provider data, or a cue/playlist) (TODO.md L8611)

**Priority:** P1 · **Effort:** L · **Recommended subagent:** Opus-class · missing-file-lane subagent · **Why:** cross-book matching (fingerprint gate) plus chapter-offset derivation from 3 different source types plus an M4B tag write on the prod library tree — the write-back-wipe risk category CLAUDE.md calls out · **Depends on:** TASK-115, TASK-116 · **Wave:** 3 · **REVIEW-CRITICAL (prod-data path): PR stays open for the owner; never weak-tier**

Source: `TODO.md` line 8611 as of commit 46628240 (later edits shift lines) — re-find it with `grep -n -F "**Backfill chapters into files that lack them, usi" TODO.md` (line numbers drift; the grep is built from the line's own text). Scope file: `scope-12.json`.

## ⛔ START HERE (do this first, exactly)

```bash
# ⛔ START HERE — do not touch code before this block succeeds
REPO=/Users/jdfalk/repos/github.com/jdfalk/audiobook-organizer   # adjust to your clone
git -C "$REPO" fetch origin
git -C "$REPO" worktree add "$REPO/.worktrees/missing-file-lane-114-build-chapters-backfill-from-a-near-exact-acoust" -b agent/missing-file-lane-114-build-chapters-backfill-from-a-near-exact-acoust origin/main
cd "$REPO/.worktrees/missing-file-lane-114-build-chapters-backfill-from-a-near-exact-acoust"
git rebase origin/main
# Go task: do NOT run 'go work init .' — it breaks the build (ambiguous genproto imports)
```

(Protocol also in `docs/agent-tasks/ORCHESTRATION.md` — the inline block above is authoritative for this task.)

## Goal

Build a backfill op that gives a chapterless file real chapters by, in preference order: (1) checking whether an existing metadata-provider integration already returns chapter titles+offsets; (2) deriving offsets from a near-exact-acoustic-match duplicate stored as N per-chapter files (cumulative duration sum = offsets, filenames often give titles); (3) reading a playlist/cue-sheet with explicit timings (depends on L8646's playlist import). GATE the duplicate/cue paths on an AcoustID match score well above the ordinary dedup threshold, reject on ANY duration mismatch beyond a small tolerance, and reconcile summed chapter durations against the target's total runtime before writing.

## Background (verify before editing)

- chapters_backfill.go only re-probes a SINGLE container's own embedded ffprobe markers — zero cross-book duplicate-matching logic, confirmed by the 0-hit grep above.
- The write target (M4B chapter atoms) needs its own write path, separate from chapters_backfill.go's Pebble-keyspace-only SaveChaptersForBook — check internal/audioutil or internal/mediainfo for existing chapter-atom-writing code before building new.
- books/itunes/** stays hands-off regardless, per this repo's dominant-incident-class rule for write-back wipes — this op must exclude that tree structurally.

- **Re-verify these anchors before editing** — line numbers drift; a zero-hit grep means STOP and report:
  ```bash
  grep -n 'duplicate\|borrow' internal/plugins/maintenance/chapters_backfill.go   # 0 hits — chapters_backfill.go has no duplicate-borrowing logic
  grep -n 'AcoustIDFingerprint' internal/database/store.go   # ≥1 hit — AcoustID fingerprint fields exist to gate the required near-exact match
  ```

### Reuse — don't invent

- Use `chapters_backfill.go's SaveChaptersForBook / DeleteChaptersForBook write path and data-loss-safety pattern (one dedicated keyspace, no bare UpdateBook)` in `internal/plugins/maintenance/chapters_backfill.go` (verify: `grep -n 'SaveChaptersForBook\|DeleteChaptersForBook' internal/plugins/maintenance/chapters_backfill.go`) — do NOT write a parallel helper.

## Step-by-step

1. Check whether any existing metadata-provider client already exposes chapter title+offset data before building anything cross-book; if so, prefer that path entirely and treat steps 2-3 as fallback only.
2. Build the per-chapter-duplicate path: find candidate duplicates via existing dedup/AcoustID-similarity scoring (internal/plugins/acoustid), require a near-exact match score well above the normal dedup threshold, and reject any candidate whose per-file durations don't sum to within a small tolerance of the target's total runtime.
3. Derive chapter offsets as the cumulative sum of the duplicate's per-track durations; derive titles from filenames when present, falling back to 'Chapter N'.
4. Find or add the M4B chapter-atom write function (search internal/audioutil, internal/mediainfo for existing atom-writing code first) and write it ONLY for apply=true runs, excluding any path under books/itunes/**.
5. Register the op, apply=false default, per this repo's established pattern.
6. Depends on L8583's chapters-served verification being usable as the 'which books lack chapters' input query — confirm that population is queryable before wiring candidate-selection.

Then, always:
- Keep the change purely additive — do not touch adjacent code, do not reorder imports beyond the formatter, do not change signatures unless a step above says so explicitly.
- Bump the file header (`version` + `last-edited: 2026-08-21`) on every file you touch; keep existing guids. New files get a fresh guid (`uuidgen | tr A-Z a-z`).
- Add a changelog fragment `changelog.d/20260821_missing-file-lane_114.md` (NO file header; format per `changelog.d/README.md`: a `### Fixed|Changed|Added` heading, a `####` title, one paragraph).
- Do NOT edit `TODO.md` — the coordinator closes the source item in one commit per wave (every brief in a wave would otherwise collide on it). In your final report, state the exact `TODO.md` line text to check off. Never add new TODO items directly — use a `todo.d/` fragment (no header).

### Edge-case semantics (conservative defaults — treat unknown as unknown, never as disqualifying)

- An incomplete duplicate (e.g. 12 of 13 tracks present, like the Successors debris cited elsewhere in TODO.md) must be rejected by the runtime-reconciliation check, not silently truncated.
- A duplicate with tracks in non-sorted filename order must be sorted by parsed track number or duration-position, not directory-listing order.

## Tests

- TestChaptersFromDuplicate_RejectsOnDurationMismatch — a candidate duplicate whose summed track durations don't match the target's runtime is rejected, no write.
- TestChaptersFromDuplicate_RejectsBelowFingerprintThreshold — a candidate below the required AcoustID match score is rejected even if durations line up.
- TestChaptersFromDuplicate_ExcludesItunesTree — a candidate or target path under books/itunes/** is never written to, regardless of match quality.
- TestChaptersFromDuplicate_HappyPath_WritesCorrectOffsets — a clean matching duplicate produces the expected cumulative offsets and titles.

Anti-over-suppression test: `TestChaptersFromDuplicate_ExcludesItunesTree` — a known-good input still passes with the new guard active.

## How to test

```bash
make ci
```
If `make ci` is too slow for iteration, first run `go build ./... && go vet ./<changed-pkg>/... && go test ./<changed-pkg>/... -count=1` (or `npm --prefix web test -- <file>` for web), then the full gate once before reporting done.

## Acceptance criteria

- [ ] go test ./internal/plugins/maintenance/... -run ChaptersFromDuplicate passes all four cases.
- [ ] A dry run against a fixture with one good candidate produces a report showing the derived chapter list with zero file writes.
- [ ] Anti-over-suppression test: `TestChaptersFromDuplicate_ExcludesItunesTree` — a known-good input still passes with the new guard active.
- [ ] Edge cases above hold (nil/empty/unknown never disqualify; a test asserts it where a filter/guard is added).
- [ ] Gate green: `make ci` exits 0; `go vet`/lint clean.
- [ ] File headers bumped on every changed file (`grep -n "last-edited: 2026-08-21" <file>` hits for each).
- [ ] Changelog fragment present: `test -f changelog.d/20260821_missing-file-lane_114.md`.

## Commit message

```
feat(missing-file-lane): Build chapters backfill from a near-exact-acoustic-match dup (TODO L8611)

<why the change was needed; what it protects; what it deliberately does NOT change>

Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>
```

## Done

STOP — report done with exact counts (`COMPLETED: n — ...` / `REMAINING: n — ...` / `BLOCKED: n — ...`); the coordinator owns push/PR/merge. Do NOT run `git push`, `gh pr`, or any merge.

## Idempotency / Rollback

If the first acceptance check below already passes at HEAD (`go test ./internal/plugins/maintenance/... -run ChaptersFromDuplicate passes all four cases.`), this task is already applied — run the acceptance checks instead of re-applying. Rollback = `git revert` the single commit; pre-existing behaviour is untouched (purely additive change).

## Coordinator notes

Owner was explicit that borrowing offsets from a different edition is WORSE than no chapters — the near-exact-match gate is the single most important correctness property of this op; do not loosen it under time pressure.
