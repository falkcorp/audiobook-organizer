// file: internal/undo/series_row.go
// version: 1.0.1
// guid: 3b8e1f64-a207-4d59-9c3e-52d0b7a1e8f3
// last-edited: 2026-11-16

package undo

import (
	"encoding/json"
	"fmt"
	"strings"

	database "github.com/falkcorp/audiobook-organizer/internal/books/store"
)

// ChangeTypeSeriesCreate: an operation created a series row. SeriesID is the row
// the store returned and NewValue a SeriesSnapshot of it, journaled AFTER the
// create returned the id, so the revert names the row it made by id and never a
// same-named row someone else made. The revert deletes the row only while it is
// still what the row says (same name and author) and no book names it; a
// crash between the create and this row leaves an empty series, which
// dedup.series-prune removes.
const ChangeTypeSeriesCreate = "series_create"

// ChangeTypeSeriesRowDelete: an operation deleted a series row it had emptied.
// SeriesID is the row and OldValue a SeriesSnapshot of it, journaled BEFORE the
// delete. The revert recreates the row under the same id (RestoreSeries), which
// is what lets the books' series_id rows go back: the series_id restore is
// refused into a series that does not exist (CheckRestoreReferent).
//
// It is not "series_delete", the record-only row dedup writes: that one
// carries no snapshot, and nothing recreates a series from it.
const ChangeTypeSeriesRowDelete = "series_row_delete"

// SeriesSnapshot is the body of a series_create (NewValue) and series_row_delete
// (OldValue) row: what RestoreSeries needs to bring the row back as it was.
type SeriesSnapshot struct {
	ID       int    `json:"id"`
	Name     string `json:"name"`
	AuthorID *int   `json:"author_id,omitempty"`
}

// NewSeriesSnapshotJSON renders s as a row body.
func NewSeriesSnapshotJSON(s SeriesSnapshot) (string, error) {
	b, err := json.Marshal(s)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

// RestoredSeriesLink is the extra RestoredTypes key (alongside "metadata_update")
// a revert counts for each series_id row it restored, so a caller can tell a
// revert that moved books between series from one that only touched other
// fields, and drop the series caches (which list books per series) only then.
const RestoredSeriesLink = "metadata_update:series_id"

// DecodeSeriesCreate parses a ChangeTypeSeriesCreate row.
func DecodeSeriesCreate(c *database.OperationChange) (SeriesSnapshot, error) {
	return decodeSeriesSnapshot(c, c.NewValue, "created series")
}

// DecodeSeriesRowDelete parses a ChangeTypeSeriesRowDelete row.
func DecodeSeriesRowDelete(c *database.OperationChange) (SeriesSnapshot, error) {
	return decodeSeriesSnapshot(c, c.OldValue, "deleted series")
}

func decodeSeriesSnapshot(c *database.OperationChange, body, what string) (SeriesSnapshot, error) {
	var v SeriesSnapshot
	if c.SeriesID == nil {
		return v, &ReferentError{Reason: ReasonSeriesIDMissing, Detail: fmt.Sprintf("%s row %s carries no series id", c.ChangeType, c.ID)}
	}
	if err := json.Unmarshal([]byte(body), &v); err != nil || v.ID <= 0 || v.ID != *c.SeriesID || strings.TrimSpace(v.Name) == "" {
		return v, &ReferentError{Reason: ReasonOldValueUnparsable, Detail: fmt.Sprintf("%s of change %s", what, c.ID)}
	}
	return v, nil
}

// SeriesRowDeleteVerdict predicts the revert of a series_row_delete row: it is
// refused when the id is live as something else or another live series holds
// the name, and already restored when the row is live exactly as snapshotted.
// The restore itself needs the SeriesRestorer capability, which a store without
// it fails closed on, as the revert does.
func SeriesRowDeleteVerdict(store ConflictChecker, c *database.OperationChange) error {
	snap, err := DecodeSeriesRowDelete(c)
	if err != nil {
		return err
	}
	if _, ok := database.AsCapability[database.SeriesRestorer](store); !ok {
		return refuse(ReasonFieldUnreadable, "this store cannot restore series %d", snap.ID)
	}
	cur, err := store.GetSeriesByID(snap.ID)
	if err != nil {
		return refuse(ReasonSeriesLookupFailed, "look up series %d: %v", snap.ID, err)
	}
	if cur != nil {
		if cur.Name == snap.Name && sameIntPointer(cur.AuthorID, snap.AuthorID) {
			return ErrAlreadyRestored
		}
		return refuse(ReasonSeriesRenamedSince, "series %d is live as %q, not the series this operation deleted (%q)", cur.ID, cur.Name, snap.Name)
	}
	holder, err := store.GetSeriesByName(snap.Name, snap.AuthorID)
	if err != nil {
		return refuse(ReasonSeriesLookupFailed, "look up series named %q: %v", snap.Name, err)
	}
	if holder != nil {
		return refuse(ReasonSeriesNameTaken, "series %d already holds the name %q; restoring series %d would duplicate it", holder.ID, holder.Name, snap.ID)
	}
	return nil
}

// restoredSeriesView answers GetSeriesByID with a stand-in for a series a
// series_row_delete row earlier in the revert order is predicted to bring back,
// so a book's series_id row (older, reverted after it) is not refused for a
// series the revert will have restored by then.
type restoredSeriesView struct {
	SeriesLookup
	restored map[int]bool
}

func (v restoredSeriesView) GetSeriesByID(id int) (*database.Series, error) {
	s, err := v.SeriesLookup.GetSeriesByID(id)
	if err != nil || s != nil {
		return s, err
	}
	if v.restored[id] {
		return &database.Series{ID: id}, nil
	}
	return nil, nil
}
