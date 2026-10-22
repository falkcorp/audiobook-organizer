// file: internal/merge/link_preflight.go
// version: 1.0.0
// guid: 5d2e7a14-8c3b-4f90-a6e1-9b0c4d7f2a35
// last-edited: 2026-10-22

package merge

import (
	"fmt"
	"slices"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// linkPreflightStore is the read-only slice of the store PreflightLink uses.
type linkPreflightStore interface {
	ITunesGuardStore
	GetBooksByVersionGroup(groupID string) ([]database.Book, error)
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
//     every live loser, AND for the live version-group siblings of each loser
//     whose group differs from the keep book's. The service re-homes those
//     siblings into the survivor's group and refuses the whole merge when one
//     of them sits in the iTunes library, so a refusal can name a book the
//     user never selected.
//
// What it does not check, so a refusal the service still gives after queueing
// remains possible: the sibling check when the keep book has no version group
// (the service then picks one of the losers' groups, and which one depends on
// the file-aware survivor election); the reused group's non-participant
// members (guarded only when the merge would write them); the deep-scan
// refusal (ProvisionalScanError); the shared-audio-path option; and a
// FilelessPrimaryError.
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
	var loserGroups []string
	keepGroup := ""
	if keep.VersionGroupID != nil {
		keepGroup = *keep.VersionGroupID
	}
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
		if b.VersionGroupID != nil && *b.VersionGroupID != "" && keepGroup != "" &&
			*b.VersionGroupID != keepGroup && !slices.Contains(loserGroups, *b.VersionGroupID) {
			loserGroups = append(loserGroups, *b.VersionGroupID)
		}
	}

	toGuard := slices.Clone(participants)
	for _, gid := range loserGroups {
		members, err := store.GetBooksByVersionGroup(gid)
		if err != nil {
			return fmt.Errorf("load version group %s membership: %w", gid, err)
		}
		for i := range members {
			if members[i].IsSoftDeleted() || slices.Contains(toGuard, members[i].ID) {
				continue
			}
			toGuard = append(toGuard, members[i].ID)
		}
	}
	return GuardITunesProtected(store, toGuard)
}

// PreflightLink is the Service's PreflightLink, over its own store.
func (ms *Service) PreflightLink(keepID string, mergeIDs []string) error {
	return PreflightLink(ms.db, keepID, mergeIDs)
}
