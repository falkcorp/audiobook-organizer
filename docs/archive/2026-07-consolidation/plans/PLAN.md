<!-- file: PLAN.md
version: 1.1.0
guid: fp-wholefile-2026-05-30
-->

# Whole-File Fingerprint Migration

**Goal:** Replace the 7-segment per-BookFile fingerprint with a single whole-file chromaprint per BookFile, stored as raw bytes. Add an LSH index later for sub-linear similarity search.

**Strategy: stop the bleeding first.** Step 1 ships ASAP so any new fingerprint extraction produces a valid whole-file fp. Then run reset + rescan once on prod to fix existing damage. Steps 2–4 follow at normal pace.

**Branch:** `feat/fingerprint-wholefile`
**Worktree:** `../audiobook-organizer-fp-wholefile`

---

## Why

- **Robust:** `fpcalc path` from offset 0 to EOF has no seek-past-EOF failure mode. Every file that plays gets a fingerprint.
- **No sentinel pollution:** `AQAAAA` (header-only) came from ffmpeg pipes seeking past lying duration metadata. With whole-file extraction this disappears.
- **Per-file spec:** "save the AcoustID for every file; multi-part books also get a combined book signature."
- **Better matching:** full audio fp > 7 × 5-min spot-checks.
- **Simpler code:** one extraction path, one field.

---

## Storage Budget

| Metric | Value |
|---|---|
| Per-file raw bytes (avg 2hr file) | ~230 KB |
| Per-file raw bytes (10hr single-file book) | ~1.15 MB |
| Reachable files in lib | ~10–15K |
| Projected fingerprint total | **2–6 GB** raw |

Stays in main PebbleDB under `BookFile.AcoustIDFingerprint []byte` field. No separate DB until/unless compaction stalls show up.

---

## Step 1 — Schema + Whole-File Extraction (SHIP FIRST)

**Goal:** Stop new fingerprints from being bad. Land + deploy under flag.

### Files to add
- `internal/fingerprint/wholefile.go`
  - `FileWholeFingerprint(path string) (raw []byte, duration float64, err error)`
  - Runs `fpcalc -raw path` — raw uint32 stream, no base64 wrap, no `-length` cap, no offset.
  - Parses `DURATION=...` and `FINGERPRINT=...` from fpcalc output.
  - Returns `[]byte` of length `4 × frame_count`.
  - Validates: frame count ≥ `MinUsefulFingerprintFrames` (80).

### Files to modify
- `internal/database/store.go`:
  - Add `BookFile.AcoustIDFingerprint []byte`
  - Add `BookFile.AcoustIDFingerprintDurationSec float64`
  - Keep `AcoustIDSeg0..6 string` for back-compat reads.
- `internal/plugins/acoustid/backfill.go`:
  - `fingerprintBookFile` switches to `FileWholeFingerprint` when `WHOLEFILE_FINGERPRINTS=true` (env-gated, default ON in this PR so it actually takes effect).
  - Writes to new field; still writes seg0 (cheap, useful as fallback during transition).
  - Stops writing seg1..6 entirely (these were the buggy ones anyway).
- `internal/dedup/engine.go`:
  - Tier-1 AcoustID compare prefers `AcoustIDFingerprint` when present, falls back to `AcoustIDSeg0`.

### Tests
- `internal/fingerprint/wholefile_test.go`:
  - known-good m4b fixture → fingerprint non-empty, duration > 0
  - tiny mp3 (10 sec) → fingerprint non-empty
  - corrupt file → returns error, no partial data
  - lying-duration m4b → still produces full fp (this is the key test)
- `internal/database/` — round-trip `BookFile.AcoustIDFingerprint` through Pebble.

### Migration
- No schema rewrite. New field is `nil` for existing rows.
- After ship: run `acoustid.reset-all` then `fingerprint-rescan` on prod to fix existing damage.

### Verify before merge
- `make ci` passes
- New field round-trips through PebbleDB
- Existing dedup tests still pass with fallback path

---

## Step 2 — Book Signature Partial Coverage (next PR)

Wire `synthesizeBookSignatureForBook` to use `SynthesizePartialBookSignature` so books with partial file coverage still get a sig with a coverage % flag. Surface coverage in UI.

---

## Step 3 — LSH Index (later PR)

Add `fpidx:<subfp>:<bookfile_id>` secondary index for sub-linear similarity search. Replaces full-scan dedup with candidate-set + Hamming refine. Bench target: <100ms query at 15K-file scale.

---

## Step 4 — Cleanup (final PR)

Remove `AcoustIDSeg1..6` fields and `FileSegments`. Keep `AcoustIDSeg0` one more release cycle as fallback.

---

## Rollback (Step 1)

- Feature flag `WHOLEFILE_FINGERPRINTS=false` reverts new-write path; reads still work because seg0 is still populated.
- New `AcoustIDFingerprint` field is additive — reverting code leaves data in place.
- `acoustid.reset-all` still works to clear everything.

---

## Status

- [ ] Step 1: Schema + whole-file extraction **(in progress)**
- [ ] Reset + rescan prod
- [ ] Step 2: Book sig partial coverage
- [ ] Step 3: LSH index
- [ ] Step 4: Cleanup
