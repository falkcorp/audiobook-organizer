<!-- file: docs/agent-tasks/abs-sync/TASK-08-progress-merge-policy.md -->
<!-- version: 1.0.0 -->
<!-- guid: ab744439-38c6-4f9c-9202-fbe0e3295da1 -->
<!-- last-edited: 2026-07-30 -->

# TASK-08 — Pure progress-merge policy package (ABS-SYNC, Phase 6 foundation)

**Priority:** P1 · **Effort:** L · **Recommended subagent:** Opus-class · pure-logic/algorithm subagent (no store, no HTTP — dense table-driven test design) · **Why:** this is the single highest-value correctness surface in the whole abs-sync project (§1.7.3/§1.8.1/§1.8.4/§1.8.7 each describe a silent-failure clobber class) and it has zero I/O, so it is fully unit-testable before any store/HTTP wiring exists · **Depends on:** none

**Gate:** PLAN -> EXECUTE AUTONOMOUSLY (worktree/PR/CI). This is a self-contained new package with no production wiring yet — nothing downstream depends on merging this PR, so there is no coordination risk.
**File-ownership:** exclusive owner of `internal/syncapi/progress/` for this task's slice (the merge-policy files listed below). TASK-09 (bookmarks) lands its own **new** files in the same directory in **wave 2**, after this PR is merged — it does not touch the files this task creates, so there is no same-file collision as long as TASK-09 starts from a fetched `origin/main` that already contains this PR. This task does **not** touch `internal/database/store.go`, `internal/database/pebble_store_playback.go`, or any HTTP handler — it is pure Go with zero imports outside the standard library.

## ⛔ START HERE (do this first, exactly)

```bash
REPO=/Users/jdfalk/repos/github.com/jdfalk/audiobook-organizer   # adjust to your clone
git -C "$REPO" fetch origin
git -C "$REPO" worktree add "$REPO/.worktrees/abs-sync-progress-merge-policy" -b agent/abs-sync-progress-merge-policy origin/main
cd "$REPO/.worktrees/abs-sync-progress-merge-policy"
git rebase origin/main
```

## Goal

Create `internal/syncapi/progress` — a **pure, I/O-free** Go package implementing the
progress conflict-resolution policy from
`docs/specs/2026-07-29-abs-sync-api-design.md` §5, §5b, and §1.8.7. "Pure" means: no
`internal/database` import, no HTTP, no clock reads inside the merge functions
themselves (callers pass timestamps in) — exactly the shape of the existing
`internal/audioutil/timeline.go` (`CumulativeOffsets`, `SynthesizeChapters`), which this
package is modeled on. This makes every rule in §5 unit-testable today, before the
Phase-6 store adapter (`UserBookState`/`UserPosition`) or any handler exists.

