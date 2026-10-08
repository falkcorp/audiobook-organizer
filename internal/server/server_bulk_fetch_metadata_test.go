// file: internal/server/server_bulk_fetch_metadata_test.go
// version: 1.3.1
// guid: 2b1c0d9e-8f7a-6b5c-4d3e-2f1a0b9c8d7e
// last-edited: 2026-10-06

package server

import (
	"bytes"
	json "encoding/json/v2"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestBulkFetchMetadata_MixedResults(t *testing.T) {
	server, cleanup := setupTestServer(t)
	defer cleanup()

	// Stub Audible (Open Library and Google Books are review-only and never
	// applied by the bulk fetch, owner decision 2026-10-06).
	aud := testutil.MockAudibleServer(t, func(title string) []testutil.AudibleTestProduct {
		if title == "NoResults" {
			return nil
		}
		return []testutil.AudibleTestProduct{{
			ASIN: "B0TESTBLK1", Title: title, Authors: []string{"Meta Author"},
			Publisher: "Meta Pub", Language: "eng", ReleaseDate: "2020-01-01",
		}}
	})
	useOnlyAudible(t, aud.URL)

	// Book that should update (has missing publisher/language/year/isbn/author).
	tempFile := filepath.Join(t.TempDir(), "bulk1.m4b")
	require.NoError(t, os.WriteFile(tempFile, []byte("audio"), 0o644))
	book1, err := database.GetGlobalStore().CreateBook(&database.Book{Title: "Book One", FilePath: tempFile, Format: "m4b"})
	require.NoError(t, err)

	// Book with missing title.
	tempFile2 := filepath.Join(t.TempDir(), "bulk2.m4b")
	require.NoError(t, os.WriteFile(tempFile2, []byte("audio"), 0o644))
	book2, err := database.GetGlobalStore().CreateBook(&database.Book{Title: "", FilePath: tempFile2, Format: "m4b"})
	require.NoError(t, err)

	// Book whose title returns no results.
	tempFile3 := filepath.Join(t.TempDir(), "bulk3.m4b")
	require.NoError(t, os.WriteFile(tempFile3, []byte("audio"), 0o644))
	book3, err := database.GetGlobalStore().CreateBook(&database.Book{Title: "NoResults", FilePath: tempFile3, Format: "m4b"})
	require.NoError(t, err)

	payload := map[string]any{
		"book_ids": []string{book1.ID, book2.ID, "missing-id", book3.ID},
	}
	body, err := json.Marshal(payload)
	require.NoError(t, err)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/metadata/bulk-fetch", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	server.router.ServeHTTP(w, req)
	require.Equal(t, http.StatusOK, w.Code)

	var resp struct {
		Data struct {
			UpdatedCount int `json:"updated_count"`
			TotalCount   int `json:"total_count"`
			Results      []struct {
				BookID  string `json:"book_id"`
				Status  string `json:"status"`
				Message string `json:"message"`
			} `json:"results"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	assert.Equal(t, 4, resp.Data.TotalCount)
	assert.GreaterOrEqual(t, resp.Data.UpdatedCount, 1)

	byID := map[string]struct {
		Status  string
		Message string
	}{}
	for _, r := range resp.Data.Results {
		byID[r.BookID] = struct {
			Status  string
			Message string
		}{Status: r.Status, Message: r.Message}
	}

	assert.Equal(t, "updated", byID[book1.ID].Status)
	assert.Equal(t, "skipped", byID[book2.ID].Status)
	assert.Equal(t, "missing title", byID[book2.ID].Message)
	assert.Equal(t, "not_found", byID["missing-id"].Status)
	assert.Equal(t, "no metadata found from any source", byID[book3.ID].Message)
}

func TestBulkFetchMetadata_OnlyMissingFalse_AllowsOverwrite(t *testing.T) {
	server, cleanup := setupTestServer(t)
	defer cleanup()

	// Stub Audible with a different publisher.
	aud := testutil.MockAudibleServer(t, func(title string) []testutil.AudibleTestProduct {
		return []testutil.AudibleTestProduct{{
			ASIN: "B0TESTBLK2", Title: title, Authors: []string{"Meta Author"},
			Publisher: "Overwrite Pub", Language: "eng", ReleaseDate: "2020-01-01",
		}}
	})
	useOnlyAudible(t, aud.URL)

	tempFile := filepath.Join(t.TempDir(), "bulk-overwrite.m4b")
	require.NoError(t, os.WriteFile(tempFile, []byte("audio"), 0o644))
	existingPublisher := "Existing Pub"
	book, err := database.GetGlobalStore().CreateBook(&database.Book{Title: "Book One", FilePath: tempFile, Format: "m4b", Publisher: &existingPublisher})
	require.NoError(t, err)

	payload := map[string]any{
		"book_ids":     []string{book.ID},
		"only_missing": false,
	}
	body, err := json.Marshal(payload)
	require.NoError(t, err)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/metadata/bulk-fetch", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	server.router.ServeHTTP(w, req)
	require.Equal(t, http.StatusOK, w.Code)

	updated, err := database.GetGlobalStore().GetBookByID(book.ID)
	require.NoError(t, err)
	require.NotNil(t, updated)
	require.NotNil(t, updated.Publisher)
	assert.Equal(t, "Overwrite Pub", *updated.Publisher)
}

// The bulk path records the author join as it stood before the apply, so undo
// puts the author back together with its credits. It passed nil, so the
// author could only be reverted with the join left as the apply made it.
func TestBulkFetchMetadata_UndoRestoresAuthorAndCredits(t *testing.T) {
	server, cleanup := setupTestServer(t)
	defer cleanup()

	aud := testutil.MockAudibleServer(t, func(string) []testutil.AudibleTestProduct {
		return []testutil.AudibleTestProduct{{ASIN: "B0TESTBLK3", Title: "Bulk Book", Authors: []string{"Bulk Author"}}}
	})
	useOnlyAudible(t, aud.URL)

	store := database.GetGlobalStore()
	tempFile := filepath.Join(t.TempDir(), "bulk-author.m4b")
	require.NoError(t, os.WriteFile(tempFile, []byte("audio"), 0o644))
	book, err := store.CreateBook(&database.Book{Title: "Bulk Book", FilePath: tempFile, Format: "m4b"})
	require.NoError(t, err)

	body, err := json.Marshal(map[string]any{"book_ids": []string{book.ID}})
	require.NoError(t, err)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/metadata/bulk-fetch", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	server.router.ServeHTTP(w, req)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	applied, err := store.GetBookByID(book.ID)
	require.NoError(t, err)
	require.NotNil(t, applied.AuthorID, "precondition: the bulk apply set the author")

	req = httptest.NewRequest(http.MethodPost, "/api/v1/audiobooks/"+book.ID+"/undo-last-apply", nil)
	w = httptest.NewRecorder()
	server.router.ServeHTTP(w, req)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	var resp struct {
		Data struct {
			Undone      []string `json:"undone_fields"`
			CreditsLeft bool     `json:"author_credits_left"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	assert.Contains(t, resp.Data.Undone, "author_name")
	assert.False(t, resp.Data.CreditsLeft, "the join must be restored with the author")

	got, err := store.GetBookByID(book.ID)
	require.NoError(t, err)
	assert.Nil(t, got.AuthorID)
	credits, err := store.GetBookAuthors(book.ID)
	require.NoError(t, err)
	assert.Empty(t, credits)
}

// An edit that commits while the bulk fetch is searching the provider survives
// the apply. The handler used to UpdateBook the whole row it read before the
// search, which reverted the edit, and recorded history from that in-memory
// row.
func TestBulkFetchMetadata_KeepsAnEditMadeDuringTheSearch(t *testing.T) {
	server, cleanup := setupTestServer(t)
	defer cleanup()

	store := database.GetGlobalStore()
	var bookID string
	aud := testutil.MockAudibleServer(t, func(string) []testutil.AudibleTestProduct {
		// The user saves the book page while the provider is answering.
		if cur, err := store.GetBookByID(bookID); err == nil && cur != nil && cur.Publisher == nil {
			pub := "User Pub"
			cur.Publisher = &pub
			_, _ = store.UpdateBook(bookID, cur)
		}
		return []testutil.AudibleTestProduct{{ASIN: "B0TESTBLK4", Title: "Edit Race Book", Authors: []string{"Race Author"}}}
	})
	useOnlyAudible(t, aud.URL)

	tempFile := filepath.Join(t.TempDir(), "edit-race.m4b")
	require.NoError(t, os.WriteFile(tempFile, []byte("audio"), 0o644))
	book, err := store.CreateBook(&database.Book{Title: "Edit Race Book", FilePath: tempFile, Format: "m4b"})
	require.NoError(t, err)
	bookID = book.ID

	body, err := json.Marshal(map[string]any{"book_ids": []string{book.ID}})
	require.NoError(t, err)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/metadata/bulk-fetch", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	server.router.ServeHTTP(w, req)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())

	got, err := store.GetBookByID(book.ID)
	require.NoError(t, err)
	require.NotNil(t, got.AuthorID, "precondition: the bulk apply set the author")
	require.NotNil(t, got.Publisher, "the edit made during the search must survive")
	assert.Equal(t, "User Pub", *got.Publisher)

	history, err := store.GetBookChangeHistory(book.ID, 100)
	require.NoError(t, err)
	for _, h := range history {
		assert.NotEqual(t, "publisher", h.Field, "the apply did not change publisher: %+v", h)
	}
}
