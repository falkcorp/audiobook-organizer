// file: internal/server/server_undo_test.go
// version: 1.2.0
// guid: a1b2c3d4-e5f6-7890-abcd-ef1234567890

package server

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	itunesservice "github.com/falkcorp/audiobook-organizer/internal/itunes/service"

	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/database"
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

func TestUndoLastApply_RevertsBatch(t *testing.T) {
	server, cleanup := setupTestServer(t)
	defer cleanup()

	// Create a book
	tempFile := filepath.Join(t.TempDir(), "undo-batch.m4b")
	require.NoError(t, os.WriteFile(tempFile, []byte("audio"), 0o644))
	book, err := database.GetGlobalStore().CreateBook(&database.Book{
		Title:    "Batch Undo Book",
		FilePath: tempFile,
		Format:   "m4b",
	})
	require.NoError(t, err)

	// Record a batch of changes (all within 2 seconds of each other)
	batchTime := time.Now()
	oldTitle := `"Original Title"`
	newTitle := `"Updated Title"`
	oldAuthor := `"Original Author"`
	newAuthor := `"Updated Author"`

	require.NoError(t, database.GetGlobalStore().RecordMetadataChange(&database.MetadataChangeRecord{
		BookID:        book.ID,
		Field:         "title",
		PreviousValue: &oldTitle,
		NewValue:      &newTitle,
		ChangeType:    "fetched",
		Source:        "Open Library",
		ChangedAt:     batchTime,
	}))
	require.NoError(t, database.GetGlobalStore().RecordMetadataChange(&database.MetadataChangeRecord{
		BookID:        book.ID,
		Field:         "author",
		PreviousValue: &oldAuthor,
		NewValue:      &newAuthor,
		ChangeType:    "fetched",
		Source:        "Open Library",
		ChangedAt:     batchTime.Add(500 * time.Millisecond),
	}))

	// Ensure server.writeBackBatcher is nil so we don't trigger actual write-back
	origBatcher := server.writeBackBatcher
	server.writeBackBatcher = nil
	defer func() { server.writeBackBatcher = origBatcher }()

	req := httptest.NewRequest(http.MethodPost, fmt.Sprintf("/api/v1/audiobooks/%s/undo-last-apply", book.ID), nil)
	w := httptest.NewRecorder()
	server.router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)

	var wrapper struct {
		Data map[string]any `json:"data"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &wrapper))
	resp := wrapper.Data
	assert.Contains(t, resp["message"], "2 field(s)")

	undoneFields, ok := resp["undone_fields"].([]any)
	require.True(t, ok)
	assert.Len(t, undoneFields, 2)

	// Verify undo records were written to history
	history, err := database.GetGlobalStore().GetBookChangeHistory(book.ID, 50)
	require.NoError(t, err)

	undoCount := 0
	for _, rec := range history {
		if rec.ChangeType == "undo" {
			undoCount++
			assert.Equal(t, "bulk-search-undo", rec.Source)
		}
	}
	assert.Equal(t, 2, undoCount, "expected 2 undo records in history")
}

func TestUndoLastApply_SkipsOldChanges(t *testing.T) {
	server, cleanup := setupTestServer(t)
	defer cleanup()

	// Create a book
	tempFile := filepath.Join(t.TempDir(), "undo-skip-old.m4b")
	require.NoError(t, os.WriteFile(tempFile, []byte("audio"), 0o644))
	book, err := database.GetGlobalStore().CreateBook(&database.Book{
		Title:    "Skip Old Changes Book",
		FilePath: tempFile,
		Format:   "m4b",
	})
	require.NoError(t, err)

	// Record an old change (more than 2 seconds before the batch)
	oldTime := time.Now().Add(-10 * time.Second)
	oldPublisher := `"Old Publisher"`
	newPublisher := `"New Publisher"`
	require.NoError(t, database.GetGlobalStore().RecordMetadataChange(&database.MetadataChangeRecord{
		BookID:        book.ID,
		Field:         "publisher",
		PreviousValue: &oldPublisher,
		NewValue:      &newPublisher,
		ChangeType:    "fetched",
		Source:        "Open Library",
		ChangedAt:     oldTime,
	}))

	// Record a recent change
	recentTime := time.Now()
	oldTitle := `"Title A"`
	newTitle := `"Title B"`
	require.NoError(t, database.GetGlobalStore().RecordMetadataChange(&database.MetadataChangeRecord{
		BookID:        book.ID,
		Field:         "title",
		PreviousValue: &oldTitle,
		NewValue:      &newTitle,
		ChangeType:    "fetched",
		Source:        "Open Library",
		ChangedAt:     recentTime,
	}))

	origBatcher := server.writeBackBatcher
	server.writeBackBatcher = nil
	defer func() { server.writeBackBatcher = origBatcher }()

	req := httptest.NewRequest(http.MethodPost, fmt.Sprintf("/api/v1/audiobooks/%s/undo-last-apply", book.ID), nil)
	w := httptest.NewRecorder()
	server.router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)

	var wrapper struct {
		Data map[string]any `json:"data"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &wrapper))
	resp := wrapper.Data
	// Only the recent change should be undone, not the old publisher change
	assert.Contains(t, resp["message"], "1 field(s)")

	undoneFields, ok := resp["undone_fields"].([]any)
	require.True(t, ok)
	assert.Len(t, undoneFields, 1)
	assert.Equal(t, "title", undoneFields[0])
}

