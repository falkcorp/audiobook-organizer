// file: internal/books/appconfig/mutate_inplace_test.go
// version: 1.0.0
// guid: 41b80bd9-ebd2-420a-8dbb-b2a1d517e9cb
// last-edited: 2026-11-21

package appconfig

import (
	"encoding/json"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/config"
)

// TestEngine_MutateInPlaceWriteIsSavedAndLeavesSnapshotsAlone: an fn that writes
// into a slice in place (an element assignment, not a new slice) is saved as a
// change, and a Snapshot taken before the Mutate, read concurrently, never sees
// the write. With Mutate's copy shallow, the write lands in the backing array
// that e.cur and the earlier Snapshot share: the diff then compares the slice
// with itself and saves nothing, and -race reports the store against the
// readers.
func TestEngine_MutateInPlaceWriteIsSavedAndLeavesSnapshotsAlone(t *testing.T) {
	f := newFake()
	f.vals["modules.books.scan.exclude_patterns"] = json.RawMessage(`["*.joker","*.bane"]`)
	e := New(f, func(config.Config) {})
	if err := e.Start(t.Context()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	old := e.Snapshot()
	if len(old.ExcludePatterns) != 2 || old.ExcludePatterns[0] != "*.joker" {
		t.Fatalf("seeded exclude_patterns = %v", old.ExcludePatterns)
	}

	var stop atomic.Bool
	var wg sync.WaitGroup
	var torn atomic.Int64
	for range 8 {
		wg.Go(func() {
			for !stop.Load() {
				if old.ExcludePatterns[0] != "*.joker" {
					torn.Add(1)
				}
				_ = e.Snapshot().ExcludePatterns
			}
		})
	}
	err := e.Mutate(func(c *config.Config) { c.ExcludePatterns[0] = "*.riddler" })
	stop.Store(true)
	wg.Wait()
	if err != nil {
		t.Fatalf("Mutate: %v", err)
	}

	if n := torn.Load(); n != 0 {
		t.Errorf("a Snapshot taken before the Mutate saw its in-place write %d times", n)
	}
	if old.ExcludePatterns[0] != "*.joker" {
		t.Errorf("the earlier Snapshot now reads %q: the Mutate wrote into storage it shares", old.ExcludePatterns[0])
	}
	if len(f.applied) != 1 {
		t.Fatalf("%d Applies for an in-place slice write, want one: the diff compared the slice with itself", len(f.applied))
	}
	if got := string(f.applied[0].Set["modules.books.scan.exclude_patterns"]); got != `["*.riddler","*.bane"]` {
		t.Fatalf("saved exclude_patterns = %s", got)
	}
	if got := e.Snapshot().ExcludePatterns; len(got) != 2 || got[0] != "*.riddler" || got[1] != "*.bane" {
		t.Fatalf("projection after Mutate = %v", got)
	}
}
