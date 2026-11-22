// file: internal/batch/constructor_test.go
// version: 0.1.0
// guid: 7c463f05-7b41-4636-b6b2-25247e441914
// last-edited: 2026-11-22

package batch_test

import (
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/audiobooks"
	"github.com/falkcorp/audiobook-organizer/internal/batch"
	"github.com/falkcorp/audiobook-organizer/internal/books/store/mocks"
)

// TestNewBatchServiceTakesTheAudiobookServiceAsPurger: the batch service is a
// plain constructor (R3-05a) whose hard delete of a trashed book takes the
// audiobook service's purge path, so the audiobook service is built first and
// passed in.
func TestNewBatchServiceTakesTheAudiobookServiceAsPurger(t *testing.T) {
	st := mocks.NewMockStore(t)
	svc := batch.NewBatchService(st, batch.WithTrashPurger(audiobooks.NewAudiobookService(st)))
	if svc == nil {
		t.Fatal("NewBatchService returned nil")
	}
}
