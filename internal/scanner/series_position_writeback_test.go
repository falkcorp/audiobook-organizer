// file: internal/scanner/series_position_writeback_test.go
// version: 1.0.0
// guid: 7b3e0d19-5a42-4c88-b6f0-2e91c7d4a86f
// last-edited: 2026-09-02

package scanner

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// scanOneBook runs the real scanner save path against a real PebbleStore and
// returns the stored row plus its series name. A non-empty fixture is the point:
// an assertion about what was STORED cannot be made against a mock that never
// writes anything.
func scanOneBook(t *testing.T, book *Book) (*database.Book, string) {
	t.Helper()

	store, cleanup := setupPebbleStore(t)
	t.Cleanup(cleanup)

	prevStore := database.GetGlobalStore()
	database.SetGlobalStore(store)
	SetStore(store)
	t.Cleanup(func() {
		database.SetGlobalStore(prevStore)
		SetStore(nil)
	})

	prevConfig := config.AppConfig
	t.Cleanup(func() { config.AppConfig = prevConfig })

	rootDir := t.TempDir()
	config.AppConfig.RootDir = rootDir

	fp := filepath.Join(rootDir, "book.m4b")
	if err := os.WriteFile(fp, []byte("test"), 0o644); err != nil {
		t.Fatalf("write temp file: %v", err)
	}
	book.FilePath = fp
	book.Format = ".m4b"

	if err := saveBookToDatabase(context.Background(), book); err != nil {
		t.Fatalf("saveBookToDatabase: %v", err)
	}

	saved, err := store.GetBookByFilePath(fp)
	if err != nil || saved == nil {
		t.Fatalf("expected saved book, err=%v", err)
	}
	seriesName := ""
	if saved.SeriesID != nil {
		all, sErr := store.GetAllSeries()
		if sErr != nil {
			t.Fatalf("GetAllSeries: %v", sErr)
		}
		for _, s := range all {
			if s.ID == *saved.SeriesID {
				seriesName = s.Name
			}
		}
	}
	return saved, seriesName
}

// The scanner used to drop the position on the floor. resolveSeriesID's own
// comment said "the scanner does not set SeriesSequence" -- true of that
// function, false of this caller, which has always set it from the file tags.
func TestScanner_StoresSeriesNameWithoutNumberAndRecordsPosition(t *testing.T) {
	tests := []struct {
		name       string
		series     string
		wantSeries string
		wantSeq    int
	}{
		{"hash suffix", "Nameless Sovereign #5", "Nameless Sovereign", 5},
		{"comma Book N", "Adeptus Mechanicus, Book 1", "Adeptus Mechanicus", 1},
		{"bare trailing number", "Discworld 05", "Discworld", 5},
		{"bracketed", "Dragon Born [04]", "Dragon Born", 4},
		{"embedded keyword", "Vampire Hunter D: Vol 09: The Rose Princess", "Vampire Hunter D", 9},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			saved, seriesName := scanOneBook(t, &Book{
				Title:  "A Book",
				Author: "An Author",
				Series: tt.series,
				// No tag position: the number in the name is the only source.
			})
			if seriesName != tt.wantSeries {
				t.Errorf("stored series name: got %q, want %q", seriesName, tt.wantSeries)
			}
			if saved.SeriesSequence == nil {
				t.Fatalf("series_sequence was not recorded; the number stripped from %q was DELETED", tt.series)
			}
			if *saved.SeriesSequence != tt.wantSeq {
				t.Errorf("series_sequence: got %d, want %d", *saved.SeriesSequence, tt.wantSeq)
			}
		})
	}
}

// A position from the file's own tags outranks one recovered from the name.
func TestScanner_DoesNotOverwriteExistingPosition(t *testing.T) {
	saved, seriesName := scanOneBook(t, &Book{
		Title:    "A Book",
		Author:   "An Author",
		Series:   "Discworld 05",
		Position: 3, // from the file tags
	})
	if seriesName != "Discworld" {
		t.Errorf("stored series name: got %q, want %q", seriesName, "Discworld")
	}
	if saved.SeriesSequence == nil || *saved.SeriesSequence != 3 {
		got := "nil"
		if saved.SeriesSequence != nil {
			got = string(rune('0' + *saved.SeriesSequence))
		}
		t.Errorf("series_sequence: got %s, want 3 -- an existing sequence must never be overwritten", got)
	}
}

// A legitimately un-numbered series is stored verbatim with no sequence invented
// for it, and an un-vouched leading number is left alone rather than mangled.
func TestScanner_LeavesCleanAndUnvouchedNamesAlone(t *testing.T) {
	for _, series := range []string{"The Expanse", "86—EIGHTY-SIX", "08. Battle for the Abyss"} {
		t.Run(series, func(t *testing.T) {
			saved, seriesName := scanOneBook(t, &Book{
				Title: "A Book", Author: "An Author", Series: series,
			})
			if seriesName != series {
				t.Errorf("stored series name: got %q, want %q unchanged", seriesName, series)
			}
			if saved.SeriesSequence != nil {
				t.Errorf("series_sequence: got %d, want none invented", *saved.SeriesSequence)
			}
		})
	}
}