func TestUndoLastApply_SkipsUndoRecordsInBatchDetection(t *testing.T) {
	server, cleanup := setupTestServer(t)
	defer cleanup()

	// Create a book
	tempFile := filepath.Join(t.TempDir(), "undo-skip-undo-type.m4b")
	require.NoError(t, os.WriteFile(tempFile, []byte("audio"), 0o644))
	book, err := database.GetGlobalStore().CreateBook(&database.Book{
		Title:    "Skip Undo Type Book",
		FilePath: tempFile,
		Format:   "m4b",
	})
	require.NoError(t, err)

	now := time.Now()

	// Record a real change first
	oldTitle := `"Before"`
	newTitle := `"After"`
	require.NoError(t, database.GetGlobalStore().RecordMetadataChange(&database.MetadataChangeRecord{
		BookID:        book.ID,
		Field:         "title",
		PreviousValue: &oldTitle,
		NewValue:      &newTitle,
		ChangeType:    "fetched",
		Source:        "Open Library",
		ChangedAt:     now.Add(-5 * time.Second),
	}))

	// Record a more recent undo record (should be skipped when finding batch time)
	require.NoError(t, database.GetGlobalStore().RecordMetadataChange(&database.MetadataChangeRecord{
		BookID:        book.ID,
		Field:         "author",
		PreviousValue: &newTitle,
		NewValue:      &oldTitle,
		ChangeType:    "undo",
		Source:        "bulk-search-undo",
		ChangedAt:     now,
	}))

	origBatcher := server.writeBackBatcher
	server.writeBackBatcher = nil
	defer func() { server.writeBackBatcher = origBatcher }()

	req := httptest.NewRequest(http.MethodPost, fmt.Sprintf("/api/v1/audiobooks/%s/undo-last-apply", book.ID), nil)
	w := httptest.NewRecorder()
	server.router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)

	var wrapper struct {
		Data map[string]any `json:"data"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &wrapper))
	resp := wrapper.Data
	// Should undo the fetched title change, skipping the undo record
	assert.Contains(t, resp["message"], "1 field(s)")
}

func TestUndoLastApply_NilPreviousValueClearsOverride(t *testing.T) {
	server, cleanup := setupTestServer(t)
	defer cleanup()

	// Create a book
	tempFile := filepath.Join(t.TempDir(), "undo-nil-prev.m4b")
	require.NoError(t, os.WriteFile(tempFile, []byte("audio"), 0o644))
	book, err := database.GetGlobalStore().CreateBook(&database.Book{
		Title:    "Nil Previous Value Book",
		FilePath: tempFile,
		Format:   "m4b",
	})
	require.NoError(t, err)

	// Record a change where previous value is nil (field was empty before)
	newISBN := `"1234567890"`
	require.NoError(t, database.GetGlobalStore().RecordMetadataChange(&database.MetadataChangeRecord{
		BookID:        book.ID,
		Field:         "isbn",
		PreviousValue: nil,
		NewValue:      &newISBN,
		ChangeType:    "fetched",
		Source:        "Open Library",
		ChangedAt:     time.Now(),
	}))

	origBatcher := server.writeBackBatcher
	server.writeBackBatcher = nil
	defer func() { server.writeBackBatcher = origBatcher }()

	req := httptest.NewRequest(http.MethodPost, fmt.Sprintf("/api/v1/audiobooks/%s/undo-last-apply", book.ID), nil)
	w := httptest.NewRecorder()
	server.router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)

	var wrapper struct {
		Data map[string]any `json:"data"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &wrapper))
	resp := wrapper.Data
	assert.Contains(t, resp["message"], "1 field(s)")
}

