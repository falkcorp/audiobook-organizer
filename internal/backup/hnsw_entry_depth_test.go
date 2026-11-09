// file: internal/backup/hnsw_entry_depth_test.go
// version: 1.0.1
// guid: f51b34fb-19af-4389-b4f1-eacff2ea0e54
// last-edited: 2026-11-09

package backup

import (
	"errors"
	"path/filepath"
	"testing"
)

// R1-15 round-8 verification review, N2: an archive whose database sits
// deeper than its root (a/b/CURRENT) has its snapshot directory beside it
// (a/hnsw), not beside the root, so a refusal of hnsw as the first
// component let a/hnsw/hnsw.clean-stop through, and a database later opened
// at target/a/b found it vouching. An hnsw component is refused at any
// depth. No real backup has one: the archive holds the database directory,
// whose entries are Pebble's flat file names and its archive directory.

// TestEntryRefusal_RefusesAnHNSWComponentAtAnyDepth: compared without case,
// the long s included, and never a name that merely contains it.
func TestEntryRefusal_RefusesAnHNSWComponentAtAnyDepth(t *testing.T) {
	for name, want := range map[string]error{
		"a/hnsw/hnsw.clean-stop":                 ErrArchiveCarriesHNSWSnapshot,
		"a/b/c/HNSW/hnsw-book.bin":               ErrArchiveCarriesHNSWSnapshot,
		"audiobooks.pebble/hnſw/x":               ErrArchiveCarriesHNSWSnapshot,
		"audiobooks.pebble/archive/hnsw":         ErrArchiveCarriesHNSWSnapshot,
		"audiobooks.pebble/hnsw/":                ErrArchiveCarriesHNSWSnapshot,
		"audiobooks.pebble/hnsw.clean-stop":      nil,
		"audiobooks.pebble/hnsw-book.bin":        nil,
		"audiobooks.pebble/archive/hnswx/1.sst":  nil,
		"audiobooks.pebble/xhnsw/MANIFEST-00001": nil,
	} {
		err := entryRefusal(name)
		if want == nil {
			if err != nil {
				t.Errorf("entryRefusal(%q) = %v, want nil", name, err)
			}
			continue
		}
		if !errors.Is(err, want) {
			t.Errorf("entryRefusal(%q) = %v, want %v", name, err, want)
		}
	}
}

// TestExtractStore_RefusesANestedRootsSnapshotEntry: the reviewer's archive
// is refused by ExtractStore's scan, before anything is written. The
// extraction refuses each entry with the same check (entryRefusal).
func TestExtractStore_RefusesANestedRootsSnapshotEntry(t *testing.T) {
	archive := filepath.Join(t.TempDir(), "hush.tar")
	writePlainTar(t, archive, [][2]string{
		{"a/b/CURRENT", "MANIFEST-000001\n"},
		{"a/hnsw/hnsw.clean-stop", `{"version":1,"model":"bge-m3"}`},
	})
	if err := extractStoreRefused(t, archive); !errors.Is(err, ErrArchiveCarriesHNSWSnapshot) {
		t.Errorf("ExtractStore = %v; want ErrArchiveCarriesHNSWSnapshot", err)
	}
}
