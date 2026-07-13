// file: internal/activity/service_unit_test.go
// version: 1.1.0

package activity

import (
	"errors"
	"testing"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/database/mocks"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// --------------------------------------------------------------------------
// ChangelogService tests (mock-based)
// --------------------------------------------------------------------------

func TestChangelogService_NilDB(t *testing.T) {
	svc := NewChangelogService(nil)
	entries, err := svc.GetBookChangelog("book-1")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "database not initialized")
	assert.Nil(t, entries)
}

func TestChangelogService_EmptyHistory(t *testing.T) {
	mockStore := mocks.NewMockStore(t)

	mockStore.EXPECT().GetBookPathHistory("book-1").Return([]database.BookPathChange{}, nil)
	mockStore.EXPECT().GetBookChangeHistory("book-1", 100).Return([]database.MetadataChangeRecord{}, nil)
	mockStore.EXPECT().GetBookChanges("book-1").Return([]*database.OperationChange{}, nil)

	svc := NewChangelogService(mockStore)
	entries, err := svc.GetBookChangelog("book-1")
	require.NoError(t, err)
	assert.Empty(t, entries)
}

func TestChangelogService_MergesAndSortsByTimestamp(t *testing.T) {
	mockStore := mocks.NewMockStore(t)

	t1 := time.Date(2026, 1, 1, 10, 0, 0, 0, time.UTC)
	t2 := time.Date(2026, 1, 2, 10, 0, 0, 0, time.UTC)
	t3 := time.Date(2026, 1, 3, 10, 0, 0, 0, time.UTC)

	mockStore.EXPECT().GetBookPathHistory("book-1").Return([]database.BookPathChange{
		{BookID: "book-1", OldPath: "/old", NewPath: "/new", ChangeType: "rename", CreatedAt: t1},
	}, nil)

	newVal := "New Title"
	mockStore.EXPECT().GetBookChangeHistory("book-1", 100).Return([]database.MetadataChangeRecord{
		{BookID: "book-1", Field: "title", NewValue: &newVal, ChangeType: "fetched", Source: "Open Library", ChangedAt: t3},
	}, nil)

	mockStore.EXPECT().GetBookChanges("book-1").Return([]*database.OperationChange{
		{OperationID: "op-1", BookID: "book-1", ChangeType: "file_move", FieldName: "path", OldValue: "/a", NewValue: "/b", CreatedAt: t2},
	}, nil)

	svc := NewChangelogService(mockStore)
	entries, err := svc.GetBookChangelog("book-1")
	require.NoError(t, err)
	require.Len(t, entries, 3)

	// Should be sorted newest-first
	assert.Equal(t, t3, entries[0].Timestamp)
	assert.Equal(t, "metadata_apply", entries[0].Type)
	assert.Equal(t, t2, entries[1].Timestamp)
	assert.Equal(t, "rename", entries[1].Type)
	assert.Equal(t, t1, entries[2].Timestamp)
	assert.Equal(t, "rename", entries[2].Type)
}

func TestChangelogService_LimitsToMax(t *testing.T) {
	mockStore := mocks.NewMockStore(t)

	// Generate more than MaxChangelogEntries path changes
	changes := make([]database.BookPathChange, MaxChangelogEntries+10)
	for i := range changes {
		changes[i] = database.BookPathChange{
			BookID:     "book-1",
			OldPath:    "/old",
			NewPath:    "/new",
			ChangeType: "rename",
			CreatedAt:  time.Now().Add(-time.Duration(i) * time.Minute),
		}
	}
	mockStore.EXPECT().GetBookPathHistory("book-1").Return(changes, nil)
	mockStore.EXPECT().GetBookChangeHistory("book-1", 100).Return([]database.MetadataChangeRecord{}, nil)
	mockStore.EXPECT().GetBookChanges("book-1").Return([]*database.OperationChange{}, nil)

	svc := NewChangelogService(mockStore)
	entries, err := svc.GetBookChangelog("book-1")
	require.NoError(t, err)
	assert.Len(t, entries, MaxChangelogEntries)
}

