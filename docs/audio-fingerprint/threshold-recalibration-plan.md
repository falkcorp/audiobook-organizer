<!-- file: docs/audio-fingerprint/threshold-recalibration-plan.md -->
<!-- version: 1.0.0 -->
<!-- guid: 8c41e2d7-5a93-4b6f-a0d8-71f3c9e25b14 -->
<!-- last-edited: 2026-09-25 -->

# Fingerprint threshold recalibration plan

Status: **plan only, nothing executed.** Written 2026-09-25 after verifying
that the Chromaprint decode fix is on `proposed-main`.

## Background

Until #3453 (commit `306de1cab`, merged 2026-09-19), `decodeAnyFingerprint`
read fpcalc's **compressed** fingerprint string as raw little-endian `uint32`
frames. Every fuzzy comparison therefore compared compressed bitstream.
Different encodes of the same audio scored about 0.5, and only byte-identical
prints matched. The fix ports Chromaprint's decompressor. On the same speech,
MP3 vs AAC went from 0.5357 to 0.9843.

Follow-ups already merged:

- `eeacc6626` added era markers. `BookFile.AcoustIDFPVersion` and
  `Book.BookSigVersion` were introduced, with `HasCurrentPrint` and
  `HasCurrentBookSig`. Every fuzzy consumer treats a legacy-era side as
  **missing** evidence, never as a mismatch.
- `b30fc926a` changed selection so that `acoustid.backfill` picks legacy-era
  prints and re-fingerprints them. It also rebuilds book signatures that are
  legacy-era.
- `#3458`: `GET /api/v1/signals/coverage` reports the print and signature era
  census (`current_era` / `legacy_era`).

Two test layers cover the decoder:

- `internal/fingerprint/testdata/chromaprint_golden.json` holds frozen fpcalc
  1.6.1 fixtures.
- `fpcalc_live_decode_test.go` checks the production entry points
  (`FileFingerprintLength`, `FileHeadSegment`) against the installed fpcalc's
  `-raw` output. It skips when fpcalc or ffmpeg is absent.

Every fuzzy threshold below was chosen or tuned while scores were noise. None
of them is known to be right. This plan does not change any of them. It says
how to get the data to change them.

## Thresholds in scope

**Group A: local Hamming similarity over decoded head-print frames.** Their
scale was distorted by the bug.

| Constant | Value | Location | Decides |
|---|---|---|---|
| `fingerprint.FuzzyMinSimilarity` | 0.80 | `internal/fingerprint/fpcalc.go` | dedup LSH tier-0 walk emit (`dedup/engine.go`), fuzzy segment lookups (`GetBookFileByAcoustIDFuzzy`, deluge discovery) |
| `LSHAcoustIDConfig.MinHamming` | 0.85 | `internal/dedup/collectors_acoustid.go` | `SigLSHAcoustID` candidate acceptance |
| `LSHAcoustIDConfig.Min/MaxConfidence` | 0.90 / 0.97 | same | confidence interpolation over `[MinHamming, 1.0]` |
| `fingerprint.LSHMinBandHits` | 2 | `internal/fingerprint/lsh.go` | fpidx band collisions before a candidate is considered |
| `sameRecordingMinSimilarity` | 0.90 | `internal/organizer/inplace_collision.go` | organize treats two rows as the same recording (LINKS rows) |
| literal `0.9` | 0.90 | `internal/reconcile/itunes_heal.go` (`resolveAmbiguousByDB`) | iTunes heal treats candidates as acoustically identical, then **merges books** |
| `MinUsefulFingerprintFrames` | 80 | `internal/fingerprint/fpcalc.go` | sentinel/too-short rejection; now means about 10 s of real frames |

**Group B: book-signature similarity.** `BookSignatureSimilarityMasked`
compares `book_sig_v1` values, which were synthesised from the misdecoded
prints.

| Constant | Value | Location | Decides |
|---|---|---|---|
| `acoustIDVetoMaxSimilarity` | 0.50 | `internal/dedup/engine.go` | **vetoes** a metadata-exact dedup candidate as different audio |
| `acoustIDVetoMinOverlap` / `minOverlapWords` | 512 | `engine.go` (veto and `BookSignatureScan`) | minimum overlap for a masked comparison |
| `FuzzyMinSimilarity` (reused) | 0.80 | `BookSignatureScan` | book-signature candidate emit |
| `sigMatchThreshold` | 0.95 | `internal/dedup/dataset/builder.go` | dataset feature "content match" |
| `sigContainmentThreshold` | 0.90 | same | dataset feature "containment" |

**Group C: AcoustID web-service scores.** These are not local Hamming, so
their scale was not distorted. `AcoustIDOnlineMinScore` (0.85,
`plugins/acoustid/online_lookup.go`) and `acoustIDHealMinScore` (0.85,
`reconcile/itunes_heal.go`) read the service's own score. Before #3453,
however, the app sent its uncompressed form to the service. Historic
`AcoustIDOnlineRecordingID` and `Score` values are therefore invalid, and
`eeacc6626` clears them on re-fingerprint. **No recalibration is needed; the
stored results need a re-run after re-fingerprinting.**

**Out of scope:** windowed-print thresholds (`WindowSetSimilarity`,
`window_similarity.go`). Windowed prints are built from `fpcalc -raw` and were
never misdecoded. They were never calibrated either, but that is separate
work.

## Precondition: the library must be re-fingerprinted first

Calibrating against legacy-era prints would measure the bug again. All
measurement uses **current-era rows only**. The gate is the era census from
`GET /api/v1/signals/coverage`. Calibration starts once `legacy_era` is small
enough that the labelled sample below can be drawn entirely from current-era
rows. It does not need to be zero.

