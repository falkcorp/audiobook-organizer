// file: internal/database/metadata_field_locks.go
// version: 1.2.0
// guid: 0d1a2cd0-a75c-4990-bb1f-bac01864e50c
// last-edited: 2026-09-02

package database

import (
	"encoding/json"
	"errors"
	"fmt"
	"reflect"

	"github.com/falkcorp/audiobook-organizer/internal/metastate"
)

// THE ONE VOCABULARY for user field locks.
//
// MetadataFieldState rows are keyed by a field-name string, and the UI promises
// "Edited fields are automatically locked to prevent overwrites from future
// fetches". That promise only holds if every WRITER of a lock and every READER
// of a lock spell the field the same way. Until 2026-09-02 they did not:
//
//   - the writer (audiobooks.UpdateAudiobook's field extractors, and the UI's
//     FIELD_TO_API / FIELD_STATE_KEYS tables) stored author_name, series_name,
//     series_position;
//   - the scanner's rescan guard consulted author, series, series_sequence --
//     keys nothing ever wrote -- so a rescan clobbered every curated author,
//     series and position while the guard reported success;
//   - no metafetch apply path (auto-fetch, candidate apply, batch-apply-cached,
//     transcription auto-match, metadata upgrade) consulted the state at all.
//
// The constants below are the keys the writer uses. Every guard must reference
// them by name, never by a literal, so a renamed key is a compile error instead
// of a silently inert guard. UserLockableFields ties each key to the Book
// column it protects; the conformance tests in internal/audiobooks and
// internal/metafetch iterate it and prove every key both is written and blocks
// its column.
const (
	FieldKeyTitle                = "title"
	FieldKeyAuthorName           = "author_name"
	FieldKeySeriesName           = "series_name"
	FieldKeySeriesPosition       = "series_position"
	FieldKeyNarrator             = "narrator"
	FieldKeyPublisher            = "publisher"
	FieldKeyLanguage             = "language"
	FieldKeyAudiobookReleaseYear = "audiobook_release_year"
	FieldKeyISBN10               = "isbn10"
	FieldKeyISBN13               = "isbn13"
	FieldKeyASIN                 = "asin"
	FieldKeyGenre                = "genre"
	FieldKeyDescription          = "description"
)

// UserLockableField maps one lock key to the Book column it protects. Column is
// the Go field name on Book (TestUserLockableFieldsNameRealBookColumns pins that
// each one exists). author_name and series_name lock ENTITY ids: "do not repoint
// this book at a different author/series" is the same intent expressed at the
// id level, and the join tables follow the id.
type UserLockableField struct {
	Key    string
	Column string
}

// UserLockableFields is every key a user can lock, in display order. Derived
// from the writers -- audiobooks.userEditFieldExtractors, ApplyOverrideToPayload
// and the UI's FIELD_TO_API table -- not from any reader's opinion of what
// matters. If a writer gains a key, add it here in the same change; the
// conformance test in internal/audiobooks fails otherwise.
var UserLockableFields = []UserLockableField{
	{Key: FieldKeyTitle, Column: "Title"},
	{Key: FieldKeyAuthorName, Column: "AuthorID"},
	{Key: FieldKeySeriesName, Column: "SeriesID"},
	{Key: FieldKeySeriesPosition, Column: "SeriesSequence"},
	{Key: FieldKeyNarrator, Column: "Narrator"},
	{Key: FieldKeyPublisher, Column: "Publisher"},
	{Key: FieldKeyLanguage, Column: "Language"},
	{Key: FieldKeyAudiobookReleaseYear, Column: "AudiobookReleaseYear"},
	{Key: FieldKeyISBN10, Column: "ISBN10"},
	{Key: FieldKeyISBN13, Column: "ISBN13"},
	{Key: FieldKeyASIN, Column: "ASIN"},
	{Key: FieldKeyGenre, Column: "Genre"},
	{Key: FieldKeyDescription, Column: "Description"},
}

// UserLockableFieldKeys returns the lock keys in UserLockableFields order.
func UserLockableFieldKeys() []string {
	keys := make([]string, 0, len(UserLockableFields))
	for _, f := range UserLockableFields {
		keys = append(keys, f.Key)
	}
	return keys
}

// AllUserLockableFieldsLocked is the fail-closed answer: every lockable key
// reported locked. Callers that cannot abort (the scanner still owns the
// file-derived columns and must record them) use it when LockedUserFields
// errors, so an unreadable state can never be mistaken for "nothing locked".
func AllUserLockableFieldsLocked() map[string]bool {
	locked := make(map[string]bool, len(UserLockableFields))
	for _, f := range UserLockableFields {
		locked[f.Key] = true
	}
	return locked
}

// MetadataFieldStateReader is the store surface the lock guard needs: the
// per-field state rows, plus the user-preference row that held a book's state
// before the rows existed (metastate.Key). The legacy blob is consulted
// read-only when a book has no rows, so a lock written before the migration
// still locks -- the migrating readers only run when a book is opened, and a
// book nobody has opened since is exactly the one a background apply would
// otherwise overwrite.
type MetadataFieldStateReader interface {
	GetMetadataFieldStates(bookID string) ([]MetadataFieldState, error)
	GetUserPreference(key string) (*UserPreference, error)
}

