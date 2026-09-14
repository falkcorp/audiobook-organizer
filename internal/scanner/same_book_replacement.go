// file: internal/scanner/same_book_replacement.go
// version: 1.0.0
// guid: 9222d374-25de-4cf6-932b-d8f13ccb6f3d
// last-edited: 2026-09-14

package scanner

import (
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/logger"
	"github.com/falkcorp/audiobook-organizer/internal/metadata"
)

// Same-book replacement counters. Atomic because createBookFilesForBook runs
// inside ProcessBooksParallel's worker goroutines.
var (
	sameBookReplacedCount   atomic.Int64 // stored row's file was replaced in place; row refreshed through the merge
	sameBookHashCheckCount  atomic.Int64 // stat said "maybe changed"; the file was hashed to decide
	sameBookRefreshErrCount atomic.Int64 // the refresh upsert failed; the row still describes the old bytes
	sameBookHashFailCount   atomic.Int64 // hashing a maybe-changed file failed; not judged this scan
)

// refreshReplacedSameBookFiles re-checks a book's EXISTING book_file rows on a
// rescan and, for any file replaced in place (different bytes at the same
// path: a re-rip, another edition, a user swapping the file), refreshes the row
// through the same scanner upsert the first scan uses.
//
// Before this, createBookFilesForBook returned as soon as the book had rows, so
// a replaced file kept the old FileHash, duration, codec, stream info,
// fingerprint and transcript forever, and dedup and content matching kept
// comparing signals of bytes that no longer exist.
//
// It writes no invalidation logic of its own. The row it builds carries the new
// FileHash, size, tags and (via probeReplacedFileAudio) the probed duration and
// codec, and BatchUpsertScannedBookFiles' merge (database.mergeBookFileFromStored)
// decides what survives: derivedBytes fields are replaced, and derivedAudio
// fields (fingerprint, transcript, stream info) are dropped unless the probe
// proves the audio is the same. That is the exact rule #3394 applies to a path
// taken over by another book; there is one rule, not two.
//
// COST. This runs for every book that reaches createBookFilesForBook on a
// rescan, so it must stay a stat per file for files that did not change.
// looksChangedSinceStored decides from stat data alone; only a file it flags
// is hashed, and not even then when the caller already hashed it (knownHashes,
// the SegmentHashes the dedup pass computed). Files are handled sequentially:
// a book has tens of files and the book loop above is already a worker pool
// (ProcessBooksParallel), so a nested pool would only oversubscribe it.
func refreshReplacedSameBookFiles(existing []database.BookFile, knownHashes map[string]string, scanLog logger.Logger) {
	var rows []database.ScannedBookFile
	for i := range existing {
		stored := &existing[i]
		if stored.FilePath == "" {
			continue
		}
		fi, err := os.Stat(stored.FilePath)
		if err != nil || fi.IsDir() {
			// Not on disk: nothing to compare. Missing-row handling is not
			// this function's job, and a failed stat is never evidence of a
			// replacement (the merge refuses to judge presenceNotSeen too).
			continue
		}
		if !looksChangedSinceStored(stored, fi) {
			continue
		}
		hash := knownHashes[stored.FilePath]
		if hash == "" {
			sameBookHashCheckCount.Add(1)
			h, herr := ComputeFileHash(stored.FilePath)
			if herr != nil {
				warnSampled(&sameBookHashFailCount, scanLog,
					"same-book replacement check: hashing %s failed: %v (not judged this scan)",
					logger.SanitizeLogValue(stored.FilePath), herr)
				continue
			}
			hash = h
		}
		if hash == "" || stored.FileHash == "" || hash == stored.FileHash {
			// Same bytes (an mtime-only change, a touch, a copy that kept the
			// content), or no stored hash to compare against. Nothing about
			// the content changed that we can prove, so nothing is written;
			// the scan-cache write-back records the new mtime.
			continue
		}

		bf := &database.BookFile{
			ID:               stored.ID,
			BookID:           stored.BookID,
			FilePath:         stored.FilePath,
			OriginalFilename: filepath.Base(stored.FilePath),
			Format:           strings.TrimPrefix(strings.ToLower(filepath.Ext(stored.FilePath)), "."),
			FileSize:         fi.Size(),
			// bfUpsertOwned: the merge writes these exactly as given, zero
			// included. The book's placement was judged when it was imported
			// and a content replacement is no reason to re-judge it, so carry
			// the stored values rather than wipe them. TrackCount is part of
			// the same placement (preserveBytes, so a changed hash would
			// otherwise drop it).
			TrackNumber: stored.TrackNumber,
			TrackCount:  stored.TrackCount,
			DiscNumber:  stored.DiscNumber,
			DiscCount:   stored.DiscCount,
		}
		readScannedFileTagsAndHash(bf, stored.FilePath, hash, scanLog)
		probeReplacedFileAudio(bf, stored.FilePath, scanLog)

		sameBookReplacedCount.Add(1)
		scanLog.Info("book file replaced in place: %s (book %s, file %s): hash %s -> %s; refreshing its byte-derived fields",
			logger.SanitizeLogValue(stored.FilePath), stored.BookID, stored.ID, stored.FileHash, hash)
		rows = append(rows, database.ScannedBookFile{File: bf, Present: true})
	}
	if len(rows) == 0 {
		return
	}
	if err := getStore().BatchUpsertScannedBookFiles(rows); err != nil {
		warnSampled(&sameBookRefreshErrCount, scanLog,
			"same-book replacement refresh failed for %d file(s) of book %s: %v (rows still describe the old bytes)",
			len(rows), rows[0].File.BookID, err)
	}
}

