// file: internal/metafetch/fetch_cache_identity_test.go
// version: 1.1.0
// guid: 6b1e9c42-8d3f-4a7e-b25c-0f9a3d7e41c8
// last-edited: 2026-09-14

package metafetch

import (
	"context"
	"encoding/json"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/metadata"
	"github.com/falkcorp/audiobook-organizer/internal/util"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// identityCountingSource is a metadata source that records how many live Search*
// calls reached it. A cache replay never calls the source, so the count is the
// direct observable for "was the stale cache row replayed?".
type identityCountingSource struct {
	name    string
	results []metadata.BookMetadata
	calls   atomic.Int32
}

func (c *identityCountingSource) Name() string { return c.name }
func (c *identityCountingSource) SearchByTitle(_ context.Context, _ string) ([]metadata.BookMetadata, error) {
	c.calls.Add(1)
	return c.results, nil
}
func (c *identityCountingSource) SearchByTitleAndAuthor(_ context.Context, _, _ string) ([]metadata.BookMetadata, error) {
	c.calls.Add(1)
	return c.results, nil
}

// fetchCacheHarness builds a MockStore whose book row is `title`/`author` and
// whose raw KV is the supplied map, plus a Service wired to one counting source.
func fetchCacheHarness(t *testing.T, raw map[string][]byte, title, author string) (*Service, *identityCountingSource) {
	t.Helper()
	mock := &database.MockStore{
		GetBookByIDFunc: func(id string) (*database.Book, error) {
			return &database.Book{ID: id, Title: title, Author: &database.Author{ID: 1, Name: author}}, nil
		},
		GetAuthorByNameFunc: func(name string) (*database.Author, error) {
			return &database.Author{ID: 1, Name: name}, nil
		},
		UpdateBookFunc:           func(_ string, book *database.Book) (*database.Book, error) { return book, nil },
		RecordMetadataChangeFunc: func(_ *database.MetadataChangeRecord) error { return nil },
		GetSeriesByNameFunc: func(name string, _ *int) (*database.Series, error) {
			return &database.Series{ID: 1, Name: name}, nil
		},
		GetRawFunc:    func(k string) ([]byte, error) { return raw[k], nil },
		SetRawFunc:    func(k string, v []byte) error { raw[k] = v; return nil },
		DeleteRawFunc: func(k string) error { delete(raw, k); return nil },
	}
	src := &identityCountingSource{
		name: "IdentityTestSource",
		results: []metadata.BookMetadata{{
			Title:  "The Way of Kings",
			Author: "Brandon Sanderson",
		}},
	}
	svc := NewService(mock)
	svc.SetOverrideSources([]metadata.MetadataSource{src})
	return svc, src
}

// TestFetchCache_LegacyRowWithoutIdentityIsNotReplayed is the A3#14
// fail-before test. A row written before search identities existed was fetched
// for whatever the book's identity was THEN (here: a garbage rip title that
// matched the wrong book). After the user corrects the title and author, a
// re-fetch inside the TTL window must go back to the provider, not replay the
// wrong-book row. At HEAD before the fix the source is never called.
func TestFetchCache_LegacyRowWithoutIdentityIsNotReplayed(t *testing.T) {
	raw := map[string][]byte{}
	svc, src := fetchCacheHarness(t, raw, "The Way of Kings", "Brandon Sanderson")

	staleResults, err := json.Marshal([]metadata.BookMetadata{{
		Title:  "Wrong Book Entirely",
		Author: "Somebody Else",
	}})
	require.NoError(t, err)
	// Hand-written legacy row: the exact shape PutCachedMetadataFetch wrote
	// before the search_identity field, keyed where the fetch path reads.
	legacy, err := json.Marshal(map[string]any{
		"book_id":    "b1",
		"source":     util.NormalizeString(metadata.ProviderKey(src)),
		"results":    json.RawMessage(staleResults),
		"best_score": 1.0,
		"cached_at":  time.Now().UTC(),
	})
	require.NoError(t, err)
	key := "metadata_fetch_cache:b1:" + util.NormalizeString(metadata.ProviderKey(src))
	raw[key] = legacy

	_, _ = svc.FetchMetadataForBook(context.Background(), "b1")

	assert.Positive(t, src.calls.Load(),
		"a cache row with no search identity must be a miss; the provider has to be asked again")

	// The miss is followed by the normal write-back at the same key, so the row
	// self-heals to the fresh result instead of being orphaned.
	var entry database.CachedMetadataEntry
	require.NoError(t, json.Unmarshal(raw[key], &entry))
	var got []metadata.BookMetadata
	require.NoError(t, json.Unmarshal(entry.Results, &got))
	require.NotEmpty(t, got)
	assert.Equal(t, "The Way of Kings", got[0].Title)
	assert.Equal(t, svc.fetchCacheIdentity(&database.Book{Title: "The Way of Kings", Author: &database.Author{Name: "Brandon Sanderson"}}),
		entry.SearchIdentity, "the rewritten row must carry the book's current identity")
}

// scanningRawStore extends a raw map with ScanPrefix and DeleteMetadataCache so
// both invalidation hooks can run against it.
func scanningRawStore(raw map[string][]byte, candidateDeleted *string) *database.MockStore {
	return &database.MockStore{
		DeleteMetadataCacheFunc: func(bookID string) error { *candidateDeleted = bookID; return nil },
		ScanPrefixFunc: func(prefix string) ([]database.KVPair, error) {
			var out []database.KVPair
			for k, v := range raw {
				if strings.HasPrefix(k, prefix) {
					out = append(out, database.KVPair{Key: k, Value: v})
				}
			}
			return out, nil
		},
		GetRawFunc:    func(k string) ([]byte, error) { return raw[k], nil },
		SetRawFunc:    func(k string, v []byte) error { raw[k] = v; return nil },
		DeleteRawFunc: func(k string) error { delete(raw, k); return nil },
	}
}

// seedStampedRow writes a fetch-cache row for b1 under src, stamped for the
// given title/author, holding one result titled resultTitle.
func seedStampedRow(t *testing.T, raw map[string][]byte, src metadata.MetadataSource, title, author, resultTitle string) string {
	t.Helper()
	blob, err := json.Marshal([]metadata.BookMetadata{{Title: resultTitle, Author: author}})
	require.NoError(t, err)
	store := scanningRawStore(raw, new(string))
	ident := database.MetadataSearchIdentity(title, author, nil, nil, nil)
	require.NoError(t, database.PutCachedMetadataFetch(store, "b1", metadata.ProviderKey(src), ident, blob, 1.0))
	return "metadata_fetch_cache:b1:" + util.NormalizeString(metadata.ProviderKey(src))
}

// TestApplyInvalidation_NarratorOnlyChangeKeepsFetchRows is the #3421 review
// fail-before. The apply/edit hook (InvalidateCachedCandidates, which
// batch_apply_one calls after EVERY successful apply) must not destroy a
// fetch row that is still valid: a narrator-only apply leaves the book's
// identity unchanged, so the next fetch replays the row instead of asking the
// provider again. At b1254282d the hook wiped every row and the source was
// called.
func TestApplyInvalidation_NarratorOnlyChangeKeepsFetchRows(t *testing.T) {
	raw := map[string][]byte{}
	svc, src := fetchCacheHarness(t, raw, "The Way of Kings", "Brandon Sanderson")
	key := seedStampedRow(t, raw, src, "The Way of Kings", "Brandon Sanderson", "The Way of Kings")
	other := "metadata_fetch_cache:b1:hardcover"
	raw[other] = raw[key]

	candidateDeleted := ""
	hook := NewService(scanningRawStore(raw, &candidateDeleted))
	require.NoError(t, hook.InvalidateCachedCandidates("b1"))

	assert.Equal(t, "b1", candidateDeleted, "the candidate cache is still cleared")
	assert.Contains(t, raw, key, "a fill-only / narrator apply must keep the provider row")
	assert.Contains(t, raw, other, "every provider's row survives the apply hook")

	// The book now differs only in narrator, which is not part of the identity.
	_, _ = svc.FetchMetadataForBook(context.Background(), "b1")
	assert.Zero(t, src.calls.Load(), "an unchanged identity must replay the cached row, not refetch")
}

// TestApplyInvalidation_TitleChangeMakesFetchRowsMiss: when an apply or edit
// changes the title, the rows fetched for the old title are not deleted by
// the hook but no longer match the stamp, so the next fetch re-queries and
// overwrites them with a row stamped for the new title.
func TestApplyInvalidation_TitleChangeMakesFetchRowsMiss(t *testing.T) {
	raw := map[string][]byte{}
	svc, src := fetchCacheHarness(t, raw, "The Way of Kings", "Brandon Sanderson")
	key := seedStampedRow(t, raw, src, "01 - track", "Brandon Sanderson", "Wrong Book Entirely")

	require.NoError(t, NewService(scanningRawStore(raw, new(string))).InvalidateCachedCandidates("b1"))

	_, _ = svc.FetchMetadataForBook(context.Background(), "b1")
	assert.Positive(t, src.calls.Load(), "a row fetched for the old title must miss after the title changes")

	var entry database.CachedMetadataEntry
	require.NoError(t, json.Unmarshal(raw[key], &entry))
	assert.Equal(t, database.MetadataSearchIdentity("The Way of Kings", "Brandon Sanderson", nil, nil, nil), entry.SearchIdentity,
		"the refetch rewrites the row for the new identity")
}

// TestInvalidateFetchCacheForBook_RejectRemovesRows: the reject ("no match")
// hook deletes every provider row for the book and only that book, because a
// rejected result is wrong for an UNCHANGED identity that the stamp cannot
// catch. Other books' rows, including an id that shares the prefix digit,
// survive.
func TestInvalidateFetchCacheForBook_RejectRemovesRows(t *testing.T) {
	raw := map[string][]byte{
		"metadata_fetch_cache:b1:audible":      []byte(`{}`),
		"metadata_fetch_cache:b1:hardcover":    []byte(`{}`),
		"metadata_fetch_cache:b2:audible":      []byte(`{}`),
		"metadata_fetch_cache:b10:openlibrary": []byte(`{}`),
	}
	candidateDeleted := ""
	require.NoError(t, NewService(scanningRawStore(raw, &candidateDeleted)).InvalidateFetchCacheForBook("b1"))

	assert.NotContains(t, raw, "metadata_fetch_cache:b1:audible")
	assert.NotContains(t, raw, "metadata_fetch_cache:b1:hardcover")
	assert.Contains(t, raw, "metadata_fetch_cache:b2:audible", "another book's row must survive")
	assert.Contains(t, raw, "metadata_fetch_cache:b10:openlibrary", "a book id sharing the prefix digit must survive")
	assert.Empty(t, candidateDeleted, "the reject hook only clears the fetch cache")
}