// LegacyMetadataStateDeleter is what DeleteLegacyMetadataState needs.
type LegacyMetadataStateDeleter interface {
	DeleteUserPreference(key string) error
}

// DeleteLegacyMetadataState removes the pre-migration state blob for a book.
// Every writer of the per-field rows MUST call this after a successful save.
//
// Why it is not optional: LockedUserFields (above) and the three migrating
// readers all fall back to the blob when a book has NO rows. Until 2026-09-02
// nothing ever deleted the blob, so for a book that had one, "unlock every
// field" deleted the rows and then the very next read fell through to the
// blob and found the field locked again -- the unlock was inert and the UI
// showed it as done. The blob is superseded the moment rows are written, and
// an empty row set that was written on purpose must stay empty.
func DeleteLegacyMetadataState(store LegacyMetadataStateDeleter, bookID string) error {
	if store == nil || bookID == "" {
		return nil
	}
	return store.DeleteUserPreference(metastate.Key(bookID))
}

// ErrFieldLocksUnavailable wraps every failure to read a book's locks. Callers
// MUST treat it as "do not write": the two error directions are not symmetric.
// Guessing "unlocked" overwrites a user edit that cannot be recovered; guessing
// "locked" leaves a fetched value unapplied until the next successful attempt.
var ErrFieldLocksUnavailable = errors.New("metadata field locks unavailable")

// LockedUserFields returns the set of field keys the user has spoken for on a
// book -- locked, or given an explicit override value -- keyed by the writer's
// vocabulary above. Only an explicit human act counts: a provider-fetched value
// (HasProviderValue) is NOT a lock, or any book that ever had metadata fetched
// would become permanently immune to correction.
//
// FAIL CLOSED: a nil reader, an empty id, or a store error returns a non-nil
// error wrapping ErrFieldLocksUnavailable and a nil map. Never proceed as if
// nothing were locked on an error.
func LockedUserFields(reader MetadataFieldStateReader, bookID string) (map[string]bool, error) {
	if reader == nil {
		return nil, fmt.Errorf("%w: no store", ErrFieldLocksUnavailable)
	}
	if bookID == "" {
		return nil, fmt.Errorf("%w: empty book id", ErrFieldLocksUnavailable)
	}

	states, err := reader.GetMetadataFieldStates(bookID)
	if err != nil {
		return nil, fmt.Errorf("%w for book %s: %w", ErrFieldLocksUnavailable, bookID, err)
	}

	locked := map[string]bool{}
	for _, st := range states {
		if st.HasUserOverride() {
			locked[st.Field] = true
		}
	}
	if len(states) > 0 {
		return locked, nil
	}

	// No rows: the book's state, if any, is still in the pre-migration blob.
	legacy, err := legacyLockedUserFields(reader, bookID)
	if err != nil {
		return nil, fmt.Errorf("%w for book %s: %w", ErrFieldLocksUnavailable, bookID, err)
	}
	for k := range legacy {
		locked[k] = true
	}
	return locked, nil
}

// legacyFieldState is the subset of the pre-migration per-field JSON this guard
// needs. override_value is kept raw: any non-null JSON value is a user value.
type legacyFieldState struct {
	OverrideValue  json.RawMessage `json:"override_value"`
	OverrideLocked bool            `json:"override_locked"`
}

func legacyLockedUserFields(reader MetadataFieldStateReader, bookID string) (map[string]bool, error) {
	pref, err := reader.GetUserPreference(metastate.Key(bookID))
	if err != nil {
		return nil, err
	}
	if pref == nil || pref.Value == nil || *pref.Value == "" {
		return nil, nil
	}
	var state map[string]legacyFieldState
	if err := json.Unmarshal([]byte(*pref.Value), &state); err != nil {
		return nil, fmt.Errorf("parse legacy metadata state: %w", err)
	}
	locked := map[string]bool{}
	for field, st := range state {
		hasValue := len(st.OverrideValue) > 0 && string(st.OverrideValue) != "null"
		if st.OverrideLocked || hasValue {
			locked[field] = true
		}
	}
	return locked, nil
}

// FieldLocks is one book's loaded lock set, and THE chokepoint every writer of
// a lockable Book column goes through. There is one implementation of "which
// columns may this write not touch" so that a writer added later cannot get it
// subtly wrong -- the 2026-09-02 audit found nine independent writers (metafetch
// apply, ISBN enrichment, scanner rescan, scanner AI nomination, diagnostics
// AI-suggestion apply, iTunes reconcile, three dedup merges) each with its own
// answer, most of them "never checked".
//
// Obtain one with LoadFieldLocks (fails closed) and wrap the mutation in Apply.
// The zero value locks nothing; it exists so tests can build one, not so callers
// can skip the load.
type FieldLocks struct {
	BookID string
	locked map[string]bool
}

