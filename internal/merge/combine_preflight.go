// file: internal/merge/combine_preflight.go
// version: 1.0.0
// guid: 3c9a1e57-8d24-4b60-a7f3-5e1d0c8b2a94
// last-edited: 2026-10-22

package merge

import (
	"fmt"
)

// PreflightCombine answers, before a combine is queued as the dedup.book-combine
// operation, the refusals CombineBooksWithOptions gives before it writes its
// first journal row, so a request that cannot succeed fails the HTTP call
// (400/404/409) instead of queueing an operation that fails later.
//
// It is read-only and takes no lock: it is advice to the caller, never a
// substitute for the Service's own checks, which stay authoritative (a book can
// change between this check and the operation). Checked, in the service's
// order: at least two distinct books, the keep book among them, every book
// exists (BookNotFoundError), none is soft-deleted (SoftDeletedInputError), and
// none is in the iTunes library (ITunesProtectedError). A failure of the combine
// after its undo pointer is journaled (CombineIncompleteError) cannot be known
// here and is reported in the operation's result.
//
// A duplicate id (including keepID repeated in mergeIDs) is an error the
// service gives as a plain error; here it is a plain error too, for the caller
// to answer as a bad request.
func PreflightCombine(store linkPreflightStore, keepID string, mergeIDs []string) error {
	if keepID == "" {
		return fmt.Errorf("primary_id is required for combine")
	}
	ids := append(append([]string{}, mergeIDs...), keepID)
	if len(ids) < 2 {
		return fmt.Errorf("need at least 2 books to combine")
	}
	seen := make(map[string]bool, len(ids))
	for _, id := range ids {
		if seen[id] {
			return fmt.Errorf("duplicate book id %s", id)
		}
		seen[id] = true
		b, err := store.GetBookByID(id)
		if err != nil {
			return fmt.Errorf("load book %s: %w", id, err)
		}
		if b == nil {
			return &BookNotFoundError{BookID: id}
		}
		if b.IsSoftDeleted() {
			return &SoftDeletedInputError{BookID: id, AsPrimary: id == keepID}
		}
	}
	return GuardITunesProtected(store, ids)
}

// PreflightCombine is the Service's PreflightCombine, over its own store.
func (ms *Service) PreflightCombine(keepID string, mergeIDs []string) error {
	return PreflightCombine(ms.db, keepID, mergeIDs)
}
