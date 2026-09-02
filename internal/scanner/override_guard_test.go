// file: internal/scanner/override_guard_test.go
// version: 2.0.0
// guid: 70a71534-36fa-4d6c-9c4a-acf8dc2de6e8
// last-edited: 2026-09-02

package scanner

import (
	"errors"
	"reflect"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/metastate"
)

// fakeFieldStateStore is the lock guard's whole store surface. GetUserPreference
// answers the legacy-blob fallback; it returns nothing unless a test sets pref.
type fakeFieldStateStore struct {
	states  []database.MetadataFieldState
	err     error
	pref    *database.UserPreference
	prefErr error
	prefKey string
	calls   int
}

func (f *fakeFieldStateStore) GetMetadataFieldStates(bookID string) ([]database.MetadataFieldState, error) {
	f.calls++
	if f.err != nil {
		return nil, f.err
	}
	return f.states, nil
}

func (f *fakeFieldStateStore) GetUserPreference(key string) (*database.UserPreference, error) {
	f.prefKey = key
	return f.pref, f.prefErr
}

// curated is the row as the user left it; scannedTags is what the file's own
// (stale, junky) tags say. Every test overlays the second onto the first. Every
// column the scanner overlays differs between the two, so "the value survived"
// can only mean the guard held -- never that the fixture happened to agree.
func curated() *database.Book {
	return &database.Book{
		Title:                "Curated Title",
		AuthorID:             new(11),
		SeriesID:             new(21),
		SeriesSequence:       new(3),
		Narrator:             new("Curated Narrator"),
		Language:             new("en"),
		Publisher:            new("Curated Publisher"),
		ASIN:                 new("CURATEDASIN"),
		Genre:                new("Curated Genre"),
		Description:          new("Curated description"),
		AudiobookReleaseYear: new(2001),
		ISBN10:               new("1111111111"),
		ISBN13:               new("9781111111111"),
	}
}

func scannedTags() *database.Book {
	return &database.Book{
		Title:                "Junk Tag Title",
		AuthorID:             new(99),
		SeriesID:             new(98),
		SeriesSequence:       new(7),
		Narrator:             new("Junk Narrator"),
		Language:             new("de"),
		Publisher:            new("Junk Publisher"),
		ASIN:                 new("JUNKASIN"),
		Genre:                new("Junk Genre"),
		Description:          new("Junk description"),
		AudiobookReleaseYear: new(1999),
		ISBN10:               new("2222222222"),
		ISBN13:               new("9782222222222"),
	}
}

