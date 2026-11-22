// file: internal/scanner/constructor_test.go
// version: 0.1.0
// guid: 73ad770a-0e32-4717-93b1-482c4e5fe66d
// last-edited: 2026-11-22

package scanner_test

import (
	"testing"

	database "github.com/falkcorp/audiobook-organizer/internal/books/store"
	"github.com/falkcorp/audiobook-organizer/internal/books/store/mocks"
	"github.com/falkcorp/audiobook-organizer/internal/scanner"
)

// TestNewScanService: a plain constructor (R3-05a). The embedding store and the
// activity writer are set by the composition root after the registry builds
// them (SetEmbeddingStore, SetActivityWriter), not by a PostInit.
func TestNewScanService(t *testing.T) {
	svc := scanner.NewScanService(mocks.NewMockStore(t))
	if svc == nil {
		t.Fatal("NewScanService returned nil")
	}
	svc.SetEmbeddingStore((*database.EmbeddingStore)(nil))
}