// LoadFieldLocks reads the book's lock set. It fails closed exactly like
// LockedUserFields: on any error the caller MUST NOT write the book.
func LoadFieldLocks(reader MetadataFieldStateReader, bookID string) (FieldLocks, error) {
	locked, err := LockedUserFields(reader, bookID)
	if err != nil {
		return FieldLocks{BookID: bookID}, err
	}
	return FieldLocks{BookID: bookID, locked: locked}, nil
}

// NewFieldLocks builds a lock set from an already-loaded map (LockedUserFields
// or AllUserLockableFieldsLocked). For callers that loaded the map themselves.
func NewFieldLocks(bookID string, locked map[string]bool) FieldLocks {
	cp := make(map[string]bool, len(locked))
	for k, v := range locked {
		if v {
			cp[k] = true
		}
	}
	return FieldLocks{BookID: bookID, locked: cp}
}

// Locked reports whether the user has spoken for key (a database.FieldKey*).
func (l FieldLocks) Locked(key string) bool { return l.locked[key] }

// Any reports whether anything at all is locked, so a writer can skip work
// (an author lookup, a provider call) it would only have to throw away.
func (l FieldLocks) Any() bool { return len(l.locked) > 0 }

// Set returns a copy of the locked keys as a map, for the strip-style guards
// that pre-filter an incoming value set rather than post-restore a Book.
func (l FieldLocks) Set() map[string]bool {
	cp := make(map[string]bool, len(l.locked))
	for k := range l.locked {
		cp[k] = true
	}
	return cp
}

// lockedColumns is the set of Book columns this lock set protects.
// series_name also protects SeriesSequence: a position belongs to its series,
// and a writer that repoints the series would otherwise leave the user's
// number attached to a series it was never about.
func (l FieldLocks) lockedColumns() map[string]string {
	cols := map[string]string{}
	for _, f := range UserLockableFields {
		if l.locked[f.Key] {
			cols[f.Column] = f.Key
		}
	}
	if l.locked[FieldKeySeriesName] {
		if _, own := cols["SeriesSequence"]; !own {
			cols["SeriesSequence"] = FieldKeySeriesName
		}
	}
	return cols
}

// Apply runs mutate on book, then puts back every locked column mutate changed,
// and returns the lock keys it had to restore (vocabulary order, deduplicated).
// A returned key means the writer TRIED to change a locked field -- callers
// report it (op summaries, responses) rather than pretend the apply was total.
//
// Restoring after the fact, rather than asking each writer to check first, is
// what makes this a chokepoint: the writer's own logic runs unchanged and the
// user's columns come out the other side intact whatever that logic did.
// Writers whose mutation has SIDE EFFECTS on a locked field (creating an author
// row, rewriting a join table) should also consult Locked before doing them;
// metafetch does (StripLockedFields), and Apply is the belt to that brace.
//
// A nil book is a no-op that returns nil.
func (l FieldLocks) Apply(book *Book, mutate func(*Book)) []string {
	if book == nil {
		return nil
	}
	cols := l.lockedColumns()
	if len(cols) == 0 {
		mutate(book)
		return nil
	}
	v := reflect.ValueOf(book).Elem()
	before := make(map[string]reflect.Value, len(cols))
	for col := range cols {
		before[col] = snapshotColumn(v.FieldByName(col))
	}

	mutate(book)

	restoredKeys := map[string]bool{}
	for col, key := range cols {
		f := v.FieldByName(col)
		if !columnEqual(before[col], f) {
			f.Set(before[col])
			restoredKeys[key] = true
		}
	}
	if len(restoredKeys) == 0 {
		return nil
	}
	var restored []string
	for _, f := range UserLockableFields {
		if restoredKeys[f.Key] {
			restored = append(restored, f.Key)
		}
	}
	return restored
}

// ApplyRespectingLocks is the one-call form: load the book's locks (fail
// closed), run mutate, restore anything locked. It returns the restored keys,
// or ErrFieldLocksUnavailable -- in which case mutate was NOT run and the book
// is untouched.
func ApplyRespectingLocks(reader MetadataFieldStateReader, book *Book, mutate func(*Book)) ([]string, error) {
	if book == nil {
		return nil, fmt.Errorf("apply respecting locks: nil book")
	}
	locks, err := LoadFieldLocks(reader, book.ID)
	if err != nil {
		return nil, err
	}
	return locks.Apply(book, mutate), nil
}

// snapshotColumn copies a column value so a mutation that writes THROUGH an
// existing pointer (*book.Narrator = x) is caught as well as one that swaps
// the pointer.
func snapshotColumn(f reflect.Value) reflect.Value {
	if f.Kind() == reflect.Pointer {
		if f.IsNil() {
			return reflect.Zero(f.Type())
		}
		p := reflect.New(f.Type().Elem())
		p.Elem().Set(f.Elem())
		return p
	}
	cp := reflect.New(f.Type()).Elem()
	cp.Set(f)
	return cp
}

func columnEqual(a, b reflect.Value) bool {
	if a.Kind() == reflect.Pointer {
		if a.IsNil() || b.IsNil() {
			return a.IsNil() == b.IsNil()
		}
		return reflect.DeepEqual(a.Elem().Interface(), b.Elem().Interface())
	}
	return reflect.DeepEqual(a.Interface(), b.Interface())
}
