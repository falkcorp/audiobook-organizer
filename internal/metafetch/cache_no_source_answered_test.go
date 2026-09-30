// file: internal/metafetch/cache_no_source_answered_test.go
// version: 1.0.0
// guid: 5c72e2bf-8a2c-461b-a89a-931d1cecdcc2
// last-edited: 2026-09-30

// A search where every provider errored used to be recorded as an empty
// answer: the search core returns a nil error when all sources fail, and
// cacheSearchResponse stamped LastEmptyFetchAt = now. During an outage or
// quota exhaustion a forced stale refetch therefore marked the whole stale
// backlog as freshly checked for 30 days without anyone having been asked.
package metafetch

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/metadata"
)

// seedStaleEmptyRow stores a zero-candidate row last checked two TTLs ago.
func seedStaleEmptyRow(t *testing.T, f *verdictFixture, title string) (*database.Book, time.Time) {
	t.Helper()
	b, err := f.store.CreateBook(&database.Book{Title: title, FilePath: "/lib/x/" + title + ".m4b"})
	require.NoError(t, err)
	old := time.Now().Add(-2 * database.MetadataCacheTTL).UTC().Truncate(time.Second)
	require.NoError(t, f.store.PutMetadataCache(&database.MetadataCandidateCache{
		BookID: b.ID, FetchedAt: old, LastEmptyFetchAt: &old, SourceHash: "h", SearchFingerprint: "f",
	}))
	return b, old
}

func TestFetchAndCache_NoSourceAnsweredLeavesTheRowStale(t *testing.T) {
	for name, limited := range map[string]bool{"FetchAndCache": false, "FetchAndCacheLimited": true} {
		t.Run(name, func(t *testing.T) {
			f := newVerdictFixture(t)
			b, old := seedStaleEmptyRow(t, f, "Outage "+name)
			f.mfs.SetOverrideSources([]metadata.MetadataSource{
				&verdictSource{name: "SrcA", byAuthorErr: errors.New("503"), byTitleErr: errors.New("503")},
				&verdictSource{name: "SrcB", byAuthorErr: metadata.ErrProviderThrottled, byTitleErr: metadata.ErrProviderThrottled},
			})

			var err error
			if limited {
				_, err = f.mfs.FetchAndCacheLimited(context.Background(), nil, b.ID, b.Title, "", "", "", SearchOptions{})
			} else {
				_, err = f.mfs.FetchAndCache(context.Background(), b.ID, b.Title, "", "", "", SearchOptions{})
			}
			require.ErrorIs(t, err, ErrNoSourceAnswered)

			stored, gerr := f.store.GetMetadataCache(b.ID)
			require.NoError(t, gerr)
			require.NotNil(t, stored)
			require.True(t, stored.FetchedAt.Equal(old), "FetchedAt moved to %v", stored.FetchedAt)
			require.NotNil(t, stored.LastEmptyFetchAt)
			require.True(t, stored.LastEmptyFetchAt.Equal(old),
				"an all-sources-failed search dated the row as checked (%v): the stale backlog would clear itself during an outage", stored.LastEmptyFetchAt)
		})
	}
}

func TestFetchAndCache_OneAnsweringSourceDatesTheRow(t *testing.T) {
	f := newVerdictFixture(t)
	b, old := seedStaleEmptyRow(t, f, "Partial Outage")
	f.mfs.SetOverrideSources([]metadata.MetadataSource{
		&verdictSource{name: "SrcA", byAuthorErr: errors.New("503"), byTitleErr: errors.New("503")},
		&verdictSource{name: "SrcB"}, // answers: nothing
	})

	_, err := f.mfs.FetchAndCacheLimited(context.Background(), nil, b.ID, b.Title, "", "", "", SearchOptions{})
	require.NoError(t, err)

	stored, gerr := f.store.GetMetadataCache(b.ID)
	require.NoError(t, gerr)
	require.NotNil(t, stored)
	require.NotNil(t, stored.LastEmptyFetchAt)
	require.True(t, stored.LastEmptyFetchAt.After(old), "a real empty answer must date the look")
	require.Contains(t, stored.EmptyAnswers, "SrcB")
	require.NotContains(t, stored.EmptyAnswers, "SrcA")
}
