<!-- file: docs/audio-fingerprint/book-signature.md -->
<!-- version: 1.0.1 -->
<!-- guid: 4b5c6d7e-8f9a-0b1c-2d3e-4f5a6b7c8d9e -->
<!-- last-edited: 2026-10-11 -->

# Book Signature: Unified Per-Book Audio Fingerprint

## Problem

The existing `AcoustIDScan` dedup layer compares per-file 7-segment chromaprint
fingerprints across books. This works when both copies of a book have the same
file split (e.g., both are 30 chapter MP3s numbered identically), but it fails
predictably for the **multi-file vs single-file split mismatch** case that
users commonly encounter:

- **Book A**: shipped as a single 18-hour `.m4b` file
- **Book B**: the same content split into 30 numbered `.mp3` chapter files

The two copies are the same audiobook, but their per-file segments line up on
different time boundaries. Matches are coincidental, not structural.

There was no "canonical book-level signature" to compare against, and no
matcher that understands "book A's full audio == concatenation of book B's
files in order."

## Solution: Book Signature v1

A deterministic **book-level** fingerprint synthesized from the per-file
7-segment chromaprints:

1. **Concatenate** all per-file segments (seg0..seg6) in file order
   (sorted by `book_files.sort_order` or `original_filename`).
2. **Decode** the base64-encoded chromaprint segments into one big `[]uint32`
   array.
3. **Down-sample** to a fixed length (4096 uint32s) via max-pooling consecutive
   non-overlapping windows. This normalizes different-length books to
   comparable signatures and bounds storage cost (~22 KiB base64).
4. **Re-encode** to base64 → stored in `books.book_sig_v1`.

The down-sampling makes multi-file and single-file copies of the same book
produce the same-length signature, and (modulo intro/outro padding) the
down-sampled hash sequences overlap the same audio peaks.

## Schema

Three new nullable columns on `books` (migration 058):

- `book_sig_v1` (TEXT): base64-encoded 4096-uint32 signature
- `book_sig_segments` (INTEGER): pre-downsample segment count (diagnostics)
- `book_sig_built_at` (DATETIME): when the signature was last synthesized

An index on `book_sig_v1` speeds lookups.

## Comparison

`BookSignatureSimilarity(a, b)` compares two signatures:

1. Decode both to `[]uint32` (length 4096 each).
2. Compute Hamming distance per uint32 word
   (`bits.OnesCount32(a[i] ^ b[i])`), sum, divide by `4096*32` for normalized
   error rate.
3. Return `similarity = 1.0 - errorRate`.

**Threshold guidance:**

- Exact: `1.0`
- Near-duplicate: ≥ `FuzzyMinSimilarity` (currently 0.80, same as per-file)
- Weak match: ≥ `0.7` (empirical; may be tuned based on production data)

## Integration

### Backfill

The startup backfill (`internal/server/acoustid_backfill.go`) now:

1. Fingerprints all files for a book (existing logic).
2. After finishing a book, calls `synthesizeBookSignatureForBook` to build the
   book signature from the newly-generated per-file segments.

The signature is skipped (left NULL) if any file has empty `acoustid_seg0`.

### Fingerprint Rescan

The on-demand `POST /api/v1/dedup/fingerprint-rescan` endpoint
(`internal/server/fingerprint_rescan.go`) also rebuilds the book signature
after re-fingerprinting a book's files, ensuring the signature stays in sync
with the per-file data.

### Dedup Scan

New `Engine.BookSignatureScan` method (`internal/dedup/engine.go`) walks all
primary books with non-NULL signatures, compares them pairwise, and emits
`dedup_candidates` rows with `layer="book_signature"` for pairs exceeding
`FuzzyMinSimilarity` (0.80).

The scan runs as the `dedup.book-signature-scan` operation, started through
`POST /api/v1/operations/v2` (requires PermScanTrigger). It originally had a
dedicated `POST /api/v1/dedup/scan-book-signature` endpoint; that route was
removed in 01-P72.

Tracked as an Operation, progress reported via the standard
`/api/v1/operations/<id>` status endpoint.

## Edge Cases

- **Bonus content / intros / outros that differ:** Down-sampling smooths these
  out into noise. Threshold tuning handles this.
- **Reordered chapters:** Files are sorted by `sort_order` or `filename`. If
  filenames are inconsistent, we fall back to scanning permutations (out of
  scope for v1 — log a `book_sig_unstable_order` warning instead).
- **Books with one file pending fingerprinting:** Signature is left NULL.
  Backfill fills it in on its next pass; the matcher skips NULL signatures.
- **Very long books (> 4096 segments after concatenation):** Pooling window
  grows; the signature length stays 4096. Resolution drops slightly but
  threshold still works because the same pooling is applied to both sides.

## Storage Cost

Down-sampled signature: `4096 * 4 bytes = 16 KiB` raw, ~22 KiB base64. For
50,000 books this is ~1.1 GiB. Acceptable — stored inline on the `books` row.

## Rollback

Schema migration is additive (three new nullable columns). Drop them or leave
them in place; the matcher just skips NULL signatures. No data loss.

## Out of Scope (v1)

- Permutation search for shuffled splits — emit a warning and skip.
- Cross-version matching (different narrators) — that's the embedding /
  metadata layer's job.
- Updating an in-flight book signature on partial file change — for now
  recompute the full signature when any file changes.

## Testing

Unit tests in `internal/fingerprint/book_signature_test.go`:

1. Deterministic signature synthesis from known-good 7-segment arrays.
2. `BookSignatureSimilarity(sig, sig) == 1.0`.
3. Multi-file split vs single-file version of same audio → similarity ≥ 0.85.
4. Unrelated books (random test data) → similarity ≤ 0.7.
5. Incomplete fingerprint (missing seg0) → `ErrIncompleteFingerprint`.

Integration test (manual):

1. Run backfill → verify `book_sig_v1` populated.
2. Start the `dedup.book-signature-scan` op via `POST /api/v1/operations/v2` → verify it runs,
   and surfaces candidates with `layer="book_signature"`.
3. A 1-file vs split-file copy of the same book in the production library
   produces a `book_signature` candidate with similarity ≥ 0.85.

## Acceptance

- `make test ./internal/fingerprint/... ./internal/dedup/... ./internal/server/...` green
- `make build-api` green
- New endpoint returns 202 with an operation_id; the operation enqueues, runs,
  and surfaces a candidate count via the existing scan-status endpoints

## References

- **Spec:** `docs/superpowers/bot-tasks/2026-05-03-unified-book-fingerprint.md`
- **Master spec:** `docs/superpowers/specs/2026-05-02-audiobook-identification-dedup-pipeline.md`
- **Implementation:** PR #XXX (to be filled in)
