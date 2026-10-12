// file: internal/deluge/mark_in_place_test.go
// version: 1.0.0
// guid: 40ec4d5f-fb5b-43c9-a2b5-718d5c2ebb77
// last-edited: 2026-10-11

package deluge

import (
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// modifyOnlyStore runs ModifyBookFile against one in-memory row the way the
// Pebble store does: (nil, nil) when absent, nothing written on
// ErrSkipBookFileWrite.
type modifyOnlyStore struct {
	row   *database.BookFile
	err   error // returned after a write is applied
	wrote bool
}

func (s *modifyOnlyStore) ModifyBookFile(bookID, fileID string, fn func(*database.BookFile) error) (*database.BookFile, error) {
	if s.row == nil || s.row.ID != fileID || s.row.BookID != bookID {
		return nil, nil
	}
	cp := *s.row
	if err := fn(&cp); err != nil {
		if errors.Is(err, database.ErrSkipBookFileWrite) {
			return s.row, nil
		}
		return nil, err
	}
	s.row, s.wrote = &cp, true
	return &cp, s.err
}

func TestMarkImportedInPlace(t *testing.T) {
	const path = "/library/Example Author/Example Book/01.mp3"
	now := time.Date(2026, 10, 11, 12, 0, 0, 0, time.UTC)
	base := database.BookFile{ID: "bf-1", BookID: "book-1", FilePath: path, DelugeHash: "abc"}

	t.Run("marks only ImportedFromDelugeAt", func(t *testing.T) {
		row := base
		s := &modifyOnlyStore{row: &row}
		got, err := MarkImportedInPlace(s, "book-1", "bf-1", path, now)
		if err != nil || got != InPlaceMarked {
			t.Fatalf("got %v, %v; want InPlaceMarked", got, err)
		}
		want := base
		want.ImportedFromDelugeAt = &now
		if !reflect.DeepEqual(*s.row, want) {
			t.Errorf("row differs in more than ImportedFromDelugeAt:\n got %+v\nwant %+v", *s.row, want)
		}
	})
	t.Run("already marked", func(t *testing.T) {
		row := base
		earlier := now.Add(-time.Hour)
		row.ImportedFromDelugeAt = &earlier
		s := &modifyOnlyStore{row: &row}
		if got, err := MarkImportedInPlace(s, "book-1", "bf-1", path, now); err != nil || got != InPlaceAlreadyMarked || s.wrote {
			t.Errorf("got %v, %v, wrote=%v; want InPlaceAlreadyMarked and no write", got, err, s.wrote)
		}
	})
	t.Run("moved meanwhile", func(t *testing.T) {
		row := base
		row.FilePath = path + ".moved"
		s := &modifyOnlyStore{row: &row}
		if got, err := MarkImportedInPlace(s, "book-1", "bf-1", path, now); err != nil || got != InPlaceMoved || s.wrote {
			t.Errorf("got %v, %v, wrote=%v; want InPlaceMoved and no write", got, err, s.wrote)
		}
	})
	t.Run("gone", func(t *testing.T) {
		s := &modifyOnlyStore{}
		if got, err := MarkImportedInPlace(s, "book-1", "bf-1", path, now); err != nil || got != InPlaceGone {
			t.Errorf("got %v, %v; want InPlaceGone", got, err)
		}
	})
	t.Run("durability unknown is marked", func(t *testing.T) {
		row := base
		s := &modifyOnlyStore{row: &row, err: database.ErrBookFileDurabilityUnknown}
		if got, err := MarkImportedInPlace(s, "book-1", "bf-1", path, now); err != nil || got != InPlaceMarked {
			t.Errorf("got %v, %v; want InPlaceMarked, nil", got, err)
		}
	})
	t.Run("write error", func(t *testing.T) {
		row := base
		s := &modifyOnlyStore{row: &row, err: errors.New("example write failure")}
		if _, err := MarkImportedInPlace(s, "book-1", "bf-1", path, now); err == nil {
			t.Error("want the write error")
		}
	})
}
