// file: internal/fileops/write_tags_safe.go
// version: 1.8.0
// guid: b4c5d6e7-f8a9-0b1c-2d3e-4f5a6b7c8d9e
// last-edited: 2026-09-13

package fileops

import (
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/filehash"
	"github.com/falkcorp/audiobook-organizer/internal/logger"
)

// WriteTagsSafeOptions configures WriteTagsSafe behavior.
type WriteTagsSafeOptions struct {
	// BookFileID is the database row ID for hash tracking. Empty = skip DB update.
	BookFileID string
	// Store receives the pre- and post-write hashes. Nil = skip DB update.
	Store database.BookFileHashUpdater
	// Provenance receives an append-only record of this write. Nil = skip.
	//
	// Store overwrites two hash columns and so keeps only the most recent
	// pair; Provenance keeps every pair ever recorded. They are separate
	// fields because the columns are what existing queries read, and the
	// ledger is what makes a hash from before this write still resolvable
	// after it.
	Provenance database.FileProvenanceRecorder
	// Actor names what is performing the write (an op, plugin, or user). It is
	// recorded on the provenance events and is otherwise unused.
	Actor string
	// Detail is a human-readable note about what this write changes, e.g.
	// `author: "" -> "Brandon Sanderson"`. Recorded on the tags_written event.
	Detail string
	// TorrentHash is the Deluge infohash of the release the file came from,
	// normally BookFile.DelugeHash. It identifies the source rather than the
	// bytes, so unlike either SHA it is unchanged by this write — which makes
	// it the most durable link back to a pristine original.
	TorrentHash string
}

