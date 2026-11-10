// file: internal/metafetch/register_test.go
// version: 1.0.2
// last-edited: 2026-11-09

package metafetch_test

import (
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/books/store/mocks"
	"github.com/falkcorp/audiobook-organizer/internal/metafetch"
	"github.com/falkcorp/audiobook-organizer/internal/serviceregistry"
)

func TestMetadataStateRegistration(t *testing.T) {
	c := serviceregistry.NewContainerFrom(metafetch.ServiceDefs()...).
		Override("store", mocks.NewMockStore(t))
	if err := c.Resolve(); err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if err := c.Build(t.Context()); err != nil {
		t.Fatalf("build: %v", err)
	}
	svc := serviceregistry.Get[*metafetch.MetadataStateService](c, "metadatastate")
	if svc == nil {
		t.Fatal("MetadataStateService is nil")
	}
}
