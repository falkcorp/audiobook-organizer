// file: internal/batch/series_deleted_test.go
// version: 1.0.1
// guid: 5b7c2e91-3d64-4f08-a1e6-92c4d80f7a13
// last-edited: 2026-11-19

package batch

import (
	"strings"
	"testing"

	database "github.com/falkcorp/audiobook-organizer/internal/books/store"
)

// A bulk edit that sets series_id to a series that was deleted is refused for
// that row only: the row's result is a failure naming the series (the endpoint
// answers 200 with per-row results, never a 500), and the book keeps its series.
func TestUpdateAudiobooks_ASeriesIDOfADeletedSeriesFailsThatRowAndNamesIt(t *testing.T) {
	store := newServingPebbleStore(t)
	kept, err := store.CreateSeries("Batman Eternal", nil)
	if err != nil {
		t.Fatal(err)
	}
	gone, err := store.CreateSeries("Batman Cataclysm", nil)
	if err != nil {
		t.Fatal(err)
	}
	if ok, derr := store.DeleteSeriesIfUnreferenced(gone.ID, gone.Name, gone.AuthorID); derr != nil || !ok {
		t.Fatalf("delete: ok=%v err=%v", ok, derr)
	}
	book, err := store.CreateBook(&database.Book{Title: "Batman One", FilePath: "/w/b-1", SeriesID: &kept.ID})
	if err != nil {
		t.Fatal(err)
	}

	resp := NewBatchService(store).UpdateAudiobooks(&BatchUpdateRequest{
		IDs: []string{book.ID}, Updates: map[string]any{"series_id": float64(gone.ID)},
	})

	if resp.Failed != 1 || resp.Success != 0 || len(resp.Results) != 1 {
		t.Fatalf("resp = %+v", resp)
	}
	if msg := resp.Results[0].Error; !strings.Contains(msg, "does not exist") || !strings.Contains(msg, "series") {
		t.Fatalf("the row's error must say the series does not exist: %q", msg)
	}
	row, err := store.GetBookByID(book.ID)
	if err != nil || row.SeriesID == nil || *row.SeriesID != kept.ID {
		t.Fatalf("a refused row keeps its series: %+v err=%v", row, err)
	}
}
