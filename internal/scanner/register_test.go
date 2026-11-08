// file: internal/scanner/register_test.go
// version: 1.0.1
// last-edited: 2026-11-08

package scanner_test

import (
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/database/mocks"
	"github.com/falkcorp/audiobook-organizer/internal/scanner"
	"github.com/falkcorp/audiobook-organizer/internal/serviceregistry"
)

func TestScanRegistration(t *testing.T) {
	c := serviceregistry.NewContainerFrom(scanner.ServiceDefs()...).
		Override("store", mocks.NewMockStore(t)).
		Override("embeddingstore", (*database.EmbeddingStore)(nil))
	if err := c.Resolve(); err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if err := c.Build(t.Context()); err != nil {
		t.Fatalf("build: %v", err)
	}
	svc := serviceregistry.Get[*scanner.ScanService](c, "scan")
	if svc == nil {
		t.Fatal("ScanService is nil")
	}
}
