// file: internal/metafetch/openlibrary_pebble_metrics_test.go
// version: 1.0.0
// guid: 340e1002-91fb-4e28-8165-2e0d4caee02b
// last-edited: 2026-10-04

package metafetch

import (
	"testing"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/openlibrary"
)

// A scrape must not queue behind a slow holder of svc.Mu (deleteOLData's
// RemoveAll, EnsureStore's open and replay): with the lock held the sample
// returns not-ok immediately, and with it free the same store samples ok.
func TestPebbleMetricsSample_DoesNotBlockOnBusyMu(t *testing.T) {
	store, err := openlibrary.NewOLStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	svc := &OpenLibraryService{OLStore: store}

	if _, ok := svc.PebbleMetricsSample(); !ok {
		t.Fatal("idle service with an open store sampled as not ok")
	}

	svc.Mu.Lock()
	done := make(chan bool, 1)
	go func() {
		_, ok := svc.PebbleMetricsSample()
		done <- ok
	}()
	select {
	case ok := <-done:
		if ok {
			t.Error("sample reported ok while Mu was held by another goroutine")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("PebbleMetricsSample blocked on a held Mu")
	}
	svc.Mu.Unlock()

	if _, ok := svc.PebbleMetricsSample(); !ok {
		t.Fatal("sample not ok after Mu was released")
	}
}
