// file: internal/database/dataloss_key_encoding_test.go
// version: 1.0.0
// guid: f5b6c7d8-9e0f-1a2b-3c4d-keyencoding001
// last-edited: 2026-07-13

package database

import (
	"fmt"
	"testing"
)

// T5 — key-encoding property test.
//
// Values that contain the index delimiters (':' and '/'), leading/trailing
// delimiters, and unicode must not corrupt any secondary-index parse. This is
// the generic form of the tag-colon-parse bug: a byte-prefix scan over
// "tag_idx:<tag>:" also matches longer tags sharing that prefix, so every
// lookup must return EXACTLY the right record and nothing that merely shares a
// delimiter-bearing prefix.

var nastyValues = []struct {
	name string
	val  string
}{
	{"internal_colon", "a:b:c"},
	{"internal_slash", "a/b/c"},
	{"leading_colon", ":leading"},
	{"trailing_colon", "trailing:"},
	{"colon_and_slash", "x:/y:z/"},
	{"unicode", "café:naïve/日本語"},
	{"unicode_colon_suffix", "日本語:"},
}

// TestKeyEncoding_PathIndex: a FilePath full of delimiters must resolve back to
// exactly its own book, and its stored Title must be byte-identical.
func TestKeyEncoding_PathIndex(t *testing.T) {
	store, cleanup := setupPebbleTestDB(t)
	defer cleanup()

	ids := map[string]string{}
	for i, nv := range nastyValues {
		path := fmt.Sprintf("/lib/%s/%s.m4b", nv.val, nv.val)
		title := "Title " + nv.val
		created, err := store.CreateBook(&Book{Title: title, FilePath: path})
		if err != nil {
			t.Fatalf("CreateBook %q: %v", nv.name, err)
		}
		ids[path] = created.ID
		_ = i
	}

	for path, wantID := range ids {
		got, err := store.GetBookByFilePath(path)
		if err != nil {
			t.Fatalf("GetBookByFilePath %q: %v", path, err)
		}
		if got == nil || got.ID != wantID {
			t.Errorf("path %q resolved to %v, want book %s", path, got, wantID)
			continue
		}
		if got.FilePath != path {
			t.Errorf("stored FilePath corrupted: got %q, want %q", got.FilePath, path)
		}
	}
}

// TestKeyEncoding_TagIndex is the crown-jewel colon-parse discrimination: a
// lookup for tag "genre" must NOT return the book tagged "genre:fantasy" (which
// shares the byte prefix), and vice-versa. Also covers arbitrary nasty tags.
func TestKeyEncoding_TagIndex(t *testing.T) {
	store, cleanup := setupPebbleTestDB(t)
	defer cleanup()

	// Prefix-sharing pair: the classic bug.
	base, err := store.CreateBook(&Book{Title: "base", FilePath: "/lib/tag-base.m4b"})
	if err != nil {
		t.Fatalf("CreateBook base: %v", err)
	}
	long, err := store.CreateBook(&Book{Title: "long", FilePath: "/lib/tag-long.m4b"})
	if err != nil {
		t.Fatalf("CreateBook long: %v", err)
	}
	if err := store.SetBookTags(base.ID, []string{"genre"}); err != nil {
		t.Fatalf("SetBookTags base: %v", err)
	}
	if err := store.SetBookTags(long.ID, []string{"genre:fantasy"}); err != nil {
		t.Fatalf("SetBookTags long: %v", err)
	}

	assertTagExactly(t, store, "genre", base.ID)
	assertTagExactly(t, store, "genre:fantasy", long.ID)

	// Arbitrary nasty tags, each on its own book, must round-trip exactly.
	for _, nv := range nastyValues {
		b, err := store.CreateBook(&Book{Title: "tag-" + nv.name, FilePath: "/lib/tag-" + nv.name + ".m4b"})
		if err != nil {
			t.Fatalf("CreateBook %q: %v", nv.name, err)
		}
		if err := store.SetBookTags(b.ID, []string{nv.val}); err != nil {
			t.Fatalf("SetBookTags %q: %v", nv.name, err)
		}
		assertTagExactly(t, store, nv.val, b.ID)
	}
}

