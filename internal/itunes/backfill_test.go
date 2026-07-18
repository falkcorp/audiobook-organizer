// file: internal/itunes/backfill_test.go
// version: 1.2.0
// guid: c9d0e1f2-a3b4-c5d6-e7f8-a9b0c1d2e3f4
// last-edited: 2026-07-18

package itunes

import (
	"context"
	"errors"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// errMockFailure is the sentinel returned by MockBackfillStore methods when
// hasError is set. Replaces a stale reference to a non-existent
// database.ErrNotFound sentinel.
var errMockFailure = errors.New("mock store failure")

// MockBackfillStore provides a minimal mock for testing backfill operations.
type MockBackfillStore struct {
	books          map[string]database.Book
	externalIDMaps []database.ExternalIDMapping
	hasError       bool
	// failBulkCreate fails only BulkCreateExternalIDMappings, leaving reads
	// (GetAllBooksCore/GetBookFiles) working so the H7 bulk-write-error
	// propagation test can exercise the write failure without the
	// pagination loop breaking out before it ever reaches a write.
	failBulkCreate bool
}

func NewMockBackfillStore() *MockBackfillStore {
	return &MockBackfillStore{
		books: make(map[string]database.Book),
	}
}

func (m *MockBackfillStore) GetAllBooksCore(limit, offset int) ([]database.BookCore, error) {
	if m.hasError {
		return nil, errMockFailure
	}
	var all []database.BookCore
	for _, b := range m.books {
		all = append(all, b.Core())
	}
	// Simulate pagination via limit/offset to allow backfill loop to terminate.
	if offset >= len(all) {
		return []database.BookCore{}, nil
	}
	end := offset + limit
	if end > len(all) {
		end = len(all)
	}
	return all[offset:end], nil
}

func (m *MockBackfillStore) GetBookFiles(bookID string) ([]database.BookFile, error) {
	if m.hasError {
		return nil, errMockFailure
	}
	return []database.BookFile{}, nil
}

func (m *MockBackfillStore) CreateExternalIDMapping(mapping *database.ExternalIDMapping) error {
	if m.hasError {
		return errMockFailure
	}
	m.externalIDMaps = append(m.externalIDMaps, *mapping)
	return nil
}

func (m *MockBackfillStore) BulkCreateExternalIDMappings(mappings []database.ExternalIDMapping) error {
	if m.hasError || m.failBulkCreate {
		return errMockFailure
	}
	m.externalIDMaps = append(m.externalIDMaps, mappings...)
	return nil
}

func (m *MockBackfillStore) SetSetting(key, value, dataType string, internal bool) error {
	if m.hasError {
		return errMockFailure
	}
	return nil
}

func TestBackfillExternalIDsWithNilStore(t *testing.T) {
	err := BackfillExternalIDs(context.Background(), nil, nil)
	if err != nil {
		t.Errorf("expected nil error for nil store, got %v", err)
	}
}

func TestBackfillExternalIDsCollectsBookPIDs(t *testing.T) {
	mockStore := NewMockBackfillStore()
	pidValue := "test-pid-123"
	mockStore.books["book1"] = database.Book{
		ID:                 "book1",
		Title:              "Test Book",
		ITunesPersistentID: &pidValue,
	}

	var progressCalls int
	err := BackfillExternalIDs(context.Background(), mockStore, func(_, _ int, _ string) {
		progressCalls++
	})
	if err != nil {
		t.Errorf("unexpected error: %v", err)
	}

	// Should have created one mapping from the book PID
	if len(mockStore.externalIDMaps) < 1 {
		t.Errorf("expected at least 1 mapping, got %d", len(mockStore.externalIDMaps))
	}

	// H7: pagination must report progress at least once per page scanned.
	if progressCalls == 0 {
		t.Error("expected progress callback to be invoked at least once, got 0 calls")
	}
}

// TestBackfillExternalIDsPropagatesBulkWriteError proves the H7 fix: a
// persistence failure during the book-pagination pass is returned to the
// caller instead of being silently discarded (previously `_ =
// store.BulkCreateExternalIDMappings(batch)`), so the op can actually fail.
func TestBackfillExternalIDsPropagatesBulkWriteError(t *testing.T) {
	mockStore := NewMockBackfillStore()
	pidValue := "test-pid-456"
	mockStore.books["book1"] = database.Book{
		ID:                 "book1",
		Title:              "Test Book",
		ITunesPersistentID: &pidValue,
	}
	mockStore.failBulkCreate = true

	err := BackfillExternalIDs(context.Background(), mockStore, nil)
	if err == nil {
		t.Fatal("expected error when BulkCreateExternalIDMappings fails, got nil")
	}
}

// TestBackfillExternalIDsPropagatesReadError proves the H7 fix: a
// GetAllBooksCore read failure during pagination is returned instead of
// silently `break`-ing out of the loop and falling through to mark the
// whole backfill "done" despite skipping the rest of the library.
func TestBackfillExternalIDsPropagatesReadError(t *testing.T) {
	mockStore := NewMockBackfillStore()
	mockStore.hasError = true

	err := BackfillExternalIDs(context.Background(), mockStore, nil)
	if err == nil {
		t.Fatal("expected error when GetAllBooksCore fails, got nil")
	}
}

func TestBackfillITunesTrackPIDsWithNoConfiguredPath(t *testing.T) {
	mockStore := NewMockBackfillStore()

	// With no configured path, should return 0 gracefully
	count, err := BackfillITunesTrackPIDs(context.Background(), mockStore, nil)
	if count != 0 {
		t.Errorf("expected 0 registered PIDs with no configured path, got %d", count)
	}
	if err != nil {
		t.Errorf("unexpected error with no path: %v", err)
	}
}
