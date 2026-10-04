// file: internal/database/book_edit_history.go
// version: 1.2.0
// guid: 5b8e2f71-0c4d-4a96-b3e7-9d1a6c2f8e40
// last-edited: 2026-10-04

package database

import (
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

// History field names that differ from the Book JSON name. author_id and
// series_id are foreign keys: their history rows carry the display names in
// PreviousValue/NewValue (what the UI and the revert-metadata-fetch job read)
// and the ids in PreviousRef/NewRef (what undo restores). series_sequence has
// always been recorded as series_position.
const (
	HistoryFieldAuthor   = "author_name"
	HistoryFieldSeries   = "series"
	HistoryFieldSeriesNo = "series_position"
)

// A dropped embedded series object. The store's series invariant
// (enforceSeriesInvariant) drops or replaces a Book.Series object that does
// not match Book.SeriesID; when the STORED row's object was the only record
// of a series (a stale object older builds left), the store writes one
// history row in the same batch as the book row: field HistoryFieldSeriesObject, change type
// ChangeTypeSeriesObjectDrop, source SeriesObjectDropSource. PreviousValue is
// the dropped object's name, PreviousRef.SeriesID its id; NewValue / NewRef
// are the object and SeriesID the row was written with. An object only the
// writer held (never stored) is refused without a row: it was not lost.
//
// The change type is not a field edit: the queued apply ignores it
// (metafetch isFieldEdit), because the scanner's own writes drop these
// objects and the scanner is not a later edit. An undo of the row is refused
// (the field is not a Book column); the object is restored by relinking the
// book to that series.
const (
	HistoryFieldSeriesObject   = "series_object"
	ChangeTypeSeriesObjectDrop = "series-object-drop"
	SeriesObjectDropSource     = "series_invariant"
)

// HistoryFieldToJSON maps each history field name that differs from its Book
// JSON name onto that JSON name.
var HistoryFieldToJSON = map[string]string{
	HistoryFieldAuthor:   "author_id",
	HistoryFieldSeries:   "series_id",
	HistoryFieldSeriesNo: "series_sequence",
}

// HistoryFieldName is the change-history field name of the Book field whose
// JSON name is jsonName.
func HistoryFieldName(jsonName string) string {
	for h, j := range HistoryFieldToJSON {
		if j == jsonName {
			return h
		}
	}
	return jsonName
}

// BookEditHistoryStore is what RecordBookEditHistory needs: the history write
// and the author/series lookups that render a foreign key as its name.
type BookEditHistoryStore interface {
	RecordMetadataChange(record *MetadataChangeRecord) error
	GetAuthorByID(id int) (*Author, error)
	GetSeriesByID(id int) (*Series, error)
}

// ChangeTypeManual is the change_type of a user's hand edit (single-book PUT
// and the web bulk edit).
const ChangeTypeManual = "manual"

// RecordBookEditHistory records one change-history row for EVERY tracked Book
// field whose stored value differs between before and after -- including a
// clear (old value -> empty) -- and nothing for a field that did not change.
// before and after are the row as the edit read it and as it wrote it (take
// before inside the ModifyBook callback, so a concurrent writer's change is
// not attributed to this edit). skip names history fields the caller already
// recorded another way (the override path records its own rows).
//
// Why every field: the queued apply (metafetch.ApplyEditsSince) refuses when
// the book's history shows an edit after it was queued. An edit that records
// no row is invisible to it, and the queued apply then overwrites the user's
// change silently. It is also the book's history view.
//
// Returns the number of rows recorded; a non-nil error means at least one row
// could not be recorded (every other row is still attempted).
func RecordBookEditHistory(s BookEditHistoryStore, before, after *Book, changeType, source string, at time.Time, skip map[string]bool) (int, error) {
	changed, err := ChangedBookFields(before, after)
	if err != nil {
		return 0, err
	}
	var errs []error
	recorded := 0
	for _, jsonName := range changed {
		field := HistoryFieldName(jsonName)
		if skip[field] {
			continue
		}
		rec := &MetadataChangeRecord{
			BookID:     after.ID,
			Field:      field,
			ChangeType: changeType,
			Source:     source,
			ChangedAt:  at,
		}
		var oldVal, newVal string
		switch jsonName {
		case "author_id":
			oldVal, newVal = authorNameOf(s, before.AuthorID), authorNameOf(s, after.AuthorID)
			rec.PreviousRef = &MetadataChangeRef{AuthorID: before.AuthorID}
			rec.NewRef = &MetadataChangeRef{AuthorID: after.AuthorID}
		case "series_id":
			oldVal, newVal = seriesNameOf(s, before.SeriesID), seriesNameOf(s, after.SeriesID)
			rec.PreviousRef = &MetadataChangeRef{SeriesID: before.SeriesID}
			rec.NewRef = &MetadataChangeRef{SeriesID: after.SeriesID}
		default:
			var rerr error
			if oldVal, rerr = RenderBookField(before, jsonName); rerr == nil {
				newVal, rerr = RenderBookField(after, jsonName)
			}
			if rerr != nil {
				// ChangedBookFields named this field, so it renders; a failure
				// is a bug, and the row is still recorded (the edit happened),
				// without values rather than not at all.
				errs = append(errs, fmt.Errorf("%s: render: %w", field, rerr))
			}
		}
		// ChangedBookFields compares stored JSON, so nil and a pointer to ""
		// differ there although nothing a user can see changed. A `"" -> ""`
		// row would refuse a queued apply for an edit that did nothing.
		if oldVal == newVal && rec.PreviousRef == nil {
			continue
		}
		oldJSON, newJSON := jsonString(oldVal), jsonString(newVal)
		rec.PreviousValue, rec.NewValue = &oldJSON, &newJSON
		if rerr := s.RecordMetadataChange(rec); rerr != nil {
			errs = append(errs, fmt.Errorf("%s: %w", field, rerr))
			continue
		}
		recorded++
	}
	if len(errs) > 0 {
		return recorded, fmt.Errorf("record edit history of %s: %w", after.ID, errors.Join(errs...))
	}
	return recorded, nil
}

func jsonString(s string) string {
	b, err := json.Marshal(s)
	if err != nil { // unreachable: a Go string always marshals
		return `""`
	}
	return string(b)
}

// authorNameOf renders an author id as its name; an unreadable or unknown id
// renders as "#<id>" so the row still says which author it was.
func authorNameOf(s BookEditHistoryStore, id *int) string {
	if id == nil {
		return ""
	}
	if a, err := s.GetAuthorByID(*id); err == nil && a != nil {
		return a.Name
	}
	return fmt.Sprintf("#%d", *id)
}

func seriesNameOf(s BookEditHistoryStore, id *int) string {
	if id == nil {
		return ""
	}
	if sr, err := s.GetSeriesByID(*id); err == nil && sr != nil {
		return sr.Name
	}
	return fmt.Sprintf("#%d", *id)
}
