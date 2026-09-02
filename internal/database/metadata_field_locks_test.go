// file: internal/database/metadata_field_locks_test.go
// version: 1.1.0
// guid: 041867f9-1429-4696-8eac-a3faef717c4d
// last-edited: 2026-09-02

package database

import (
	"errors"
	"reflect"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/metastate"
)

// Every Column in the vocabulary must be a real Book field, so the mapping
// cannot silently name a column that was renamed away.
func TestUserLockableFieldsNameRealBookColumns(t *testing.T) {
	bookType := reflect.TypeOf(Book{})
	seen := map[string]bool{}
	for _, f := range UserLockableFields {
		if f.Key == "" || f.Column == "" {
			t.Errorf("vocabulary entry %+v has an empty key or column", f)
		}
		if seen[f.Key] {
			t.Errorf("lock key %q listed twice", f.Key)
		}
		seen[f.Key] = true
		if _, ok := bookType.FieldByName(f.Column); !ok {
			t.Errorf("lock key %q maps to Book.%s, which does not exist", f.Key, f.Column)
		}
	}
	if got, want := len(UserLockableFieldKeys()), len(UserLockableFields); got != want {
		t.Errorf("UserLockableFieldKeys() has %d keys, vocabulary has %d", got, want)
	}
	for k := range AllUserLockableFieldsLocked() {
		if !seen[k] {
			t.Errorf("AllUserLockableFieldsLocked reports %q, not in the vocabulary", k)
		}
	}
	if len(AllUserLockableFieldsLocked()) != len(UserLockableFields) {
		t.Errorf("AllUserLockableFieldsLocked locks %d keys, vocabulary has %d",
			len(AllUserLockableFieldsLocked()), len(UserLockableFields))
	}
}

type lockReader struct {
	states    []MetadataFieldState
	statesErr error
	pref      *UserPreference
	prefErr   error
	prefKey   string
}

func (r *lockReader) GetMetadataFieldStates(string) ([]MetadataFieldState, error) {
	return r.states, r.statesErr
}

func (r *lockReader) GetUserPreference(key string) (*UserPreference, error) {
	r.prefKey = key
	return r.pref, r.prefErr
}

func TestLockedUserFields_RowsDecideWhenPresent(t *testing.T) {
	r := &lockReader{states: []MetadataFieldState{
		{Field: FieldKeyAuthorName, OverrideLocked: true},
		{Field: FieldKeyTitle, OverrideValue: new(`"Curated"`)},
		{Field: FieldKeyNarrator, FetchedValue: new(`"From Provider"`)},
		{Field: FieldKeySeriesName},
	}}
	locked, err := LockedUserFields(r, "b1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !locked[FieldKeyAuthorName] {
		t.Error("OverrideLocked row not reported locked")
	}
	if !locked[FieldKeyTitle] {
		t.Error("OverrideValue-only row not reported locked: an explicit value is the user's word")
	}
	if locked[FieldKeyNarrator] {
		t.Error("fetched-only row reported locked: a provider value is not a user act")
	}
	if locked[FieldKeySeriesName] {
		t.Error("empty row reported locked")
	}
	if r.prefKey != "" {
		t.Error("legacy blob consulted even though rows exist")
	}
}

func TestLockedUserFields_FallsBackToLegacyBlobWhenNoRows(t *testing.T) {
	blob := `{"series_position":{"override_value":3,"override_locked":true},` +
		`"title":{"override_value":"Curated"},` +
		`"narrator":{"fetched_value":"Prov"},` +
		`"language":{"override_value":null}}`
	r := &lockReader{pref: &UserPreference{Value: &blob}}
	locked, err := LockedUserFields(r, "b1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if r.prefKey != metastate.Key("b1") {
		t.Errorf("legacy blob read under %q, want %q", r.prefKey, metastate.Key("b1"))
	}
	if !locked[FieldKeySeriesPosition] || !locked[FieldKeyTitle] {
		t.Errorf("legacy locks missed: %v", locked)
	}
	if locked[FieldKeyNarrator] {
		t.Error("legacy fetched-only field reported locked")
	}
	if locked[FieldKeyLanguage] {
		t.Error("legacy null override reported locked")
	}
}

func TestLockedUserFields_NoStateMeansNothingLocked(t *testing.T) {
	locked, err := LockedUserFields(&lockReader{}, "b1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(locked) != 0 {
		t.Errorf("want no locks, got %v", locked)
	}
}

