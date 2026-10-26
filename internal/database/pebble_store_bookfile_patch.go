// file: internal/database/pebble_store_bookfile_patch.go
// version: 1.5.0
// guid: c4e71a93-5b28-4f0d-8e6a-2d9f7b1c3a54
// last-edited: 2026-10-26

package database

import (
	"errors"
	"fmt"
	"time"
)

// ErrBookFileChangedSince is returned by PatchBookFileFields when a
// precondition (IfTrackNumber / IfDiscNumber) does not match the stored row.
var ErrBookFileChangedSince = errors.New("book file changed since it was read")

// BookFileFieldPatch names the book_file fields PatchBookFileFields may set.
// A nil field is left as stored.
type BookFileFieldPatch struct {
	TrackNumber  *int
	DiscNumber   *int
	SkipScan     *bool
	DownloadHash *string

	// Audio properties, refreshed after a transcode re-encodes the file.
	Codec        *string
	BitrateKbps  *int
	SampleRateHz *int

	// Per-file display title and the book's track count, written by the
	// metafetch segment-titles pass. Neither is secondary-indexed.
	Title      *string
	TrackCount *int

	// Preconditions: when set, the stored value must equal it, or nothing is
	// written and ErrBookFileChangedSince is returned (compare-and-set).
	IfTrackNumber *int
	IfDiscNumber  *int
}

// PatchBookFileFields re-reads book_file:<bookID>:<fileID> and sets only the
// fields named in patch, leaving every other column as stored.
//
// It exists because the whole-row write it replaces (read the row, change
// the track, UpsertBookFile the lot) put back whatever the row held at read
// time, so a concurrent writer's field, such as enrich-book-files' Duration,
// was reverted. Here the read and the write are one step under the row's
// lockBookFile stripe, which UpdateBookFile and UpdateBookFileHashes also hold
// across their own read and commit. The stripe does not order this against
// the writers that take none (a create claiming an iTunes ID, the bulk moves,
// the fingerprint sweeps, the scan-cache stamp), so the commit is a compare-and-swap on the bytes
// read (setBookFileRowIfUnchanged): a row that changed meanwhile is re-read and
// the patch (and its If* preconditions) re-applied to it, up to
// maxStageRetries times, then ErrBookFileConflict. The window left is a caller
// that read the row itself, earlier, and hands the whole stale row to
// UpdateBookFile or an upsert: the stripe orders the two writes but cannot make
// the stale one fresh.
//
// before is the row as read, after as written (equal when nothing changed, in
// which case nothing is written). Both are nil when the row does not exist.
//
// Differences from UpdateBookFile, on purpose:
//   - No secondary-index rewrite: writeBookFileSecondaryIndexes indexes ID,
//     ITunesPersistentID, FilePath, FileHash and OriginalFileHash, none of
//     which this method can change.
//   - No preserve-on-nil guards (AcoustIDFingerprint, IntroTranscription):
//     those protect against a caller handing in a memdb-stripped struct.
//     Here the struct is the stored row itself, so every column is already
//     the stored value. marshalBookFileDropSegs is the same encoder, so the
//     AcoustID segment drop matches.
func (s *PebbleStore) PatchBookFileFields(bookID, fileID string, patch BookFileFieldPatch) (before, after *BookFile, err error) {
	unlock := s.lockBookFile(fileID)
	notify := false
	defer func() {
		unlock()
		// After the unlock, as in updateBookFile: the recompute takes the
		// book's stripe, and no file stripe is held while one is taken.
		if notify {
			s.notifyBookFileChange(bookID)
		}
	}()

	book := bookID
	got, err := rmwBookFile(&book, fileID, func() (bookFilePatched, error) {
		return s.patchBookFileFieldsOnce(bookID, fileID, patch)
	})
	notify = got.notify
	if errors.Is(err, ErrBookFileConflict) {
		err = fmt.Errorf("PatchBookFileFields: write %s: %w", fileID, err)
	}
	return got.before, got.after, err
}

// bookFilePatched is one pass of PatchBookFileFields' result.
type bookFilePatched struct {
	before, after *BookFile
	notify        bool
}