**Blocker (owner decision):** the one path that replaces legacy head prints
is `acoustid.backfill`, which runs fpcalc locally. The standing rule is **no
decodes or fingerprints on U0, and never run `acoustid.backfill` there.**

- `acoustid.window-backfill` has a remote-only Mac-worker mode (#3477).
- The head-print backfill has no such mode.

Before any re-fingerprinting, the owner must choose one of:

1. Add a remote-worker mode to `acoustid.backfill` (or teach the fp-worker
   lease API #3466 to produce head prints), then run it from the Macs.
2. Allow a one-off exception for `acoustid.backfill` on U0.
3. Retire head prints as a fuzzy signal and move the Group A consumers to
   windowed prints. This is the window vs whole-file question, which is the
   owner's call. It would make most of Group A moot.

Both schedule paths are currently off by default:

- `scheduled.acoustid_backfill.enabled=false`
- `maintenance.acoustid_backfill=false` gates `library.optimize`

A manual enqueue of `acoustid.backfill` is ungated.

## fpidx (LSH index) state

What the code does today:

- `UpdateBookFile` deletes a file's `fpidx:` rows through its `fpidx_meta`
  row, then writes new ones (`writeFingerprintLSHIndexes`). The write skips
  legacy-era prints. Re-fingerprinting a file therefore replaces its garbage
  rows.
- `dedup.lsh-index-build` **skips** legacy-era prints and does not index them.
- It also skips any file whose `fpidx_meta` row is already at
  `LSHIndexVersion` (0x02, **not bumped by the fix**).
- As a result, `fpidx:` rows written from legacy prints before 2026-09-19
  **stay in the index**. Nothing purges them.
- `LookupAcoustIDCandidates` ranks by band-hit count and applies the
  `MaxCandidates` cap (200) **before** `CollectLSHAcoustID` filters out
  legacy candidates. A garbage row that reaches `LSHMinBandHits` can therefore
  take a slot from a real candidate.
- Subprints of random bitstream rarely collide with real ones, so the
  exposure is small but not zero.
- About 66k missing-file rows can never be re-fingerprinted. Their garbage
  rows are permanent unless something removes them.

Required cleanup, after the re-fingerprint and before calibrating MinHamming
and LSHMinBandHits:

1. Purge the `fpidx:` and `fpidx_meta:` rows of every book file where
   `!HasCurrentPrint()`. This is a new maintenance op, or a mode of
   `dedup.lsh-index-build` that deletes where it currently skips. Bumping
   `LSHIndexVersion` alone is **not** enough: the build would re-index current
   rows, but it never deletes legacy rows it skips.
2. Re-run `dedup.lsh-index-build` and confirm that the `fpidx_meta` row count
   equals the number of current-era book files with a usable print.

## How to measure (on prod data, computed on the Mac)

**Pairs.** Build a labelled set of book-file pairs and book pairs where both
sides are current-era:

- **Positives:**
  - owner-confirmed version links
  - merged duplicates in the gold-label store (`dedup.mine-gold-labels` /
    `dedup.rebuild-gold-labels` / `dedup.rescore-labeled-examples`)
  - byte-different files of one book (different encodes or rips)
- **Negatives:**
  - gold "not duplicate" labels
  - random same-author pairs
  - **same-publisher pairs, stratified separately.** Head prints cover the
    first 120 s, which is mostly the shared Audible intro
    (`EdgeSkipFraction` trims only 10%). Same-publisher negatives are the case
    most likely to score high. If they overlap the positives, no Group A
    threshold can separate them. That result is itself the finding and feeds
    the window vs whole-file decision.

**Scores.** For each pair, compute `WholeFileSimilarity` (Group A) and
`BookSignatureSimilarityMasked` with its overlap (Group B). The inputs are
stored bytes, so there is no audio decoding. Export the prints read-only
through the API and compute on the Mac; do not add CPU load on U0.

**Sweep.** Reuse the shape of `dedup.calibrate-composite` and
`dedup.calibrate-embedding-thresholds`:

- Sweep 0.50 to 0.99 in 0.01 steps.
- Report precision and recall per stratum.
- Pick each threshold at a target precision matched to its consequence.

| Consequence | Target precision | Thresholds |
|---|---|---|
| **Merges or links automatically** | ≥ 0.99 | iTunes heal 0.9, `sameRecordingMinSimilarity` |
| **Emits a review candidate** | ≥ 0.90 | `FuzzyMinSimilarity`, MinHamming |

The **veto** (`acoustIDVetoMaxSimilarity`) is the mirror case. Pick it from
the positives' low tail: the largest value at which fewer than 0.5% of true
duplicates would be vetoed.

**Sample size.** Aim for at least 500 pairs per stratum. Report a Wilson
interval on each precision figure. If an interval is wider than the gap
between two candidate thresholds, gather more data before choosing.

## After thresholds change

1. Update the constants (Group A and B) in one PR, with the measured
   precision/recall table in the PR body.
2. The emission-time veto ran on garbage during the 2026-09-17
   `dedup.full-scan` and may have suppressed real duplicates. After the
   re-fingerprint and the new thresholds, re-run `dedup.book-signature-scan`
   and a `dedup.full-scan` so they can surface.
3. `ReevaluateAcoustIDConflicts` with `apply` stays manual. Run it only after
   the thresholds are measured.
4. Re-run `acoustid.lookup-online` for the rows whose online verdict was
   cleared.

## Open questions for the owner

1. Which route re-fingerprints the legacy head prints (see the blocker above)?
2. Keep head prints as a fuzzy signal at all, or move Group A consumers to
   windowed prints? The window vs whole-file choice is yours.
3. Is a new purge op for legacy `fpidx:` rows acceptable, as opposed to a
   delete mode inside `dedup.lsh-index-build`?
