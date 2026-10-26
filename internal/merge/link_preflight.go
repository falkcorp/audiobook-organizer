// file: internal/merge/link_preflight.go
// version: 1.0.0
// guid: 5d2e7a14-8c3b-4f90-a6e1-9b0c4d7f2a35
// last-edited: 2026-10-26

package merge

import (
	"fmt"

	"github.com/falkcorp/audiobook-organizer/internal/database"
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
//     skips it (applyBookMergeReroute). It is left out of the rest unless it
//     already belongs to a version group a live participant holds: the
//     service admits such a loser (a completed merge replayed, or a version
//     the user deleted) and then runs its iTunes guard on it like any
//     participant, so the preflight does too. A trashed loser outside every
//     live participant's group is refused by the service as a soft-deleted
//     input, which the op skips before any iTunes check;
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
	liveGroups := map[string]bool{}
	if keep.VersionGroupID != nil && *keep.VersionGroupID != "" {
		liveGroups[*keep.VersionGroupID] = true
	}
	var trashed []*database.Book
	seen := map[string]bool{keepID: true}
	for _, id := range mergeIDs {
		if seen[id] {
			continue
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
			trashed = append(trashed, b) // the op skips it unless its group is a live one
			continue
		}
		participants = append(participants, id)
		if b.VersionGroupID != nil && *b.VersionGroupID != "" {
			liveGroups[*b.VersionGroupID] = true
		}
	}
	// The service's own admission rule for a trashed loser (MergeBooksWithOptions,
	// "necessary condition"): its group is one a live participant holds.
	for _, b := range trashed {
		if b.VersionGroupID != nil && liveGroups[*b.VersionGroupID] {
			participants = append(participants, b.ID)
		}
	}
	return GuardITunesProtected(store, participants)
}

// PreflightLink is the Service's PreflightLink, over its own store.
func (ms *Service) PreflightLink(keepID string, mergeIDs []string) error {
	return PreflightLink(ms.db, keepID, mergeIDs)
}
