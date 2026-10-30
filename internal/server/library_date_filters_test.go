// file: internal/server/library_date_filters_test.go
// version: 1.0.1
// guid: a0ea869a-92d6-4bed-9440-69e27ae052a0
// last-edited: 2026-10-30

package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/stretchr/testify/require"
)

// The Library page does not send a typed query as one string: it splits the
// search box and sends the field terms as filters=[{field,value}]. These tests
// drive that route with a real store and a signed-in caller, because it is the
// path the search help's own examples (date_added:>-1y, last_played:>-30d) take.
// Names and ids are synthetic.
func TestLibraryListDateFilters(t *testing.T) {
	srv, adminToken, _, seedID, cleanup := setupUserTagsAuthzTestServer(t)
	defer cleanup()

	store, ok := database.GetGlobalStore().(*database.PebbleStore)
	require.True(t, ok, "expected a PebbleStore")
	store.WaitForWarmup()
	admin, err := store.GetUserByUsername("admin")
	require.NoError(t, err)

	// The harness's own book, created before everything below and never played.
	seed, err := store.GetBookByID(seedID)
	require.NoError(t, err)

	// The store stamps CreatedAt itself, so the books are told apart by WHEN they
	// were created relative to a marker taken between the creations.
	first, err := store.CreateBook(&database.Book{Title: "Synthetic First Add", FilePath: "/synthetic/first.m4b"})
	require.NoError(t, err)
	time.Sleep(20 * time.Millisecond)
	marker := time.Now().UTC().Format(time.RFC3339Nano)
	time.Sleep(20 * time.Millisecond)
	second, err := store.CreateBook(&database.Book{Title: "Synthetic Second Add", FilePath: "/synthetic/second.m4b"})
	require.NoError(t, err)
	third, err := store.CreateBook(&database.Book{Title: "Synthetic Third Add", FilePath: "/synthetic/third.m4b"})
	require.NoError(t, err)
	// Touch the first book so its UpdatedAt moves past the marker.
	first.Narrator = new("Synthetic Narrator")
	_, err = store.UpdateBook(first.ID, first)
	require.NoError(t, err)

	ago := func(d int) time.Time { return time.Now().UTC().AddDate(0, 0, -d) }
	// admin's own play history; bob's row on the third book must not be read.
	require.NoError(t, store.SetUserBookState(&database.UserBookState{
		UserID: admin.ID, BookID: first.ID, LastActivityAt: ago(5), Status: database.UserBookStatusInProgress}))
	require.NoError(t, store.SetUserBookState(&database.UserBookState{
		UserID: admin.ID, BookID: second.ID, LastActivityAt: ago(200), Status: database.UserBookStatusFinished}))
	require.NoError(t, store.SetUserBookState(&database.UserBookState{
		UserID: "bob", BookID: third.ID, LastActivityAt: ago(1), Status: database.UserBookStatusInProgress}))

	list := func(filters string) (titles []string, code int, body string) {
		t.Helper()
		req := httptest.NewRequest(http.MethodGet, "/api/v1/audiobooks?limit=50&filters="+url.QueryEscape(filters), nil)
		req.Header.Set("Authorization", "Bearer "+adminToken)
		w := httptest.NewRecorder()
		srv.router.ServeHTTP(w, req)
		if w.Code != http.StatusOK {
			return nil, w.Code, w.Body.String()
		}
		var env struct {
			Data struct {
				Items []struct {
					Title string `json:"title"`
				} `json:"items"`
			} `json:"data"`
		}
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &env), w.Body.String())
		for _, it := range env.Data.Items {
			titles = append(titles, it.Title)
		}
		return titles, w.Code, w.Body.String()
	}
	added := []string{first.Title, second.Title, third.Title} // the three created below
	every := append([]string{seed.Title}, added...)

	for _, tc := range []struct {
		name    string
		filters string
		want    []string
	}{
		{"date_added after a marker", `[{"field":"date_added","value":">` + marker + `"}]`, []string{second.Title, third.Title}},
		{"date_added before a marker, by alias", `[{"field":"added","value":"<` + marker + `"}]`, []string{seed.Title, first.Title}},
		{"date_modified after a marker", `[{"field":"date_modified","value":">` + marker + `"}]`, added},
		{"date_modified before the marker, by alias", `[{"field":"modified","value":"<` + marker + `"}]`, []string{seed.Title}},
		{"date_added:>-1y keeps books added in the last year", `[{"field":"date_added","value":">-1y"}]`, every},
		{"date_added:<-1y keeps none of them", `[{"field":"date_added","value":"<-1y"}]`, nil},
		{"a relative range ending now", `[{"field":"date_added","value":"-1y..now"}]`, every},
		{"a negated relative filter", `[{"field":"date_added","value":">-1y","negated":true}]`, nil},
		{"last_played:>-30d is a date, not presence", `[{"field":"last_played","value":">-30d"}]`, []string{first.Title}},
		{"last_played:-30d..now", `[{"field":"last_played","value":"-30d..now"}]`, []string{first.Title}},
		{"-last_played:>-6m includes books never played by this caller", `[{"field":"last_played","value":">-6m","negated":true}]`, []string{seed.Title, second.Title, third.Title}},
		{"last_played:* is ever played", `[{"field":"last_played","value":"*"}]`, []string{first.Title, second.Title}},
		{"read_status:unstarted matches a book with no row for the caller", `[{"field":"read_status","value":"unstarted"}]`, []string{seed.Title, third.Title}},
		{"read_status:(a|b) is the union of both statuses", `[{"field":"read_status","value":"(finished|in_progress)"}]`, []string{first.Title, second.Title}},
		{"read_status:a|b without parentheses is the same union", `[{"field":"read_status","value":"finished|in_progress"}]`, []string{first.Title, second.Title}},
		{"a single read_status still works", `[{"field":"read_status","value":"finished"}]`, []string{second.Title}},
		{"a negated alternation keeps the rest", `[{"field":"read_status","value":"finished|in_progress","negated":true}]`, []string{seed.Title, third.Title}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, code, body := list(tc.filters)
			require.Equal(t, http.StatusOK, code, body)
			require.ElementsMatch(t, tc.want, got, body)
		})
	}

	t.Run("an unsigned offset is a 400 that shows the signed spelling", func(t *testing.T) {
		for _, f := range []string{
			`[{"field":"date_added","value":">30d"}]`,
			`[{"field":"last_played","value":"30d"}]`,
		} {
			_, code, body := list(f)
			require.Equal(t, http.StatusBadRequest, code, body)
			require.Contains(t, body, "-30d")
		}
	})
	t.Run("an unknown status in a read_status alternation is a 400 that names it", func(t *testing.T) {
		for _, v := range []string{"(finished|bogus)", "finished|bogus"} {
			_, code, body := list(`[{"field":"read_status","value":"` + v + `"}]`)
			require.Equal(t, http.StatusBadRequest, code, body)
			require.Contains(t, body, "bogus")
		}
	})
	t.Run("a malformed date is a 400, not an empty list", func(t *testing.T) {
		_, code, body := list(`[{"field":"date_modified","value":"yesterday"}]`)
		require.Equal(t, http.StatusBadRequest, code, body)
		require.Contains(t, body, "invalid date")
	})
}
