// file: internal/importer/constructor_test.go
// version: 0.1.0
// guid: 22d4679e-84af-4130-ac7e-9d5c13e810a8
// last-edited: 2026-11-22

package importer_test

import (
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/books/store/mocks"
	"github.com/falkcorp/audiobook-organizer/internal/importer"
)

// TestNewImportPathService: a plain constructor (R3-05a) that builds from the
// store alone.
func TestNewImportPathService(t *testing.T) {
	if svc := importer.NewImportPathService(mocks.NewMockStore(t)); svc == nil {
		t.Fatal("NewImportPathService returned nil")
	}
}
