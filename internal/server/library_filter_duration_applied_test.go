// file: internal/server/library_filter_duration_applied_test.go
// version: 1.0.0
// guid: 5f2c8b73-1d9e-4a06-b7c4-3e8a0d6f2b91
// last-edited: 2026-09-27

package server

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"sort"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// End to end through GET /api/v1/audiobooks with the `filters` parameter the
// web search bar sends: the owner's "needs metadata, hide chapter files"
// query, the count that reaches the UI, and the search= rejection that
// replaced a silent whole-library answer.
func TestListAudiobooks_DurationAndMetadataAppliedFilters(t *testing.T) {
	server, cleanup := setupTestServer(t)
	defer cleanup()

	dur := func(v int) *int { return &v }
	chapter := createTestBookWithFields(t, "Chapter 03", nil, dur(600))
	needs := createTestBookWithFields(t, "Needs Metadata", nil, dur(36000))
	done := createTestBookWithFields(t, "Already Matched", nil, dur(36000))
	confirmed := createTestBookWithFields(t, "Audio Confirmed", nil, dur(20000))
	unknown := createTestBookWithFields(t, "Unprobed", nil, nil)

	store := database.GetGlobalStore()
	for id, status := range map[string]string{done.ID: "matched", confirmed.ID: "audio_confirmed"} {
		s := status
		_, err := store.ModifyBook(id, func(b *database.Book) error {
			b.MetadataReviewStatus = &s
			return nil
		})
		require.NoError(t, err)
	}

	list := func(t *testing.T, filters string, extra string) (int, []string, int) {
		t.Helper()
		q := url.Values{"limit": {"50"}}
		if filters != "" {
			q.Set("filters", filters)
		}
		req := httptest.NewRequest(http.MethodGet, "/api/v1/audiobooks?"+q.Encode()+extra, nil)
		w := httptest.NewRecorder()
		server.router.ServeHTTP(w, req)
		if w.Code != http.StatusOK {
			return w.Code, nil, -1
		}
		resp := parseJSONResponse(t, w)
		data := resp["data"].(map[string]any)
		var titles []string
		for _, it := range data["items"].([]any) {
			titles = append(titles, it.(map[string]any)["title"].(string))
		}
		sort.Strings(titles)
		return w.Code, titles, int(data["count"].(float64))
	}

	t.Run("needs metadata and over 20 minutes", func(t *testing.T) {
		code, titles, count := list(t, `[{"field":"metadata","value":"applied","negated":true},{"field":"duration","value":">20m","negated":false}]`, "")
		require.Equal(t, http.StatusOK, code)
		assert.Equal(t, []string{needs.Title}, titles)
		assert.Equal(t, 1, count, "count must be the filtered total")
	})
	t.Run("short files", func(t *testing.T) {
		_, titles, count := list(t, `[{"field":"duration","value":"<20m"}]`, "")
		assert.Equal(t, []string{chapter.Title}, titles)
		assert.Equal(t, 1, count)
	})
	t.Run("range with units", func(t *testing.T) {
		_, titles, _ := list(t, `[{"field":"duration","value":"[5h TO 6h]"}]`, "")
		assert.Equal(t, []string{confirmed.Title}, titles)
	})
	t.Run("unknown duration", func(t *testing.T) {
		_, titles, _ := list(t, `[{"field":"has_duration","value":"no"}]`, "")
		assert.Equal(t, []string{unknown.Title}, titles)
	})
	t.Run("applied covers audio_confirmed", func(t *testing.T) {
		_, titles, _ := list(t, `[{"field":"metadata","value":"applied"}]`, "")
		assert.Equal(t, []string{done.Title, confirmed.Title}, titles)
	})
	t.Run("invalid duration is a 400, not count 0", func(t *testing.T) {
		code, _, _ := list(t, `[{"field":"duration","value":">soon"}]`, "")
		assert.Equal(t, http.StatusBadRequest, code)
	})
	t.Run("unindexed filter field inside search is a 400, not the whole library", func(t *testing.T) {
		code, _, _ := list(t, "", "&search="+url.QueryEscape("-review:matched"))
		assert.Equal(t, http.StatusBadRequest, code)
		code, _, _ = list(t, "", "&search="+url.QueryEscape("Re:Zero"))
		assert.Equal(t, http.StatusOK, code, "a colon in ordinary text must still search")
	})
}

// ids= is the Library's live-update refetch: exactly the named rows, with the
// count of that set, whatever else is on the page.
func TestListAudiobooks_IDsParam(t *testing.T) {
	server, cleanup := setupTestServer(t)
	defer cleanup()
	a := createTestBook(t, "IDs A")
	b := createTestBook(t, "IDs B")
	_ = createTestBook(t, "IDs C")

	req := httptest.NewRequest(http.MethodGet, "/api/v1/audiobooks?limit=50&ids="+a.ID+","+b.ID, nil)
	w := httptest.NewRecorder()
	server.router.ServeHTTP(w, req)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	data := parseJSONResponse(t, w)["data"].(map[string]any)
	assert.Len(t, data["items"].([]any), 2)
	assert.Equal(t, float64(2), data["count"])
}
