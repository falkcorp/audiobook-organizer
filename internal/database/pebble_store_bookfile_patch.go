// file: internal/database/pebble_store_bookfile_patch.go
// version: 1.0.1
// guid: c4e71a93-5b28-4f0d-8e6a-2d9f7b1c3a54
// last-edited: 2026-09-13

package database

import (
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/cockroachdb/pebble/v2"
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
// was reverted. Here the read and the write are one step under
// bookFilePatchMu; the only window left is against a whole-row writer
// (UpdateBookFile/UpsertBookFile) that read before this write and commits
// after it. Closing that needs a per-book write lock shared by every book_file
// writer.
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
	s.bookFilePatchMu.Lock()
	defer s.bookFilePatchMu.Unlock()

	key := []byte(fmt.Sprintf("book_file:%s:%s", bookID, fileID))
	value, closer, err := s.db.Get(key)
	if errors.Is(err, pebble.ErrNotFound) {
		return nil, nil, nil
	}
	if err != nil {
		return nil, nil, fmt.Errorf("PatchBookFileFields: read %s: %w", fileID, err)
	}
	var row BookFile
	decErr := json.Unmarshal(value, &row)
	closer.Close()
	if decErr != nil {
		return nil, nil, fmt.Errorf("PatchBookFileFields: decode %s: %w", fileID, decErr)
	}
	orig := row

	if patch.IfTrackNumber != nil && row.TrackNumber != *patch.IfTrackNumber {
		return &orig, &orig, fmt.Errorf("book file %s track_number is %d, expected %d: %w",
			fileID, row.TrackNumber, *patch.IfTrackNumber, ErrBookFileChangedSince)
	}
	if patch.IfDiscNumber != nil && row.DiscNumber != *patch.IfDiscNumber {
		return &orig, &orig, fmt.Errorf("book file %s disc_number is %d, expected %d: %w",
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
	if !changed {
		return &orig, &orig, nil
	}

	row.UpdatedAt = time.Now()
	data, err := marshalBookFileDropSegs(&row)
	if err != nil {
		return nil, nil, fmt.Errorf("PatchBookFileFields: encode %s: %w", fileID, err)
	}
	if err := s.db.Set(key, data, pebble.Sync); err != nil {
		return nil, nil, fmt.Errorf("PatchBookFileFields: write %s: %w", fileID, err)
	}
	s.UpsertBookFileToMemDB(&row)
	// Same post-commit notification as UpdateBookFile, so every listener that
	// saw the whole-row write sees this one.
	s.notifyBookFileChange(bookID)
	return &orig, &row, nil
}
