// file: internal/deluge/mark_in_place_test.go
// version: 1.1.0
// guid: 40ec4d5f-fb5b-43c9-a2b5-718d5c2ebb77
// last-edited: 2026-10-11

package deluge

import (
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/tagger"
)

// modifyOnlyStore runs ModifyBookFile against one in-memory row the way the
// Pebble store does: (nil, nil) when absent, nothing written on
// ErrSkipBookFileWrite.
type modifyOnlyStore struct {
	row   *database.BookFile
	err   error // returned after a write is applied
	wrote bool
	calls int
}

func (s *modifyOnlyStore) ModifyBookFile(bookID, fileID string, fn func(*database.BookFile) error) (*database.BookFile, error) {
	s.calls++
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
		got, err := MarkImportedInPlace(s, nil, nil, "book-1", "bf-1", path, now)
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
		if got, err := MarkImportedInPlace(s, nil, nil, "book-1", "bf-1", path, now); err != nil || got != InPlaceAlreadyMarked || s.wrote {
			t.Errorf("got %v, %v, wrote=%v; want InPlaceAlreadyMarked and no write", got, err, s.wrote)
		}
	})
	t.Run("moved meanwhile", func(t *testing.T) {
		row := base
		row.FilePath = path + ".moved"
		s := &modifyOnlyStore{row: &row}
		if got, err := MarkImportedInPlace(s, nil, nil, "book-1", "bf-1", path, now); err != nil || got != InPlaceMoved || s.wrote {
			t.Errorf("got %v, %v, wrote=%v; want InPlaceMoved and no write", got, err, s.wrote)
		}
	})
	t.Run("gone", func(t *testing.T) {
		s := &modifyOnlyStore{}
		if got, err := MarkImportedInPlace(s, nil, nil, "book-1", "bf-1", path, now); err != nil || got != InPlaceGone {
			t.Errorf("got %v, %v; want InPlaceGone", got, err)
		}
	})
	t.Run("durability unknown is marked", func(t *testing.T) {
		row := base
		s := &modifyOnlyStore{row: &row, err: database.ErrBookFileDurabilityUnknown}
		if got, err := MarkImportedInPlace(s, nil, nil, "book-1", "bf-1", path, now); err != nil || got != InPlaceMarked {
			t.Errorf("got %v, %v; want InPlaceMarked, nil", got, err)
		}
	})
	t.Run("write error", func(t *testing.T) {
		row := base
		s := &modifyOnlyStore{row: &row, err: errors.New("example write failure")}
		if _, err := MarkImportedInPlace(s, nil, nil, "book-1", "bf-1", path, now); err == nil {
			t.Error("want the write error")
		}
	})
}

// A protected path is never marked imported in place: the row stays pending,
// the way ImportToLibraryWith's src == dest branch refuses it
// (tagger.ErrProtectedPathWrite). Every arm of InPlaceProtection is covered:
// config.ProtectedPaths, the iTunes library directory, the checker's static
// list, a Deluge save_path the checker knows, and a plain PathChecker.
func TestMarkImportedInPlace_ProtectedLeftPending(t *testing.T) {
	const dir = "/library/Example Author/Example Book"
	const path = dir + "/01.mp3"
	now := time.Date(2026, 10, 11, 12, 0, 0, 0, time.UTC)
	seeding := func() (map[string]TorrentStatus, error) {
		return map[string]TorrentStatus{"aabbcc": {SavePath: dir}}, nil
	}
	cases := []struct {
		name    string
		cfg     *config.Config
		checker tagger.PathChecker
	}{
		{name: "config protected path", cfg: &config.Config{ProtectedPaths: []string{dir}}},
		{name: "iTunes library directory", cfg: func() *config.Config {
			c := &config.Config{}
			c.ITunes.LibraryReadPath = dir + "/iTunes Library.xml"
			return c
		}()},
		{name: "checker static prefix", checker: newTestCache(func() (map[string]TorrentStatus, error) { return nil, nil }, []string{dir})},
		{name: "deluge save path", checker: newTestCache(seeding, nil)},
		{name: "plain path checker", checker: dirProtected{dir: dir}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			row := database.BookFile{ID: "bf-1", BookID: "book-1", FilePath: path, DelugeHash: "abc"}
			s := &modifyOnlyStore{row: &row}
			got, err := MarkImportedInPlace(s, tc.cfg, tc.checker, "book-1", "bf-1", path, now)
			if err != nil || got != InPlaceProtected {
				t.Fatalf("got %v, %v; want InPlaceProtected, nil", got, err)
			}
			if s.calls != 0 || s.wrote || s.row.ImportedFromDelugeAt != nil {
				t.Errorf("calls=%d wrote=%v imported_at=%v; want the row untouched", s.calls, s.wrote, s.row.ImportedFromDelugeAt)
			}
		})
	}
}

// The protection check is the one ImportToLibraryWith makes, so a path that
// neither the config nor the checker protects is still marked, and a
// typed-nil cache (an unset *ProtectedPathCache field) protects nothing
// rather than panicking.
func TestMarkImportedInPlace_UnprotectedStillMarked(t *testing.T) {
	const path = "/library/Example Author/Example Book/01.mp3"
	now := time.Date(2026, 10, 11, 12, 0, 0, 0, time.UTC)
	var unset *ProtectedPathCache
	for name, checker := range map[string]tagger.PathChecker{
		"other save path": newTestCache(func() (map[string]TorrentStatus, error) {
			return map[string]TorrentStatus{"aabbcc": {SavePath: "/downloads/example"}}, nil
		}, []string{"/music/iTunes"}),
		"typed nil cache": unset,
	} {
		t.Run(name, func(t *testing.T) {
			row := database.BookFile{ID: "bf-1", BookID: "book-1", FilePath: path}
			s := &modifyOnlyStore{row: &row}
			cfg := &config.Config{ProtectedPaths: []string{"/music/iTunes"}}
			if got, err := MarkImportedInPlace(s, cfg, checker, "book-1", "bf-1", path, now); err != nil || got != InPlaceMarked {
				t.Errorf("got %v, %v; want InPlaceMarked", got, err)
			}
		})
	}
}