// THE test that matters: a locked field survives a rescan. This is the standing
// "a running scan CLOBBERS applied metadata" data-loss bug.
//
// The keys are LITERALS, spelled the way the writer stores them
// (audiobooks.UpdateAudiobook's field extractors and the UI's FIELD_TO_API
// table) -- deliberately not the database.FieldKey* constants. The previous
// version of this test locked "author", "series" and "series_sequence", which
// matched the guard's own list and nothing the writer ever wrote, so it passed
// while a rescan clobbered every curated author, series and position in the
// library. A test that reads its keys from the code under test cannot catch
// that; one that spells the writer's keys by hand can.
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
		{"author_name", func(t *testing.T, g *database.Book) {
			if g.AuthorID == nil || *g.AuthorID != 11 {
				t.Errorf("locked author_name was repointed to %v", g.AuthorID)
			}
		}},
		{"series_name", func(t *testing.T, g *database.Book) {
			if g.SeriesID == nil || *g.SeriesID != 21 {
				t.Errorf("locked series_name was repointed to %v", g.SeriesID)
			}
		}},
		{"series_position", func(t *testing.T, g *database.Book) {
			if g.SeriesSequence == nil || *g.SeriesSequence != 3 {
				t.Errorf("locked series_position was overwritten with %v", g.SeriesSequence)
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
		{"asin", func(t *testing.T, g *database.Book) {
			if g.ASIN == nil || *g.ASIN != "CURATEDASIN" {
				t.Errorf("locked asin was overwritten with %v", g.ASIN)
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
			if !locked[tc.key] {
				t.Fatalf("writer key %q was not reported locked: the guard does not "+
					"speak the writer's vocabulary", tc.key)
			}
			got := curated()
			applyScannerFields(got, scannedTags(), locked)
			tc.assert(t, got)
		})
	}
}

// The old, wrong spellings must NOT lock anything. If they did, the vocabulary
// would have grown a second name for the same column and the two could drift
// apart again.
func TestLockedFieldsForBook_ObsoleteKeysAreNotInTheVocabulary(t *testing.T) {
	for _, stale := range []string{"author", "series", "series_sequence", "series_index"} {
		for _, f := range database.UserLockableFields {
			if f.Key == stale {
				t.Errorf("obsolete key %q is in database.UserLockableFields; the writer "+
					"stores author_name/series_name/series_position", stale)
			}
		}
	}
}

// Conformance: every column applyScannerFields overlays that has a lock key in
// database.UserLockableFields must be held by that key. Derived from the
// vocabulary, not from a hand list, so a key added to the vocabulary for a
// column the scanner overlays fails here until the guard covers it.
//
// The negative half runs first: with nothing locked the column must take the
// scanned value (proving the fixture differs AND the scanner actually overlays
// it). A column the scanner never sets is reported and skipped -- there is
// nothing to guard.
func TestApplyScannerFields_GuardsEveryLockableColumnItOverlays(t *testing.T) {
	overlaid := 0
	for _, f := range database.UserLockableFields {
		t.Run(f.Key, func(t *testing.T) {
			want := reflect.ValueOf(*curated()).FieldByName(f.Column).Interface()
			junk := reflect.ValueOf(*scannedTags()).FieldByName(f.Column).Interface()
			if reflect.DeepEqual(want, junk) {
				t.Fatalf("fixture bug: curated and scanned agree on %s; this column cannot be tested", f.Column)
			}

			unlocked := curated()
			applyScannerFields(unlocked, scannedTags(), map[string]bool{})
			gotUnlocked := reflect.ValueOf(*unlocked).FieldByName(f.Column).Interface()
			if reflect.DeepEqual(gotUnlocked, want) {
				t.Logf("scanner does not overlay Book.%s; key %q has nothing to guard here", f.Column, f.Key)
				return
			}
			if !reflect.DeepEqual(gotUnlocked, junk) {
				t.Fatalf("unlocked overlay of %s produced %v, neither curated nor scanned", f.Column, gotUnlocked)
			}
			overlaid++

			store := &fakeFieldStateStore{states: []database.MetadataFieldState{
				{Field: f.Key, OverrideLocked: true},
			}}
			locked, ok := lockedFieldsForBook(store, "book-1")
			if !ok {
				t.Fatal("healthy store reported a read failure")
			}
			got := curated()
			applyScannerFields(got, scannedTags(), locked)
			gotLocked := reflect.ValueOf(*got).FieldByName(f.Column).Interface()
			if !reflect.DeepEqual(gotLocked, want) {
				t.Errorf("lock %q did not hold Book.%s: got %v, want %v", f.Key, f.Column, gotLocked, want)
			}
		})
	}
	// Title, AuthorID, SeriesID, SeriesSequence, Narrator, Language, Publisher, ASIN.
	if overlaid != 8 {
		t.Errorf("scanner overlays %d lockable columns, expected 8; update the "+
			"coverage comment in override_guard.go", overlaid)
	}
}

// An explicit override with no lock is equally the user's word.
func TestApplyScannerFields_OverrideValueAloneProtects(t *testing.T) {
	store := &fakeFieldStateStore{states: []database.MetadataFieldState{
		{Field: database.FieldKeyTitle, OverrideValue: new(`"Curated Title"`)},
	}}
	locked, _ := lockedFieldsForBook(store, "book-1")
	got := curated()
	applyScannerFields(got, scannedTags(), locked)
	if got.Title != "Curated Title" {
		t.Errorf("a field with an explicit user override was overwritten with %q", got.Title)
	}
}

// A lock written before the per-field rows existed lives in the legacy
// user-preference blob. A book nobody has opened since the migration is exactly
// the one a background rescan would otherwise clobber.
func TestApplyScannerFields_LegacyBlobLockProtects(t *testing.T) {
	blob := `{"author_name":{"override_value":"Curated Author","override_locked":true}}`
	store := &fakeFieldStateStore{pref: &database.UserPreference{Value: &blob}}
	locked, ok := lockedFieldsForBook(store, "book-1")
	if !ok {
		t.Fatal("healthy store reported a read failure")
	}
	if store.prefKey != metastate.Key("book-1") {
		t.Errorf("legacy blob read under %q, want %q", store.prefKey, metastate.Key("book-1"))
	}
	got := curated()
	applyScannerFields(got, scannedTags(), locked)
	if got.AuthorID == nil || *got.AuthorID != 11 {
		t.Errorf("author locked in the legacy blob was repointed to %v", got.AuthorID)
	}
	if got.Title != "Junk Tag Title" {
		t.Errorf("unlocked title should still be overlaid, got %q", got.Title)
	}
}

// The converse, so the guard cannot pass by simply blocking everything: an
// UNLOCKED field must still take the scanned value, or a rescan would stop
// picking up genuine tag corrections.
func TestApplyScannerFields_UnlockedFieldsStillOverlaid(t *testing.T) {
	store := &fakeFieldStateStore{states: []database.MetadataFieldState{
		{Field: database.FieldKeyTitle, OverrideLocked: true},
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
	if got.AuthorID == nil || *got.AuthorID != 99 {
		t.Errorf("an UNLOCKED author was not overlaid: %v", got.AuthorID)
	}
}

// A provider-fetched value with no user action must NOT block the overlay --
// otherwise any book that ever had metadata fetched becomes permanently immune
// to re-tagging. This pins the HasUserOverride-only choice.
func TestApplyScannerFields_FetchedValueAloneDoesNotBlock(t *testing.T) {
	store := &fakeFieldStateStore{states: []database.MetadataFieldState{
		{Field: database.FieldKeyTitle, FetchedValue: new(`"From A Provider"`)},
	}}
	locked, ok := lockedFieldsForBook(store, "book-1")
	if !ok {
		t.Fatal("healthy store reported a read failure")
	}
	if locked[database.FieldKeyTitle] {
		t.Fatal("a fetched-only field was treated as user-locked: this path guards " +
			"USER intent, and blocking on FetchedValue would freeze re-tagging forever")
	}
	got := curated()
	applyScannerFields(got, scannedTags(), locked)
	if got.Title != "Junk Tag Title" {
		t.Errorf("fetched-only field should still be overlaid, got %q", got.Title)
	}
}

// FAIL CLOSED. An unreadable state must lock everything lockable: overwriting a
// user edit is unrecoverable, skipping one overlay is not.
func TestLockedFieldsForBook_FailsClosedOnError(t *testing.T) {
	for name, store := range map[string]*fakeFieldStateStore{
		"rows error":   {err: errors.New("pebble: iterator closed")},
		"legacy error": {prefErr: errors.New("pebble: closed")},
	} {
		t.Run(name, func(t *testing.T) {
			locked, ok := lockedFieldsForBook(store, "book-1")
			if ok {
				t.Error("a store error was reported as a successful read")
			}
			for _, key := range database.UserLockableFieldKeys() {
				if !locked[key] {
					t.Errorf("field %q not locked after a state-read error: a scan would "+
						"overwrite user edits it could not verify", key)
				}
			}

			got := curated()
			applyScannerFields(got, scannedTags(), locked)
			if !reflect.DeepEqual(got, curated()) {
				t.Errorf("fail-closed did not actually protect the row: %+v", got)
			}
		})
	}
}

// A nil store is the same unverifiable situation as an error.
func TestLockedFieldsForBook_NilStoreFailsClosed(t *testing.T) {
	locked, ok := lockedFieldsForBook(nil, "book-1")
	if ok {
		t.Error("nil store reported a successful read")
	}
	if !locked[database.FieldKeyTitle] || !locked[database.FieldKeyAuthorName] {
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

// The provider ids without a lock key (OpenLibraryID, HardcoverID, GoogleBooksID,
// WorkID) cannot be locked today, so a row claiming to lock one is inert. Pinned
// so that adding a key for one forces applyScannerFields to guard it in the same
// change (TestApplyScannerFields_GuardsEveryLockableColumnItOverlays enforces
// the other direction).
func TestApplyScannerFields_UnkeyedProviderIDsAreNotGuarded(t *testing.T) {
	store := &fakeFieldStateStore{states: []database.MetadataFieldState{
		{Field: "open_library_id", OverrideLocked: true},
	}}
	locked, _ := lockedFieldsForBook(store, "book-1")
	got := curated()
	got.OpenLibraryID = new("OLCURATED")
	scanned := scannedTags()
	scanned.OpenLibraryID = new("OLJUNK")
	applyScannerFields(got, scanned, locked)
	if got.OpenLibraryID == nil || *got.OpenLibraryID != "OLJUNK" {
		t.Errorf("OpenLibraryID has no lock key so it must still be overlaid; got %v. "+
			"If a key was just added to database.UserLockableFields, guard it in applyScannerFields", got.OpenLibraryID)
	}
}
