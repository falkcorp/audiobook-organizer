// file: internal/openlibrary/store_close_race_test.go
// version: 1.0.0
// guid: 2d6f8a41-7c3e-4b95-a0d8-5e1f9b3c6a72
// last-edited: 2026-10-11

package openlibrary

import (
	"errors"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/cockroachdb/pebble/v2"
)

// OLStore.Close must wait for a lookup still holding an iterator instead of
// closing the engine under it (pebble then panics "inconsistent reference
// count" or reports "leaked iterators"), and lookups after Close begins must
// fail with an error wrapping pebble.ErrClosed, not panic.
func TestOLStoreClose_WaitsForInFlightLookup(t *testing.T) {
	s, err := NewOLStore(filepath.Join(t.TempDir(), "ol"))
	if err != nil {
		t.Fatal(err)
	}
	for i := range 32 {
		if err := s.db.Set([]byte(fmt.Sprintf("%s%03d", prefixWork, i)), []byte(`{"key":"x"}`), pebble.NoSync); err != nil {
			t.Fatal(err)
		}
	}
	it, err := s.db.NewIter(&pebble.IterOptions{LowerBound: []byte(prefixWork), UpperBound: []byte("ol:work;")})
	if err != nil {
		t.Fatal(err)
	}
	it.First()

	closeErr := make(chan error, 1)
	go func() { closeErr <- s.Close() }()

	deadline := time.Now().Add(10 * time.Second)
	for {
		_, lerr := s.LookupWork("001")
		if errors.Is(lerr, pebble.ErrClosed) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("lookups never refused after Close began (last error %v)", lerr)
		}
		time.Sleep(time.Millisecond)
	}
	select {
	case err := <-closeErr:
		t.Fatalf("Close returned (%v) while a lookup still held an iterator", err)
	default:
	}

	func() {
		defer func() {
			if rec := recover(); rec != nil {
				t.Fatalf("releasing the in-flight iterator panicked: %v", rec)
			}
		}()
		_ = it.Close()
	}()
	select {
	case err := <-closeErr:
		if err != nil {
			t.Fatalf("Close: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Close did not return after the lookup released its iterator")
	}
	if _, err := s.LookupAuthor("x"); !errors.Is(err, pebble.ErrClosed) {
		t.Fatalf("lookup after Close returned %v; want an error wrapping pebble.ErrClosed", err)
	}
	if _, ok := s.PebbleMetricsSample(); ok {
		t.Fatal("metrics sample on a closed store reported ok")
	}
	if err := s.Close(); err != nil {
		t.Fatalf("second Close: %v", err)
	}
}
