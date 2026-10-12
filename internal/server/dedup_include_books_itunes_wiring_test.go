// file: internal/server/dedup_include_books_itunes_wiring_test.go
// version: 1.0.0
// guid: b4e81c07-5d92-4a63-9f1e-27c0a8d3e514
// last-edited: 2026-10-11

// The Duplicates lane's include_books listing computes is_itunes from the
// store's book_file rows and external ids. The handler tests give it a mock
// store; this one drives the real router with the store storeForWiring hands
// dedup (a real Pebble store, memdb reads included), so a wiring that stopped
// providing those two reads, or a store that answered them differently from the
// mock, fails here.

package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

func TestDedupListCandidates_IncludeBooks_ITunesFlag_RealStore(t *testing.T) {
	server, adminToken, _, _, cleanup := setupUserTagsAuthzTestServer(t)
	defer cleanup()
	require.NotNil(t, server.embeddingStore, "the test server must have an embedding store")
	store := server.store

	mk := func(title, path string) *database.Book {
		b, err := store.CreateBook(&database.Book{Title: title, FilePath: path})
		require.NoError(t, err)
		return b
	}
	// viaExt is an iTunes copy ONLY through a live external id; viaRow only
	// through a book_file row's persistent id; plain is neither.
	viaExt := mk("Via External ID", "/lib/ext.m4b")
	viaRow := mk("Via Row", "/lib/row.m4b")
	plain := mk("Plain", "/lib/plain.m4b")
	require.NoError(t, store.CreateExternalIDMapping(&database.ExternalIDMapping{
		Source: "itunes", ExternalID: "0123456789ABCDEF", BookID: viaExt.ID,
	}))
	require.NoError(t, store.CreateBookFile(&database.BookFile{
		ID: "f-row", BookID: viaRow.ID, FilePath: "/lib/row.m4b", ITunesPersistentID: "FEDCBA9876543210",
	}))

	sim := 0.95
	for _, pair := range [][2]string{{viaExt.ID, plain.ID}, {viaRow.ID, plain.ID}} {
		require.NoError(t, server.embeddingStore.UpsertCandidate(database.DedupCandidate{
			EntityType: "book", EntityAID: pair[0], EntityBID: pair[1],
			Layer: "embedding", Similarity: &sim, Status: "pending",
		}))
	}

	req := httptest.NewRequest(http.MethodGet, "/api/v1/dedup/candidates?include_books=true", nil)
	req.Header.Set("Authorization", "Bearer "+adminToken)
	w := httptest.NewRecorder()
	server.router.ServeHTTP(w, req)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())

	var resp struct {
		Data struct {
			Candidates []map[string]any `json:"candidates"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	require.Len(t, resp.Data.Candidates, 2)
	got := map[string]bool{}
	for _, row := range resp.Data.Candidates {
		for _, side := range []string{"book_a", "book_b"} {
			b, ok := row[side].(map[string]any)
			require.True(t, ok, "%s missing: %v", side, row)
			flag, ok := b["is_itunes"].(bool)
			require.True(t, ok, "%s.is_itunes must be present and boolean: %v", side, b)
			got[b["id"].(string)] = flag
		}
	}
	require.Equal(t, map[string]bool{viaExt.ID: true, viaRow.ID: true, plain.ID: false}, got)
}
