// file: internal/testutil/integration_test.go
// version: 1.1.0
// guid: b7c8d9e0-f1a2-4b3c-8d9e-0f1a2b3c4d5e
// last-edited: 2026-07-07

package testutil

import (
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// TestSetupIntegration_MemDBWarmupComplete is a regression test for the
// flaky-scan root cause (TestScanService_MultiChapterAudiobook, CI-only
// flake): SetupIntegration used to return before PebbleStore's async memdb
// warmup goroutine had published its snapshot.
//
// A book created immediately after setup could race that goroutine: if the
// memdb write-through triggered by CreateBook landed while mem()==nil
// (warmup not yet published), memSync silently no-oped (see
// internal/database/memdb_sync.go), and the eventual warmup snapshot could
// still miss the book — permanently hiding it from GetAllBooks() for the
// life of the store. On an idle dev machine, warmup of a fresh, near-empty
// DB finishes before the first write, masking the bug; under CI/full-suite
// load the warmup goroutine can be scheduled late enough to actually race a
// scan's CreateBook call, which is why the scan test passed reliably in
// isolation but failed intermittently in CI.
//
// The fix: SetupIntegration now calls store.WaitForWarmup() before
// returning, guaranteeing the memdb snapshot is published (or PebbleStore
// has permanently fallen back to direct Pebble reads) before any test code
// runs. This test locks in that contract directly, rather than relying on
// timing to reproduce the race.
func TestSetupIntegration_MemDBWarmupComplete(t *testing.T) {
	env, cleanup := SetupIntegration(t)
	defer cleanup()

	ps, ok := env.Store.(*database.PebbleStore)
	if !ok {
		t.Fatalf("expected env.Store to be *database.PebbleStore, got %T", env.Store)
	}
	if !ps.IsMemReady() {
		t.Fatal("expected memdb warmup to be complete immediately after SetupIntegration returns; " +
			"a scan starting now would race the warmup goroutine (see WaitForWarmup doc)")
	}

	book, err := ps.CreateBook(&database.Book{
		Title:    "Warmup Race Regression",
		FilePath: "/tmp/warmup-race-regression.mp3",
	})
	if err != nil {
		t.Fatalf("CreateBook: %v", err)
	}

	books, err := ps.GetAllBooksCore(100, 0)
	if err != nil {
		t.Fatalf("GetAllBooksCore: %v", err)
	}
	for _, b := range books {
		if b.ID == book.ID {
			return
		}
	}
	t.Fatalf("created book %s not visible via GetAllBooksCore immediately after creation (memdb warmup race)", book.ID)
}
