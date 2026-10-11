// file: internal/server/handlers/audiobooks/ident_filter_test.go
// version: 1.0.0
// guid: 7a275abf-d4f2-4303-b515-c05d00c6d023
// last-edited: 2026-10-11

package audiobookshandler_test

import (
	"fmt"
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// TestIdentFilter_BuildingReturns503: an ident: query while the
// identification index is being built is a 503 with Retry-After, never a
// count (or a 500), and is not cached.
func TestIdentFilter_BuildingReturns503(t *testing.T) {
	q := filtersQuery(t, []map[string]string{{"field": "ident", "value": "missing"}})
	h, deps := newHandler(t)
	deps.rec.listErr = fmt.Errorf("list: %w", database.ErrIdentIndexBuilding)
	c, w := newCtx("GET", q, nil, nil)
	h.ListAudiobooks(c)
	require.Equal(t, http.StatusServiceUnavailable, w.Code, w.Body.String())
	require.Equal(t, "5", w.Header().Get("Retry-After"))
	require.Contains(t, w.Body.String(), "identification index is being built")
	require.Zero(t, deps.listCache.Len(), "a refusal is never cached")
}

// TestIdentFilter_HandlerBoundary: a valid ident: value reaches the list
// build untouched (the service resolves it) and is never served from or put
// in the library-generation list cache; an unknown value is a 400 that names
// the valid ones, before the list build runs.
func TestIdentFilter_HandlerBoundary(t *testing.T) {
	h, deps := newHandler(t)
	c, w := newCtx("GET", filtersQuery(t, []map[string]string{{"field": "ident", "value": "missing"}}), nil, nil)
	h.ListAudiobooks(c)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	require.True(t, deps.rec.listFiltersSeen)
	require.Len(t, deps.rec.listFilters.FieldFilters, 1)
	require.Equal(t, "ident", deps.rec.listFilters.FieldFilters[0].Field)
	require.Zero(t, deps.listCache.Len(), "an ident: list is not cached under the library generation")

	for _, bad := range []string{"nope", "/miss/", "miss*"} {
		h, deps := newHandler(t)
		c, w := newCtx("GET", filtersQuery(t, []map[string]string{{"field": "ident", "value": bad}}), nil, nil)
		h.ListAudiobooks(c)
		require.Equal(t, http.StatusBadRequest, w.Code, "%s: %s", bad, w.Body.String())
		require.Contains(t, w.Body.String(), "unasked, asked_empty, has_candidates, applied, held, missing", bad)
		require.False(t, deps.rec.listFiltersSeen, bad)
	}

	// asin: is an ordinary field the list accepts.
	h, deps = newHandler(t)
	c, w = newCtx("GET", filtersQuery(t, []map[string]string{{"field": "asin", "value": "B0SYNTH001"}}), nil, nil)
	h.ListAudiobooks(c)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	require.True(t, deps.rec.listFiltersSeen)
}
