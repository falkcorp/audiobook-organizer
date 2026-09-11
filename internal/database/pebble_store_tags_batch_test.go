// file: internal/database/pebble_store_tags_batch_test.go
// version: 1.0.0
// guid: 9c224f63-19ee-44cb-a550-22d5ec756246
// last-edited: 2026-09-11

package database

import (
	"path/filepath"
	"reflect"
	"testing"
)

// TestGetBookTagsByBookIDs_MatchesPerBookReads: the batch reader must
// return exactly what GetBookTags returns for each book, omit untagged
// books, dedupe repeated IDs, and not bleed tags across IDs that are
// string prefixes of each other ("b1" vs "b10").
func TestGetBookTagsByBookIDs_MatchesPerBookReads(t *testing.T) {
	store, err := NewPebbleStoreInMemory(filepath.Join(t.TempDir(), "db"))
	if err != nil {
		t.Fatalf("pebble: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	for _, id := range []string{"b1", "b10", "b2"} {
		if _, err := store.CreateBook(&Book{ID: id, Title: id, FilePath: "/tmp/" + id, Format: "m4b"}); err != nil {
			t.Fatalf("create %s: %v", id, err)
		}
	}
	for _, tag := range []string{"zeta", "alpha"} {
		if err := store.AddBookTag("b1", tag); err != nil {
			t.Fatalf("tag b1: %v", err)
		}
	}
	if err := store.AddBookTagWithSource("b10", "system:only-on-b10", "dedup"); err != nil {
		t.Fatalf("tag b10: %v", err)
	}

	got, err := store.GetBookTagsByBookIDs([]string{"b1", "b10", "b2", "b1", "", "missing"})
	if err != nil {
		t.Fatalf("batch: %v", err)
	}
	want := map[string][]string{}
	for _, id := range []string{"b1", "b10"} {
		tags, err := store.GetBookTags(id)
		if err != nil {
			t.Fatalf("GetBookTags %s: %v", id, err)
		}
		want[id] = tags
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("batch tags = %v, want %v", got, want)
	}
	if got["b1"][0] != "alpha" {
		t.Fatalf("b1 tags not sorted: %v", got["b1"])
	}

	empty, err := store.GetBookTagsByBookIDs(nil)
	if err != nil || len(empty) != 0 {
		t.Fatalf("empty input: got %v, %v", empty, err)
	}
}
