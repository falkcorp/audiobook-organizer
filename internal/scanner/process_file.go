// file: internal/scanner/process_file.go
// version: 1.9.0
// guid: a1b2c3d4-e5f6-7890-abcd-ef1234567890
// last-edited: 2026-09-01

// Package scanner provides file scanning and processing utilities for the
// audiobook organizer. ProcessFile is the single-pass entry point that opens
// a file exactly once and extracts metadata, media info, and a content hash.
package scanner

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sync/atomic"
	"time"

	"github.com/dhowden/tag"
	"github.com/falkcorp/audiobook-organizer/internal/audioutil"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/filehash"
	"github.com/falkcorp/audiobook-organizer/internal/logger"
	"github.com/falkcorp/audiobook-organizer/internal/mediainfo"
	"github.com/falkcorp/audiobook-organizer/internal/metadata"
)

const (
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
// processFileTimeout is the per-file wall-clock cap for ProcessFile.
//
// ProcessFile's chain (os.Stat -> os.Open -> tag.ReadFrom -> SHA-256 read) is
// entirely blocking syscalls and third-party parsing that do NOT respect Go
// context cancellation. tag.ReadFrom in particular walks an MP4 atom tree whose
// lengths come from the file itself, so a malformed or truncated container can
// make it spin or attempt an enormous read with no way to interrupt it.
//
// This is not hypothetical. Prod library.scan stalled at the SAME item across 9
// runs over 3 days (2026-08-21..23): the numerator stuck at 14912 while the
// denominator drifted 40109 -> 40089, ending each run with "abandoned: op
// goroutine did not exit within grace after context cancellation". A fixed
// numerator with a moving denominator is a deterministic hang on one specific
// input, not a race, and "did not exit after cancellation" is what an
// uncancellable syscall looks like from the outside.
//
// The bound is deliberately generous rather than tight. It exists to convert an
// INFINITE hang into a normal per-file failure, not to police slow files: the
// legitimate worst case is a full SHA-256 of a 100 MB file plus a tag read over
// a network filesystem, which is tens of seconds on a bad link. 120s leaves
// several times that headroom, so a timeout here means genuinely stuck.
//
// The goroutine is intentionally leaked on timeout -- it will unblock whenever
// the kernel or the parser recovers. This mirrors extractWithTimeout in
// internal/plugins/maintenance/duration_reextract.go, which documented this
// exact hazard for mediainfo.Extract and then guarded only its own call site.
const processFileTimeout = 120 * time.Second

// ProcessFileWithTimeout runs ProcessFile under processFileTimeout and honours
// ctx, so a scan can be cancelled between files and cannot be stalled forever by
// one of them.
//
// Callers on a scan path must use this rather than ProcessFile directly. A
// timeout is returned as an ordinary error, which the scanner already handles:
// it falls back to filename-derived metadata and increments the per-file scan
// fail counter that feeds auto-quarantine.
func ProcessFileWithTimeout(ctx context.Context, filePath string) (*metadata.Metadata, *mediainfo.MediaInfo, string, error) {
	return processFileBounded(ctx, filePath, processFileTimeout, ProcessFile)
}

// processFileBounded is the testable core of ProcessFileWithTimeout: the work
// function and the timeout are parameters so the timeout and cancellation arms
// can be exercised without a file that actually hangs. Nothing that hangs on
// demand exists on disk, and a test that cannot reach the timeout arm is not
// testing the fix.
func processFileBounded(
	ctx context.Context,
	filePath string,
	timeout time.Duration,
	work func(string) (*metadata.Metadata, *mediainfo.MediaInfo, string, error),
) (*metadata.Metadata, *mediainfo.MediaInfo, string, error) {
	type result struct {
		meta *metadata.Metadata
		mi   *mediainfo.MediaInfo
		hash string
		err  error
	}
	// Buffered so the goroutine can always send and exit even after we have
	// given up on it. An unbuffered channel here would turn every timeout into
	// a PERMANENT goroutine leak rather than a temporary one.
	ch := make(chan result, 1)
	go func() {
		meta, mi, hash, err := work(filePath)
		ch <- result{meta, mi, hash, err}
	}()

	timer := time.NewTimer(timeout)
	defer timer.Stop()

	select {
	case r := <-ch:
		return r.meta, r.mi, r.hash, r.err
	case <-timer.C:
		return nil, nil, "", fmt.Errorf("ProcessFile: timed out after %v on %q "+
			"(uncancellable read; the file is likely malformed or the filesystem is stuck)",
			timeout, filePath)
	case <-ctx.Done():
		return nil, nil, "", fmt.Errorf("ProcessFile: cancelled on %q: %w", filePath, ctx.Err())
	}
}

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

	// Size the OPEN DESCRIPTOR, not the path stat'd above. The two differ
	// whenever the file is replaced between the stat and the open — which is a
	// live condition here, not a theoretical one: fileops.WriteTagsSafe finishes
	// with an atomic os.Rename over this path, and internal/organizer renames
	// files under RootDir. Using fi.Size() there pairs the NEW file's bytes with
	// the OLD file's size, and since the size is folded into the identity digest
	// the result is a well-formed hash that describes no file that ever existed.
	dfi, err := f.Stat()
	if err != nil {
		return nil, nil, "", fmt.Errorf("ProcessFile: stat open handle for %q: %w", filePath, err)
	}
	fileSize := dfi.Size()

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

	// No seek needed: BookFileHashFromFile positions the handle itself. Kept as
	// a comment rather than a call because an explicit Seek here would read as
	// the thing that makes the hash correct, and it is not.
	hash, err := computeHashFromReader(f, fileSize)
	if err != nil {
		return &meta, mi, "", fmt.Errorf("ProcessFile: hash %q: %w", filePath, err)
	}

	return &meta, mi, hash, nil
}

