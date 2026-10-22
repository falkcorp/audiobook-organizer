// file: internal/batch/register_test.go
// version: 1.1.0
// last-edited: 2026-10-22

package batch_test

import (
	"testing"

	// The batch service needs the audiobook service: its hard delete of a
	// trashed book goes through the purge path (batch.WithTrashPurger).
	_ "github.com/falkcorp/audiobook-organizer/internal/audiobooks"
	"github.com/falkcorp/audiobook-organizer/internal/batch"
	"github.com/falkcorp/audiobook-organizer/internal/database/mocks"
	"github.com/falkcorp/audiobook-organizer/internal/serviceregistry"
)

func TestBatchRegistration(t *testing.T) {
	c := serviceregistry.NewContainer().
		Override("store", mocks.NewMockStore(t)).
		Include("audiobook", "batch")
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
