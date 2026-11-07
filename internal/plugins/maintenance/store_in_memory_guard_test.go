// file: internal/plugins/maintenance/store_in_memory_guard_test.go
// version: 1.0.0
// guid: 5d0b7c3e-91a4-4e6f-8b25-c4a17e903d68
// last-edited: 2026-11-06

package maintenance

import (
	"os"
	"strings"
	"testing"
)

// onDiskStoreAllowed lists the test files that may open an on-disk Pebble
// store, with the reason each needs the real filesystem. Every other test in
// this package uses database.NewPebbleStoreInMemory.
//
// The package spends almost no CPU. What it spends is time waiting on fsync:
// with an on-disk store per test it ran 10 minutes on a loaded CI Mac and hit
// the default test timeout, and the in-memory store (same engine, no disk)
// took it to under two. A new test that opens a store on disk puts that cost
// back, so it fails here.
var onDiskStoreAllowed = map[string]string{
	"intro_transcribe_journal_test.go": "closes the store and reopens the same path, the restart under test",
	"series_phantom_repair_test.go":    "seeds a real store; skipped in -short",
	"booksig_sidecar_migrate_test.go":  "seeds a real store; skipped in -short",
}

func TestTestsOpenInMemoryStores(t *testing.T) {
	t.Parallel()
	// Built from parts so this file does not match its own search.
	needle := "database.New" + "PebbleStore("
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		name := e.Name()
		if !strings.HasSuffix(name, "_test.go") {
			continue
		}
		if _, ok := onDiskStoreAllowed[name]; ok {
			continue
		}
		src, err := os.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		for i, line := range strings.Split(string(src), "\n") {
			if strings.Contains(line, needle) && !strings.HasPrefix(strings.TrimSpace(line), "//") {
				t.Errorf("%s:%d opens an on-disk store; use database.NewPebbleStoreInMemory (or, if the test must reopen the path, add the file to onDiskStoreAllowed with the reason)", name, i+1)
			}
		}
	}
}
