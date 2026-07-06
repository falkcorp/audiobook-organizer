// file: internal/database/memdb_strip.go
// version: 1.3.0
// guid: a1b2c3d4-mema-aaaa-aaaa-stripbook0001
// last-edited: 2026-07-05

package database

// stripBookForMemdb returns a shallow copy of `src` with heavy, rarely-
// queried fields cleared. Memdb-resident Books are used for indexed
// iteration and predicate filtering — they don't need the full payload.
// Callers that need the full Book (UI enrichment, write paths) fetch it
// from Pebble via GetBookByID, which is the canonical source.
//
// Memory math (392K-book production library):
//
//	Description avg ~500-2000 chars  → ~400MB-1.5GB across all books
//	BookSigV1 base64 4096 uint32s    → ~22KB per fingerprinted book
//	BookSigV1Mask base64 4096-bit    → ~700B per fingerprinted book
//	VersionNotes (rare, multi-line)  → typically empty, occasionally KB
//
// Stripping these from memdb cuts the radix tree's resident size from
// ~10GB to ~2GB. Fields are cleared to nil pointers (saves the string
// data, not just the pointer), so the underlying string bytes become
// GC-eligible after the original *Book goes out of scope.
//
// Predicates that filter by these fields (e.g. `field:description`)
// silently miss against stripped books. The predicate paths that need
// them (rare) should be routed through Pebble's GetBookByID instead.
func stripBookForMemdb(src *Book) *Book {
	if src == nil {
		return nil
	}
	cp := *src
	cp.Description = nil
	cp.VersionNotes = nil
	cp.BookSigV1 = nil
	cp.BookSigV1Mask = nil
	cp.BookSigSegments = nil
	cp.BookSigBuiltAt = nil
	cp.BookSigCoveragePct = nil
	// Pre-resolved Author/Series pointers are nil at warm time anyway —
	// they're hydrated separately via authorsMap/seriesMap in the
	// service layer. Clear defensively in case a caller pre-fills them.
	cp.Author = nil
	cp.Series = nil
	return &cp
}

// stripBookFileForMemdb returns a shallow copy of `src` with heavy
// fingerprint-diagnostic fields cleared before memdb insertion. Mirrors the
// stripBookForMemdb pattern: memdb holds a projection sufficient for indexed
// iteration and predicate filtering; callers needing the full payload fetch
// it from Pebble via GetBookFiles(bookID).
//
// Memory math (~308K book_files in production, fable5 T019 sizing):
//
//	AcoustIDSeg0      (1 string × ~300-500B)        → ~90-150 MB at full coverage
//	AcoustIDSeg1..6   (6 strings × ~300-500B each)  → ~550-900 MB at full coverage
//	Total Seg0..6 strip win                          → ~550-900 MB RSS (~25-35%)
//	AcoustIDFingerprint (~230 KB per 2hr file)       → ~3+ GB (already stripped)
//	FingerprintDiagnosticJSON (*string, KB-class)    → can dominate when populated
//	FingerprintFailureReason / Detail (*string)      → small but per-row
//	FingerprintFailedAt (*time.Time)                 → 24B retained (needed by LSH builder)
//
// Combined drop from Seg0..6 + diagnostic fields: ~550-900 MB + diagnostic overhead.
//
// FingerprintFailedAt is intentionally NOT stripped: it is 24 bytes per row (~7 MB
// at 308K rows) and is read by the LSH index builder to skip permanently-failed files
// from the fingerprint-rescan enqueue queue. Stripping it would cause the builder to
// re-enqueue those books on every warm-memdb run, triggering an infinite retry loop.
//
// AcoustIDSeg0..6 strip rationale (fable5 T019):
//
// Before T013: Seg0 was the last live memdb reader — fingerprint.ComputeFingerprintFields
// called GetAcoustIDSeg0() on memdb-sourced BookFile rows (via GetBookFilesForIDsCore,
// renamed from GetBookFilesForIDs in STOREFID Phase 3) to compute the per-book
// fingerprint_status badge on every /api/v1/audiobooks list.
// Seg1..6 were read by MemStore.GetBookFileByAcoustIDFuzzy (the O(N) dedup path).
//
// After T013: the O(N) fuzzy scan (GetBookFileByAcoustIDFuzzy) is retired — the
// LSH secondary index (fpidx:) + CollectLSHAcoustID replaced it. GetAcoustIDSeg0()
// was migrated to fall back to AcoustIDFingerprintDurationSec > 0 (preserved in
// memdb) for the fingerprint_status badge. Seg0..6 now have zero memdb readers.
//
// Pebble-direct callers (dedup engine via GetBookFiles, handler_files.go, dedup
// comparison handler, AcoustIDScan's seg-based tier-1 exact match, backfill ops)
// are unaffected — they read via Pebble, which retains the full fields.
func stripBookFileForMemdb(src *BookFile) *BookFile {
	if src == nil {
		return nil
	}
	cp := *src
	// FingerprintFailedAt is intentionally kept — see comment above.
	cp.FingerprintFailureReason = nil
	cp.FingerprintFailureDetail = nil
	cp.FingerprintDiagnosticJSON = nil
	// AcoustIDFingerprint is the whole-file raw chromaprint stream
	// (~230 KB per 2hr file). Pebble-direct callers that need the whole-file
	// fp fetch it via GetBookFile / GetBookFiles bypass paths.
	cp.AcoustIDFingerprint = nil
	// AcoustIDSeg0..6: deprecated 7-segment fields (store.go:685-694).
	// Stripped as of fable5 T019 — all memdb readers retired by T013:
	//   - O(N) fuzzy scan (GetBookFileByAcoustIDFuzzy): deleted by T013.
	//   - fingerprint_status badge (GetAcoustIDSeg0 via GetBookFilesForIDsCore):
	//     migrated to AcoustIDFingerprintDurationSec proxy in T019.
	// Pebble-direct paths (dedup engine, handler_files, dedup comparison
	// handler) remain unaffected — they read via GetBookFiles / GetBookFile.
	cp.AcoustIDSeg0 = ""
	cp.AcoustIDSeg1 = ""
	cp.AcoustIDSeg2 = ""
	cp.AcoustIDSeg3 = ""
	cp.AcoustIDSeg4 = ""
	cp.AcoustIDSeg5 = ""
	cp.AcoustIDSeg6 = ""
	return &cp
}