func assertTagExactly(t *testing.T, store Store, tag, wantID string) {
	t.Helper()
	ids, err := store.GetBooksByTag(tag)
	if err != nil {
		t.Fatalf("GetBooksByTag %q: %v", tag, err)
	}
	if len(ids) != 1 || ids[0] != wantID {
		t.Errorf("GetBooksByTag(%q) = %v, want exactly [%s]", tag, ids, wantID)
	}
}

// TestKeyEncoding_WorkIndex: a WorkID containing internal colons must not
// mis-parse the book:work:<wid>:<bookID> key. Two books with delimiter-bearing
// work IDs must each be listed under their own work and nothing else.
func TestKeyEncoding_WorkIndex(t *testing.T) {
	store, cleanup := setupPebbleTestDB(t)
	defer cleanup()

	widA := "WORK:A:1"
	widB := "WORK:A:1:extra" // shares the byte prefix "WORK:A:1"
	a, err := store.CreateBook(&Book{Title: "wa", FilePath: "/lib/wa.m4b", WorkID: strPtr(widA)})
	if err != nil {
		t.Fatalf("CreateBook a: %v", err)
	}
	b, err := store.CreateBook(&Book{Title: "wb", FilePath: "/lib/wb.m4b", WorkID: strPtr(widB)})
	if err != nil {
		t.Fatalf("CreateBook b: %v", err)
	}

	gotA, err := store.GetBooksByWorkID(widA)
	if err != nil {
		t.Fatalf("GetBooksByWorkID A: %v", err)
	}
	if !containsIDWB(gotA, a.ID) {
		t.Errorf("work %q missing its own book %s (got %d)", widA, a.ID, len(gotA))
	}
	if containsIDWB(gotA, b.ID) {
		t.Errorf("work %q leaked prefix-sharing book %s", widA, b.ID)
	}

	gotB, err := store.GetBooksByWorkID(widB)
	if err != nil {
		t.Fatalf("GetBooksByWorkID B: %v", err)
	}
	if !containsIDWB(gotB, b.ID) || containsIDWB(gotB, a.ID) {
		t.Errorf("work %q listing wrong: got %d books, want exactly [%s]", widB, len(gotB), b.ID)
	}
}

// TestKeyEncoding_AuthorSeriesNames: delimiter-bearing author/series names must
// store losslessly and resolve to exactly the right record by name.
func TestKeyEncoding_AuthorSeriesNames(t *testing.T) {
	store, cleanup := setupPebbleTestDB(t)
	defer cleanup()
	ps := store.(*PebbleStore)

	for _, nv := range nastyValues {
		aName := "Author " + nv.val
		author, err := ps.CreateAuthor(aName)
		if err != nil {
			t.Fatalf("CreateAuthor %q: %v", nv.name, err)
		}
		gotA, err := ps.GetAuthorByName(aName)
		if err != nil || gotA == nil || gotA.ID != author.ID {
			t.Errorf("GetAuthorByName(%q) = %v (err %v), want id %d", aName, gotA, err, author.ID)
		}
		if gotA != nil && gotA.Name != aName {
			t.Errorf("author name corrupted: got %q, want %q", gotA.Name, aName)
		}

		sName := "Series " + nv.val
		series, err := ps.CreateSeries(sName, nil)
		if err != nil {
			t.Fatalf("CreateSeries %q: %v", nv.name, err)
		}
		gotS, err := ps.GetSeriesByName(sName, nil)
		if err != nil || gotS == nil || gotS.ID != series.ID {
			t.Errorf("GetSeriesByName(%q) = %v (err %v), want id %d", sName, gotS, err, series.ID)
		}
		if gotS != nil && gotS.Name != sName {
			t.Errorf("series name corrupted: got %q, want %q", gotS.Name, sName)
		}
	}
}