// looksChangedSinceStored is the cheap gate: could the file at stored.FilePath
// hold different bytes from the ones the row describes? It reads only the stat
// result, never the file.
//
//   - Size differs from the stored FileSize (the scanner writes FileSize on
//     every row it builds) or from the scan-cache LastScanSize: changed.
//   - A scan-cache LastScanMtime is recorded and differs: changed.
//   - No LastScanMtime (the scan-cache stamp is mirrored onto a file row only
//     for single-file books, so multi-file rows usually have none): changed
//     when the file was modified after the row was last written. A file whose
//     mtime predates the row cannot hold bytes the row has not seen. The price
//     of this fallback is a repeat hash on rescans of a file touched without a
//     content change (the row is not rewritten when the hash matches); such
//     files are re-read by the scan anyway.
//
// A zero stored FileSize is "unknown", not "empty", and is not compared.
func looksChangedSinceStored(stored *database.BookFile, fi os.FileInfo) bool {
	size := fi.Size()
	if stored.FileSize > 0 && stored.FileSize != size {
		return true
	}
	if stored.LastScanSize != nil && *stored.LastScanSize != size {
		return true
	}
	if stored.LastScanMtime != nil {
		return *stored.LastScanMtime != fi.ModTime().Unix()
	}
	return !stored.UpdatedAt.IsZero() && fi.ModTime().After(stored.UpdatedAt)
}

// readScannedFileTagsAndHash fills the tag- and hash-derived fields of a
// scanner-built row: RawTags and Title from the file's tags, and FileHash /
// OriginalFileHash / OriginalFileHashKind from hash, or from ComputeFileHash
// when hash is empty. It returns the tag's stated placement (zero on a failed
// tag read). Shared by the first-scan loop in createBookFilesForBook and the
// same-book replacement refresh, so both build the row the same way.
func readScannedFileTagsAndHash(bf *database.BookFile, filePath, hash string, scanLog logger.Logger) metadata.TagPlacement {
	var placement metadata.TagPlacement
	if meta, merr := metadata.ExtractMetadata(filePath, nil); merr == nil {
		bf.RawTags = meta.AllTags
		placement = metadata.PlacementFromMetadata(meta)
		if meta.Title != "" {
			bf.Title = meta.Title
		}
	} else {
		scanLog.Debug("tag read failed for %s (keeping positional track): %v", logger.SanitizeLogValue(filePath), merr)
	}

	if hash == "" {
		if h, herr := ComputeFileHash(filePath); herr == nil {
			hash = h
		}
	}
	if hash != "" {
		bf.FileHash = hash
		bf.OriginalFileHash = hash
		// Both sources are filehash.BookFileHash (the segment hashes come from
		// the same ComputeFileHash). The merge freezes a stored value of this
		// kind; a legacy stored value of unknown kind is replaced.
		bf.OriginalFileHashKind = database.FileHashKindSampled
	}
	return placement
}