func TestChangelogService_StoreErrorsNonFatal(t *testing.T) {
	mockStore := mocks.NewMockStore(t)

	// All three sources return errors — should still succeed with empty results
	mockStore.EXPECT().GetBookPathHistory("book-1").Return(nil, errors.New("path error"))
	mockStore.EXPECT().GetBookChangeHistory("book-1", 100).Return(nil, errors.New("meta error"))
	mockStore.EXPECT().GetBookChanges("book-1").Return(nil, errors.New("op error"))

	svc := NewChangelogService(mockStore)
	entries, err := svc.GetBookChangelog("book-1")
	require.NoError(t, err)
	assert.Empty(t, entries)
}

func TestChangelogService_TagWriteEntryType(t *testing.T) {
	mockStore := mocks.NewMockStore(t)

	mockStore.EXPECT().GetBookPathHistory("book-1").Return(nil, nil)

	newVal := "overridden"
	mockStore.EXPECT().GetBookChangeHistory("book-1", 100).Return([]database.MetadataChangeRecord{
		{BookID: "book-1", Field: "title", NewValue: &newVal, ChangeType: "override", Source: "manual", ChangedAt: time.Now()},
	}, nil)
	mockStore.EXPECT().GetBookChanges("book-1").Return(nil, nil)

	svc := NewChangelogService(mockStore)
	entries, err := svc.GetBookChangelog("book-1")
	require.NoError(t, err)
	require.Len(t, entries, 1)
	assert.Equal(t, "tag_write", entries[0].Type)
	assert.Contains(t, entries[0].Summary, "Tag written")
}

func TestChangelogService_OperationChangeTypes(t *testing.T) {
	mockStore := mocks.NewMockStore(t)

	mockStore.EXPECT().GetBookPathHistory("book-1").Return(nil, nil)
	mockStore.EXPECT().GetBookChangeHistory("book-1", 100).Return(nil, nil)

	now := time.Now()
	mockStore.EXPECT().GetBookChanges("book-1").Return([]*database.OperationChange{
		{OperationID: "op-1", BookID: "book-1", ChangeType: "tag_write", FieldName: "title", OldValue: "A", NewValue: "B", CreatedAt: now},
		{OperationID: "op-2", BookID: "book-1", ChangeType: "metadata_update", FieldName: "author", OldValue: "X", NewValue: "Y", CreatedAt: now.Add(-time.Second)},
	}, nil)

	svc := NewChangelogService(mockStore)
	entries, err := svc.GetBookChangelog("book-1")
	require.NoError(t, err)
	require.Len(t, entries, 2)

	// Map by type for easier assertion
	byType := map[string]ChangeLogEntry{}
	for _, e := range entries {
		byType[e.Type] = e
	}

	tagEntry, ok := byType["tag_write"]
	require.True(t, ok)
	assert.Contains(t, tagEntry.Summary, "Tags written")

	metaEntry, ok := byType["metadata_apply"]
	require.True(t, ok)
	assert.Contains(t, metaEntry.Summary, "Metadata updated")
}

// --------------------------------------------------------------------------
// Path-history entry labeling (import vs rename vs organize)
// --------------------------------------------------------------------------

