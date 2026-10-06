// file: internal/importer/service_test.go
// version: 1.2.0
// guid: e1f2a3b4-c5d6-7e8f-9a0b-1c2d3e4f5b6c
// last-edited: 2026-10-06

package importer

import (
	"context"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/pkg/plugin/sdk"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestImportService_ImportFile_MissingFile(t *testing.T) {
	mockDB := &database.MockStore{}
	is := NewImportService(mockDB)

	req := &ImportFileRequest{
		FilePath: "/nonexistent/file.m4b",
	}

	_, err := is.ImportFile(req)

	if err == nil {
		t.Error("expected error for missing file")
	}
}

func TestImportService_ImportFile_UnsupportedExtension(t *testing.T) {
	mockDB := &database.MockStore{}
	is := NewImportService(mockDB)

	req := &ImportFileRequest{
		FilePath: "test.txt",
	}

	_, err := is.ImportFile(req)

	if err == nil {
		t.Error("expected error for unsupported file type")
	}
}

// ─── M4 registry wiring tests ────────────────────────────────────────────────

// fakeRegistry is a minimal sdk.Registry test double that records EnqueueOp calls.
type fakeRegistry struct {
	enqueuedOp     string
	enqueuedParams any
	callCount      int
}

func (f *fakeRegistry) RegisterOp(_ sdk.OperationDef) error { return nil }
func (f *fakeRegistry) EnqueueOp(_ context.Context, defID string, params any, _ ...sdk.EnqueueOption) (string, error) {
	f.callCount++
	f.enqueuedOp = defID
	f.enqueuedParams = params
	return "", nil
}

// TestImportService_SetRegistry verifies that SetRegistry stores the registry
// reference on the ImportService and that the field is nil before wiring.
func TestImportService_SetRegistry_Wiring(t *testing.T) {
	t.Parallel()
	mockDB := &database.MockStore{}
	is := NewImportService(mockDB)

	// Before wiring: opRegistry must be nil.
	require.Nil(t, is.opRegistry, "opRegistry should be nil before SetRegistry")

	fake := &fakeRegistry{}
	is.SetRegistry(fake)

	assert.Equal(t, fake, is.opRegistry, "SetRegistry must store the registry reference")
}

// TestImportService_SetRegistry_NilSafe verifies that passing nil to SetRegistry
// clears the registry (e.g. for test teardown or hot-reload scenarios).
func TestImportService_SetRegistry_NilSafe(t *testing.T) {
	t.Parallel()
	mockDB := &database.MockStore{}
	is := NewImportService(mockDB)

	fake := &fakeRegistry{}
	is.SetRegistry(fake)
	is.SetRegistry(nil) // clear

	assert.Nil(t, is.opRegistry, "SetRegistry(nil) must clear the stored registry")
}

// A series named after the book's author, or one person of its credit, is the
// "Author - Title" split read as "Series - Title" and is never created.
func TestSeriesNamesAuthor(t *testing.T) {
	assert.True(t, seriesNamesAuthor("Brandon Sanderson", "Brandon Sanderson", nil))
	assert.True(t, seriesNamesAuthor("Terry Pratchett", "", []database.Author{{Name: "Neil Gaiman"}, {Name: "Terry Pratchett"}}))
	assert.False(t, seriesNamesAuthor("Mistborn", "Brandon Sanderson", []database.Author{{Name: "Brandon Sanderson"}}))
}
