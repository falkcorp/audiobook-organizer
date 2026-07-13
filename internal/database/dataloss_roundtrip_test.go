// file: internal/database/dataloss_roundtrip_test.go
// version: 1.0.0
// guid: e4a5b6c7-8d9e-0f1a-2b3c-roundtrip00001
// last-edited: 2026-07-13

package database

import (
	"encoding/json"
	"reflect"
	"testing"
)

// T2 — round-trip fidelity per full-replace setter.
//
// A generic wipe detector: build a fully-populated record (every field set to a
// distinctive non-zero sentinel via the reflective populator from T1), snapshot
// it, write it back UNCHANGED via the setter, re-read, and assert field-by-field
// equality. Any field that fails to round-trip and is NOT in the a-priori,
// struct-semantics-justified exclusion set is a data-loss wipe → the test fails
// loudly rather than being papered over.
//
// The exclusion sets are decided from struct/setter SEMANTICS before running,
// NOT grown to make the test green. If you see a NEW field fail here, do not add
// it to the exclusion list reflexively — first determine whether the setter is
// wiping something it should preserve (a real bug).

// jsonClone deep-copies via marshal→unmarshal so the snapshot is independent of
// the setter's in-place mutation of the input struct.
func jsonClone(t *testing.T, src, dst any) {
	t.Helper()
	b, err := json.Marshal(src)
	if err != nil {
		t.Fatalf("jsonClone marshal: %v", err)
	}
	if err := json.Unmarshal(b, dst); err != nil {
		t.Fatalf("jsonClone unmarshal: %v", err)
	}
}

// assertFieldsRoundTrip compares got vs want field-by-field, skipping the named
// exclusions (with the reason baked into the failure message when something
// unexpected drifts).
func assertFieldsRoundTrip(t *testing.T, label string, got, want any, excluded map[string]string) {
	t.Helper()
	gv := reflect.ValueOf(got).Elem()
	wv := reflect.ValueOf(want).Elem()
	for i := 0; i < gv.NumField(); i++ {
		name := gv.Type().Field(i).Name
		if !gv.Field(i).CanInterface() {
			continue
		}
		if reason, skip := excluded[name]; skip {
			_ = reason
			continue
		}
		if !reflect.DeepEqual(gv.Field(i).Interface(), wv.Field(i).Interface()) {
			t.Errorf("%s: field %q did NOT round-trip: got %#v, want %#v. "+
				"If this is a legitimately derived/dropped field, document it in the exclusion set with justification; "+
				"otherwise the setter is WIPING data — treat as a real bug.",
				label, name, gv.Field(i).Interface(), wv.Field(i).Interface())
		}
	}
}

// TestRoundTrip_UpdateBook writes a fully-populated Book back unchanged and
// asserts nothing is dropped. Exclusions (a priori, by semantics):
//
//	CreatedAt — UpdateBook preserves the ORIGINAL row's created_at (from the
//	            CreateBook seed), not the caller's value. By design.
//	UpdatedAt — UpdateBook stamps time.Now() on every write. By design.
func TestRoundTrip_UpdateBook(t *testing.T) {
	store, cleanup := setupPebbleTestDB(t)
	defer cleanup()

	created, err := store.CreateBook(&Book{Title: "seed", FilePath: "/lib/rt-book.m4b"})
	if err != nil {
		t.Fatalf("CreateBook: %v", err)
	}

	full := &Book{}
	dlPopulateNonZero(t, reflect.ValueOf(full).Elem(), "Book")
	full.ID = created.ID
	full.FilePath = "/lib/rt-book.m4b"

	want := &Book{}
	jsonClone(t, full, want)

	if _, err := store.UpdateBook(created.ID, full); err != nil {
		t.Fatalf("UpdateBook: %v", err)
	}
	got, err := store.GetBookByID(created.ID)
	if err != nil || got == nil {
		t.Fatalf("GetBookByID: err=%v got=%v", err, got)
	}

	assertFieldsRoundTrip(t, "UpdateBook", got, want, map[string]string{
		"CreatedAt": "preserved from the original row, not the caller",
		"UpdatedAt": "stamped to now on every write",
	})
}

