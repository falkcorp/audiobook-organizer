// file: internal/server/handlers/audiobooks/search_limits_test.go
// version: 1.0.0
// guid: 4969947c-9081-4cfe-acaa-5dd0af4d0264
// last-edited: 2026-10-10

package audiobookshandler_test

import (
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/querygrammar"
)

func filtersQuery(t *testing.T, filters []map[string]string) string {
	t.Helper()
	raw, err := json.Marshal(filters)
	require.NoError(t, err)
	return "/audiobooks?filters=" + url.QueryEscape(string(raw))
}

// TestListAudiobooks_FilterSetCap: the filters JSON is bounded as a whole --
// at most 8 regex or * filters and 1,024 bytes of values -- with a 400 that
// says which limit, before any row is scanned (the list build never runs).
func TestListAudiobooks_FilterSetCap(t *testing.T) {
	nine := make([]map[string]string, 9)
	for i := range nine {
		nine[i] = map[string]string{"field": "title", "value": "/a" + strings.Repeat("b", i) + "/"}
	}
	wide := make([]map[string]string, 5)
	for i := range wide {
		wide[i] = map[string]string{"field": "title", "value": strings.Repeat("w", 220)}
	}
	for name, tc := range map[string]struct {
		filters []map[string]string
		want    string
	}{
		"nine patterns":     {nine, "9 regex or * filters; the limit is 8"},
		"1,100 value bytes": {wide, "total 1100 bytes; the limit is 1024"},
	} {
		h, deps := newHandler(t)
		c, w := newCtx("GET", filtersQuery(t, tc.filters), nil, nil)
		h.ListAudiobooks(c)
		require.Equal(t, http.StatusBadRequest, w.Code, name)
		require.Contains(t, w.Body.String(), tc.want, name)
		require.False(t, deps.rec.listFiltersSeen, "%s: refused before the list build", name)
	}
	// Eight patterns are a query.
	h, deps := newHandler(t)
	c, w := newCtx("GET", filtersQuery(t, nine[:8]), nil, nil)
	h.ListAudiobooks(c)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	require.True(t, deps.rec.listFiltersSeen)
}

// TestListAudiobooks_SearchLimitsAreNot500: a spent pattern budget is a 400
// that says the search was too slow; every pattern slot busy is a 503 with
// Retry-After. Neither is an internal error, and neither is cached.
func TestListAudiobooks_SearchLimitsAreNot500(t *testing.T) {
	q := filtersQuery(t, []map[string]string{{"field": "title", "value": "/x/"}})

	h, deps := newHandler(t)
	deps.rec.listErr = &querygrammar.TooSlowError{Budget: time.Second}
	c, w := newCtx("GET", q, nil, nil)
	h.ListAudiobooks(c)
	require.Equal(t, http.StatusBadRequest, w.Code, w.Body.String())
	require.Contains(t, w.Body.String(), "too slow")
	require.Zero(t, deps.listCache.Len(), "a refusal is never cached")

	h, deps = newHandler(t)
	deps.rec.listErr = &querygrammar.BusyError{Slots: 2, Waited: 2 * time.Second}
	c, w = newCtx("GET", q, nil, nil)
	h.ListAudiobooks(c)
	require.Equal(t, http.StatusServiceUnavailable, w.Code, w.Body.String())
	require.Equal(t, "2", w.Header().Get("Retry-After"))
	require.Contains(t, w.Body.String(), "pattern searches")
}
