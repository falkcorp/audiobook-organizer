// file: internal/undo/revert_plan.go
// version: 1.0.0
// guid: 7c3e9a51-2f84-4b6d-a0e7-5d1c8b4f2e96
// last-edited: 2026-09-29

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
// too.
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
		restored: map[string]bool{}, refusedBook: map[string]bool{}}
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
	p.Order = append(p.Order, deferred...)
	return p, nil
}

func (p *RevertPlan) retiredRow(c *database.OperationChange) bool {
	_, ok := p.deps[c.BookID]
	return ok && !p.depIDs[c.ID]
}

// Gate returns the ReasonDependentNotReverted refusal of a retired book's row
// whose dependencies were not all reverted (or whose book was already
// refused), or nil. Call it for each row of Order in turn, before reverting.
func (p *RevertPlan) Gate(c *database.OperationChange) error {
	if !p.retiredRow(c) {
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

// Record notes a row's outcome (nil: restored, including already restored).
func (p *RevertPlan) Record(c *database.OperationChange, err error) {
	if err == nil {
		p.restored[c.ID] = true
		return
	}
	if c.ChangeType == ChangeTypeBookSoftDelete {
		p.refusedBook[c.BookID] = true
	}
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
