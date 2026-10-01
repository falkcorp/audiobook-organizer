// file: internal/metafetch/search_bypass_fetch_cache_test.go
// version: 1.1.0
// guid: 3c7a9e51-2b84-4f06-9d1e-8a5c0f2b6d47
// last-edited: 2026-10-01

package metafetch

import (
	"encoding/json"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/metadata"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A forced search (SearchOptions.BypassFetchCache) must ask the provider even
// when the per-source fetch cache holds a fresh row for the book's identity,
// and must replace that row with the fresh answer. Without the option the
// row is replayed and the provider is not called.
func TestSearchMetadataForBook_BypassFetchCacheReasksAndRewrites(t *testing.T) {
	prevTTL := config.AppConfig.MetadataFetchCacheTTLDays
	config.AppConfig.MetadataFetchCacheTTLDays = 30
	t.Cleanup(func() { config.AppConfig.MetadataFetchCacheTTLDays = prevTTL })

	raw := map[string][]byte{}
	svc, src := fetchCacheHarness(t, raw, "The Way of Kings", "Brandon Sanderson")
	identity := svc.fetchCacheIdentity(&database.Book{Title: "The Way of Kings", Author: &database.Author{Name: "Brandon Sanderson"}})
	// The search reads its per-variant rows (the first variant: the title +
	// the author). The cached answer is a strong match, so the fan-out stops
	// on it and no other variant is asked live either.
	stale, err := json.Marshal([]metadata.BookMetadata{{Title: "The Way of Kings", Author: "Brandon Sanderson", Publisher: "cached"}})
	require.NoError(t, err)
	variantRow := variantCacheSource(src, queryVariant{Title: "The Way of Kings", Author: "Brandon Sanderson"})
	require.NoError(t, database.PutCachedMetadataFetch(svc.db, "b1", variantRow, identity, stale, 0))

	_, err = svc.SearchMetadataForBookWithOptions("b1", "The Way of Kings", "Brandon Sanderson", "", "", SearchOptions{})
	require.NoError(t, err)
	require.Zero(t, src.calls.Load(), "an unforced search replays the fresh cache row")

	_, err = svc.SearchMetadataForBookWithOptions("b1", "The Way of Kings", "Brandon Sanderson", "", "", SearchOptions{BypassFetchCache: true})
	require.NoError(t, err)
	assert.Positive(t, src.calls.Load(), "a forced search must ask the provider again")

	entry, err := database.GetCachedMetadataFetch(svc.db, "b1", src.Name(), identity)
	require.NoError(t, err)
	require.NotNil(t, entry)
	var got []metadata.BookMetadata
	require.NoError(t, json.Unmarshal(entry.Results, &got))
	require.NotEmpty(t, got)
	assert.Equal(t, "The Way of Kings", got[0].Title, "the fresh answer replaces the cached row")

	vEntry, err := database.GetCachedMetadataFetch(svc.db, "b1", variantRow, identity)
	require.NoError(t, err)
	require.NotNil(t, vEntry)
	got = nil
	require.NoError(t, json.Unmarshal(vEntry.Results, &got))
	require.NotEmpty(t, got)
	assert.Empty(t, got[0].Publisher, "the forced answer replaces the per-variant row too")
}
