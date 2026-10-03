// file: internal/server/handlers/audiobooks/handler_series_clear_test.go
// version: 1.0.0
// guid: 7f040721-48bc-4639-919d-3da56ceab6e3
// last-edited: 2026-10-03

package audiobookshandler_test

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/gin-gonic/gin"

	audiobookspkg "github.com/falkcorp/audiobook-organizer/internal/audiobooks"
	"github.com/falkcorp/audiobook-organizer/internal/cache"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/plugin"
	audiobookshandler "github.com/falkcorp/audiobook-organizer/internal/server/handlers/audiobooks"
	audiobooksmocks "github.com/falkcorp/audiobook-organizer/internal/server/handlers/audiobooks/mocks"
)

// PUT /audiobooks/:id {"series_name": ""} through the real update service and
// a real store: the response, and the row a later GET reads, carry no series.
// Observed on prod 2026-10-03 (a standalone novel kept its series after a 200).
func TestUpdateAudiobook_EmptySeriesNameResponseHasNoSeries(t *testing.T) {
	store, err := database.NewPebbleStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	series, err := store.CreateSeries("Redshirts", nil)
	if err != nil {
		t.Fatal(err)
	}
	seq := 1
	book, err := store.CreateBook(&database.Book{Title: "Redshirts", FilePath: "/protected/redshirts.m4b",
		SeriesID: &series.ID, Series: series, SeriesSequence: &seq})
	if err != nil {
		t.Fatal(err)
	}

	svc := audiobooksmocks.NewMockAudiobookService(t)
	svc.EXPECT().InvalidateBookCaches().Return()
	h := audiobookshandler.New(
		store, svc, audiobookspkg.NewAudiobookUpdateService(store),
		func() audiobookshandler.WriteBackEnqueuer { return nil },
		nil, nil, nil, nil,
		cache.New[gin.H]("l", 0), cache.New[gin.H]("f", 0),
		cache.New[*audiobookspkg.AuthorWithCountListResponse]("a", 0),
		cache.New[*audiobookspkg.SeriesWithCountsResponse]("s", 0),
		nil, nil,
		func(string) bool { return true }, // protected path: no file write-back
		func(b *database.Book) any { return b },
		nil, nil,
		func(context.Context, plugin.Event) {},
	)

	c, w := newCtx("PUT", "/audiobooks/"+book.ID, map[string]any{"series_name": ""}, p("id", book.ID))
	h.UpdateAudiobook(c)
	if w.Code != http.StatusOK {
		t.Fatalf("want 200, got %d (%s)", w.Code, w.Body.String())
	}
	var resp map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	body := resp
	if data, ok := resp["data"].(map[string]any); ok {
		body = data
	}
	for _, k := range []string{"series", "series_id", "series_sequence"} {
		if v, ok := body[k]; ok && v != nil {
			t.Errorf("response still has %s = %v", k, v)
		}
	}
	row, err := store.GetBookByID(book.ID)
	if err != nil {
		t.Fatal(err)
	}
	if row.SeriesID != nil || row.Series != nil || row.SeriesSequence != nil {
		t.Errorf("stored row kept the series: id=%v series=%+v seq=%v", row.SeriesID, row.Series, row.SeriesSequence)
	}
}
