// file: internal/metafetch/search_bypass_fetch_cache_test.go
// version: 1.0.0
// guid: 3c7a9e51-2b84-4f06-9d1e-8a5c0f2b6d47
// last-edited: 2026-09-30

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
	stale, err := json.Marshal([]metadata.BookMetadata{{Title: "Cached Answer", Author: "Brandon Sanderson"}})
	require.NoError(t, err)
	require.NoError(t, database.PutCachedMetadataFetch(svc.db, "b1", src.Name(), identity, stale, 0))

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
}
