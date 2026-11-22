// file: internal/work/constructor_test.go
// version: 0.1.0
// guid: fca86b4f-f1fd-4c39-8a05-843c31416c1c
// last-edited: 2026-11-22

package work_test

import (
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/books/store/mocks"
	"github.com/falkcorp/audiobook-organizer/internal/work"
)

// TestNewWorkService: R3-05a replaced the registry def with a plain constructor
// the composition root (internal/server/domain_services.go) calls; it builds
// from the store alone.
func TestNewWorkService(t *testing.T) {
	if svc := work.NewWorkService(mocks.NewMockStore(t)); svc == nil {
		t.Fatal("NewWorkService returned nil")
	}
}