func TestUndoLastApply_WriteBackBatcherEnqueued(t *testing.T) {
	server, cleanup := setupTestServer(t)
	defer cleanup()

	// Create a book
	tempFile := filepath.Join(t.TempDir(), "undo-writeback.m4b")
	require.NoError(t, os.WriteFile(tempFile, []byte("audio"), 0o644))
	book, err := database.GetGlobalStore().CreateBook(&database.Book{
		Title:    "WriteBack Undo Book",
		FilePath: tempFile,
		Format:   "m4b",
	})
	require.NoError(t, err)

	// Record a change to undo
	oldVal := `"Old"`
	newVal := `"New"`
	require.NoError(t, database.GetGlobalStore().RecordMetadataChange(&database.MetadataChangeRecord{
		BookID:        book.ID,
		Field:         "title",
		PreviousValue: &oldVal,
		NewValue:      &newVal,
		ChangeType:    "fetched",
		Source:        "Open Library",
		ChangedAt:     time.Now(),
	}))

	// Set up a real batcher (with auto write-back enabled)
	origBatcher := server.writeBackBatcher
	origConfig := config.AppConfig
	config.AppConfig.ITunes.AutoWriteBack = true
	config.AppConfig.ITunes.LibraryReadPath = "/fake/path.xml"
	batcher := itunesservice.NewWriteBackBatcher(1*time.Hour, itunesservice.WriteBackBatcherConfig{AutoWriteBack: true, ITLWriteBackEnabled: true, LibraryWritePath: "/tmp/test.itl"}, nil) // long delay so it won't flush
	server.writeBackBatcher = batcher
	defer func() {
		// Stop the server's fileIOPool before restoring globals to avoid races
		// with in-flight workers reading config.AppConfig.
		if server.fileIOPool != nil {
			server.fileIOPool.Stop()
		}
		// Stop pool workers before restoring globals to avoid races
		if p := GetGlobalFileIOPool(); p != nil {
			p.Stop()
			SetGlobalFileIOPool(nil)
		}
		_ = batcher.Stop(context.Background())
		server.writeBackBatcher = origBatcher
		config.AppConfig = origConfig
	}()

	req := httptest.NewRequest(http.MethodPost, fmt.Sprintf("/api/v1/audiobooks/%s/undo-last-apply", book.ID), nil)
	w := httptest.NewRecorder()
	server.router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)

	// Verify the book ID was enqueued in the batcher
	enqueued := batcher.HasPendingBook(book.ID)
	assert.True(t, enqueued, "expected book ID to be enqueued in WriteBackBatcher")
}

// ---------- applyAudiobookMetadata write_back flag tests ----------

func TestApplyAudiobookMetadata_WriteBackTrue(t *testing.T) {
	server, cleanup := setupTestServer(t)
	defer cleanup()

	// Create a book to apply metadata to
	tempFile := filepath.Join(t.TempDir(), "apply-wb-true.m4b")
	require.NoError(t, os.WriteFile(tempFile, []byte("audio"), 0o644))
	book, err := database.GetGlobalStore().CreateBook(&database.Book{
		Title:    "Apply WriteBack True",
		FilePath: tempFile,
		Format:   "m4b",
	})
	require.NoError(t, err)

	// Set up batcher
	origBatcher := server.writeBackBatcher
	origConfig := config.AppConfig
	config.AppConfig.ITunes.AutoWriteBack = true
	config.AppConfig.ITunes.LibraryReadPath = "/fake/path.xml"
	batcher := itunesservice.NewWriteBackBatcher(1*time.Hour, itunesservice.WriteBackBatcherConfig{AutoWriteBack: true, ITLWriteBackEnabled: true, LibraryWritePath: "/tmp/test.itl"}, nil)
	server.writeBackBatcher = batcher
	defer func() {
		// Stop the server's fileIOPool before restoring globals to avoid races
		// with in-flight workers reading config.AppConfig.
		if server.fileIOPool != nil {
			server.fileIOPool.Stop()
		}
		// Stop pool workers before restoring globals to avoid races
		if p := GetGlobalFileIOPool(); p != nil {
			p.Stop()
			SetGlobalFileIOPool(nil)
		}
		_ = batcher.Stop(context.Background())
		server.writeBackBatcher = origBatcher
		config.AppConfig = origConfig
	}()

	writeBack := true
	payload := map[string]any{
		"candidate": map[string]any{
			"title":  "New Title",
			"author": "New Author",
			"source": "Open Library",
			"score":  0.95,
		},
		"fields":     []string{"title", "author"},
		"write_back": writeBack,
	}
	body, err := json.Marshal(payload)
	require.NoError(t, err)

	req := httptest.NewRequest(http.MethodPost, fmt.Sprintf("/api/v1/audiobooks/%s/apply-metadata", book.ID), bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	server.router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)

	// Write-back now runs in a background goroutine — wait briefly for it to enqueue
	time.Sleep(500 * time.Millisecond)

	// Verify enqueued
	enqueued := batcher.HasPendingBook(book.ID)
	assert.True(t, enqueued, "expected book ID to be enqueued when write_back=true")
}