func TestLockedUserFields_FailsClosed(t *testing.T) {
	cases := []struct {
		name   string
		reader MetadataFieldStateReader
		bookID string
	}{
		{"nil reader", nil, "b1"},
		{"empty id", &lockReader{}, ""},
		{"rows error", &lockReader{statesErr: errors.New("pebble: iterator closed")}, "b1"},
		{"legacy error", &lockReader{prefErr: errors.New("pebble: closed")}, "b1"},
		{"legacy garbage", &lockReader{pref: &UserPreference{Value: new("{not json")}}, "b1"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			locked, err := LockedUserFields(tc.reader, tc.bookID)
			if err == nil {
				t.Fatalf("want an error, got locks %v", locked)
			}
			if !errors.Is(err, ErrFieldLocksUnavailable) {
				t.Errorf("error %v does not wrap ErrFieldLocksUnavailable", err)
			}
			if locked != nil {
				t.Errorf("a failed read must not hand back a usable map (got %v): a caller "+
					"that ignores the error would treat it as 'nothing locked'", locked)
			}
		})
	}
}

// curatedLockBook has a real value in every lockable column so a mutation to
// any of them is observable.
func curatedLockBook() *Book {
	return &Book{
		ID:                   "b-apply",
		Title:                "Curated Title",
		AuthorID:             new(7),
		SeriesID:             new(21),
		SeriesSequence:       new(1),
		Narrator:             new("Curated Narrator"),
		Publisher:            new("Curated Publisher"),
		Language:             new("en"),
		AudiobookReleaseYear: new(2001),
		ISBN10:               new("1111111111"),
		ISBN13:               new("9781111111111"),
		ASIN:                 new("B000CURATED"),
		Genre:                new("Curated Genre"),
		Description:          new("Curated description."),
	}
}

// clobberEverything is a writer that changes every lockable column.
func clobberEverything(b *Book) {
	b.Title = "Clobbered"
	b.AuthorID = new(8)
	b.SeriesID = new(22)
	b.SeriesSequence = new(5)
	b.Narrator = new("Clobbered Narrator")
	b.Publisher = new("Clobbered Publisher")
	b.Language = new("de")
	b.AudiobookReleaseYear = new(2020)
	b.ISBN10 = new("2222222222")
	b.ISBN13 = new("9782222222222")
	b.ASIN = new("B000CLOBBER")
	b.Genre = new("Clobbered Genre")
	b.Description = new("Clobbered description.")
}

func columnOf(t *testing.T, b *Book, column string) any {
	t.Helper()
	v := reflect.ValueOf(b).Elem().FieldByName(column)
	if !v.IsValid() {
		t.Fatalf("Book has no column %q", column)
	}
	if v.Kind() == reflect.Pointer {
		if v.IsNil() {
			return nil
		}
		return v.Elem().Interface()
	}
	return v.Interface()
}

// Every key, through the chokepoint: unlocked the column changes (so the
// fixture can see the writer run), locked it is restored and reported, and a
// sibling column still changes (so a blanket refusal cannot pass).
func TestFieldLocksApply_RestoresEveryLockedColumn(t *testing.T) {
	for _, f := range UserLockableFields {
		t.Run(f.Key, func(t *testing.T) {
			want := columnOf(t, curatedLockBook(), f.Column)

			open := curatedLockBook()
			if restored := (FieldLocks{}).Apply(open, clobberEverything); restored != nil {
				t.Fatalf("nothing locked but restored %v", restored)
			}
			if reflect.DeepEqual(columnOf(t, open, f.Column), want) {
				t.Fatalf("fixture cannot observe the writer: Book.%s unchanged when unlocked", f.Column)
			}

			locks := NewFieldLocks("b-apply", map[string]bool{f.Key: true})
			book := curatedLockBook()
			restored := locks.Apply(book, clobberEverything)
			if got := columnOf(t, book, f.Column); !reflect.DeepEqual(got, want) {
				t.Errorf("lock %s: Book.%s = %v, want %v", f.Key, f.Column, got, want)
			}
			if !reflect.DeepEqual(restored, []string{f.Key}) {
				t.Errorf("restored = %v, want [%s]", restored, f.Key)
			}
			sibling := "Title"
			if f.Key == FieldKeyTitle {
				sibling = "Narrator"
			}
			if reflect.DeepEqual(columnOf(t, book, sibling), columnOf(t, curatedLockBook(), sibling)) {
				t.Errorf("locking %s also blocked Book.%s", f.Key, sibling)
			}
		})
	}
}

func TestFieldLocksApply_SeriesNameLockKeepsThePosition(t *testing.T) {
	locks := NewFieldLocks("b-apply", map[string]bool{FieldKeySeriesName: true})
	book := curatedLockBook()
	restored := locks.Apply(book, clobberEverything)
	if *book.SeriesID != 21 || *book.SeriesSequence != 1 {
		t.Errorf("series lock must hold both the series and its position: id=%d seq=%d", *book.SeriesID, *book.SeriesSequence)
	}
	if !reflect.DeepEqual(restored, []string{FieldKeySeriesName}) {
		t.Errorf("restored = %v, want [series_name] (the position is reported under the lock that protected it)", restored)
	}
}

