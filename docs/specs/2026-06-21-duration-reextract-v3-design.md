<!-- file: docs/specs/2026-06-21-duration-reextract-v3-design.md -->
<!-- version: 1.0.0 -->
<!-- guid: 7f3a9c21-4e85-4b0d-9a2c-6d18f5e3b740 -->
<!-- last-edited: 2026-06-21 -->

# duration-reextract v3 — fingerprint-duration-first backfill

## Problem

Books imported before PR #1555 carry wrong `Book.Duration` values. The old
`mediainfo` estimator derived duration from `fileSize ÷ assumed-bitrate`, which
for m4b/m4a assumed 128 kbps and was routinely ~2× too short. Wrong durations
poison dedup duration-matching (`checkDurationMatch`) and metadata scoring.

The `maintenance.duration-reextract` op (v2) corrects this by re-reading the true
duration via ffprobe — but it shells out **once per segment**, making a
whole-library pass a multi-hour operation.

## Key finding: this is a stock, not a flow

Both live import paths already produce **real** durations, so nothing entering
the library is wrong anymore:

- **Filesystem scan/import** — `mediainfo.BuildFromTag` calls `realDurationSec`
  (ffprobe) first and only falls back to the flagged estimate on failure
  (`internal/mediainfo/mediainfo.go:128`, PR #1555).
- **iTunes import** — uses iTunes's own `track.TotalTime / 1000`
  (`internal/itunes/import.go:374`), a real measured value.

So the bad durations are a **bounded historical backlog** (pre-#1555 scanned
books), not a recurring inflow. The right shape is a **one-time drain**, not an
ongoing scheduled re-scan. No `lastDurationScanTime`/`lastDurationFixTime`
tracking fields are needed: "what still needs correcting" is derivable per book,
and idempotency already exists via the diff tolerance.

## The speedup: read the duration we already stored

Fingerprinting already measured and stored a real per-file duration in
`BookFile.AcoustIDFingerprintDurationSec` (float seconds). fpcalc produced it by
decoding the whole file, so it is as authoritative as ffprobe — the field exists
precisely *because* container metadata lies about duration. It is also
memdb-safe (kept, not stripped). ~275K files have it.

So v3 reads that stored value instead of re-running ffprobe, turning the fast
majority of the backlog into a pure DB pass. ffprobe remains only for the
residue (files never fingerprinted).

## Goal

Rework `maintenance.duration-reextract` to be fingerprint-duration-first:
a fast DB pass for fingerprinted books, with inline ffprobe fallback (no cap)
for the never-fingerprinted tail. One op corrects the whole backlog.

Non-goals: no new persisted fields, no scheduled cadence, no change to the op
ID, params, or write path.

## Design

### Per-segment duration source (priority order)

1. **Fingerprint duration** — if `seg.AcoustIDFingerprintDurationSec > 0`, use
   `round()` of it. No subprocess → fast.
2. **ffprobe fallback** — else `mediainfo.Extract(seg.FilePath)`; trust only a
   real result (`!DurationEstimated`). The `FingerprintFailedAt` tombstone does
   **not** gate this: ffprobe can often read a container header even when
   full-decode fingerprinting failed, and the worst case is simply skipping the
   book.

### Trust invariant (unchanged from v2)

A book is corrected only when **every present segment** yields a real duration
(fingerprint or real-ffprobe). Any missing / unreadable / estimated segment
skips the whole book (counted, not half-written), so `Book.Duration` is never a
partial sum.

### Write path (unchanged from v2)

- Changed segments → `UpdateBookFile` (full-replace on pebble-direct rows,
  preserving the fingerprint per #1552, refreshing memdb per #1560).
- Then `RecomputeBookAggregates(book.ID)` sums corrected segments into
  `Book.Duration`.
- Virtual single-file books (no `BookFile` rows) have no fingerprint field, so
  they go straight to the ffprobe path and write `Book.Duration` directly.

### What changes vs v2

Only the per-segment "compute the real duration" block: add the fingerprint-first
branch before the `mediainfo.Extract` fall-through. Everything else is retained:
params (`dryRun` default true, `limit`), thresholds (`durationRelTolerance` 2%,
`durationAbsToleranceS` 5s), heartbeat cadence, example capture, dry-run safety.

### Telemetry

Add two counters to the heartbeat + summary lines: `from-fingerprint=N` and
`from-ffprobe=N`, so a dry-run immediately reveals how much of the backlog is the
fast path vs the slow tail.

## Testing

Extend `internal/plugins/maintenance/duration_reextract_test.go`:

- (a) Multi-file book where all segments have `AcoustIDFingerprintDurationSec`
  → corrected with **zero** ffprobe calls.
- (b) Mixed book (some segments fingerprinted, some not) → ffprobe fills the gaps.
- (c) A segment with neither a real fingerprint nor a real ffprobe result →
  whole book skipped.
- (d) Idempotent re-run skips already-correct books.

ffprobe is exercised via real `mediainfo.Extract` against fixtures, consistent
with existing tests; fingerprint-sourced paths need no subprocess.

## Rollback

Dry-run is the default, so a bad run writes nothing. The change is confined to
one file (`internal/plugins/maintenance/duration_reextract.go`) with the op ID
and `Run` signature unchanged, so revert is a single-file `git revert`.
