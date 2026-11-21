// file: internal/books/store_tunables.go
// version: 0.1.0
// guid: 4a9d2e63-8b17-4c5f-a0e2-7d6c1b3f9e48
// last-edited: 2026-11-20

package books

import (
	"encoding/json"
	"fmt"
	"os"

	database "github.com/falkcorp/audiobook-organizer/internal/books/store"
	"github.com/falkcorp/audiobook-organizer/internal/config"
	coreconfig "github.com/falkcorp/mediacore/config"
)

// The store tunables' core keys (rename rows enabled_sort_indexes and
// library_counts_cache_min_interval_seconds). Both are read once, when a
// store opens.
const (
	sortIndexesKey    coreconfig.Path = "modules.books.search.enabled_sort_indexes"
	countsIntervalKey coreconfig.Path = "modules.books.cache.library_counts_cache_min_interval_seconds"
)

// ApplyStoreTunables selects the library sort fields that get a memdb sorted
// index and the library-counts cache interval, for the next store that opens:
// memdbSchema() reads the index set once, when NewMemStore builds the schema,
// so it MUST run before the store opens. An unknown index name is warned
// about, not ignored silently: a typo would leave a sort on the slow path
// with no sign why. The projection's enabled_sort_indexes becomes the set
// actually built, so a typo is not reported as an enabled index.
func ApplyStoreTunables(indexes []string, countsIntervalSeconds int) {
	if unknown := database.SetEnabledSortIndexes(indexes); len(unknown) > 0 {
		fmt.Fprintf(os.Stderr, "Warning: unknown enabled_sort_indexes entries ignored: %v "+
			"(the environment form is a JSON list: ABO_BOOKS_SEARCH_ENABLED_SORT_INDEXES='[\"year\",\"created_at\"]')\n", unknown)
	}
	built := database.EnabledSortIndexFields()
	config.Mutate(func(c *config.Config) { c.EnabledSortIndexes = built })
	database.SetLibraryCountsCacheMinInterval(countsIntervalSeconds)
}

// storeTunablesFrom reads the two tunables from the booted App's config store
// (Deps.Config), whose effective value of a restart-bound key is the one this
// process booted with, stored value included: what books' Register builds its
// store with. ok is false when acc holds neither key.
func storeTunablesFrom(acc interface {
	Lookup(coreconfig.Path) (coreconfig.KeyState, bool)
}) (indexes []string, countsIntervalSeconds int, err error) {
	if ks, ok := acc.Lookup(sortIndexesKey); ok && len(ks.Effective) > 0 {
		if err := json.Unmarshal(ks.Effective, &indexes); err != nil {
			return nil, 0, fmt.Errorf("books: %s: %w", sortIndexesKey, err)
		}
	}
	if ks, ok := acc.Lookup(countsIntervalKey); ok && len(ks.Effective) > 0 {
		if err := json.Unmarshal(ks.Effective, &countsIntervalSeconds); err != nil {
			return nil, 0, fmt.Errorf("books: %s: %w", countsIntervalKey, err)
		}
	}
	return indexes, countsIntervalSeconds, nil
}
