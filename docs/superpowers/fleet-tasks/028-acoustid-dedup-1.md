<!-- file: docs/superpowers/fleet-tasks/028-acoustid-dedup-1.md -->
<!-- version: 1.0.0 -->
<!-- guid: a8f394f0-7022-4d9e-a09c-5d64d8c0580c -->
<!-- last-edited: 2026-08-22 -->

# Task 028: ACOUSTID-DEDUP-1 — Fingerprint-similarity duplicate detection

**Depends on:** none (independent of stats tasks)
**Estimated effort:** L
**Wave:** 8 (AcoustID)

## Goal

Use AcoustID segment fingerprints to detect duplicates even when file hashes differ
(re-encodes, format conversions). Surface as an "Acoustic Duplicates" card in Maintenance.

## Context

- AcoustID segments: `acoustid_seg0`–`acoustid_seg6` on `book_files` (7 segments, each a
  hex string of the segment's chromaprint fingerprint)
- Similarity: compare segment by segment — if ≥4/7 segments match (or similarity score ≥ 0.6),
  flag as acoustic duplicate
- The existing dedup engine: `internal/dedup/engine.go` — add acoustic dedup as a new scan type
- Dedup candidates: store in the existing `dedup_candidates` or similar table — check how
  embedding-based dedup stores candidates for the pattern
- UI: Maintenance tab card + optionally a new section in `BookDedup.tsx`

## Files to modify

- `internal/dedup/engine.go` — add `ScanAcousticDuplicates(ctx) error`
- `internal/database/store.go` — add `GetBooksWithFingerprints`, `SaveAcousticDedupCandidates`
- `internal/database/pebble_store.go` — implement
- `internal/server/` — add route `POST /api/v1/dedup/acoustic-scan`
- `web/src/pages/Maintenance.tsx` — "Acoustic Duplicates" card with trigger button
- `web/src/pages/BookDedup.tsx` — new tab or section for acoustic dedup results

## Instructions

### 1. Fingerprint similarity function

```go
// acousticSimilarity returns the fraction of matching segments between two books.
// Segments are compared as exact string matches (same chromaprint hash = identical segment).
func acousticSimilarity(a, b *BookFile) float64 {
    segsA := []string{a.AcoustIDSeg0, a.AcoustIDSeg1, ..., a.AcoustIDSeg6}
    segsB := []string{b.AcoustIDSeg0, b.AcoustIDSeg1, ..., b.AcoustIDSeg6}
    var matching, total int
    for i := range segsA {
        if segsA[i] == "" && segsB[i] == "" { continue } // both empty = don't count
        if segsA[i] != "" && segsB[i] != "" {
            total++
            if segsA[i] == segsB[i] { matching++ }
        }
    }
    if total == 0 { return 0 }
    return float64(matching) / float64(total)
}
```

### 2. `ScanAcousticDuplicates`

```go
func (e *Engine) ScanAcousticDuplicates(ctx context.Context, reporter operations.Reporter) error {
    files, _ := e.store.GetBooksWithFingerprints(ctx) // only books with ≥1 segment
    reporter.SetTotal(len(files))

    var candidates []AcousticDedupCandidate
    for i, a := range files {
        for _, b := range files[i+1:] {
            if ctx.Err() != nil { return ctx.Err() }
            sim := acousticSimilarity(a, b)
            if sim >= 0.6 {
                candidates = append(candidates, AcousticDedupCandidate{
                    BookAID: a.BookID, BookBID: b.BookID, Similarity: sim,
                })
            }
        }
        reporter.SetProgress(i + 1)
    }

    return e.store.SaveAcousticDedupCandidates(ctx, candidates)
}
```

Note: O(n²) is fine up to ~10K fingerprinted files. Add a note to optimize with LSH if > 50K.

### 3. Operation registration

Register `"acoustic-dedup-scan"` in the dedup plugin or maintenance plugin.

### 4. UI card in Maintenance

"Acoustic Duplicates" card shows the count of candidate pairs. "Scan" button triggers the op.

### 5. Results in BookDedup

Add "Acoustic Duplicates" tab alongside existing dedup tabs showing candidate pairs with
similarity score and side-by-side metadata.

## Test

```bash
go test ./internal/dedup/... -run TestAcoustic -v -count=1
make ci
```

## Commit

```
feat(dedup): acoustic fingerprint similarity duplicate detection (ACOUSTID-DEDUP-1)
```

## PR title

`feat(dedup): acoustic duplicate detection — ACOUSTID-DEDUP-1`

## After merging

Mark `- [ ] **ACOUSTID-DEDUP-1**` as `- [x]` in `TODO.md`.
