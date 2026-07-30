// file: internal/scanner/process_file.go
// version: 1.3.0
// guid: a1b2c3d4-e5f6-7890-abcd-ef1234567890
// last-edited: 2026-07-30

// Package scanner provides file scanning and processing utilities for the
// audiobook organizer. ProcessFile is the single-pass entry point that opens
// a file exactly once and extracts metadata, media info, and a content hash.
package scanner

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync/atomic"
	"time"

	"github.com/dhowden/tag"
	"github.com/falkcorp/audiobook-organizer/internal/audioutil"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/logger"
	"github.com/falkcorp/audiobook-organizer/internal/mediainfo"
	"github.com/falkcorp/audiobook-organizer/internal/metadata"
)

const (
	hashThreshold = 100 * 1024 * 1024 // 100 MB — files above this get a partial hash
	hashChunkSize = 10 * 1024 * 1024  // 10 MB chunks for the partial hash
	// MaxScanBufferBytes is the named compile-time bound on per-operation buffer
	// allocations. It equals hashChunkSize so CodeQL can statically verify that
	// every make([]byte, MaxScanBufferBytes) call is bounded.
	MaxScanBufferBytes = hashChunkSize // 10 MB

	// chapterProbeTimeout bounds each ffprobe subprocess call made while
	// extracting/synthesizing chapters. Matches mediainfo's
	// ffprobeDurationTimeout value — ffprobe only reads container/stream
	// headers for these calls, so 20s is generous while still bounding a
	// hung subprocess.
	chapterProbeTimeout = 20 * time.Second
)

// chapterStoreAssertErrCount counts PersistChaptersForBook calls where the
// package store is non-nil but not a *database.PebbleStore — a wiring
// mismatch, not a per-book data condition. Logged via warnSampled (1st +
// every 1000th) rather than every call, alongside this file's existing
// dupLookupErrCount-style counters in scanner.go.
var chapterStoreAssertErrCount atomic.Int64

// ProcessFile opens filePath exactly once and returns:
//   - meta: extracted audio metadata (never nil on success)
//   - mi:   technical media info (nil for directories or when tags cannot be read)
//   - hash: SHA-256 hex string of the file content (empty for directories)
//
// The hash algorithm matches ComputeFileHash: full SHA-256 for files ≤100 MB,
// and first-10MB + last-10MB + file-size for larger files.
//
// Existing callers of metadata.ExtractMetadata, mediainfo.Extract, and
// ComputeFileHash are unaffected — those functions continue to work as before.
func ProcessFile(filePath string) (*metadata.Metadata, *mediainfo.MediaInfo, string, error) {
	if filePath == "" {
		return nil, nil, "", fmt.Errorf("ProcessFile: empty file path")
	}

	// stat first — catches non-existence and distinguishes dirs from files
	fi, err := os.Stat(filePath)
	if err != nil {
		return nil, nil, "", fmt.Errorf("ProcessFile: stat %q: %w", filePath, err)
	}

	// Directories: fall back to metadata-only extraction (no mediainfo, no hash)
	if fi.IsDir() {
		meta, err := metadata.ExtractMetadata(filePath, nil)
		if err != nil {
			return nil, nil, "", fmt.Errorf("ProcessFile: directory metadata for %q: %w", filePath, err)
		}
		return &meta, nil, "", nil
	}

	// Open the file once
	f, err := os.Open(filePath)
	if err != nil {
		return nil, nil, "", fmt.Errorf("ProcessFile: open %q: %w", filePath, err)
	}
	defer f.Close()

	fileSize := fi.Size()

	// Read tags — on failure we still need to hash, so don't abort yet
	tagMeta, tagErr := tag.ReadFrom(f)

	// Extract metadata
	var meta metadata.Metadata
	var mi *mediainfo.MediaInfo

	if tagErr != nil {
		defaultLog.Warn("scanner.ProcessFile: tag read failed for %s: %v; using filename fallback", filePath, tagErr)
		meta, err = metadata.ExtractMetadata(filePath, nil) // opens file again — rare error path
		if err != nil {
			defaultLog.Warn("scanner.ProcessFile: filename fallback also failed for %s: %v", filePath, err)
		}
		// mi stays nil — we have no tag to build from
	} else {
		meta = metadata.BuildMetadataFromTag(tagMeta, filePath, nil)
		mi = mediainfo.BuildFromTag(tagMeta, filePath, fileSize)
	}

	// Seek back to start for hashing
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return &meta, mi, "", fmt.Errorf("ProcessFile: seek to start for hashing %q: %w", filePath, err)
	}

	// Compute hash (matches ComputeFileHash logic exactly)
	hash, err := computeHashFromReader(f, fileSize)
	if err != nil {
		return &meta, mi, "", fmt.Errorf("ProcessFile: hash %q: %w", filePath, err)
	}

	return &meta, mi, hash, nil
}

