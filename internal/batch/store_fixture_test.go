// file: internal/batch/store_fixture_test.go
// version: 1.0.0
// guid: 4d9e1b62-7a35-4c80-b3f1-8e26a05c7d94
// last-edited: 2026-11-19

package batch

import (
	"path/filepath"
	"testing"

	database "github.com/falkcorp/audiobook-organizer/internal/books/store"
)

// newServingPebbleStore opens a standalone store on a fresh temporary
// directory and returns it only once its memdb is published and serving.
//
// NewPebbleStore starts the memdb warmup on a goroutine and returns at once;
// until it publishes, a store that wants a memdb refuses every operation that
// has no Pebble path, and DeleteSeriesIfUnreferenced is one (it will not run a
// library scan under every book lock). A test that deletes a series straight
// after opening raced that goroutine. The signal waited on is the warmup's own
// completion (WaitForWarmup), then WarmupStatus to confirm it ended published
// rather than fallen back to Pebble reads, so a warmup that failed fails the
// fixture with that reason instead of a refusal several calls later.
func newServingPebbleStore(t *testing.T) *database.PebbleStore {
	t.Helper()
	store, err := database.NewPebbleStore(filepath.Join(t.TempDir(), "db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	store.WaitForWarmup()
	if ready, done, _ := store.WarmupStatus(); !done || !ready {
		t.Fatalf("the fixture's memdb warmup finished without publishing (ready=%v done=%v)", ready, done)
	}
	return store
}