// WriteTagsSafe writes audio metadata tags to path safely:
//  1. Copies the file to a sibling temp file in the same directory
//  2. Calls writeFn(tmpPath) to perform the actual tag write on the copy
//  3. On success: atomically renames the temp file over the original
//
// When BOTH opts.BookFileID and opts.Store are set it additionally computes
// original_file_hash before the write and post_metadata_hash after it, and
// persists the pair. Those two SHA-256 passes each stream the whole audio file,
// so they are skipped entirely when there is no row to persist them against.
//
// Returns (originalHash, postHash, error); the hashes are "" when not computed.
// On writeFn failure the original file is left untouched and the temp file is
// removed.
//
// writeFn receives a temp copy, so a writer that itself wraps its work in
// WriteTagsSafe would double the copy-and-hash cost. Writers meant to be called
// from here have in-place variants — see metadata.WriteMetadataToFileInPlace and
// tagger.WriteTagsInPlace.
func WriteTagsSafe(path string, writeFn func(tmpPath string) error, opts WriteTagsSafeOptions) (originalHash, postHash string, err error) {
	// The hashes exist solely to be persisted against a book_file row. When no
	// row is supplied there is nothing to persist and every caller discards the
	// return values, so computing them is pure waste — and it is expensive waste:
	// ComputeFileHashAndSize streams the ENTIRE audio file through SHA-256 with no size
	// cap and no mtime/size shortcut, twice per call. On NAS-backed audiobooks
	// that dominated the cost of a tag write.
	// Provenance needs the same two digests the columns do, so either
	// destination is reason enough to compute them. Without this the ledger
	// would silently record empty digests whenever Store was nil.
	wantHashes := (opts.BookFileID != "" && opts.Store != nil) || opts.Provenance != nil
	var originalSize int64

	// Step 1: fingerprint the original file before any modification.
	if wantHashes {
		originalHash, originalSize, err = ComputeFileHashAndSize(path)
		if err != nil {
			return "", "", fmt.Errorf("WriteTagsSafe: hash original %s: %w", path, err)
		}
	}
	// Step 1a: the identity digest of the pre-write bytes, for the row's
	// original_file_hash. That column must hold the SAME kind of digest as
	// file_hash (filehash.BookFileHash, sampled), never originalHash above (a
	// whole-file SHA-256): the two differ for any large file, and a mixed pair
	// reads as "changed outside the app" in the integrity check. Cheap: it reads
	// two fixed-size windows, not the whole file.
	var preIdentityHash string
	if opts.BookFileID != "" && opts.Store != nil {
		if h, herr := filehash.BookFileHash(path); herr == nil {
			preIdentityHash = h
		} else {
			logger.New("fileops").Warn("WriteTagsSafe: pre-write identity hash not computed; original_file_hash left as is: book_file_id=%s path=%s error=%v",
				logger.SanitizeLogValue(opts.BookFileID), logger.SanitizeLogValue(path), herr)
		}
	}

	// Step 1b: record the pre-write state BEFORE touching anything. If the
	// process dies during the write, this row is what survives — recording it
	// afterwards would lose precisely the case the ledger exists for.
	recordEvent(opts, database.FileEventObserved, path, originalHash, originalSize, "", "pre-write")

	// Step 2: create temp file in the same directory so os.Rename is atomic
	// (same filesystem mount). Use the same extension so taglib can detect
	// the container format correctly.
	dir := filepath.Dir(path)
	ext := filepath.Ext(path)
	tmpFile, err := os.CreateTemp(dir, ".writetmp-*"+ext)
	if err != nil {
		return originalHash, "", fmt.Errorf("WriteTagsSafe: create temp: %w", err)
	}
	tmpPath := tmpFile.Name()
	tmpFile.Close()

	// Always remove the temp file on failure.
	defer func() {
		if err != nil {
			_ = os.Remove(tmpPath)
		}
	}()

	// Step 3: copy original → temp (preserve permissions).
	if err = CopyFileInto(path, tmpPath); err != nil {
		return originalHash, "", fmt.Errorf("WriteTagsSafe: copy to temp: %w", err)
	}

	// Step 4: let the caller write tags into the temp copy.
	if err = writeFn(tmpPath); err != nil {
		return originalHash, "", fmt.Errorf("WriteTagsSafe: writeFn: %w", err)
	}

	// Step 5: atomic rename — old file replaced only on success.
	if err = os.Rename(tmpPath, path); err != nil {
		return originalHash, "", fmt.Errorf("WriteTagsSafe: rename: %w", err)
	}

	// Step 6: fingerprint the result (only when it will be persisted).
	if wantHashes {
		// Assign to the named return, not a new local: `:=` here would shadow
		// postHash and the function would return an empty hash on success.
		var postSize int64
		postHash, postSize, err = ComputeFileHashAndSize(path)
		if err != nil {
			return originalHash, "", fmt.Errorf("WriteTagsSafe: hash result %s: %w", path, err)
		}

		// Step 7: record the completed write in the append-only ledger.
		recordEvent(opts, database.FileEventTagsWritten, path, postHash, postSize, opts.Detail, "")

		// Step 8: update the two hash columns. The bytes are already on disk, so
		// a failure here cannot fail the write — returning an error would invite
		// the caller to retry and write the file twice. It must not be silent
		// either: this used to be `_ =`, which is how the columns could drift
		// from the files without anyone noticing.
		//
		// FileHash is updated too, with the canonical identity digest the
		// scanner computes (filehash.BookFileHash, head+tail+size), NOT postHash
		// (a whole-file SHA-256). Leaving FileHash on the old bytes made the next
		// rescan see a different hash for the same audio and treat the file as
		// replaced. A hashing failure leaves FileHash as it was: the merge then
		// keeps the audio-derived fields and asks for a re-read.
		if opts.BookFileID != "" && opts.Store != nil {
			identityHash, herr := filehash.BookFileHash(path)
			if herr != nil {
				identityHash = ""
				logger.New("fileops").Warn("WriteTagsSafe: identity hash not computed; file_hash left unchanged: book_file_id=%s path=%s error=%v",
					logger.SanitizeLogValue(opts.BookFileID), logger.SanitizeLogValue(path), herr)
			}
			if uerr := opts.Store.UpdateBookFileHashes(opts.BookFileID, preIdentityHash, postHash, identityHash); uerr != nil {
				logger.New("fileops").Warn("WriteTagsSafe: hash columns not updated; file was written and the ledger holds the record: book_file_id=%s path=%s error=%v",
					logger.SanitizeLogValue(opts.BookFileID), logger.SanitizeLogValue(path), uerr)
			}
		}
	}

	return originalHash, postHash, nil
}

// recordEvent appends one provenance event, if a recorder is configured.
//
// Provenance is observational: a failure to record must never fail or alter the
// file operation being observed. It is logged rather than returned for that
// reason — but it is logged, because a ledger with silent gaps is worse than no
// ledger, since it reads as authoritative.
func recordEvent(opts WriteTagsSafeOptions, kind database.FileEventKind, path, sha string, size int64, detail, note string) {
	if opts.Provenance == nil {
		return
	}
	if sha == "" && opts.BookFileID == "" {
		// Would be rejected as an unadoptable orphan; skip rather than log noise.
		return
	}
	// The size is passed in rather than looked up here: it comes off the same
	// open handle the hash does, so the two provably describe the same bytes.
	digest := database.FileDigest{
		SHA256Full:  sha,
		SizeBytes:   size,
		TorrentHash: opts.TorrentHash,
	}
	if note != "" && detail == "" {
		detail = note
	}
	ev := database.FileEvent{
		BookFileID: opts.BookFileID,
		Path:       path,
		Kind:       kind,
		At:         time.Now(),
		Digest:     digest,
		Detail:     detail,
		Actor:      opts.Actor,
	}
	if err := opts.Provenance.AppendFileEvent(ev); err != nil {
		slog.Warn("WriteTagsSafe: provenance event not recorded",
			"kind", kind, "path", logger.SanitizeLogValue(path), "book_file_id", opts.BookFileID, "error", err)
	}
}