// computeHashFromReader hashes content from an open file reader.
// For files ≤ hashThreshold it hashes all bytes; for larger files it hashes
// the first MaxScanBufferBytes bytes + last MaxScanBufferBytes bytes + the
// file size. This is the same algorithm as ComputeFileHash.
// MaxScanBufferBytes == hashChunkSize, so CodeQL can statically verify the
// allocation bound without any runtime cap check.
func computeHashFromReader(f *os.File, fileSize int64) (string, error) {
	if fileSize > hashThreshold {
		h := sha256.New()

		// First chunk
		first := make([]byte, MaxScanBufferBytes)
		n, err := f.Read(first)
		if err != nil && err != io.EOF {
			return "", err
		}
		h.Write(first[:n])

		// Last chunk (seek from end)
		if fileSize > MaxScanBufferBytes {
			if _, err := f.Seek(-MaxScanBufferBytes, io.SeekEnd); err != nil {
				return "", err
			}
			last := make([]byte, MaxScanBufferBytes)
			n, err = f.Read(last)
			if err != nil && err != io.EOF {
				return "", err
			}
			h.Write(last[:n])
		}

		// Include size in hash
		h.Write([]byte(fmt.Sprintf("%d", fileSize)))

		return hex.EncodeToString(h.Sum(nil)), nil
	}

	// Full hash for smaller files
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// PersistChaptersForBook extracts and persists the chapter list for the book
// whose Book.FilePath is bookFilePath, unless it already has one. It takes a
// file path rather than a book ID because scanner.Book (the in-memory
// scan-time struct) has no ID field -- the scan pipeline always re-looks-up
// the persisted database.Book by path after saving, and this function does
// the same.
//
// Two cases, per docs/specs/2026-07-29-abs-sync-api-design.md §1.8.5 (real
// Audiobookshelf 2.36.0 ground truth):
//   - Single-file book (0 or 1 BookFile rows): trust the file's own embedded
//     chapters as-is.
//   - Multi-file book (>1 BookFile rows): synthesize one chapter per file
//     from re-probed, unrounded per-track durations and each file's own
//     title tag -- never that file's own embedded sub-chapters, even if
//     present.
//
// Behavior on edge cases, in order of evaluation:
//   - No store wired (getStore() == nil): silent no-op, mirrors every other
//     `if store == nil` guard in this package.
//   - Store wired but not a *database.PebbleStore: a genuine wiring
//     regression (the chapter methods only exist on the concrete type) --
//     logged via warnSampled (1st occurrence + every 1000th), not silent.
//   - Book not found by path: quiet no-op -- "not saved yet" is a data
//     condition/race, not a wiring bug.
//   - Chapters already persisted: quiet no-op (idempotent -- a rescan of an
//     unchanged book must not re-run ffprobe every pass).
//   - ffprobe failure for a given file: logged via scanLog (nil-safe,
//     falls back to defaultLog) and swallowed -- non-fatal to the scan.
//
// LABELED SCOPE DECISION (§5b): this function does NOT implement the
// ≥2s finished-detection tolerance §5b also recommends. There is no
// finished-detection code in this package to hang it on yet -- that
// mechanism lives in the not-yet-built progress adapter (Phase 6, per the
// spec). See the DURATION-AUTHORITY comment on synthesizeMultiFileChapters
// for what Phase 6 must do instead.
func PersistChaptersForBook(ctx context.Context, bookFilePath string, scanLog logger.Logger) error {
	store := getStore()
	if store == nil {
		return nil
	}

	ps, ok := store.(*database.PebbleStore)
	if !ok {
		warnSampled(&chapterStoreAssertErrCount, scanLog,
			"scanner.PersistChaptersForBook: store is %T, not *database.PebbleStore -- chapter extraction skipped for %s",
			store, bookFilePath)
		return nil
	}

	dbBook, err := store.GetBookByFilePath(bookFilePath)
	if err != nil || dbBook == nil {
		// "Book not saved yet" (or a stale path) is a data condition, not a
		// wiring bug -- stay quiet.
		return nil
	}

	existingChapters, err := ps.GetChaptersForBook(dbBook.ID)
	if err != nil {
		logChapterWarn(scanLog, "scanner.PersistChaptersForBook: GetChaptersForBook(%s) failed: %v", dbBook.ID, err)
		return nil
	}
	if len(existingChapters) > 0 {
		return nil // idempotent: already extracted on a previous scan
	}

	files, err := store.GetBookFiles(dbBook.ID)
	if err != nil {
		logChapterWarn(scanLog, "scanner.PersistChaptersForBook: GetBookFiles(%s) failed: %v", dbBook.ID, err)
		return nil
	}

	var chapters []audioutil.Chapter
	if len(files) <= 1 {
		filePath := dbBook.FilePath
		if len(files) == 1 && files[0].FilePath != "" {
			filePath = files[0].FilePath
		}
		chapters = probeSingleFileChapters(ctx, filePath, scanLog)
	} else {
		chapters = synthesizeMultiFileChapters(ctx, files, scanLog)
	}
	if len(chapters) == 0 {
		return nil // nothing to persist
	}

	dbChapters := make([]database.Chapter, len(chapters))
	for i, c := range chapters {
		dbChapters[i] = database.Chapter{ID: c.ID, StartSec: c.StartSec, EndSec: c.EndSec, Title: c.Title}
	}
	if err := ps.SaveChaptersForBook(dbBook.ID, dbChapters); err != nil {
		logChapterWarn(scanLog, "scanner.PersistChaptersForBook: SaveChaptersForBook(%s) failed: %v", dbBook.ID, err)
	}
	return nil
}

// probeSingleFileChapters probes filePath's own embedded chapters and
// returns them as-is (nil, non-error result on a file with no chapters --
// audioutil.ProbeChapters's own documented convention). ffprobe failures are
// logged and swallowed; this function never returns an error itself so
// PersistChaptersForBook's dispatch stays a short, uniform caller.
func probeSingleFileChapters(ctx context.Context, filePath string, scanLog logger.Logger) []audioutil.Chapter {
	probeCtx, cancel := context.WithTimeout(ctx, chapterProbeTimeout)
	defer cancel()
	chs, err := audioutil.ProbeChapters(probeCtx, "", filePath)
	if err != nil {
		logChapterWarn(scanLog, "scanner.PersistChaptersForBook: ProbeChapters(%s) failed: %v", filePath, err)
		return nil
	}
	return chs
}

// synthesizeMultiFileChapters builds one chapter per BookFile in files
// (already ordered disc/track/path ASC by GetBookFiles) from each file's own
// title tag and re-probed, unrounded duration -- never from that file's own
// embedded sub-chapters, even when present (real ABS ground truth,
// docs/specs/2026-07-29-abs-sync-api-design.md §1.8.5). Runs sequentially,
// one ffprobe subprocess call per file, inside the caller's already-bounded
// scan worker slot -- see the concurrency note on PersistChaptersForBook's
// caller in scanner.go; this function must never spawn its own goroutines
// per track, which would multiply concurrency beyond the scan's configured
// worker count.
//
// DURATION-AUTHORITY NOTE (§5b -- read before touching finished-detection):
// the resulting chapters[len-1].EndSec is the SUM of these re-probed,
// unrounded per-track durations. That is, BY DESIGN, a different number from
// the book's own, already-persisted Book.Duration field (an *int, set
// elsewhere in the scan pipeline from the container/estimate path -- see
// mediainfo.BuildFromTag / BookFile.DurationEstimated). On the committed
// odyssey fixture the three legitimate durations disagree by ~52ms: m4b
// container duration, m4b last-embedded-chapter end, and sum-of-tracks. Any
// future finished-detection code (the §5b-recommended ≥2s tolerance,
// deferred to a later phase and NOT implemented here) MUST compare against
// this chapter-derived, sum-of-tracks value -- not Book.Duration -- since it
// matches real Audiobookshelf startOffset values exactly.
func synthesizeMultiFileChapters(ctx context.Context, files []database.BookFile, scanLog logger.Logger) []audioutil.Chapter {
	tracks := make([]audioutil.TrackInfo, len(files))
	for i, f := range files {
		probeCtx, cancel := context.WithTimeout(ctx, chapterProbeTimeout)
		dur, err := audioutil.ProbeDurationSeconds(probeCtx, "", f.FilePath)
		cancel()
		if err != nil {
			logChapterWarn(scanLog, "scanner.PersistChaptersForBook: ProbeDurationSeconds(%s) failed: %v", f.FilePath, err)
			// dur stays 0 -- this one track contributes no offset rather
			// than aborting chapter extraction for the whole book.
		}
		tracks[i] = audioutil.TrackInfo{
			Title:       f.Title,
			Filename:    filepath.Base(f.FilePath),
			DurationSec: dur,
		}
	}
	return audioutil.SynthesizeChapters(tracks)
}

// logChapterWarn logs a non-sampled, per-call warning for chapter
// extraction failures (ffprobe errors, store read/write errors) -- distinct
// from the sampled chapterStoreAssertErrCount wiring warning, since these
// are per-book data-path failures worth seeing individually, not a
// high-frequency wiring mismatch that needs sampling. scanLog is nil-safe.
func logChapterWarn(scanLog logger.Logger, format string, args ...interface{}) {
	if scanLog != nil {
		scanLog.Warn(format, args...)
		return
	}
	defaultLog.Warn(format, args...)
}
