// file: internal/database/metadata_field_locks.go
// version: 1.0.0
// guid: 0d1a2cd0-a75c-4990-bb1f-bac01864e50c
// last-edited: 2026-09-02

package database

import (
	"encoding/json"
	"errors"
	"fmt"

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
