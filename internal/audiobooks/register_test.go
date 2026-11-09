// file: internal/audiobooks/register_test.go
// version: 1.0.2
// last-edited: 2026-11-09

package audiobooks_test

import (
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/audiobooks"
	"github.com/falkcorp/audiobook-organizer/internal/books/store/mocks"
	"github.com/falkcorp/audiobook-organizer/internal/serviceregistry"
)

func TestAudiobookRegistration(t *testing.T) {
	c := serviceregistry.NewContainerFrom(audiobooks.ServiceDefs()...).
		Override("store", mocks.NewMockStore(t))
	if err := c.Resolve(); err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if err := c.Build(t.Context()); err != nil {
		t.Fatalf("build: %v", err)
	}
	svc := serviceregistry.Get[*audiobooks.AudiobookService](c, "audiobook")
	if svc == nil {
		t.Fatal("AudiobookService is nil")
	}
}
