// file: internal/server/handlers/metadata_cache_deferred_test.go
// version: 1.0.0
// guid: 50ced779-c827-4b62-93cb-f6b631d3a60b
// last-edited: 2026-10-06

package handlers

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/metafetch"
)

// The review page's "deferred" chip counts a book only while its fallback
// lookup was deferred AND it still has no usable candidate: one that gained a
// usable candidate since (a dialog search, a chain refresh) waits on nothing,
// and the selection never picks it again, so it would sit in the chip
// forever. Synthetic fixtures.
func TestLoadCacheRows_DeferredOnlyWithoutAUsableCandidate(t *testing.T) {
	store, err := database.NewPebbleStore(t.TempDir())
	require.NoError(t, err)
	require.NoError(t, database.RunMigrations(store))
	t.Cleanup(func() { _ = store.Close() })
	svc := metafetch.NewService(store)

	deferred := map[string]database.FallbackAttempt{
		"Google Books": {At: time.Now().UTC(), Outcome: database.FallbackOutcomeDeferred},
	}
	seed := func(id, title string, score float64) {
		_, err := store.CreateBook(&database.Book{ID: id, Title: title, FilePath: "/lib/" + id + "/book.m4b"})
		require.NoError(t, err)
		raw, err := json.Marshal(metafetch.MetadataCandidate{Source: "Audible", Title: title, Score: score})
		require.NoError(t, err)
		require.NoError(t, store.PutMetadataCache(&database.MetadataCandidateCache{
			BookID: id, FetchedAt: time.Now().UTC(), SourceHash: "h-" + id,
			SearchFingerprint: metafetch.FingerprintPrefix + "q-" + id,
			Candidates:        []json.RawMessage{raw}, FallbackAttempts: deferred,
		}))
	}
	seed("usable", "Usable Example", 0.95)
	seed("below-floor", "Below Floor Example", 0.3)
	seed("rejected", "Rejected Example", 0.95)
	require.NoError(t, store.SetRaw("rejected_candidate:rejected:Audible|Rejected Example", []byte("1")))

	set, err := loadCacheRows(context.Background(), store, svc)
	require.NoError(t, err)
	got := map[string]bool{}
	for _, r := range set.rows {
		got[r.sum.BookID] = r.fallbackDeferred
	}
	require.Equal(t, map[string]bool{"usable": false, "below-floor": true, "rejected": true}, got)
}