// TestRoundTrip_UpdateBookFile writes a fully-populated BookFile back unchanged.
// Exclusions (a priori, by semantics):
//
//	ID / CreatedAt / UpdatedAt — identity + timestamps managed by the setter.
//	AcoustIDSeg0..6            — deliberately DROPPED on write by
//	                             marshalBookFileDropSegs (T020); the stored row
//	                             never carries them. Not a wipe of live data.
func TestRoundTrip_UpdateBookFile(t *testing.T) {
	store, cleanup := setupPebbleTestDB(t)
	defer cleanup()

	book, err := store.CreateBook(&Book{Title: "rt-file-book", FilePath: "/lib/rt-file-book.m4b"})
	if err != nil {
		t.Fatalf("CreateBook: %v", err)
	}

	ps := store.(*PebbleStore)
	seed := &BookFile{BookID: book.ID, FilePath: "/lib/rt-file-book/01.mp3"}
	if err := ps.CreateBookFile(seed); err != nil {
		t.Fatalf("CreateBookFile: %v", err)
	}

	full := &BookFile{}
	dlPopulateNonZero(t, reflect.ValueOf(full).Elem(), "BookFile")
	full.ID = seed.ID
	full.BookID = book.ID

	want := &BookFile{}
	jsonClone(t, full, want)

	if err := ps.UpdateBookFile(seed.ID, full); err != nil {
		t.Fatalf("UpdateBookFile: %v", err)
	}
	files, err := ps.GetBookFiles(book.ID)
	if err != nil {
		t.Fatalf("GetBookFiles: %v", err)
	}
	var got *BookFile
	for i := range files {
		if files[i].ID == seed.ID {
			got = &files[i]
			break
		}
	}
	if got == nil {
		t.Fatalf("book file %s not found after update", seed.ID)
	}

	assertFieldsRoundTrip(t, "UpdateBookFile", got, want, map[string]string{
		"ID":           "identity, set by the setter",
		"CreatedAt":    "preserved from the original row",
		"UpdatedAt":    "stamped to now on every write",
		"AcoustIDSeg0": "deprecated 7-seg field intentionally dropped on write (T020)",
		"AcoustIDSeg1": "deprecated 7-seg field intentionally dropped on write (T020)",
		"AcoustIDSeg2": "deprecated 7-seg field intentionally dropped on write (T020)",
		"AcoustIDSeg3": "deprecated 7-seg field intentionally dropped on write (T020)",
		"AcoustIDSeg4": "deprecated 7-seg field intentionally dropped on write (T020)",
		"AcoustIDSeg5": "deprecated 7-seg field intentionally dropped on write (T020)",
		"AcoustIDSeg6": "deprecated 7-seg field intentionally dropped on write (T020)",
	})
}

// TestRoundTrip_AuthorSeries covers the author/series setters that actually
// exist. NOTE: there is no UpsertAuthor/UpsertSeries full-struct setter (the
// task brief assumed one) — Author={ID,Name} and Series={ID,Name,AuthorID}
// carry NO heavy/denormalized fields, so the write-back-wipe class does not
// apply. These setters take individual fields, so round-trip fidelity is simply
// create→read and rename→read.
func TestRoundTrip_AuthorSeries(t *testing.T) {
	store, cleanup := setupPebbleTestDB(t)
	defer cleanup()
	ps := store.(*PebbleStore)

	author, err := ps.CreateAuthor("Ursula K. Le Guin")
	if err != nil {
		t.Fatalf("CreateAuthor: %v", err)
	}
	gotA, err := ps.GetAuthorByID(author.ID)
	if err != nil || gotA == nil {
		t.Fatalf("GetAuthorByID: err=%v got=%v", err, gotA)
	}
	if gotA.Name != "Ursula K. Le Guin" || gotA.ID != author.ID {
		t.Errorf("author round-trip: got %+v", gotA)
	}
	if err := ps.UpdateAuthorName(author.ID, "U. K. Le Guin"); err != nil {
		t.Fatalf("UpdateAuthorName: %v", err)
	}
	if gotA, _ = ps.GetAuthorByID(author.ID); gotA == nil || gotA.Name != "U. K. Le Guin" {
		t.Errorf("author rename not persisted: got %+v", gotA)
	}

	aid := author.ID
	series, err := ps.CreateSeries("Earthsea", &aid)
	if err != nil {
		t.Fatalf("CreateSeries: %v", err)
	}
	gotS, err := ps.GetSeriesByID(series.ID)
	if err != nil || gotS == nil {
		t.Fatalf("GetSeriesByID: err=%v got=%v", err, gotS)
	}
	if gotS.Name != "Earthsea" || gotS.AuthorID == nil || *gotS.AuthorID != aid {
		t.Errorf("series round-trip: got %+v", gotS)
	}
	if err := ps.UpdateSeriesName(series.ID, "Earthsea Cycle"); err != nil {
		t.Fatalf("UpdateSeriesName: %v", err)
	}
	if gotS, _ = ps.GetSeriesByID(series.ID); gotS == nil || gotS.Name != "Earthsea Cycle" {
		t.Errorf("series rename not persisted: got %+v", gotS)
	}
}
