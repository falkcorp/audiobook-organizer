// file: internal/audiobooks/constructor_test.go
// version: 0.1.0
// guid: 9418cbb0-af48-476d-9b77-d3007cfe1ae0
// last-edited: 2026-11-22

package audiobooks_test

import (
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/audiobooks"
	"github.com/falkcorp/audiobook-organizer/internal/books/store/mocks"
)

// TestNewAudiobookAndOrganizeServices: both are plain constructors (R3-05a)
// that build from the store alone. The audiobook service's activity service is
// set by the composition root after the registry builds it (SetActivityService),
// not by a PostInit.
func TestNewAudiobookAndOrganizeServices(t *testing.T) {
	st := mocks.NewMockStore(t)
	if svc := audiobooks.NewAudiobookService(st); svc == nil {
		t.Fatal("NewAudiobookService returned nil")
	}
	if svc := audiobooks.NewOrganizeService(st); svc == nil {
		t.Fatal("NewOrganizeService returned nil")
	}
}
