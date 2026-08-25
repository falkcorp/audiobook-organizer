// file: internal/server/handlers/audiobooks/restrict_ids_handler_test.go
// version: 1.0.0
// guid: 2e6b8d40-9c15-4a73-bf28-5d31e70a9c64
// last-edited: 2026-08-25

package audiobookshandler_test

import (
	"net/http"
	"testing"

	audiobookshandler "github.com/falkcorp/audiobook-organizer/internal/server/handlers/audiobooks"
	audiobooksmocks "github.com/falkcorp/audiobook-organizer/internal/server/handlers/audiobooks/mocks"
	"github.com/stretchr/testify/require"
)

// The has_file_errors and quick-query params used to answer the whole request
// from their ID slice, discarding the search, filters, author_id, series_id and
// sort that arrived alongside them. They now seed ListFilters.RestrictToIDs and
// fall through to the normal pipeline.
//
// These assert on the ListFilters the handler HANDED to buildListResponse, not
// on the response body: the stub returns the same canned page whatever it is
// given, so a body assertion cannot see a dropped predicate.

type fileErrorStore struct {
	*audiobooksmocks.MockAudiobooksStore
	ids []string
}

func (s *fileErrorStore) ListBooksWithFileErrors() ([]string, error) { return s.ids, nil }

type quickQueryStore struct {
	*audiobooksmocks.MockAudiobooksStore
	ids []string
}

func (s *quickQueryStore) GetAllBookIDsForQuickQuery(string) ([]string, error) { return s.ids, nil }

type bothStore struct {
	*audiobooksmocks.MockAudiobooksStore
	fileErrIDs []string
	quickIDs   []string
}

func (s *bothStore) ListBooksWithFileErrors() ([]string, error) { return s.fileErrIDs, nil }
func (s *bothStore) GetAllBookIDsForQuickQuery(string) ([]string, error) {
	return s.quickIDs, nil
}

func keysOf(t *testing.T, m map[string]struct{}) []string {
	t.Helper()
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

func TestFastPathsSeedRestrictToIDsInsteadOfAnsweringAlone(t *testing.T) {
	t.Run("has_file_errors seeds the restriction", func(t *testing.T) {
		h, d := newHandlerWithStore(t, func(m *audiobooksmocks.MockAudiobooksStore) audiobookshandler.AudiobooksStore {
			return &fileErrorStore{MockAudiobooksStore: m, ids: []string{"b1", "b2", "b3"}}
		})
		c, w := newCtx("GET", "/audiobooks?has_file_errors=true", nil, nil)
		h.ListAudiobooks(c)
		require.Equal(t, http.StatusOK, w.Code)
		require.True(t, d.rec.listFiltersSeen,
			"the request must reach buildListResponse, not return from a fast path")
		require.ElementsMatch(t, []string{"b1", "b2", "b3"},
			keysOf(t, d.rec.listFilters.RestrictToIDs))
	})

	t.Run("the rest of the query survives alongside it", func(t *testing.T) {
		// This is the bug itself: search and the filters JSON arriving WITH
		// has_file_errors used to be discarded silently.
		h, d := newHandlerWithStore(t, func(m *audiobooksmocks.MockAudiobooksStore) audiobookshandler.AudiobooksStore {
			return &fileErrorStore{MockAudiobooksStore: m, ids: []string{"b1"}}
		})
		c, w := newCtx("GET",
			`/audiobooks?has_file_errors=true&sort_by=title&library_state=organized`+
				`&filters=[{"field":"genre","value":"fantasy"}]`, nil, nil)
		h.ListAudiobooks(c)
		require.Equal(t, http.StatusOK, w.Code)
		require.True(t, d.rec.listFiltersSeen)
		require.ElementsMatch(t, []string{"b1"}, keysOf(t, d.rec.listFilters.RestrictToIDs))
		require.Equal(t, "title", d.rec.listFilters.SortBy, "sort must survive the fast path")
		require.Equal(t, "organized", d.rec.listFilters.LibraryState, "library_state must survive")
		require.Len(t, d.rec.listFilters.FieldFilters, 1, "field filters must survive")
		require.Equal(t, "genre", d.rec.listFilters.FieldFilters[0].Field)
	})

	t.Run("a store without the capability restricts to NOTHING, not everything", func(t *testing.T) {
		// The set must be non-nil and empty. Leaving it nil would mean "no
		// restriction" and list the whole library for a request that asked to
		// narrow it -- strictly worse than the bug being fixed.
		h, d := newHandler(t)
		c, w := newCtx("GET", "/audiobooks?has_file_errors=true", nil, nil)
		h.ListAudiobooks(c)
		require.Equal(t, http.StatusOK, w.Code)
		require.True(t, d.rec.listFiltersSeen)
		require.NotNil(t, d.rec.listFilters.RestrictToIDs,
			"a capability-less store must yield an EMPTY set, never nil")
		require.Empty(t, d.rec.listFilters.RestrictToIDs)
	})

	t.Run("quick query seeds the restriction", func(t *testing.T) {
		h, d := newHandlerWithStore(t, func(m *audiobooksmocks.MockAudiobooksStore) audiobookshandler.AudiobooksStore {
			return &quickQueryStore{MockAudiobooksStore: m, ids: []string{"q1", "q2"}}
		})
		c, w := newCtx("GET", "/audiobooks?missing_covers=true", nil, nil)
		h.ListAudiobooks(c)
		require.Equal(t, http.StatusOK, w.Code)
		require.True(t, d.rec.listFiltersSeen)
		require.ElementsMatch(t, []string{"q1", "q2"}, keysOf(t, d.rec.listFilters.RestrictToIDs))
	})

	t.Run("both params intersect rather than one winning", func(t *testing.T) {
		// The old code returned from whichever fast path it tested first and
		// ignored the other. Overlap is partial on purpose: an assertion where
		// one set contains the other cannot tell intersection from assignment.
		h, d := newHandlerWithStore(t, func(m *audiobooksmocks.MockAudiobooksStore) audiobookshandler.AudiobooksStore {
			return &bothStore{
				MockAudiobooksStore: m,
				fileErrIDs:          []string{"a", "b", "c"},
				quickIDs:            []string{"b", "c", "d"},
			}
		})
		c, w := newCtx("GET", "/audiobooks?has_file_errors=true&missing_covers=true", nil, nil)
		h.ListAudiobooks(c)
		require.Equal(t, http.StatusOK, w.Code)
		require.True(t, d.rec.listFiltersSeen)
		require.ElementsMatch(t, []string{"b", "c"},
			keysOf(t, d.rec.listFilters.RestrictToIDs),
			"both restrictions must apply, not just the first one tested")
	})

	t.Run("an ordinary request carries NO restriction", func(t *testing.T) {
		// nil, not empty: an unrestricted list must not be narrowed to nothing.
		h, d := newHandler(t)
		c, w := newCtx("GET", "/audiobooks", nil, nil)
		h.ListAudiobooks(c)
		require.Equal(t, http.StatusOK, w.Code)
		require.True(t, d.rec.listFiltersSeen)
		require.Nil(t, d.rec.listFilters.RestrictToIDs,
			"no restriction param means nil (list everything), never an empty set")
	})
}
