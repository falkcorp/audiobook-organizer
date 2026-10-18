// file: internal/server/handlers/audiobooks/date_filter_cache_test.go
// version: 1.0.0
// guid: 9528fb30-8d79-49a5-a646-57b90ec2c614
// last-edited: 2026-10-18

package audiobookshandler_test

import (
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"
)

// A relative date filter (date_added:>-1y) is the same request text tomorrow
// and a different set of books, so its page must not be stored under a key made
// of that text. An absolute one is stable and caches as any list does.
func TestListAudiobooks_RelativeDateFilterIsNotCached(t *testing.T) {
	for _, tc := range []struct {
		name      string
		field     string
		value     string
		wantCache bool
	}{
		{"relative date_added", "date_added", ">-1y", false},
		{"relative alias", "modified", "-30d..now", false},
		{"now", "date_modified", "<now", false},
		{"absolute date_added", "date_added", ">=2026-01-01", true},
		{"absolute range", "added", "2026-01-01..2026-03-31", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h, deps := newHandler(t)
			c, w := newCtx("GET", filtersQuery(t, []map[string]string{{"field": tc.field, "value": tc.value}}), nil, nil)
			h.ListAudiobooks(c)
			require.Equal(t, http.StatusOK, w.Code, w.Body.String())
			require.True(t, deps.rec.listFiltersSeen)
			if tc.wantCache {
				require.Equal(t, 1, deps.listCache.Len(), "an absolute date list is cached like any other")
			} else {
				require.Zero(t, deps.listCache.Len(), "a relative window moves with the clock; it must not be cached")
			}
		})
	}
}

// The date fields the search help advertises are accepted by the list route, and
// a bad term is a 400 before the list is built.
func TestListAudiobooks_DateFilterBoundary(t *testing.T) {
	for _, tc := range []struct{ field, value string }{
		{"date_added", ">-1y"}, {"added", ">-1y"}, {"date_modified", "<-30d"}, {"modified", "now"},
		{"last_played", ">-30d"}, {"last_played", "-30d..now"}, {"last_played", "*"},
	} {
		h, deps := newHandler(t)
		c, w := newCtx("GET", filtersQuery(t, []map[string]string{{"field": tc.field, "value": tc.value}}), nil, nil)
		h.ListAudiobooks(c)
		require.Equal(t, http.StatusOK, w.Code, "%s:%s %s", tc.field, tc.value, w.Body.String())
		require.True(t, deps.rec.listFiltersSeen, "%s:%s", tc.field, tc.value)
	}
	for _, tc := range []struct{ field, value, want string }{
		{"date_added", ">30d", "-30d"},
		{"last_played", "30d", "-30d"},
		{"date_modified", "yesterday", "invalid date"},
		{"added", "2026-02-31", "invalid date"},
	} {
		h, deps := newHandler(t)
		c, w := newCtx("GET", filtersQuery(t, []map[string]string{{"field": tc.field, "value": tc.value}}), nil, nil)
		h.ListAudiobooks(c)
		require.Equal(t, http.StatusBadRequest, w.Code, "%s:%s %s", tc.field, tc.value, w.Body.String())
		require.Contains(t, w.Body.String(), tc.want)
		require.False(t, deps.rec.listFiltersSeen, "%s:%s must be refused before the list is built", tc.field, tc.value)
	}
}
