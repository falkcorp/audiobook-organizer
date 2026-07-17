# PLAN: LSH Secondary Index for AcoustID Whole-File Fingerprints

Branch: `feat/fp-lsh-index`
Worktree: `../audiobook-organizer-fp-lsh`

## Goal

Make the dedup engine's AcoustID fuzzy walk sub-linear. Today
`acoustidFuzzyEnabled` is OFF in prod because each book triggers a
~308K-row Hamming sweep. With LSH we want median candidate set <100
BookFiles per query, p50 query latency <100ms at 15K-file scale.

## Inputs (audited, not assumed)

- `BookFile.AcoustIDFingerprint []byte` — packed LE uint32, **8 frames/s**,
  **4 bytes/frame** ⇒ 32 B/s of audio. 2 hr ≈ 230 KB, 10 hr ≈ 1.15 MB.
- 308K BookFiles in prod (per `docs/perf-audit-2026-05-29-getall-callers.md`).
- Existing Pebble exact-match index: `book_file_acoustid:<seg>` → `<bookID>:<fileID>`.
- `fingerprint.WholeFileSimilarity([]byte, []byte) (float64, error)` already
  exists for refine step.

## Locked design decisions

| Decision | Choice | Why |
|---|---|---|
| **Band count B** | **64** | ~95% recall on 5% bit-flip; ~60 MB key space (cheap vs the 2-6 GB raw store) |
| **Subprint width** | **8 bytes** (2 consecutive uint32 frames raw) | Deterministic, no MinHash bookkeeping; collision matches a literal 64-bit window |
| **Stride** | **Proportional** = `(frameCount − 2*skip) / B` | Uniform sampling across 30-min and 10-hr files |
| **Edge skip** | Reuse `EdgeSkipFraction = 0.10` from `wholefile.go` | Matches `WholeFileSimilarity`'s skip; intros/outros excluded |
| **Min band hits** | **2** | Suppresses single-collision noise; a real near-dup hits many bands |
| **Index version byte** | **1 byte** (`0x01`) in value | Allows v2 coexistence later via prefix-delete |
| **Pebble key** | `fpidx:` + band(1B) + subprint(8B) + `:` + bookFileID | Prefix-iterable per (band, subprint) without Get |
| **Aux mapping** | `fpidx_meta:<bookFileID>` → marshalled `[]Subprint` | Cheap delete on `UpdateBookFile` (no need to re-derive from old fp) |
| **`reset-all` wipes fpidx** | **Yes** | Keeps invariant: empty fp store ⇒ empty index |

## Files to add / modify

### NEW
- `internal/fingerprint/lsh.go`
  - `type Subprint [8]byte`
  - `func Subprints(raw []byte) (subprints []Subprint, bandIDs []byte, err error)` — deterministic, frame-aligned, returns one entry per band
  - `const LSHIndexVersion byte = 0x01`
  - `const LSHBandCount = 64`
  - `const LSHMinBandHits = 2`
- `internal/fingerprint/lsh_test.go`
  - Deterministic order, correct count
  - Identical fp ⇒ all 64 bands collide
  - 5% bit-flipped fp ⇒ ≥48 (75%) bands still collide
  - Short fp (< 2*skip + B*2 frames) ⇒ returns 0 subprints, no panic
- `internal/plugins/acoustid/lsh_backfill.go`
  - Operation `acoustid.lsh-backfill` — iterates `book_file:` prefix in Pebble, writes LSH entries for any BookFile that has `AcoustIDFingerprint` but no `fpidx_meta:<id>`. Batched 2000/commit. Resumable.

### EDIT
- `internal/database/pebble_store.go`
  - `writeBookFileSecondaryIndexes(batch, f)` — also write `fpidx:` + `fpidx_meta:`
  - `deleteBookFileSecondaryIndexes(batch, f)` — also delete via `fpidx_meta:` lookup
  - `LookupAcoustIDCandidates(fp []byte, maxCandidates int) ([]string, error)` — see read path
  - `ClearAllAcoustIDFingerprints` — extend the existing bulk-clear to also prefix-delete `fpidx:` and `fpidx_meta:`
- `internal/dedup/engine.go` (lines 2240–2290)
  - When `f.AcoustIDFingerprint != nil`: call `LookupAcoustIDCandidates`, fetch each via existing `GetBookFile`, run `WholeFileSimilarity`, emit if ≥ `FuzzyMinSimilarity`.
  - Seg-based fuzzy walk becomes a fallback for un-rescanned rows (keeps the `acoustidFuzzyEnabled` gate as an emergency kill-switch).

### TESTS (in addition to lsh_test.go above)
- `internal/database/pebble_store_lsh_test.go`
  - Write fp ⇒ `LookupAcoustIDCandidates` returns self
  - `UpdateBookFile` swaps fp ⇒ old subprint keys gone, new keys present
  - `ClearAllAcoustIDFingerprints` ⇒ `fpidx:` prefix empty
  - Bench `BenchmarkLookupAcoustIDCandidates_15K` — p50 < 100 ms
- `internal/dedup/engine_lsh_test.go`
  - 3 books, 2 near-dup ⇒ candidate set = {pair}, similarity ≥ threshold

## Build sequence

1. **Wave 1 (sequential, foundation)**: ship `internal/fingerprint/lsh.go` + tests. Nothing else can land without it.
2. **Wave 2 (parallel, after Wave 1 lands locally)**: 4 agents in parallel — they touch disjoint files:
   - Agent A: `pebble_store.go` write/delete hooks + `LookupAcoustIDCandidates` + tests
   - Agent B: `dedup/engine.go` fuzzy path swap + integration test
   - Agent C: `acoustid/lsh_backfill.go` admin op + tests
   - Agent D: `acoustid/reset_all.go` extension to wipe `fpidx:` + test
3. **Wave 3 (integration)**: full `make ci`, then perf bench.

## Migration

- Code-level: index writes go live on next `UpdateBookFile` (so rescan
  populates naturally).
- One-shot: `acoustid.lsh-backfill` op handles the existing 308K rows.
  Run after deploy, no force-rescan needed (subprints derive from
  already-stored raw fps).

## Rollback

- New `LookupAcoustIDCandidates` is purely additive — if it misbehaves,
  flip `acoustidFuzzyEnabled` back to OFF (already the prod default) and
  the engine reverts to exact-match-only. Index keys are dead data, no
  functional impact.
- `acoustid.reset-all` still works to clear all fingerprint state
  including the new index.

## Open questions (none blocking)

The two research agents flagged these — I'm picking defaults and
shipping. Easy to revisit:

- B=64 chosen over B=16 (locked above).
- 8-byte raw windows chosen over low-16-bit MinHash (locked above).
- Reset-all wipes fpidx (locked above).

If any of these turn out wrong, all three are localized changes.
