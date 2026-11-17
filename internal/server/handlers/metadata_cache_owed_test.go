// file: internal/server/handlers/metadata_cache_owed_test.go
// version: 1.0.0
// guid: 6c1d8e47-2f93-4b0a-a5e8-7d3b9f10c264
// last-edited: 2026-11-17

package handlers

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	database "github.com/falkcorp/audiobook-organizer/internal/books/store"
	"github.com/falkcorp/audiobook-organizer/internal/metafetch"
)

// A review row says which chain sources its book still owes an answer and which
// it gave up on (additive owed_pending / owed_exhausted), so the review page can
// show "a source is still owed" / "gave up on a source". Synthetic fixtures.
func TestReviewRows_ServeOwedAndExhaustedSources(t *testing.T) {
	store, err := database.NewPebbleStore(t.TempDir())
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })
	svc := metafetch.NewService(store)

	now := time.Now().UTC()
	seed := func(id string, owed map[string]database.OwedSource) {
		_, err := store.CreateBook(&database.Book{ID: id, Title: "Gotham " + id, FilePath: "/lib/" + id + "/book.m4b"})
		require.NoError(t, err)
		raw, err := json.Marshal(metafetch.MetadataCandidate{Source: "Audible", Title: "Gotham " + id, Score: 0.95})
		require.NoError(t, err)
		require.NoError(t, store.PutMetadataCache(&database.MetadataCandidateCache{
			BookID: id, FetchedAt: now, SourceHash: "h-" + id,
			SearchFingerprint: metafetch.FingerprintPrefix + "q-" + id,
			Candidates:        []json.RawMessage{raw}, OwedSources: owed,
		}))
	}
	seed("clean", nil)
	seed("pending", map[string]database.OwedSource{"Audnexus (Audible)": {Since: now, LastAttempt: now, Attempts: 2}})
	seed("gaveup", map[string]database.OwedSource{
		"Audnexus (Audible)": {Since: now, LastAttempt: now, Attempts: database.OwedMaxAsks},
		"Google Books":       {Since: now, LastAttempt: now, Attempts: 1},
	})

	set, err := loadCacheRows(context.Background(), store, svc)
	require.NoError(t, err)
	type state struct{ pending, exhausted []string }
	got := map[string]state{}
	for _, r := range set.rows {
		got[r.sum.BookID] = state{r.owedPending, r.owedExhausted}
	}
	require.Equal(t, map[string]state{
		"clean":   {},
		"pending": {pending: []string{"Audnexus (Audible)"}},
		"gaveup":  {pending: []string{"Google Books"}, exhausted: []string{"Audnexus (Audible)"}},
	}, got)

	// Served on the row, and omitted when empty (a clean book's row is unchanged).
	served := func(id string) map[string]any {
		for _, r := range set.rows {
			if r.sum.BookID != id {
				continue
			}
			blob, err := json.Marshal((&unreviewableRow{row: &snapshotRow{loadedCacheRow: r}}).result(&database.Book{ID: id}, now))
			require.NoError(t, err)
			var decoded map[string]any
			require.NoError(t, json.Unmarshal(blob, &decoded))
			return decoded
		}
		t.Fatalf("no row for %s", id)
		return nil
	}
	gaveup := served("gaveup")
	require.Equal(t, []any{"Audnexus (Audible)"}, gaveup["owed_exhausted"])
	require.Equal(t, []any{"Google Books"}, gaveup["owed_pending"])
	require.NotContains(t, served("clean"), "owed_pending")
	require.NotContains(t, served("clean"), "owed_exhausted")
}
