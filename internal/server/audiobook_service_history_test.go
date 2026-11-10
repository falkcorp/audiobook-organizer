// file: internal/server/audiobook_service_history_test.go
// version: 1.0.1
// guid: d4e5f6a7-b8c9-0d1e-2f3a-4b5c6d7e8f9a

package server

import (
	"context"
	"encoding/json"
	"testing"

	database "github.com/falkcorp/audiobook-organizer/internal/books/store"
	"github.com/oklog/ulid/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestUpdateAudiobook_FieldExtractorRecordsHistory verifies that updating the title
// through UpdateAudiobook creates a metadata_changes_history entry.
func TestUpdateAudiobook_FieldExtractorRecordsHistory(t *testing.T) {
	_, cleanup := setupTestServer(t)
	defer cleanup()

	store := database.GetGlobalStore()

	bookID := ulid.Make().String()
	book := &database.Book{
		ID:       bookID,
		Title:    "Before Title",
		FilePath: "/tmp/history_test.m4b",
		Format:   "m4b",
	}
	_, err := store.CreateBook(book)
	require.NoError(t, err)

	svc := NewAudiobookService(store)

	req := &UpdateAudiobookRequest{
		Updates: &AudiobookUpdate{
			Book: &database.Book{
				Title: "After Title",
			},
		},
		RawPayload: map[string]json.RawMessage{
			"title": json.RawMessage(`"After Title"`),
		},
	}
	_, err = svc.UpdateAudiobook(context.Background(), bookID, req)
	require.NoError(t, err)

	// Verify a history entry was recorded.
	history, err := store.GetBookChangeHistory(bookID, 100)
	require.NoError(t, err)
	require.NotEmpty(t, history, "expected at least one history entry after title change")

	var found bool
	for _, h := range history {
		if h.Field == "title" {
			found = true
			if h.NewValue != nil {
				assert.Contains(t, *h.NewValue, "After Title")
			}
		}
	}
	assert.True(t, found, "expected a history entry for the 'title' field")
}

// TestUpdateAudiobook_NoHistoryWhenValueUnchanged verifies that saving the same
// title twice does NOT create a duplicate history entry.
func TestUpdateAudiobook_NoHistoryWhenValueUnchanged(t *testing.T) {
	_, cleanup := setupTestServer(t)
	defer cleanup()

	store := database.GetGlobalStore()

	bookID := ulid.Make().String()
	book := &database.Book{
		ID:       bookID,
		Title:    "Stable Title",
		FilePath: "/tmp/stable.m4b",
		Format:   "m4b",
	}
	_, err := store.CreateBook(book)
	require.NoError(t, err)

	svc := NewAudiobookService(store)
	req := &UpdateAudiobookRequest{
		Updates: &AudiobookUpdate{
			Book: &database.Book{
				Title: "Stable Title",
			},
		},
		RawPayload: map[string]json.RawMessage{
			"title": json.RawMessage(`"Stable Title"`),
		},
	}

	// First save
	_, err = svc.UpdateAudiobook(context.Background(), bookID, req)
	require.NoError(t, err)

	historyBefore, _ := store.GetBookChangeHistory(bookID, 100)
	count1 := len(historyBefore)

	// Second save with same value
	_, err = svc.UpdateAudiobook(context.Background(), bookID, req)
	require.NoError(t, err)

	historyAfter, _ := store.GetBookChangeHistory(bookID, 100)
	count2 := len(historyAfter)

	assert.Equal(t, count1, count2, "no new history entries when value is unchanged")
}
