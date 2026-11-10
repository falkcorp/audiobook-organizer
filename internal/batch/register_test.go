// file: internal/batch/register_test.go
// version: 1.1.3
// last-edited: 2026-11-09

package batch_test

import (
	"slices"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/audiobooks"
	"github.com/falkcorp/audiobook-organizer/internal/batch"
	"github.com/falkcorp/audiobook-organizer/internal/books/store/mocks"
	"github.com/falkcorp/audiobook-organizer/internal/serviceregistry"
)

func TestBatchRegistration(t *testing.T) {
	// The batch service needs the audiobook service: its hard delete of a
	// trashed book goes through the purge path (batch.WithTrashPurger).
	c := serviceregistry.NewContainerFrom(slices.Concat(batch.ServiceDefs(), audiobooks.ServiceDefs())...).
		Override("store", mocks.NewMockStore(t))
	if err := c.Resolve(); err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if err := c.Build(t.Context()); err != nil {
		t.Fatalf("build: %v", err)
	}
	svc := serviceregistry.Get[*batch.BatchService](c, "batch")
	if svc == nil {
		t.Fatal("BatchService is nil")
	}
}
