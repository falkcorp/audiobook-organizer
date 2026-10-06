// file: internal/undo/revert_plan.go
// version: 1.7.0
// guid: 7c3e9a51-2f84-4b6d-a0e7-5d1c8b4f2e96
// last-edited: 2026-10-06

package undo

import (
	"fmt"
	"slices"
	"sort"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// RevertPlan is the order an operation revert (audiobooks.RevertService)
// processes its rows in, and the gate that holds back the rows of a retired
// book. PreflightUndoConflicts walks the same plan, so its prediction of which
// rows are refused matches the revert's.
//
// Rows are processed newest first, except that every row of a retired book
// (its soft-delete, primary demote, merged-into, cleared path, moved external
// ids and followed user state) waits until every row of the operation that
// moved or repointed that book's file elsewhere has been reverted, and is
// refused if one of those was not: restoring the book would leave two live
// books owning one file, and restoring only its path, ids or progress onto a
// book that stays retired hands them to the purge (which deletes a purged
// book's file_path and tombstones its ids). Once one row of a retired book is
// refused that way, or its soft-delete fails, its remaining rows are refused
// too. Within each book its soft-delete row goes first (softDeleteFirst), so
// "remaining" is every other row of the book, including rows journaled after
// the soft-delete.
type RevertPlan struct {
	// Order is every row, in the order the revert processes them.
	Order []*database.OperationChange
	// Stamps are the soft-delete stamps the operation journaled, for
	// CheckSoftDeleteCurrent.
	Stamps SoftDeleteStamps

	deps        map[string][]string
	depIDs      map[string]bool
	restored    map[string]bool
	refusedBook map[string]bool
	// handedOff: books whose version group this operation handed to another
	// member after retiring them (a ChangeTypeBookPrimaryHandoff row, see
	// NoteHandOffs).
	handedOff map[string]bool
	// demoted: books this operation journaled a primary demote for
	// (NoteHandOffs), reverted or not.
	demoted map[string]bool
	// fieldLocks: this operation's ChangeTypeFieldLock rows by book and
	// field, every one of them (reverted or not), for CheckPairedFieldLock.
	fieldLocks map[string][]*database.OperationChange
	// seriesLinks: this operation's series_id metadata_update row per book
	// (the newest, voided ones left out), for the series_sequence pairing
	// (SeriesLinkOf).
	seriesLinks map[string]*database.OperationChange
}

// PlanRevert orders rows (an operation's not-yet-reverted restorable rows, in
// journal order). files reads a book's book_file rows; nil skips the repoint
// dependencies (a preflight store that cannot read them), keeping the
// book_file_reassign ones.
func PlanRevert(rows []*database.OperationChange, files func(bookID string) ([]database.BookFile, error)) (*RevertPlan, error) {
	deps, err := RetireDependents(rows, files)
	if err != nil {
		return nil, err
	}
	p := &RevertPlan{Stamps: OpSoftDeleteStamps(rows), deps: deps, depIDs: map[string]bool{},
		restored: map[string]bool{}, refusedBook: map[string]bool{}, handedOff: map[string]bool{}, demoted: map[string]bool{},
		fieldLocks: map[string][]*database.OperationChange{}, seriesLinks: map[string]*database.OperationChange{}}
	for _, ids := range deps {
		for _, id := range ids {
			p.depIDs[id] = true
		}
	}
	var deferred []*database.OperationChange
	for _, c := range slices.Backward(rows) {
		if p.retiredRow(c) {
			deferred = append(deferred, c)
			continue
		}
		p.Order = append(p.Order, c)
	}
	p.Order = append(softDeleteFirst(p.Order), softDeleteFirst(deferred)...)
	return p, nil
}

// softDeleteFirst moves each book's newest soft-delete row ahead of every
// other row of that book in list, keeping everything else in place. A row
// journaled AFTER a book's soft-delete (fs-regroup-xml's external-id moves
// off the shell, a hand-off note) was otherwise reverted before it, so a
// soft-delete revert that then failed could not refuse it (Gate): its
// change landed on a book that stayed retired.
func softDeleteFirst(list []*database.OperationChange) []*database.OperationChange {
	first := map[string]*database.OperationChange{}
	for _, c := range list {
		if c.ChangeType == ChangeTypeBookSoftDelete {
			if _, ok := first[c.BookID]; !ok {
				first[c.BookID] = c
			}
		}
	}
	if len(first) == 0 {
		return list
	}
	out := make([]*database.OperationChange, 0, len(list))
	emitted := map[string]bool{}
	for _, c := range list {
		sd, ok := first[c.BookID]
		if !ok {
			out = append(out, c)
			continue
		}
		if !emitted[c.BookID] {
			emitted[c.BookID] = true
			out = append(out, sd)
		}
		if c != sd {
			out = append(out, c)
		}
	}
	return out
}

func (p *RevertPlan) retiredRow(c *database.OperationChange) bool {
	_, ok := p.deps[c.BookID]
	return ok && !p.depIDs[c.ID]
}

// Gate returns the ReasonDependentNotReverted refusal of a retired book's row
// whose dependencies were not all reverted (or whose book was already
// refused), or nil. Call it for each row of Order in turn, before reverting.
//
// A book whose soft-delete revert failed (Record) refuses EVERY later row of
// it, not only those of a book with file dependencies: a retire that moved
// no book_file row (the duplicate-copies retire journals none) otherwise
// restored its merged-into pointer, path and ids onto a book that stayed
// retired, and the demote revert ran against the wrong group state.
func (p *RevertPlan) Gate(c *database.OperationChange) error {
	if !p.retiredRow(c) {
		if p.refusedBook[c.BookID] {
			return &ReferentError{Reason: ReasonDependentNotReverted,
				Detail: fmt.Sprintf("book %s was not restored, so its %s change is not either", c.BookID, c.ChangeType)}
		}
		return nil
	}
	var err error
	for _, id := range p.deps[c.BookID] {
		if !p.restored[id] {
			err = &ReferentError{Reason: ReasonDependentNotReverted,
				Detail: fmt.Sprintf("change %s (the file of book %s moved or repointed elsewhere) was not reverted; book %s stays retired", id, c.BookID, c.BookID)}
			break
		}
	}
	if err == nil && p.refusedBook[c.BookID] {
		err = &ReferentError{Reason: ReasonDependentNotReverted,
			Detail: fmt.Sprintf("book %s was not restored, so its %s change is not either", c.BookID, c.ChangeType)}
	}
	if err != nil {
		p.refusedBook[c.BookID] = true
	}
	return err
}

// Record notes a row's outcome: err nil is restored (ErrAlreadyRestored
// included).
func (p *RevertPlan) Record(c *database.OperationChange, err error) {
	if err == nil {
		p.restored[c.ID] = true
		return
	}
	if c.ChangeType == ChangeTypeBookSoftDelete {
		p.refusedBook[c.BookID] = true
	}
}

// NoteHandOffs takes every row of the operation, reverted or not, and notes
// the books it has a ChangeTypeBookPrimaryHandoff row for (and the field-lock
// and series_id rows the paired checks read).
func (p *RevertPlan) NoteHandOffs(all []*database.OperationChange) {
	for _, c := range all {
		switch c.ChangeType {
		case ChangeTypeBookPrimaryHandoff:
			p.handedOff[c.BookID] = true
		case ChangeTypeBookPrimaryDemote:
			p.demoted[c.BookID] = true
		case ChangeTypeFieldLock:
			if !c.Voided {
				k := c.BookID + "\x00" + c.FieldName
				p.fieldLocks[k] = append(p.fieldLocks[k], c)
			}
		case "metadata_update":
			if c.FieldName == "series_id" && !c.Voided {
				p.seriesLinks[c.BookID] = c
			}
		}
	}
}

// SeriesLinkOf returns this operation's series_id row for c's book, or nil
// (NoteHandOffs collects them from every row of the operation).
func (p *RevertPlan) SeriesLinkOf(c *database.OperationChange) *database.OperationChange {
	if p == nil {
		return nil
	}
	return p.seriesLinks[c.BookID]
}

// FieldLocksOf returns this operation's field-lock rows for c's book and
// field (NoteHandOffs collects them from every row of the operation).
func (p *RevertPlan) FieldLocksOf(c *database.OperationChange) []*database.OperationChange {
	if p == nil {
		return nil
	}
	return p.fieldLocks[c.BookID+"\x00"+c.FieldName]
}

// HandedOff reports whether this operation recorded handing bookID's version
// group to another member after retiring it (ChangeTypeBookPrimaryHandoff).
// That row is written only after the hand-off, so it is evidence the
// operation changed the group's flags. Only then may an already-restored
// primary demote of bookID re-crown its group (a retry whose earlier Crown
// failed). Without it the group is not the operation's to change: a retire
// cut off before it wrote, whatever an earlier revert pass counted already
// restored, or a flag a user set since.
func (p *RevertPlan) HandedOff(bookID string) bool { return p.handedOff[bookID] }

// ChangedPrimary reports whether this operation journaled a primary demote
// or a hand-off for bookID: evidence it set out to change the primary of the
// book's version group, so its revert may settle that group.
func (p *RevertPlan) ChangedPrimary(bookID string) bool {
	return p.handedOff[bookID] || p.demoted[bookID]
}

// RetireDependents maps each book a soft-delete row of rows restores to the
// ids of the rows (in rows) that moved one of its book_file rows onto another
// book (book_file_reassign with OldValue == the book) or repointed another
// book's row at one of its files (book_file_repoint_location whose new path is
// one of the book's row paths). Only books with at least one such row are
// listed. files nil skips the repoint rows.
func RetireDependents(rows []*database.OperationChange, files func(bookID string) ([]database.BookFile, error)) (map[string][]string, error) {
	retired := map[string]bool{}
	for _, c := range rows {
		if c.ChangeType == ChangeTypeBookSoftDelete {
			retired[c.BookID] = true
		}
	}
	out := map[string][]string{}
	if len(retired) == 0 {
		return out, nil
	}
	repointedTo := map[string][]string{} // new path -> repoint row ids
	for _, c := range rows {
		switch c.ChangeType {
		case ChangeTypeBookFileReassign:
			if retired[c.OldValue] && c.OldValue != c.BookID {
				out[c.OldValue] = append(out[c.OldValue], c.ID)
			}
		case ChangeTypeBookFileRepoint:
			if to, err := DecodeBookFileLocation(c.NewValue); err == nil {
				repointedTo[to.Path] = append(repointedTo[to.Path], c.ID)
			}
		}
	}
	if len(repointedTo) == 0 || files == nil {
		return out, nil
	}
	ids := make([]string, 0, len(retired))
	for id := range retired {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		fs, err := files(id)
		if err != nil {
			// Fail closed: without the book's rows the order cannot be proven.
			return nil, fmt.Errorf("read book_file rows of %s to order the revert: %w", id, err)
		}
		for _, f := range fs {
			if rowIDs, ok := repointedTo[f.FilePath]; ok {
				for _, rid := range rowIDs {
					if !slices.Contains(out[id], rid) {
						out[id] = append(out[id], rid)
					}
				}
			}
		}
	}
	return out, nil
}
