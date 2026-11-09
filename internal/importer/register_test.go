// file: internal/importer/register_test.go
// version: 1.0.2
// last-edited: 2026-11-09

package importer_test

import (
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/books/store/mocks"
	"github.com/falkcorp/audiobook-organizer/internal/importer"
	"github.com/falkcorp/audiobook-organizer/internal/serviceregistry"
)

func TestImportPathRegistration(t *testing.T) {
	c := serviceregistry.NewContainerFrom(importer.ServiceDefs()...).
		Override("store", mocks.NewMockStore(t))
	if err := c.Resolve(); err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if err := c.Build(t.Context()); err != nil {
		t.Fatalf("build: %v", err)
	}
	svc := serviceregistry.Get[*importer.ImportPathService](c, "importpath")
	if svc == nil {
		t.Fatal("ImportPathService is nil")
	}
}
