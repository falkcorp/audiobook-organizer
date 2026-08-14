// file: internal/search/mapping_version_test.go
// version: 1.0.0
// guid: 6b1f83d7-4e29-4c05-9a3b-27d6c8e10f45
// last-edited: 2026-08-13

package search

import (
	"os"
	"path/filepath"
	"testing"
)

// The mapping-version marker is the riskiest part of the stopword change, and
// the part that unit tests of bookIndexMapping() cannot reach: bleve persists
// the mapping INSIDE the index and bleve.Open uses the stored copy, so a
// mapping edit is inert until the index is recreated. These tests drive the
// real Open() against a real on-disk index.
//
// Two failure modes matter more than the happy path:
//
//   - never recreating  => the analyzer change silently does nothing in prod
//   - always recreating => a full re-index on EVERY restart, forever

func indexAt(t *testing.T, path string) *BleveIndex {
	t.Helper()
	idx, err := Open(path)
	if err != nil {
		t.Fatalf("Open(%s): %v", path, err)
	}
	t.Cleanup(func() { _ = idx.Close() })
	return idx
}

func docCount(t *testing.T, idx *BleveIndex) uint64 {
	t.Helper()
	n, err := idx.DocCount()
	if err != nil {
		t.Fatalf("DocCount: %v", err)
	}
	return n
}

// seedOne indexes a single book so an index has content worth preserving —
// which is what makes "was it recreated?" observable via DocCount.
func seedOne(t *testing.T, idx *BleveIndex) {
	t.Helper()
	if err := idx.IndexBook(BookDocument{BookID: "b1", Title: "Persisted"}); err != nil {
		t.Fatalf("IndexBook: %v", err)
	}
	if got := docCount(t, idx); got != 1 {
		t.Fatalf("seed: DocCount = %d, want 1", got)
	}
}

func TestOpenWritesMappingMarkerOnCreate(t *testing.T) {
	path := filepath.Join(t.TempDir(), "library.bleve")
	idx := indexAt(t, path)

	if idx.RecreatedForMappingChange() {
		t.Error("a brand-new index must not report itself as recreated; " +
			"the server would skip the first-time bulk backfill")
	}
	got, err := os.ReadFile(mappingMarkerPath(path))
	if err != nil {
		t.Fatalf("marker not written on create: %v", err)
	}
	if string(got) != bookMappingVersion+"\n" {
		t.Errorf("marker = %q, want %q", got, bookMappingVersion+"\n")
	}
}

// TestOpenPreservesIndexWhenMarkerMatches is the guard against rebuilding the
// library on every restart. If this fails, prod re-indexes 67k books forever.
func TestOpenPreservesIndexWhenMarkerMatches(t *testing.T) {
	path := filepath.Join(t.TempDir(), "library.bleve")

	first, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	seedOne(t, first)
	if err := first.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	second := indexAt(t, path)
	if second.RecreatedForMappingChange() {
		t.Fatal("REBUILD LOOP: reopening a current-version index reported a " +
			"mapping change; every restart would wipe and re-index the library")
	}
	if got := docCount(t, second); got != 1 {
		t.Errorf("existing documents lost on reopen: DocCount = %d, want 1", got)
	}
}

// TestOpenRecreatesOnStaleMarker covers both stale shapes: an explicitly older
// version, and NO marker at all — the latter being every index built before
// this change, i.e. the exact state of production at deploy time.
func TestOpenRecreatesOnStaleMarker(t *testing.T) {
	for _, tc := range []struct {
		name  string
		stale func(t *testing.T, path string)
	}{
		{
			name: "older version recorded",
			stale: func(t *testing.T, path string) {
				if err := os.WriteFile(mappingMarkerPath(path), []byte("1\n"), 0o644); err != nil {
					t.Fatalf("write stale marker: %v", err)
				}
			},
		},
		{
			// This is production on the day this ships.
			name: "no marker at all (pre-marker index)",
			stale: func(t *testing.T, path string) {
				if err := os.Remove(mappingMarkerPath(path)); err != nil {
					t.Fatalf("remove marker: %v", err)
				}
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "library.bleve")

			first, err := Open(path)
			if err != nil {
				t.Fatalf("Open: %v", err)
			}
			seedOne(t, first)
			if err := first.Close(); err != nil {
				t.Fatalf("Close: %v", err)
			}
			tc.stale(t, path)

			second := indexAt(t, path)
			if !second.RecreatedForMappingChange() {
				t.Fatal("stale mapping was NOT detected: the index kept its old " +
					"analyzer, so the stopword fix would silently do nothing in " +
					"production while every test here still passed")
			}
			if got := docCount(t, second); got != 0 {
				t.Errorf("recreated index should be empty, DocCount = %d", got)
			}
			// The marker must be rewritten, or the next boot recreates again.
			if got := readMappingMarker(path); got != bookMappingVersion {
				t.Errorf("marker after recreate = %q, want %q — without this the "+
					"index is rebuilt on every restart", got, bookMappingVersion)
			}
		})
	}
}

// TestOpenRecreateIsNotRepeated pins the settle-down behaviour: one recreate,
// then stability. A recreate that repeats is worse than no fix at all, because
// search would be permanently mid-rebuild.
func TestOpenRecreateIsNotRepeated(t *testing.T) {
	path := filepath.Join(t.TempDir(), "library.bleve")

	first, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if err := first.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if err := os.Remove(mappingMarkerPath(path)); err != nil {
		t.Fatalf("remove marker: %v", err)
	}

	second, err := Open(path)
	if err != nil {
		t.Fatalf("Open (recreate): %v", err)
	}
	if !second.RecreatedForMappingChange() {
		t.Fatal("expected the first reopen to recreate")
	}
	if err := second.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	third := indexAt(t, path)
	if third.RecreatedForMappingChange() {
		t.Error("REBUILD LOOP: the index recreated itself a second time; the " +
			"marker written during recreate is not being honoured")
	}
}

// TestRecreatedFlagFalseOnNil documents the nil-receiver contract. The server
// calls this on s.searchIndex, which is nil whenever search is disabled or
// failed to open, and must not panic during startup.
func TestRecreatedFlagFalseOnNil(t *testing.T) {
	var idx *BleveIndex
	if idx.RecreatedForMappingChange() {
		t.Error("nil index must report false")
	}
}
