// file: internal/scanner/override_guard_test.go
// version: 1.0.0
// guid: 70a71534-36fa-4d6c-9c4a-acf8dc2de6e8
// last-edited: 2026-08-24

package scanner

import (
	"errors"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

type fakeFieldStateStore struct {
	states []database.MetadataFieldState
	err    error
	calls  int
}

func (f *fakeFieldStateStore) GetMetadataFieldStates(bookID string) ([]database.MetadataFieldState, error) {
	f.calls++
	if f.err != nil {
		return nil, f.err
	}
	return f.states, nil
}

// curated is the row as the user left it; scannedTags is what the file's own
// (stale, junky) tags say. Every test overlays the second onto the first.
func curated() *database.Book {
	return &database.Book{
		Title:          "Curated Title",
		AuthorID:       intPtr(11),
		SeriesID:       intPtr(21),
		SeriesSequence: intPtr(3),
		Narrator:       strPtr("Curated Narrator"),
		Language:       strPtr("en"),
		Publisher:      strPtr("Curated Publisher"),
		ASIN:           strPtr("CURATEDASIN"),
	}
}

func scannedTags() *database.Book {
	return &database.Book{
		Title:          "Junk Tag Title",
		AuthorID:       intPtr(99),
		SeriesID:       intPtr(98),
		SeriesSequence: intPtr(7),
		Narrator:       strPtr("Junk Narrator"),
		Language:       strPtr("de"),
		Publisher:      strPtr("Junk Publisher"),
		ASIN:           strPtr("JUNKASIN"),
	}
}

// THE test that matters: a locked field survives a rescan. This is the standing
// "a running scan CLOBBERS applied metadata" data-loss bug.
func TestApplyScannerFields_LockedFieldSurvivesRescan(t *testing.T) {
	for _, tc := range []struct {
		key    string
		assert func(t *testing.T, got *database.Book)
	}{
		{"title", func(t *testing.T, g *database.Book) {
			if g.Title != "Curated Title" {
				t.Errorf("locked title was overwritten with %q", g.Title)
			}
		}},
		{"author", func(t *testing.T, g *database.Book) {
			if g.AuthorID == nil || *g.AuthorID != 11 {
				t.Errorf("locked author was repointed to %v", g.AuthorID)
			}
		}},
		{"series", func(t *testing.T, g *database.Book) {
			if g.SeriesID == nil || *g.SeriesID != 21 {
				t.Errorf("locked series was repointed to %v", g.SeriesID)
			}
		}},
		{"series_sequence", func(t *testing.T, g *database.Book) {
			if g.SeriesSequence == nil || *g.SeriesSequence != 3 {
				t.Errorf("locked series_sequence was overwritten with %v", g.SeriesSequence)
			}
		}},
		{"narrator", func(t *testing.T, g *database.Book) {
			if g.Narrator == nil || *g.Narrator != "Curated Narrator" {
				t.Errorf("locked narrator was overwritten with %v", g.Narrator)
			}
		}},
		{"language", func(t *testing.T, g *database.Book) {
			if g.Language == nil || *g.Language != "en" {
				t.Errorf("locked language was overwritten with %v", g.Language)
			}
		}},
		{"publisher", func(t *testing.T, g *database.Book) {
			if g.Publisher == nil || *g.Publisher != "Curated Publisher" {
				t.Errorf("locked publisher was overwritten with %v", g.Publisher)
			}
		}},
	} {
		t.Run(tc.key, func(t *testing.T) {
			store := &fakeFieldStateStore{states: []database.MetadataFieldState{
				{Field: tc.key, OverrideLocked: true},
			}}
			locked, ok := lockedFieldsForBook(store, "book-1")
			if !ok {
				t.Fatal("state read reported failure on a healthy store")
			}
			got := curated()
			applyScannerFields(got, scannedTags(), locked)
			tc.assert(t, got)
		})
	}
}

// An explicit override with no lock is equally the user's word.
func TestApplyScannerFields_OverrideValueAloneProtects(t *testing.T) {
	store := &fakeFieldStateStore{states: []database.MetadataFieldState{
		{Field: "title", OverrideValue: strPtr(`"Curated Title"`)},
	}}
	locked, _ := lockedFieldsForBook(store, "book-1")
	got := curated()
	applyScannerFields(got, scannedTags(), locked)
	if got.Title != "Curated Title" {
		t.Errorf("a field with an explicit user override was overwritten with %q", got.Title)
	}
}

// The converse, so the guard cannot pass by simply blocking everything: an
// UNLOCKED field must still take the scanned value, or a rescan would stop
// picking up genuine tag corrections.
func TestApplyScannerFields_UnlockedFieldsStillOverlaid(t *testing.T) {
	store := &fakeFieldStateStore{states: []database.MetadataFieldState{
		{Field: "title", OverrideLocked: true},
	}}
	locked, _ := lockedFieldsForBook(store, "book-1")
	got := curated()
	applyScannerFields(got, scannedTags(), locked)

	if got.Title != "Curated Title" {
		t.Fatalf("precondition: locked title should be held, got %q", got.Title)
	}
	if got.Narrator == nil || *got.Narrator != "Junk Narrator" {
		t.Errorf("an UNLOCKED field was not overlaid: narrator=%v -- the guard is "+
			"blocking indiscriminately, which silently freezes the whole library", got.Narrator)
	}
	if got.Publisher == nil || *got.Publisher != "Junk Publisher" {
		t.Errorf("an UNLOCKED field was not overlaid: publisher=%v", got.Publisher)
	}
}

// A provider-fetched value with no user action must NOT block the overlay --
// otherwise any book that ever had metadata fetched becomes permanently immune
// to re-tagging. This pins the HasUserOverride-only choice.
func TestApplyScannerFields_FetchedValueAloneDoesNotBlock(t *testing.T) {
	store := &fakeFieldStateStore{states: []database.MetadataFieldState{
		{Field: "title", FetchedValue: strPtr(`"From A Provider"`)},
	}}
	locked, ok := lockedFieldsForBook(store, "book-1")
	if !ok {
		t.Fatal("healthy store reported a read failure")
	}
	if locked["title"] {
		t.Fatal("a fetched-only field was treated as user-locked: this path guards " +
			"USER intent, and blocking on FetchedValue would freeze re-tagging forever")
	}
	got := curated()
	applyScannerFields(got, scannedTags(), locked)
	if got.Title != "Junk Tag Title" {
		t.Errorf("fetched-only field should still be overlaid, got %q", got.Title)
	}
}

// FAIL CLOSED. An unreadable state must lock everything guarded: overwriting a
// user edit is unrecoverable, skipping one overlay is not.
func TestLockedFieldsForBook_FailsClosedOnError(t *testing.T) {
	store := &fakeFieldStateStore{err: errors.New("pebble: iterator closed")}
	locked, ok := lockedFieldsForBook(store, "book-1")
	if ok {
		t.Error("a store error was reported as a successful read")
	}
	for _, key := range guardedFieldKeys {
		if !locked[key] {
			t.Errorf("field %q not locked after a state-read error: a scan would "+
				"overwrite user edits it could not verify", key)
		}
	}

	got := curated()
	applyScannerFields(got, scannedTags(), locked)
	if got.Title != "Curated Title" || got.Narrator == nil || *got.Narrator != "Curated Narrator" {
		t.Error("fail-closed did not actually protect the row")
	}
}

// A nil store is the same unverifiable situation as an error.
func TestLockedFieldsForBook_NilStoreFailsClosed(t *testing.T) {
	locked, ok := lockedFieldsForBook(nil, "book-1")
	if ok {
		t.Error("nil store reported a successful read")
	}
	if !locked["title"] {
		t.Error("nil store did not fail closed")
	}
}

// Fields the scanner is authoritative for must never be guarded -- a stale lock
// must not be able to freeze a file's real hash/size, which would break dedup.
func TestApplyScannerFields_FileDerivedFieldsAreNeverGuarded(t *testing.T) {
	store := &fakeFieldStateStore{err: errors.New("locked everything")}
	locked, _ := lockedFieldsForBook(store, "book-1")

	dst := curated()
	dst.FilePath = "/old/path.m4b"
	scanned := scannedTags()
	scanned.FilePath = "/new/path.m4b"
	scanned.FileSize = func() *int64 { v := int64(4242); return &v }()
	scanned.Format = "m4b"

	applyScannerFields(dst, scanned, locked)

	if dst.FilePath != "/new/path.m4b" {
		t.Errorf("FilePath is file-derived and must always update, got %q", dst.FilePath)
	}
	if dst.FileSize == nil || *dst.FileSize != 4242 {
		t.Errorf("FileSize is file-derived and must always update, got %v", dst.FileSize)
	}
	if dst.Format != "m4b" {
		t.Errorf("Format is file-derived and must always update, got %q", dst.Format)
	}
}

// ASIN and the other provider ids have NO field-state key, so nothing can lock
// them today. Pinned so that if a key is ever added, this test fails and forces
// guardedFieldKeys to be updated in the same change.
func TestApplyScannerFields_UnkeyedProviderIDsAreNotGuarded(t *testing.T) {
	store := &fakeFieldStateStore{states: []database.MetadataFieldState{
		{Field: "asin", OverrideLocked: true},
	}}
	locked, _ := lockedFieldsForBook(store, "book-1")
	got := curated()
	applyScannerFields(got, scannedTags(), locked)
	if got.ASIN == nil || *got.ASIN != "JUNKASIN" {
		t.Errorf("ASIN has no entry in guardedFieldKeys so it must still be overlaid; "+
			"got %v. If an \"asin\" key was just added, add it to guardedFieldKeys too", got.ASIN)
	}
}
