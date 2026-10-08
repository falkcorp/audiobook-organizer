// file: internal/server/server_undo_test.go
// version: 1.4.0
// guid: a1b2c3d4-e5f6-7890-abcd-ef1234567890
// last-edited: 2026-09-13

package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/metafetch"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ---------- undoLastApply handler tests ----------

func TestUndoLastApply_NoHistory(t *testing.T) {
	server, cleanup := setupTestServer(t)
	defer cleanup()

	// Create a book with no change history
	tempFile := filepath.Join(t.TempDir(), "undo-no-history.m4b")
	require.NoError(t, os.WriteFile(tempFile, []byte("audio"), 0o644))
	book, err := database.GetGlobalStore().CreateBook(&database.Book{
		Title:    "No History Book",
		FilePath: tempFile,
		Format:   "m4b",
	})
	require.NoError(t, err)

	req := httptest.NewRequest(http.MethodPost, fmt.Sprintf("/api/v1/audiobooks/%s/undo-last-apply", book.ID), nil)
	w := httptest.NewRecorder()
	server.router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusNotFound, w.Code)

	var resp map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	errMsg, _ := resp["error"].(string)
	assert.Contains(t, errMsg, "not found")
}

func TestUndoLastApply_OnlyUndoRecords(t *testing.T) {
	server, cleanup := setupTestServer(t)
	defer cleanup()

	// Create a book
	tempFile := filepath.Join(t.TempDir(), "undo-only-undos.m4b")
	require.NoError(t, os.WriteFile(tempFile, []byte("audio"), 0o644))
	book, err := database.GetGlobalStore().CreateBook(&database.Book{
		Title:    "Only Undos Book",
		FilePath: tempFile,
		Format:   "m4b",
	})
	require.NoError(t, err)

	// Insert only undo-type records
	oldVal := `"Old Title"`
	newVal := `"New Title"`
	require.NoError(t, database.GetGlobalStore().RecordMetadataChange(&database.MetadataChangeRecord{
		BookID:        book.ID,
		Field:         "title",
		PreviousValue: &oldVal,
		NewValue:      &newVal,
		ChangeType:    "undo",
		Source:        "bulk-search-undo",
		ChangedAt:     time.Now(),
	}))

	req := httptest.NewRequest(http.MethodPost, fmt.Sprintf("/api/v1/audiobooks/%s/undo-last-apply", book.ID), nil)
	w := httptest.NewRecorder()
	server.router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusNotFound, w.Code)

	var resp map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	errMsg, _ := resp["error"].(string)
	assert.Contains(t, errMsg, "not found")
}

// createApplyBook makes a book whose publisher/description are empty and
// whose title is file-derived junk, so the apply's IsBetter checks take the
// candidate's values.
func createApplyBook(t *testing.T, name string) *database.Book {
	t.Helper()
	tempFile := filepath.Join(t.TempDir(), name+".m4b")
	require.NoError(t, os.WriteFile(tempFile, []byte("audio"), 0o644))
	book, err := database.GetGlobalStore().CreateBook(&database.Book{
		Title:    "track01",
		FilePath: tempFile,
		Format:   "m4b",
	})
	require.NoError(t, err)
	return book
}

func applyCandidate(t *testing.T, s *Server, bookID string, cand metafetch.MetadataCandidate) {
	t.Helper()
	_, err := s.metadataFetchService.ApplyMetadataCandidate(bookID, cand, nil)
	require.NoError(t, err)
}