This package does **not** wire into the HTTP layer or the Pebble store — that adapter
work is later Phase-6 scope (README wave 2+/Phase 6 in the spec's phase table) and is
explicitly out of scope here.

## Background (verify before editing)

- **The store this policy will eventually adapt already exists — do not rebuild it.**
  `internal/database/pebble_store_playback.go` has `SetUserPosition`/`GetUserPosition`
  (`UserPosition{UserID, BookID, SegmentID, PositionSeconds, UpdatedAt}`) and
  `SetUserBookState`/`GetUserBookState`
  (`UserBookState{UserID, BookID, Status, StatusManual, LastActivityAt, LastSegmentID,
  TotalListenedSeconds, ProgressPct, UpdatedAt}` with statuses
  `unstarted|in_progress|finished|abandoned` — see `internal/database/store.go:798-818`
  for the exact struct and the `UserBookStatus*` constants). HTTP routes for it already
  exist at `internal/server/wire_library_routes.go:69-74` (`POST/GET
  /books/:id/position`, `GET /books/:id/state`, `PATCH/DELETE /books/:id/status`,
  `GET /me/:status`). **This task does not touch any of those files.** It produces a
  standalone decision function; a later task wires it in.
- **Re-verify these anchors before editing** — line numbers drift:
  ```bash
  grep -n "SetUserPosition\|GetUserPosition\|SetUserBookState\|GetUserBookState" internal/database/pebble_store_playback.go
  # Expected: 6 hits — the 4 func signature lines (SetUserPosition ~:17, GetUserPosition ~:32,
  # SetUserBookState ~:97, GetUserBookState ~:128) plus 2 internal call sites
  # (SetUserBookState calling GetUserBookState ~:107, and one more ~:170)
  sed -n '798,818p' internal/database/store.go
  # Expected: the UserBookState struct + UserBookStatus* const block, exactly as quoted above
  find internal/syncapi -maxdepth 1 -type d
  # Expected: internal/syncapi and internal/syncapi/conformance only — internal/syncapi/progress
  # does not exist yet
  ls internal/audioutil/timeline.go internal/audioutil/timeline_test.go
  # Expected: both exist — this is the sibling pure-package pattern to mirror (file header,
  # doc-comment style, table-driven test style)
  ```
- **Read spec §5, §5b, and §1.8.7 in full before writing code** — the ten rules below are
  a synthesis of both sections and they interact (e.g. the "sticky finished" rule and the
  offline-replay guard are two *different* mechanisms that must not be conflated — see
  step 2 below).
- Module path is `github.com/falkcorp/audiobook-organizer` (falkcorp, not jdfalk); repo
  Go version is `go 1.26.0` (`go.mod:3`).

## Design (this is the contract — implement exactly this API shape)

`internal/syncapi/progress/policy.go`:

```go
package progress

// Progress is the pure (user, item) progress record this package operates on.
// "item" is deliberately opaque here (a raw Book ULID today, an ABS sync_item
// syncID once TASK-01 ships) — this package does not know or care which identity
// scheme the caller uses.
type Progress struct {
	CurrentTime float64 // seconds, whole-book position (never a percentage)
	Duration    float64 // seconds; spec §5b: pick ONE authoritative duration per book
	                     // (sum-of-tracks recommended) and pass it consistently — this
	                     // package does not compute duration, only compares against it
	IsFinished  bool
	UpdatedAtMs int64   // ms epoch, server-authoritative once stored (spec §1.7.3 #1)
}

// FinishedToleranceSec is the §5b tolerance: currentTime within this many seconds
// of duration counts as finished. Measured worst-case container/chapter/track-sum
// skew on a real m4b is ~52ms; 2s is comfortably above that and far below a
// meaningful amount of unlistened audio.
const FinishedToleranceSec = 2.0

// IsWithinFinishedTolerance reports whether currentTime has reached duration
// within FinishedToleranceSec (spec §5b).
func IsWithinFinishedTolerance(currentTime, duration float64) bool

// MergeIncoming implements spec §5 rules 1-4 for endpoints where the server
// DERIVES isFinished (POST /api/session/:id/sync — clients do not send
// isFinished/progress there, spec §1.8.7 amendment). Rules, in order:
//  1. Newer wins: if incoming.UpdatedAtMs > server.UpdatedAtMs, accept incoming
//     wholesale, EXCEPT isFinished is sticky-OR'd (rule 3 below), not overwritten.
//  2. Stale device, forward-only: otherwise (incoming not newer), accept ONLY IF
//     incoming.CurrentTime > server.CurrentTime — advance CurrentTime and
//     UpdatedAtMs, still sticky-OR isFinished. A stale-and-behind update is
//     rejected outright (accepted=false, result==server unchanged).
//  3. Finished is sticky: result.IsFinished = server.IsFinished ||
//     IsWithinFinishedTolerance(result.CurrentTime, result.Duration). Once true
//     it can only ever become true again here — /sync has no path to clear it
//     (see MergeExplicit for the only path that can).
// Returns the merged Progress and whether incoming caused any change.
func MergeIncoming(server, incoming Progress) (result Progress, accepted bool)

// MergeExplicit implements the PATCH /api/me/progress/:id path, where Absorb
// DOES send isFinished/progress explicitly and the server must honour rather
// than contradict them (spec §1.8.7 amendment to §5). Applies the same
// newer-wins / forward-only base rules as MergeIncoming, but:
//   - only when accepted via the "newer wins" branch may incoming.IsFinished
//     override server.IsFinished (including clearing it back to false —
//     "re-opening" a finished book, which ABS allows per spec §5 rule 4);
//   - a strictly-newer isFinished:true->false transition additionally resets
//     result.CurrentTime to 0 (mirrors real ABS "mark as not started"; a
//     forward-only accept via rule 2 can NEVER carry an explicit isFinished,
//     since /sync-style forward-only updates don't originate from a client
//     that supplies isFinished at all in this codepath).
func MergeExplicit(server, incoming Progress) (result Progress, accepted bool)

// MergeOfflineReplay implements the abs-shim-derived guard for
// POST /api/session/local[-all] (spec §1.8.7 last bullet — a distinct
// requirement from §1.7.3 #2's "return 200 for unknown IDs" rule, which is
// an HTTP-layer concern for a later task, not this function's job):
// offline clients re-stamp stale backlog entries with updatedAt = now before
// replaying them, which defeats a timestamp-based comparison outright — a
// backlog entry could look "newer" by timestamp while its position is far
// behind current server state. This guard IGNORES timestamps entirely and is
// forward-only on CurrentTime alone: accept iff incoming.CurrentTime >
// server.CurrentTime. UpdatedAtMs is still bumped via NextServerTimestampMs
// when accepted, so downstream conflict checks see a fresh write.
func MergeOfflineReplay(server, incoming Progress) (result Progress, accepted bool)

// MergeCombine implements spec §5 rule 5 / §4.2's merge-follow hook: when two
// items combine (a dedup merge surfacing two previously-independent progress
// records for the same book), the combined record takes max(currentTime),
// OR(isFinished), max(updatedAt). Unlike the Merge* functions above this is
// unconditional — both inputs are already "this device's real position", there
// is no stale/forward-only question.
func MergeCombine(a, b Progress) Progress

// NextServerTimestampMs returns an UpdatedAtMs value for a server-initiated
// write that is guaranteed to beat AudioBooth's tie-break (spec §1.8.7 second
// bullet): AudioBooth compares timestamps with strict `>` after truncating
// BOTH sides via integer `/1000` (whole seconds), so two writes inside the
// same wall-clock second compare equal and the client's cached value wins,
// silently discarding the server's update. This returns max(nowMs,
// prevUpdatedAtMs + 1000) whenever nowMs's truncated-to-seconds value would
// not already exceed prevUpdatedAtMs's, guaranteeing
// result/1000 > prevUpdatedAtMs/1000.
func NextServerTimestampMs(prevUpdatedAtMs, nowMs int64) int64

// AddListenedDelta implements POST /api/session/{id}/sync's "timeListened"
// semantics (spec §1.8.4 / §1.7's finding 3): a DELTA the server must ADD to
// the running total. Negative deltas (a malformed or clock-skewed client) are
// clamped to 0 rather than subtracted, so a buggy client can never rewind
// total listened time.
func AddListenedDelta(runningTotalSec, deltaSec float64) float64

// SetListenedCumulative implements POST /api/session/local[-all]'s
// "timeListening" semantics (spec §1.8.4): a CUMULATIVE total (idempotent
// set), NEVER additive — reading it as a delta is the exact abs-shim bug
// this spec calls out (records zero listening time from both clients).
// Forward-only: returns max(runningTotalSec, cumulativeSec) so a stale
// replayed session (see MergeOfflineReplay) cannot rewind a total that a
// newer session already advanced past it.
func SetListenedCumulative(runningTotalSec, cumulativeSec float64) float64

// ValidateFinishedDuration enforces the invariant behind spec §1.8.7's
// null-duration trap ("isFinished: true with a null duration sets the
// client's currentTime to 0"): any Progress about to be serialized with
// IsFinished true must carry a positive Duration. Returns an error
// otherwise. This is a guard for the future Phase-6 DTO layer to call
// before writing a response body — Progress.Duration is a required
// (non-pointer) float64 specifically so this invariant is checkable here
// rather than only at the JSON boundary.
func ValidateFinishedDuration(p Progress) error
```

Everything above is standard library only (no `internal/database`, no `time.Now()`
inside the functions — callers supply `nowMs`/timestamps explicitly, which is what
makes this pure and deterministic to test).

## Step-by-step

1. Create `internal/syncapi/progress/policy.go` with the file header (new file — mint a
   fresh guid) and the exact API shape above. Match the doc-comment density and citation
   style of `internal/audioutil/timeline.go` (cite the spec section for every rule).
2. **Before writing any test, write out on paper (as a code comment block at the top of
   `policy_test.go`) the four distinct decision paths and which rule governs each**, so
   the "sticky finished" rule (§5 rule 4) is never accidentally conflated with the
   offline-replay guard (§1.8.7 last bullet) — they are different mechanisms for
   different endpoints and must not share a code path:
   - `/sync` (isFinished server-derived) → `MergeIncoming`
   - `PATCH /api/me/progress/:id` (isFinished client-supplied) → `MergeExplicit`
   - `/session/local[-all]` (offline replay, timestamps are unreliable) →
     `MergeOfflineReplay`
   - dedup merge-follow → `MergeCombine`
3. Implement `IsWithinFinishedTolerance`, `NextServerTimestampMs`,
   `AddListenedDelta`, `SetListenedCumulative`, `ValidateFinishedDuration` first — they
   are the simplest, most isolated functions and unblock the merge functions' tests.
4. Implement `MergeIncoming`, `MergeExplicit`, `MergeOfflineReplay`, `MergeCombine` in
   that order, running `go test ./internal/syncapi/progress/... -race -count=1` after
   each (TDD: write the test for the function first, watch it fail for the right reason,
   then implement — per repo TDD convention).
5. Write `internal/syncapi/progress/policy_test.go` as **dense table-driven tests**,
   including at minimum these named cases (one `t.Run` sub-test per row is fine):
   - `TestMergeIncoming_NewerWins` — incoming.UpdatedAtMs strictly greater, distinct
     CurrentTime → incoming wins wholesale (except IsFinished handling, see next case).
   - `TestMergeIncoming_StaleForwardAdvances` — incoming older by timestamp but
     CurrentTime greater than server's → accepted, position advances (spec §5 rule 3's
     exact example: "an offline device that listened further still advances").
   - `TestMergeIncoming_StaleBehindRejected` — incoming older by timestamp AND
     CurrentTime less than or equal to server's → rejected, server state unchanged. This
     is **the specific clobber the spec fears** — assert byte-for-byte (`reflect.DeepEqual`
     or field-by-field) that `result == server`, not just that `accepted == false`.
   - **Property-style matrix for the stale-device cases**: a table with at least 6 rows
     crossing `{incoming older, incoming newer, incoming equal timestamp}` ×
     `{incoming ahead, incoming behind, incoming equal position}`, asserting
     accept/reject and the resulting CurrentTime for every combination (equal timestamp
     counts as "not newer" → forward-only branch). This directly matches the task's
     "property-style matrix for the stale-device cases" requirement.
   - `TestMergeIncoming_FinishedIsSticky` — server.IsFinished=true, incoming is a
     strictly-newer update with CurrentTime below the tolerance threshold (as /sync would
     send — it cannot express "un-finish") → result.IsFinished stays true.
   - `TestMergeIncoming_FinishedDetection_LastChapterEnd` — regression test for spec §5b:
     construct `Duration = 9975.480544` (real m4b container duration from the spec's
     measured example) and `CurrentTime = 9975.428000` (real last-chapter-end value) —
     assert `IsWithinFinishedTolerance` returns true and a `MergeIncoming` call at that
     position sets `result.IsFinished = true`. This is the exact "sits at 99% forever"
     bug the spec requires a regression test for.
   - `TestMergeExplicit_CanClearFinished` — server.IsFinished=true, a strictly-newer
     incoming with `IsFinished=false` → result.IsFinished=false AND
     result.CurrentTime==0 (the reset side effect).
   - `TestMergeExplicit_HonoursExplicitFinishedTrue` — incoming (strictly newer) sets
     IsFinished=true even if CurrentTime is below tolerance → server must honour it, not
     contradict it (spec §1.8.7: "there the server must honour rather than contradict
     them").
   - `TestMergeOfflineReplay_IgnoresReStampedTimestamp` — incoming.UpdatedAtMs is
     artificially set far in the future (simulating a client re-stamping a stale backlog
     entry with `updatedAt = now`) but incoming.CurrentTime is LESS than server's → must
     be rejected anyway (proves this function does not use timestamps at all, unlike
     MergeIncoming — the exact case that would falsely "win" under MergeIncoming's rules).
   - `TestMergeOfflineReplay_AdvancesWhenAhead` — incoming.CurrentTime greater than
     server's, any timestamp → accepted.
   - `TestMergeCombine_TakesMaxAndOr` — two Progress values with different
     CurrentTime/IsFinished/UpdatedAtMs → result has max(CurrentTime), OR(IsFinished),
     max(UpdatedAtMs).
   - `TestNextServerTimestampMs_TieCase` — `prevUpdatedAtMs` and `nowMs` in the SAME
     wall-clock second (e.g. `prevUpdatedAtMs=1000`, `nowMs=1500`) → result/1000 must be
     strictly greater than 1000/1000, i.e. result >= 2000. This is the exact "two writes
     inside the same wall-clock second" tie case spec §1.8.7 calls out — required by this
     task's brief explicitly.
   - `TestNextServerTimestampMs_NowAlreadyAheadIsUsedAsIs` — `nowMs` already a full
     second-plus ahead of `prevUpdatedAtMs` → result == nowMs (no artificial inflation
     when it isn't needed).
   - `TestAddListenedDelta_Adds` and `TestAddListenedDelta_ClampsNegative`.
   - `TestSetListenedCumulative_SetsNotAdds` — proves it is NOT additive (the exact
     abs-shim bug: calling it twice with the same cumulative value must not double it).
   - `TestSetListenedCumulative_ForwardOnly` — a smaller cumulative value than what is
     already stored must not rewind the total.
   - `TestValidateFinishedDuration_RejectsZeroDurationWhenFinished` and
     `TestValidateFinishedDuration_AllowsUnfinishedZeroDuration`.
6. Run `gofmt -l internal/syncapi/progress/` (expect empty output) and
   `go vet ./internal/syncapi/progress/...`.
7. Add a `changelog.d/` fragment: `changelog.d/20260730_abs_sync_progress_merge_policy.md`
   (mint your own guid; follow the `### Added` format in
   `changelog.d/20260729_010000_chapter_extraction.md` as your style template — explain
   the sticky-finished vs. offline-replay distinction and the §5b tolerance rationale,
   since that is the non-obvious design decision a reviewer needs explained).
8. Bump file headers on every file you create (new files start at `version: 1.0.0` with a
   freshly minted guid — do not reuse this task brief's guid).

Anti-over-suppression: N/A — this package makes no error-suppression decisions; every
"reject" path returns `accepted=false` explicitly rather than swallowing an error.

## How to test

```bash
cd "$REPO/.worktrees/abs-sync-progress-merge-policy"
go build ./internal/syncapi/progress/...
go test ./internal/syncapi/progress/... -race -count=1 -v
# Expected: every TestXxx name listed in step 5 present and PASS; no other package
# affected (this is a brand-new package, so `go build ./...` elsewhere is unaffected)
go vet ./internal/syncapi/progress/...
gofmt -l internal/syncapi/progress/
# Expected: gofmt prints nothing (already formatted)
go build ./...
# Expected: whole-repo build still succeeds (new package has no callers yet, so nothing
# else can break)
```

## Acceptance criteria

- [ ] `internal/syncapi/progress/policy.go` exists implementing exactly the function set
      in the Design section: `MergeIncoming`, `MergeExplicit`, `MergeOfflineReplay`,
      `MergeCombine`, `NextServerTimestampMs`, `AddListenedDelta`,
      `SetListenedCumulative`, `ValidateFinishedDuration`, `IsWithinFinishedTolerance`,
      plus the `Progress` type and `FinishedToleranceSec` constant
- [ ] Zero imports from `internal/database`, `internal/server`, or any HTTP package —
      `grep -n '"github.com/falkcorp/audiobook-organizer/internal/' internal/syncapi/progress/policy.go`
      returns 0 hits (pure, no internal deps)
- [ ] All 19 named test cases in step 5 exist and pass, including the tie-case test
      (`TestNextServerTimestampMs_TieCase`), both DELTA/CUMULATIVE tests
      (`TestAddListenedDelta_*`, `TestSetListenedCumulative_*`), and the last-chapter-end
      regression test (`TestMergeIncoming_FinishedDetection_LastChapterEnd`)
- [ ] The property-style stale-device matrix (step 5, "property-style matrix") has at
      least 6 rows and is a single table-driven test, not 6 separate hand-written tests
- [ ] `go test ./internal/syncapi/progress/... -race -count=1` passes, pasted in the PR
      body
- [ ] `gofmt -l internal/syncapi/progress/` is empty and `go vet
      ./internal/syncapi/progress/...` is clean
- [ ] `go build ./...` still succeeds repo-wide
- [ ] `changelog.d/` fragment added with a unique filename
- [ ] File headers present and correct on every new file (fresh guids, not this brief's)
- [ ] Anti-over-suppression: N/A (documented above)

## Commit message

```
feat(abs-sync): add pure progress-merge policy package (#TASK-08)

internal/syncapi/progress implements the spec §5/§5b/§1.8.7 conflict-resolution
rules as pure, I/O-free decision functions: newer-wins with forward-only
stale-device advance, sticky-finished, a >=2s finished-detection tolerance
(m4b container/chapter/track-sum skew is ~52ms), a separate offline-replay
guard that ignores re-stamped timestamps, dedup merge-combine (max/OR/max),
an AudioBooth tie-break-safe timestamp bumper, and the timeListened-delta vs
timeListening-cumulative split. No store or HTTP wiring yet -- that is later
Phase-6 scope; this PR is fully unit-tested standalone.

Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>
Claude-Session: https://claude.ai/code/session_01J29y3VpN7FTczJmLeUJimt
```

## PR + merge

```bash
git push -u origin agent/abs-sync-progress-merge-policy
gh pr create --fill
gh pr merge <number> --rebase
```

## Idempotency / Rollback

If `internal/syncapi/progress/policy.go` already exists with all the functions listed in
the Design section and `go test ./internal/syncapi/progress/... -race -count=1` passes,
the work is already done — run the acceptance checks instead of re-implementing.
Rollback = revert the single commit; nothing else in the repo imports this package yet
(verify with `grep -rn 'syncapi/progress' internal/ --include=*.go | grep -v
internal/syncapi/progress/` — expect 0 hits before this task's PR merges), so reverting
is a clean no-op for every other package.