func TestApplyAudiobookMetadata_WriteBackOmitted(t *testing.T) {
	server, cleanup := setupTestServer(t)
	defer cleanup()

	// Create a book
	tempFile := filepath.Join(t.TempDir(), "apply-wb-omit.m4b")
	require.NoError(t, os.WriteFile(tempFile, []byte("audio"), 0o644))
	book, err := database.GetGlobalStore().CreateBook(&database.Book{
		Title:    "Apply WriteBack Omit",
		FilePath: tempFile,
		Format:   "m4b",
	})
	require.NoError(t, err)

	// Set up batcher
	origBatcher := server.writeBackBatcher
	origConfig := config.AppConfig
	config.AppConfig.ITunes.AutoWriteBack = true
	config.AppConfig.ITunes.LibraryReadPath = "/fake/path.xml"
	batcher := itunesservice.NewWriteBackBatcher(1*time.Hour, itunesservice.WriteBackBatcherConfig{AutoWriteBack: true, ITLWriteBackEnabled: true, LibraryWritePath: "/tmp/test.itl"}, nil)
	server.writeBackBatcher = batcher
	defer func() {
		// Stop the server's fileIOPool before restoring globals to avoid races
		// with in-flight workers reading config.AppConfig.
		if server.fileIOPool != nil {
			server.fileIOPool.Stop()
		}
		// Stop pool workers before restoring globals to avoid races
		if p := GetGlobalFileIOPool(); p != nil {
			p.Stop()
			SetGlobalFileIOPool(nil)
		}
		_ = batcher.Stop(context.Background())
		server.writeBackBatcher = origBatcher
		config.AppConfig = origConfig
	}()

	// Omit write_back field entirely — should default to true
	payload := map[string]any{
		"candidate": map[string]any{
			"title":  "New Title",
			"author": "New Author",
			"source": "Open Library",
			"score":  0.95,
		},
		"fields": []string{"title", "author"},
	}
	body, err := json.Marshal(payload)
	require.NoError(t, err)

	req := httptest.NewRequest(http.MethodPost, fmt.Sprintf("/api/v1/audiobooks/%s/apply-metadata", book.ID), bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	server.router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)

	// Write-back now runs in a background goroutine — wait briefly for it to enqueue
	time.Sleep(500 * time.Millisecond)

	// Verify enqueued (defaults to true)
	enqueued := batcher.HasPendingBook(book.ID)
	assert.True(t, enqueued, "expected book ID to be enqueued when write_back is omitted (defaults to true)")
}

func TestApplyAudiobookMetadata_WriteBackFalse(t *testing.T) {
	server, cleanup := setupTestServer(t)
	defer cleanup()

	// Create a book
	tempFile := filepath.Join(t.TempDir(), "apply-wb-false.m4b")
	require.NoError(t, os.WriteFile(tempFile, []byte("audio"), 0o644))
	book, err := database.GetGlobalStore().CreateBook(&database.Book{
		Title:    "Apply WriteBack False",
		FilePath: tempFile,
		Format:   "m4b",
	})
	require.NoError(t, err)

	// Set up batcher
	origBatcher := server.writeBackBatcher
	origConfig := config.AppConfig
	config.AppConfig.ITunes.AutoWriteBack = true
	config.AppConfig.ITunes.LibraryReadPath = "/fake/path.xml"
	batcher := itunesservice.NewWriteBackBatcher(1*time.Hour, itunesservice.WriteBackBatcherConfig{AutoWriteBack: true, ITLWriteBackEnabled: true, LibraryWritePath: "/tmp/test.itl"}, nil)
	server.writeBackBatcher = batcher
	defer func() {
		// Stop the server's fileIOPool before restoring globals to avoid races
		// with in-flight workers reading config.AppConfig.
		if server.fileIOPool != nil {
			server.fileIOPool.Stop()
		}
		// Stop pool workers before restoring globals to avoid races
		if p := GetGlobalFileIOPool(); p != nil {
			p.Stop()
			SetGlobalFileIOPool(nil)
		}
		_ = batcher.Stop(context.Background())
		server.writeBackBatcher = origBatcher
		config.AppConfig = origConfig
	}()

	writeBack := false
	payload := map[string]any{
		"candidate": map[string]any{
			"title":  "New Title",
			"author": "New Author",
			"source": "Open Library",
			"score":  0.95,
		},
		"fields":     []string{"title", "author"},
		"write_back": writeBack,
	}
	body, err := json.Marshal(payload)
	require.NoError(t, err)

	req := httptest.NewRequest(http.MethodPost, fmt.Sprintf("/api/v1/audiobooks/%s/apply-metadata", book.ID), bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	server.router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)

	// Verify NOT enqueued
	enqueued := batcher.HasPendingBook(book.ID)
	assert.False(t, enqueued, "expected book ID NOT to be enqueued when write_back=false")
}
