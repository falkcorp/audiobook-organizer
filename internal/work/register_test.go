// file: internal/work/register_test.go
// version: 1.0.2
// last-edited: 2026-11-09

package work_test

import (
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/books/store/mocks"
	"github.com/falkcorp/audiobook-organizer/internal/serviceregistry"
	"github.com/falkcorp/audiobook-organizer/internal/work"
)

func TestWorkRegistration(t *testing.T) {
	c := serviceregistry.NewContainerFrom(work.ServiceDefs()...).
		Override("store", mocks.NewMockStore(t))
	if err := c.Resolve(); err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if err := c.Build(t.Context()); err != nil {
		t.Fatalf("build: %v", err)
	}
	svc := serviceregistry.Get[*work.WorkService](c, "work")
	if svc == nil {
		t.Fatal("WorkService is nil")
	}
}