func postUndoLastApply(t *testing.T, s *Server, bookID string) (int, map[string]any) {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, fmt.Sprintf("/api/v1/audiobooks/%s/undo-last-apply", bookID), nil)
	w := httptest.NewRecorder()
	s.router.ServeHTTP(w, req)
	var wrapper struct {
		Data map[string]any `json:"data"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &wrapper)
	return w.Code, wrapper.Data
}

func mustGetBook(t *testing.T, id string) *database.Book {
	t.Helper()
	b, err := database.GetGlobalStore().GetBookByID(id)
	require.NoError(t, err)
	require.NotNil(t, b)
	return b
}

// Undo-last-apply puts the BOOK ROW back and leaves no override behind. It
// used to only write the pre-apply values into the override state: the book
// kept the applied values, and HasUserOverride then froze them.
func TestUndoLastApply_RevertsBatch(t *testing.T) {
	server, cleanup := setupTestServer(t)
	defer cleanup()

	book := createApplyBook(t, "undo-batch")
	applyCandidate(t, server, book.ID, metafetch.MetadataCandidate{
		Title: "The Applied Title", Publisher: "Applied Pub", Description: "Applied description", Source: "Open Library",
	})
	applied := mustGetBook(t, book.ID)
	require.Equal(t, "The Applied Title", applied.Title, "precondition: the apply landed")
	require.NotNil(t, applied.Publisher)

	code, resp := postUndoLastApply(t, server, book.ID)
	require.Equal(t, http.StatusOK, code, "resp %v", resp)
	assert.ElementsMatch(t, []any{"title", "publisher", "description"}, filterStrings(resp["undone_fields"], "title", "publisher", "description"))

	got := mustGetBook(t, book.ID)
	assert.Equal(t, "track01", got.Title)
	assert.Empty(t, derefStr(got.Publisher))
	assert.Empty(t, derefStr(got.Description), "description must be back to empty")

	states, err := database.GetGlobalStore().GetMetadataFieldStates(book.ID)
	require.NoError(t, err)
	for _, st := range states {
		assert.Nil(t, st.OverrideValue, "undo must not create an override on %s", st.Field)
		assert.False(t, st.OverrideLocked, "undo must not lock %s", st.Field)
	}
}

func filterStrings(v any, keep ...string) []any {
	list, _ := v.([]any)
	var out []any
	for _, x := range list {
		for _, k := range keep {
			if x == k {
				out = append(out, x)
			}
		}
	}
	return out
}

// A field edited after the apply is left as the user set it; the others are
// still reverted.
func TestUndoLastApply_LeavesFieldChangedSince(t *testing.T) {
	server, cleanup := setupTestServer(t)
	defer cleanup()

	book := createApplyBook(t, "undo-changed-since")
	applyCandidate(t, server, book.ID, metafetch.MetadataCandidate{
		Title: "The Applied Title", Publisher: "Applied Pub", Source: "Open Library",
	})
	edited := mustGetBook(t, book.ID)
	userPub := "User Pub"
	edited.Publisher = &userPub
	_, err := database.GetGlobalStore().UpdateBook(book.ID, edited)
	require.NoError(t, err)

	code, resp := postUndoLastApply(t, server, book.ID)
	require.Equal(t, http.StatusOK, code, "resp %v", resp)
	assert.Contains(t, resp["changed_since_fields"], "publisher")

	got := mustGetBook(t, book.ID)
	assert.Equal(t, "track01", got.Title)
	require.NotNil(t, got.Publisher)
	assert.Equal(t, "User Pub", *got.Publisher)
}

// Two applies in a row: undo reverts only the last one, by batch id, however
// close together they ran.
func TestUndoLastApply_UndoesOnlyTheLatestApply(t *testing.T) {
	server, cleanup := setupTestServer(t)
	defer cleanup()

	book := createApplyBook(t, "undo-latest")
	applyCandidate(t, server, book.ID, metafetch.MetadataCandidate{Publisher: "Pub One", Source: "Open Library"})
	applyCandidate(t, server, book.ID, metafetch.MetadataCandidate{Description: "Desc two", Source: "Audible"})

	code, resp := postUndoLastApply(t, server, book.ID)
	require.Equal(t, http.StatusOK, code, "resp %v", resp)

	got := mustGetBook(t, book.ID)
	assert.Empty(t, derefStr(got.Description), "the second apply is undone")
	require.NotNil(t, got.Publisher, "the first apply is kept")
	assert.Equal(t, "Pub One", *got.Publisher)
}

// A second undo does not walk back to the apply before the last one: the
// newest apply is already undone, so it is refused and the older apply stays.
// It used to skip undone batches and revert the previous apply.
func TestUndoLastApply_SecondCallIsRefused(t *testing.T) {
	server, cleanup := setupTestServer(t)
	defer cleanup()

	book := createApplyBook(t, "undo-twice")
	applyCandidate(t, server, book.ID, metafetch.MetadataCandidate{Publisher: "Pub One", Source: "Open Library"})
	applyCandidate(t, server, book.ID, metafetch.MetadataCandidate{Description: "Desc two", Source: "Audible"})

	code, resp := postUndoLastApply(t, server, book.ID)
	require.Equal(t, http.StatusOK, code, "resp %v", resp)
	code, resp = postUndoLastApply(t, server, book.ID)
	require.Equal(t, http.StatusConflict, code, "the second undo must be refused, resp %v", resp)

	got := mustGetBook(t, book.ID)
	assert.Empty(t, derefStr(got.Description))
	require.NotNil(t, got.Publisher, "the older apply must be kept")
	assert.Equal(t, "Pub One", *got.Publisher)
}

// An author row whose book_authors join was not recorded is refused: putting
// author_id back alone would leave the join naming the applied author.
func TestUndoLastApply_AuthorNeverRevertedAlone(t *testing.T) {
	server, cleanup := setupTestServer(t)
	defer cleanup()

	store := database.GetGlobalStore()
	author, err := store.CreateAuthor("Applied Author")
	require.NoError(t, err)
	book := createApplyBook(t, "undo-author-alone")
	book.AuthorID = &author.ID
	_, err = store.UpdateBook(book.ID, book)
	require.NoError(t, err)
	oldVal, newVal := `""`, `"Applied Author"`
	require.NoError(t, store.RecordMetadataChange(&database.MetadataChangeRecord{
		BookID: book.ID, Field: "author_name", PreviousValue: &oldVal, NewValue: &newVal,
		PreviousRef: &database.MetadataChangeRef{}, NewRef: &database.MetadataChangeRef{AuthorID: &author.ID},
		ChangeType: "fetched", Source: "Open Library", ChangedAt: time.Now(), BatchID: "apply-authoralone",
	}))

	code, resp := postUndoLastApply(t, server, book.ID)
	require.Equal(t, http.StatusOK, code, "resp %v", resp)
	assert.Contains(t, resp["failed_fields"], "author_name")
	got := mustGetBook(t, book.ID)
	require.NotNil(t, got.AuthorID, "author_id must not be reverted without its join")
	assert.Equal(t, author.ID, *got.AuthorID)
}

// History rows written before batch ids existed cannot be grouped into one
// apply; guessing (the old ±2s window) is refused.
func TestUndoLastApply_LegacyRowsAreRefused(t *testing.T) {
	server, cleanup := setupTestServer(t)
	defer cleanup()

	book := createApplyBook(t, "undo-legacy")
	oldVal, newVal := `"Before"`, `"After"`
	require.NoError(t, database.GetGlobalStore().RecordMetadataChange(&database.MetadataChangeRecord{
		BookID: book.ID, Field: "title", PreviousValue: &oldVal, NewValue: &newVal,
		ChangeType: "fetched", Source: "Open Library", ChangedAt: time.Now(),
	}))

	code, _ := postUndoLastApply(t, server, book.ID)
	assert.Equal(t, http.StatusConflict, code)
	assert.Equal(t, "track01", mustGetBook(t, book.ID).Title)
}

// History is recorded from what the apply actually wrote, after it
// committed: a title the IsBetter checks refused is not recorded, and a field
// outside the old nine-field list (description) is.
func TestApplyHistory_RecordsOnlyWhatTheApplyWrote(t *testing.T) {
	server, cleanup := setupTestServer(t)
	defer cleanup()

	tempFile := filepath.Join(t.TempDir(), "history.m4b")
	require.NoError(t, os.WriteFile(tempFile, []byte("audio"), 0o644))
	book, err := database.GetGlobalStore().CreateBook(&database.Book{Title: "The Real Long Title", FilePath: tempFile, Format: "m4b"})
	require.NoError(t, err)

	applyCandidate(t, server, book.ID, metafetch.MetadataCandidate{Title: "Ab", Description: "New description", Source: "Open Library"})
	require.Equal(t, "The Real Long Title", mustGetBook(t, book.ID).Title, "precondition: the short title is refused")

	history, err := database.GetGlobalStore().GetBookChangeHistory(book.ID, 1000)
	require.NoError(t, err)
	var sawDescription bool
	for _, r := range history {
		assert.NotEqual(t, "title", r.Field, "a refused title must not be recorded as applied")
		if r.Field == "description" {
			sawDescription = true
			assert.NotEmpty(t, r.BatchID)
		}
	}
	assert.True(t, sawDescription, "the applied description must be recorded")
}

func derefStr(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}
