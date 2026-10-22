// file: internal/merge/link_preflight.go
// version: 1.1.0
// guid: 5d2e7a14-8c3b-4f90-a6e1-9b0c4d7f2a35
// last-edited: 2026-10-22

package merge

import (
	"fmt"
	"slices"
)

// linkPreflightStore is the read-only slice of the store PreflightLink uses.
type linkPreflightStore interface {
	ITunesGuardStore
}

// PreflightLink answers, before a book-link operation is queued, the refusals
// MergeBooksWithOptions would answer after it ran, so a request that cannot
// succeed fails the HTTP call (409) instead of queueing an operation that
// fails minutes later and shows as an "accepted" merge.
//
// It is read-only and takes no lock: it is advice to the caller, never a
// substitute for the Service's own guards, which stay authoritative (a book
// can change between this check and the operation).
//
// What it checks:
//   - the keep book exists (BookNotFoundError) and is not soft-deleted
//     (SoftDeletedInputError, AsPrimary);
//   - a LOSER that is soft-deleted is not an error: the dedup.book-merge op
//     skips it (applyBookMergeReroute), so it is left out of the rest;
//   - a loser that does not exist is BookNotFoundError;
//   - the iTunes-library refusal (ITunesProtectedError) for the keep book and
//     every live loser: the books the user selected.
//
// Version-group siblings and reused-group members that the merge re-homes or
// demotes are NOT checked for iTunes files. The merge writes only their
// version_group_id and primary flag, never a file, a tag or a path, and the
// owner approved such database-only changes to iTunes books on 2026-10-22, so
// an iTunes copy that was not selected no longer refuses the merge (see
// Service.MergeBooks).
//
// What it does not check, so a refusal the service still gives after queueing
// remains possible: the deep-scan refusal (ProvisionalScanError); the
// shared-audio-path option; and a FilelessPrimaryError.
func PreflightLink(store linkPreflightStore, keepID string, mergeIDs []string) error {
	keep, err := store.GetBookByID(keepID)
	if err != nil {
		return fmt.Errorf("load book %s: %w", keepID, err)
	}
	if keep == nil {
		return &BookNotFoundError{BookID: keepID}
	}
	if keep.IsSoftDeleted() {
		return &SoftDeletedInputError{BookID: keepID, AsPrimary: true}
	}

	participants := []string{keepID}
	for _, id := range mergeIDs {
		if id == keepID || slices.Contains(participants, id) {
			continue
		}
		b, err := store.GetBookByID(id)
		if err != nil {
			return fmt.Errorf("load book %s: %w", id, err)
		}
		if b == nil {
			return &BookNotFoundError{BookID: id}
		}
		if b.IsSoftDeleted() {
			continue // skipped by the op, not an error
		}
		participants = append(participants, id)
	}
	return GuardITunesProtected(store, participants)
}

// PreflightLink is the Service's PreflightLink, over its own store.
func (ms *Service) PreflightLink(keepID string, mergeIDs []string) error {
	return PreflightLink(ms.db, keepID, mergeIDs)
}