// TestChangelogService_ImportPathChangeLabeledImport is the regression for the
// reported bug: an import path-change (OldPath == "", ChangeType "import", written
// by PebbleStore.CreateBook) was mislabeled as a rename and rendered
// "Renamed — → /newpath" with an empty "from". It must now be an "import" entry
// with "Imported — <path>" and NO phantom "→".
func TestChangelogService_ImportPathChangeLabeledImport(t *testing.T) {
	mockStore := mocks.NewMockStore(t)
	ts := time.Date(2026, 1, 1, 10, 0, 0, 0, time.UTC)

	mockStore.EXPECT().GetBookPathHistory("book-1").Return([]database.BookPathChange{
		{BookID: "book-1", OldPath: "", NewPath: "/mnt/lib/book.mp3", ChangeType: "import", CreatedAt: ts},
	}, nil)
	mockStore.EXPECT().GetBookChangeHistory("book-1", 100).Return(nil, nil)
	mockStore.EXPECT().GetBookChanges("book-1").Return(nil, nil)

	svc := NewChangelogService(mockStore)
	entries, err := svc.GetBookChangelog("book-1")
	require.NoError(t, err)
	require.Len(t, entries, 1)
	assert.Equal(t, "import", entries[0].Type)
	assert.Equal(t, "Imported — /mnt/lib/book.mp3", entries[0].Summary)
	assert.NotContains(t, entries[0].Summary, "→")
	assert.NotContains(t, entries[0].Summary, "Renamed")
}

// TestChangelogService_RealRenameStillLabeledRename guards that a genuine rename
// (non-empty OldPath) keeps the rename type and the old → new summary.
func TestChangelogService_RealRenameStillLabeledRename(t *testing.T) {
	mockStore := mocks.NewMockStore(t)
	ts := time.Date(2026, 1, 1, 10, 0, 0, 0, time.UTC)

	mockStore.EXPECT().GetBookPathHistory("book-1").Return([]database.BookPathChange{
		{BookID: "book-1", OldPath: "/old/name.mp3", NewPath: "/new/name.mp3", ChangeType: "rename", CreatedAt: ts},
	}, nil)
	mockStore.EXPECT().GetBookChangeHistory("book-1", 100).Return(nil, nil)
	mockStore.EXPECT().GetBookChanges("book-1").Return(nil, nil)

	svc := NewChangelogService(mockStore)
	entries, err := svc.GetBookChangelog("book-1")
	require.NoError(t, err)
	require.Len(t, entries, 1)
	assert.Equal(t, "rename", entries[0].Type)
	assert.Equal(t, "Renamed — /old/name.mp3 → /new/name.mp3", entries[0].Summary)
}

// TestChangelogService_OrganizePathChangeLabeledOrganize covers the Bug-2
// provenance record: CreateOrganizedVersion now writes an "organize" path-change
// carrying the real source path, which must render as a rename-type "Organized —
// <old> → <new>" entry so the change log shows where the file was organized FROM.
func TestChangelogService_OrganizePathChangeLabeledOrganize(t *testing.T) {
	mockStore := mocks.NewMockStore(t)
	ts := time.Date(2026, 1, 1, 10, 0, 0, 0, time.UTC)

	mockStore.EXPECT().GetBookPathHistory("book-1").Return([]database.BookPathChange{
		{BookID: "book-1", OldPath: "/staging/Star Wars: The Force Unleashed.mp3", NewPath: "/mnt/lib/2-03 (SW-022) Star Wars_ The Force U.mp3", ChangeType: "organize", CreatedAt: ts},
	}, nil)
	mockStore.EXPECT().GetBookChangeHistory("book-1", 100).Return(nil, nil)
	mockStore.EXPECT().GetBookChanges("book-1").Return(nil, nil)

	svc := NewChangelogService(mockStore)
	entries, err := svc.GetBookChangelog("book-1")
	require.NoError(t, err)
	require.Len(t, entries, 1)
	assert.Equal(t, "rename", entries[0].Type)
	assert.Contains(t, entries[0].Summary, "Organized — /staging/Star Wars: The Force Unleashed.mp3 →")
}

// --------------------------------------------------------------------------
// DerefStrDisplay
// --------------------------------------------------------------------------

func TestDerefStrDisplay_NilAndValue(t *testing.T) {
	assert.Equal(t, "<nil>", DerefStrDisplay(nil))

	s := "hello"
	assert.Equal(t, "hello", DerefStrDisplay(&s))
}
