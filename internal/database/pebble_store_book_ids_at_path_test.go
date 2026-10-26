// file: internal/database/pebble_store_book_ids_at_path_test.go
// version: 1.0.0
// guid: 7d1f3a85-4c92-4e60-8b2d-0a5e9c3f7b14
// last-edited: 2026-10-25

package database

import (
	"reflect"
	"sort"
	"testing"
)

// BookIDsAtPath is LiveBookIDsAtPath without the live filter: a trashed row
// names its path too, from the index and from the pre-backfill scan alike, and
// a path nothing names is empty.
func TestBookIDsAtPath_IncludesTrashedRows_IndexAndScanAgree(t *testing.T) {
	s := newAtPathStore(t)
	defer s.Close()
	yes := true
	for _, b := range []*Book{
		{ID: "01LIVE", Title: "live", FilePath: "/lib/shared.m4b"},
		{ID: "01TRASH", Title: "trashed", FilePath: "/lib/shared.m4b", MarkedForDeletion: &yes},
		{ID: "01ONLYTRASH", Title: "only trashed", FilePath: "/lib/trashed-only.m4b", MarkedForDeletion: &yes},
		{ID: "01OTHER", Title: "other", FilePath: "/lib/other.m4b"},
	} {
		if _, err := s.CreateBook(b); err != nil {
			t.Fatalf("create %s: %v", b.ID, err)
		}
	}

	check := func(label string) {
		t.Helper()
		for path, want := range map[string][]string{
			"/lib/shared.m4b":       {"01LIVE", "01TRASH"},
			"/lib/trashed-only.m4b": {"01ONLYTRASH"},
			"/lib/other.m4b":        {"01OTHER"},
			"/lib/nobody.m4b":       nil,
		} {
			sort.Strings(want)
			got, err := s.BookIDsAtPath(path)
			if err != nil {
				t.Fatalf("%s: BookIDsAtPath(%s): %v", label, path, err)
			}
			if len(got) != len(want) || (len(want) > 0 && !reflect.DeepEqual(got, want)) {
				t.Fatalf("%s: BookIDsAtPath(%s) = %v, want %v", label, path, got, want)
			}
		}
		live, err := s.LiveBookIDsAtPath("/lib/trashed-only.m4b")
		if err != nil || len(live) != 0 {
			t.Fatalf("%s: LiveBookIDsAtPath of a trashed-only path = %v %v, want none", label, live, err)
		}
	}

	check("index")
	resetAtPathIndex(t, s)
	check("scan fallback")
}

// A row rewritten as valid JSON out of band has a marker and no index key; the
// all-rows read returns it at its path whether it is live or trashed, where the
// live read drops the trashed one.
func TestBookIDsAtPath_RepairedOutOfBandTrashedRowIsACandidate(t *testing.T) {
	s := newAtPathStore(t)
	defer s.Close()
	seedUndecodableAfterBackfill(t, s)

	setRawBookRow(t, s, "01BADROW", `{"id":"01BADROW","file_path":"/elsewhere","marked_for_deletion":true}`)
	if ids, err := s.BookIDsAtPath("/elsewhere"); err != nil || !reflect.DeepEqual(ids, []string{"01BADROW"}) {
		t.Fatalf("BookIDsAtPath(/elsewhere) = %v %v, want [01BADROW]", ids, err)
	}
	if ids, err := s.LiveBookIDsAtPath("/elsewhere"); err != nil || len(ids) != 0 {
		t.Fatalf("LiveBookIDsAtPath(/elsewhere) = %v %v, want none for a trashed row", ids, err)
	}
}

// An undecodable row anywhere fails the all-rows read closed too: its path is
// unknown, so no path can be vouched for.
func TestBookIDsAtPath_UndecodableRowFailsClosed(t *testing.T) {
	s := newAtPathStore(t)
	defer s.Close()
	seedUndecodableAfterBackfill(t, s)
	if ids, err := s.BookIDsAtPath("/anything"); err == nil {
		t.Fatalf("BookIDsAtPath = %v, nil; want a fail-closed error", ids)
	}
}
