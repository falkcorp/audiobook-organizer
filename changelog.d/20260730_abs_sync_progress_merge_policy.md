<!-- file: changelog.d/20260730_abs_sync_progress_merge_policy.md -->
<!-- version: 1.0.0 -->
<!-- guid: f62b62db-6859-4ff2-920e-9dbce91be438 -->
<!-- last-edited: 2026-07-30 -->

### Added

#### Progress conflict-resolution policy (`internal/syncapi/progress`)

Added the pure, I/O-free decision functions implementing the ABS Sync API progress
conflict-resolution rules from `docs/specs/2026-07-29-abs-sync-api-design.md` §5, §5b and
§1.8.7. Nothing here imports `internal/database`, touches HTTP, or reads the clock —
callers pass every timestamp in explicitly — so every rule the spec locks down is
deterministically unit-testable ahead of the Phase-6 `UserBookState`/`UserPosition` store
adapter and the handlers that will call it. Modeled on the sibling pure package
`internal/audioutil` (`CumulativeOffsets`, `SynthesizeChapters`). No production wiring
yet; this change is standalone and has no callers.

The base rule is §5's newer-wins-with-forward-only-fallback: a strictly newer
`updatedAt` is accepted wholesale, and an *older* update is accepted **only** when its
position is ahead of the server's. That asymmetry is the whole point — an offline device
that listened further while offline must still advance the position, while one that is
merely behind can never rewind newer server progress. A rejected merge returns the stored
record field-for-field untouched, which the tests assert with `reflect.DeepEqual` rather
than only checking the accept flag. A stale-but-ahead accept advances `UpdatedAtMs`
monotonically (it never moves the stored timestamp backwards), since a rewound server
timestamp would make the *next* update from any device falsely look newer and win a
conflict it should lose.

**Three different merge entry points, deliberately not one.** The two rules that both
sound like "don't go backwards" are separate mechanisms for separate endpoints and must
not share a code path:

- `MergeIncoming` — `POST /api/session/:id/sync`, where clients do not send `isFinished`
  at all and the server derives it. Finished is **sticky** here: it is OR'd with the
  tolerance check and never cleared, because this endpoint has no way to express
  "un-finish".
- `MergeExplicit` — `PATCH /api/me/progress/:id`, where Absorb *does* send `isFinished`
  and the server must honour rather than contradict it. Only a strictly-newer update may
  override the stored flag, including clearing it (re-opening a finished book, which ABS
  allows); that true→false transition also resets the position to 0, mirroring real ABS
  "mark as not started". A forward-only accept can never carry an explicit `isFinished`,
  so it keeps the sticky behaviour.
- `MergeOfflineReplay` — `POST /api/session/local[-all]`, where timestamps are
  **unusable**: offline clients re-stamp stale backlog entries with `updatedAt = now`
  before replaying them, so a far-behind position arrives looking newer. This guard
  ignores timestamps entirely and is forward-only on position alone. A regression test
  feeds the same inputs to both functions and asserts `MergeIncoming` accepts while
  `MergeOfflineReplay` rejects — which is exactly why they are two functions.

`MergeCombine` covers the dedup merge-follow case (§5 rule 5): two previously independent
progress records for what turned out to be one book are combined unconditionally with
max-position / OR-finished / max-timestamp, since both are real device positions and
there is no staleness question to answer.

**Why the finished tolerance is 2 seconds, not an epsilon.** A single book has three
legitimate, mutually disagreeing durations, measured on the committed Odyssey fixture:
m4b container `9975.480544`, m4b last-chapter-end `9975.428000`, and sum-of-mp3-tracks
`9975.431111`. The ~52 ms spread is structural (m4b chapter marks are millisecond-
quantized via `time_base 1/1000` while per-track durations are frame-accurate), so a
client that plays to the end of the final *chapter* stops short of the *container*
duration. With a tight epsilon a fully listened book never auto-marks finished and sits
at 99% forever; `FinishedToleranceSec = 2.0` is comfortably above the worst inter-source
skew and far below a meaningful amount of audio. A regression test drives a merge at the
real last-chapter-end position and asserts finished becomes true.

Also included, each with the silent-failure mode it prevents:

- `NextServerTimestampMs` — AudioBooth compares `lastUpdate` with strict `>` *after*
  truncating both sides via integer `/1000`, so two writes inside the same wall-clock
  second compare equal and the client's cached value wins, silently discarding the
  server's update. This returns a stamp guaranteed to beat that tie-break by a whole
  second, without inflating a timestamp that is already ahead.
- `AddListenedDelta` vs `SetListenedCumulative` — `/sync`'s `timeListened` (past tense)
  is a **delta to add**, while `/session/local[-all]`'s `timeListening` (gerund) is a
  **cumulative set**. Reading the wrong key records zero listening time from both
  clients (a known reference implementation does exactly this). The delta form clamps
  non-positive values instead of subtracting; the cumulative form is idempotent and
  forward-only so a replayed session cannot rewind the total. Both discard NaN and
  infinities rather than poisoning the stored total.
- `ValidateFinishedDuration` — sending `isFinished: true` without a duration zeroes the
  client's position, so `Progress.Duration` is a required non-pointer field and this
  guard is checkable before the future DTO layer serializes a response body.