// computeHashFromReader hashes content from an open file reader, producing the
// canonical book_files.file_hash identity digest for a file of fileSize bytes.
//
// This used to be a third hand-written copy of the algorithm, kept in step with
// scanner.ComputeFileHash by a comment and one cross-checking test. It now
// delegates to internal/filehash so there is exactly one implementation: a
// duplicated algorithm makes any one-sided fix inert.
func computeHashFromReader(f *os.File, fileSize int64) (string, error) {
	return filehash.BookFileHashFromFile(f, fileSize)
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

	// AsPebbleStore, not a bare assertion. Traced 2026-08-19: this store is
	// the BARE one -- server.NewServer calls scanner.SetStore(resolvedStore)
	// before Start installs the Bleve indexedStore decorator, and nothing sets
	// it again afterwards -- so the bare form was NOT failing here today. This
	// is hardening, not a bug fix: the failure mode if that wiring ever changes
	// is silent (a sampled warning that reads like an unsupported backend
	// rather than a bug), and the cost of being right by construction is one
	// function call.
	ps := resolveChapterStore(store)
	if ps == nil {
		warnSampled(&chapterStoreAssertErrCount, scanLog,
			"scanner.PersistChaptersForBook: store %T does not carry the chapter capability -- chapter extraction skipped for %s",
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

// chapterStore is the read/write pair chapter persistence needs.
//
// Neither method is on database.Store (compile-probed 2026-08-19), so a bare
// assertion fails through the Bleve indexedStore decorator. Named rather than
// resolved with database.AsPebbleStore so this package does not depend on the
// concrete type by name -- see
// docs/plans/2026-08-19-split-the-pebblestore-surface.md.
type chapterStore interface {
	GetChaptersForBook(bookID string) ([]database.Chapter, error)
	SaveChaptersForBook(bookID string, chapters []database.Chapter) error
}

// resolveChapterStore walks the decorator chain, returning nil on a backend
// that cannot persist chapters so the caller logs a sampled warning and skips.
func resolveChapterStore(s any) chapterStore {
	if c, ok := database.AsCapability[chapterStore](s); ok {
		return c
	}
	return nil
}