// patchBookFileFieldsOnce is one read-apply-commit pass of PatchBookFileFields.
// It returns an error wrapping errBookFileRowVanished when the commit found the
// row changed since the read (nothing was written; the caller re-reads).
func (s *PebbleStore) patchBookFileFieldsOnce(bookID, fileID string, patch BookFileFieldPatch) (res bookFilePatched, err error) {
	key := bookFileKey(bookID, fileID)
	row0, raw, err := s.readBookFileRowAt(key)
	if err != nil {
		return res, fmt.Errorf("PatchBookFileFields: read %s: %w", fileID, err)
	}
	if row0 == nil {
		return res, nil
	}
	row := *row0
	orig := row

	if patch.IfTrackNumber != nil && row.TrackNumber != *patch.IfTrackNumber {
		res.before, res.after = &orig, &orig
		return res, fmt.Errorf("book file %s track_number is %d, expected %d: %w",
			fileID, row.TrackNumber, *patch.IfTrackNumber, ErrBookFileChangedSince)
	}
	if patch.IfDiscNumber != nil && row.DiscNumber != *patch.IfDiscNumber {
		res.before, res.after = &orig, &orig
		return res, fmt.Errorf("book file %s disc_number is %d, expected %d: %w",
			fileID, row.DiscNumber, *patch.IfDiscNumber, ErrBookFileChangedSince)
	}

	changed := false
	if patch.TrackNumber != nil && row.TrackNumber != *patch.TrackNumber {
		row.TrackNumber, changed = *patch.TrackNumber, true
	}
	if patch.DiscNumber != nil && row.DiscNumber != *patch.DiscNumber {
		row.DiscNumber, changed = *patch.DiscNumber, true
	}
	if patch.SkipScan != nil && row.SkipScan != *patch.SkipScan {
		row.SkipScan, changed = *patch.SkipScan, true
	}
	if patch.DownloadHash != nil && row.DownloadHash != *patch.DownloadHash {
		row.DownloadHash, changed = *patch.DownloadHash, true
	}
	if patch.Codec != nil && row.Codec != *patch.Codec {
		row.Codec, changed = *patch.Codec, true
	}
	if patch.BitrateKbps != nil && row.BitrateKbps != *patch.BitrateKbps {
		row.BitrateKbps, changed = *patch.BitrateKbps, true
	}
	if patch.SampleRateHz != nil && row.SampleRateHz != *patch.SampleRateHz {
		row.SampleRateHz, changed = *patch.SampleRateHz, true
	}
	if patch.Title != nil && row.Title != *patch.Title {
		row.Title, changed = *patch.Title, true
	}
	if patch.TrackCount != nil && row.TrackCount != *patch.TrackCount {
		row.TrackCount, changed = *patch.TrackCount, true
	}
	if !changed {
		res.before, res.after = &orig, &orig
		return res, nil
	}

	row.UpdatedAt = time.Now()
	data, err := marshalBookFileDropSegs(&row)
	if err != nil {
		return res, fmt.Errorf("PatchBookFileFields: encode %s: %w", fileID, err)
	}
	// Under the book's owner stripe and only while the row is still committed
	// AND still holds the bytes read (book_delete_owns_files.go).
	wrote, err := s.setBookFileRowIfUnchanged(bookID, key, raw, data)
	if !bookFileApplied(err) {
		return res, fmt.Errorf("PatchBookFileFields: write %s: %w", fileID, err)
	}
	switch wrote {
	case bookFileRowGone:
		return res, fmt.Errorf("PatchBookFileFields: %s was deleted while being patched", fileID)
	case bookFileRowChanged:
		return res, errBookFileRowVanished
	}
	s.UpsertBookFileToMemDB(&row)
	// Same post-commit notification as UpdateBookFile, so every listener that
	// saw the whole-row write sees this one (sent by the caller's deferred
	// unlock).
	res.before, res.after, res.notify = &orig, &row, true
	if err != nil {
		// Applied but not known durable: memdb already mirrors it; say so.
		return res, fmt.Errorf("PatchBookFileFields: %s: %w", fileID, err)
	}
	return res, nil
}
