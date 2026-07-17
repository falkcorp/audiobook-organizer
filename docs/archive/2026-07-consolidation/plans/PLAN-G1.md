# PLAN: MAYDEPLOY-G1 — Scanner Multi-File Audiobook Detection

## Goal

Detect at scan time that N audio files in a single folder belong to ONE
multi-file audiobook (chapters/tracks of one work) and create a single
Book with N BookFiles instead of N separate single-file Books.

## Existing detection (already in tree)

1. `groupFilesIntoBooks` (scanner.go) — groups when ALL sampled files share
   exact album tag.
2. `consolidateChapterGroups` (chapter_consolidation.go) — only matches
   `^\d+[\s\-\.]+` leading-numeric stems, requires avg duration < 10min.
3. `DetectChapterGroups` (chapter_consolidator.go) — post-scan over DB.

## Gap this PR fills

- `Title - NN_MM.ext` (Tarkin pattern, `/` becomes `_` on disk)
- `Title (NN of MM).ext`, `Title (NN-MM).ext`
- `Chapter NN.ext`, `Part N of M.ext`, `Part NN.ext`, `Track NN.ext`
- Tag quorum (≥75%) instead of 100% album-tag agreement

## Files to change

- NEW `internal/scanner/multifile_detector.go`
- NEW `internal/scanner/multifile_detector_test.go`
- EDIT `internal/scanner/scanner.go` — invoke detector at top of
  `groupFilesIntoBooks`; positive match returns one Book with SegmentFiles.

## Detection rules

Positive when:
- N ≥ 3 files in directory, AND
- ≥75% of files yield a sequential index via the pattern set, AND
  detected indices are dense in [min..max] (≥75% fill), AND
- ≥75% of files have non-empty album OR album_artist, and those agree
  (case-insensitive normalized).

## Tests

Positive: Tarkin `(NN of 85)` ×85; `Chapter NN` ×12; `Part N of M` ×8;
bare `NN` ×5. Negative: 5 distinct titles; 2 files (below min); 4 files
with disagreeing albums.

## Build/test

`go build ./...`, `go test ./internal/scanner/... -count=1 -timeout 120s`.

## Rollback

Revert PR — single new file plus a small additive block in scanner.go.