func TestFieldLocksApply_UnchangedLockedColumnIsNotReported(t *testing.T) {
	locks := NewFieldLocks("b-apply", AllUserLockableFieldsLocked())
	book := curatedLockBook()
	restored := locks.Apply(book, func(b *Book) { b.FilePath = "/moved/elsewhere.m4b" })
	if restored != nil {
		t.Errorf("a writer that touched no locked column was reported as restored: %v", restored)
	}
	if book.FilePath != "/moved/elsewhere.m4b" {
		t.Errorf("non-lockable column must still be written")
	}
}

func TestFieldLocksApply_CatchesWritesThroughThePointer(t *testing.T) {
	locks := NewFieldLocks("b-apply", map[string]bool{FieldKeyNarrator: true})
	book := curatedLockBook()
	restored := locks.Apply(book, func(b *Book) { *b.Narrator = "Overwritten in place" })
	if *book.Narrator != "Curated Narrator" {
		t.Errorf("Narrator = %q, want the curated value restored", *book.Narrator)
	}
	if !reflect.DeepEqual(restored, []string{FieldKeyNarrator}) {
		t.Errorf("restored = %v", restored)
	}
}

func TestFieldLocksApply_NilToValueAndValueToNil(t *testing.T) {
	// A locked-BLANK field is the case the fill-empty writers miss: the user
	// cleared it on purpose and the writer "helpfully" fills it back in.
	locks := NewFieldLocks("b-apply", map[string]bool{FieldKeyISBN13: true, FieldKeyGenre: true})
	book := curatedLockBook()
	book.ISBN13 = nil
	restored := locks.Apply(book, func(b *Book) {
		b.ISBN13 = new("9783333333333") // fill-empty
		b.Genre = nil                   // clear
	})
	if book.ISBN13 != nil {
		t.Errorf("locked-blank ISBN13 was filled: %q", *book.ISBN13)
	}
	if book.Genre == nil || *book.Genre != "Curated Genre" {
		t.Errorf("locked Genre was cleared")
	}
	if !reflect.DeepEqual(restored, []string{FieldKeyISBN13, FieldKeyGenre}) {
		t.Errorf("restored = %v", restored)
	}
}

func TestApplyRespectingLocks_FailsClosedWithoutMutating(t *testing.T) {
	book := curatedLockBook()
	ran := false
	restored, err := ApplyRespectingLocks(&lockReader{statesErr: errors.New("pebble: closed")}, book, func(b *Book) {
		ran = true
		clobberEverything(b)
	})
	if !errors.Is(err, ErrFieldLocksUnavailable) {
		t.Fatalf("err = %v, want ErrFieldLocksUnavailable", err)
	}
	if ran {
		t.Errorf("mutate ran despite an unreadable lock set")
	}
	if restored != nil {
		t.Errorf("restored = %v on error", restored)
	}
	if !reflect.DeepEqual(book, curatedLockBook()) {
		t.Errorf("book changed on a failed lock read")
	}
	if _, err := ApplyRespectingLocks(&lockReader{}, nil, clobberEverything); err == nil {
		t.Errorf("nil book must be an error, not a silent no-op")
	}
}

func TestApplyRespectingLocks_ReadsRowsAndLegacyBlob(t *testing.T) {
	// Rows win.
	book := curatedLockBook()
	restored, err := ApplyRespectingLocks(&lockReader{states: []MetadataFieldState{
		{Field: FieldKeyTitle, OverrideLocked: true},
	}}, book, clobberEverything)
	if err != nil {
		t.Fatal(err)
	}
	if book.Title != "Curated Title" || *book.Narrator != "Clobbered Narrator" {
		t.Errorf("row lock: title=%q narrator=%q", book.Title, *book.Narrator)
	}
	if !reflect.DeepEqual(restored, []string{FieldKeyTitle}) {
		t.Errorf("restored = %v", restored)
	}

	// No rows: the legacy blob still locks.
	book = curatedLockBook()
	pref := `{"author_name":{"override_locked":true}}`
	restored, err = ApplyRespectingLocks(&lockReader{pref: &UserPreference{Value: &pref}}, book, clobberEverything)
	if err != nil {
		t.Fatal(err)
	}
	if *book.AuthorID != 7 {
		t.Errorf("legacy author lock not honored: %d", *book.AuthorID)
	}
	if !reflect.DeepEqual(restored, []string{FieldKeyAuthorName}) {
		t.Errorf("restored = %v", restored)
	}
}

func TestFieldLocksAccessors(t *testing.T) {
	l := NewFieldLocks("b", map[string]bool{FieldKeyTitle: true, FieldKeyGenre: false})
	if !l.Locked(FieldKeyTitle) || l.Locked(FieldKeyGenre) || !l.Any() {
		t.Errorf("accessors disagree with the map: %+v", l)
	}
	if got := l.Set(); !reflect.DeepEqual(got, map[string]bool{FieldKeyTitle: true}) {
		t.Errorf("Set() = %v", got)
	}
	if (FieldLocks{}).Any() {
		t.Errorf("zero value must lock nothing")
	}
}
