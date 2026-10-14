// file: internal/undo/author_merge.go
// version: 1.1.0
// guid: b7d21f94-3a68-4e05-9c1b-5f8e0a3d7c26
// last-edited: 2026-10-14

package undo

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// Journal rows an author merge (entities.author-merge and ai.author-scan-apply,
// which share one loop) and an author-to-narrator reclassify write. Each is
// journaled BEFORE the write it describes, with the write's before-image, and
// voided (database.OperationChange.Voided) when the write is refused, so a row
// that stands always describes a write that happened.
//
// They replace the unstructured "author_reassign" / "author_delete" text rows the
// merge used to write (record only; those rows, and the ones other jobs still
// write under those names, stay not restorable: the unparsable label).
const (
	// ChangeTypeAuthorMergeCredits: one book's credit moved from the merged-away
	// author to the keep author. OldValue is a TitleRelinkCreditsSnapshot (the
	// junction in order and the primary AuthorID before); NewValue an
	// AuthorCreditsMove carrying the junction as written.
	ChangeTypeAuthorMergeCredits = "author_merge_credits"
	// ChangeTypeAuthorMergeDelete: the merged-away author row was deleted and a
	// tombstone left. OldValue is an AuthorSnapshot; NewValue an AuthorMergeDelete.
	// It is journaled after every credit row of its author, so a revert (newest
	// first) brings the author back before it puts a credit onto it.
	ChangeTypeAuthorMergeDelete = "author_merge_delete"
	// ChangeTypeAuthorReclassifyCredits: one book's credit moved from an author to
	// a narrator link. Same shapes as ChangeTypeAuthorMergeCredits; the move also
	// names the narrator and whether the link was added.
	ChangeTypeAuthorReclassifyCredits = "author_reclassify_credits"
	// ChangeTypeAuthorReclassifyDelete: the reclassified author row was deleted.
	// OldValue is an AuthorSnapshot; NewValue an AuthorReclassifyDelete.
	ChangeTypeAuthorReclassifyDelete = "author_reclassify_delete"
	// ChangeTypeAIScanResultApplied: ai.author-scan-apply marked a scan result
	// applied. NewValue is an AIScanResultApplied. A ledger note (IsLedgerOnly): it
	// restores nothing itself, and is marked reverted only when the whole operation
	// is, which is the signal the server's revert wrapper acts on to mark the result
	// pending again (Server.revertOperation). Without it, undoing an AI apply brought
	// the authors back while the review list still said "applied".
	ChangeTypeAIScanResultApplied = "ai_scan_result_applied"
)

// AIScanResultApplied is NewValue of a ChangeTypeAIScanResultApplied row.
type AIScanResultApplied struct {
	ScanID   int `json:"scan_id"`
	ResultID int `json:"result_id"`
}

// DecodeAIScanResultApplied reads the scan result a ChangeTypeAIScanResultApplied
// row names.
func DecodeAIScanResultApplied(c *database.OperationChange) (AIScanResultApplied, error) {
	var v AIScanResultApplied
	if err := json.Unmarshal([]byte(c.NewValue), &v); err != nil || v.ScanID <= 0 || v.ResultID <= 0 {
		return v, &ReferentError{Reason: ReasonOldValueUnparsable, Detail: fmt.Sprintf("applied scan result of change %s", c.ID)}
	}
	return v, nil
}

// NewAIScanResultAppliedRow builds the ledger note for one applied scan result.
// The caller sets ID and OperationID.
func NewAIScanResultAppliedRow(scanID, resultID int) (*database.OperationChange, error) {
	nv, err := json.Marshal(AIScanResultApplied{ScanID: scanID, ResultID: resultID})
	if err != nil {
		return nil, err
	}
	return &database.OperationChange{
		ChangeType: ChangeTypeAIScanResultApplied, FieldName: fmt.Sprintf("ai_scan:%d:result:%d", scanID, resultID),
		NewValue: string(nv),
	}, nil
}

// AuthorAliasSnapshot is one alias of a deleted author.
type AuthorAliasSnapshot struct {
	// ID is the alias row's id. A merge moves the alias onto the keep author
	// under this id (database.MergeDeleteAuthor), and its undo moves exactly this
	// row back; a reclassify deletes it, and its undo recreates it by name.
	ID   int    `json:"id,omitempty"`
	Name string `json:"name"`
	Type string `json:"type"`
}

// AuthorSnapshot is a deleted author as its delete rows store it: enough to
// recreate the row under the same id and name, its aliases, and what else the
// delete changed that the undo puts back.
type AuthorSnapshot struct {
	ID      int                   `json:"id"`
	Name    string                `json:"name"`
	Aliases []AuthorAliasSnapshot `json:"aliases,omitempty"`
	// SharedNameIndex: the author did NOT hold its normalized author:name: entry
	// when it was deleted; another author whose name normalizes the same (a case
	// or whitespace variant) did. The restore then writes no entry and is not
	// refused by that holder (database.AuthorNameIndexOnRestore). The zero value
	// is the common case, an author that held its own entry.
	SharedNameIndex bool `json:"shared_name_index,omitempty"`
	// InboundTombstones are authors merged INTO this one earlier, whose redirects
	// pointed at it when it was merged away. One flattened onto the keep author
	// while the merge stood (ResolveTombstoneChains) is pointed back by the undo.
	InboundTombstones []int `json:"inbound_tombstones,omitempty"`
}

// AliasIDs are the snapshot's alias row ids (aliases journaled without one left out).
func (s AuthorSnapshot) AliasIDs() []int {
	var ids []int
	for _, al := range s.Aliases {
		if al.ID > 0 {
			ids = append(ids, al.ID)
		}
	}
	return ids
}

// MergeSnapshotFrom builds the snapshot of an author MergeDeleteAuthor removed,
// from what the delete reported under its locks.
func MergeSnapshotFrom(author database.Author, res *database.AuthorMergeDeleteResult) AuthorSnapshot {
	snap := AuthorSnapshot{ID: author.ID, Name: author.Name}
	if res == nil {
		return snap
	}
	snap.SharedNameIndex = !res.OwnedNameIndex
	snap.InboundTombstones = append([]int(nil), res.InboundTombstones...)
	for _, al := range res.MovedAliases {
		snap.Aliases = append(snap.Aliases, AuthorAliasSnapshot{ID: al.ID, Name: al.AliasName, Type: al.AliasType})
	}
	return snap
}

// AuthorPartlyRestoredError: the revert of an author delete row brought the
// author back (it is live), but not every alias it had. The row fails, so it
// stays unreverted and a retry finishes it, but the author's credits still go
// back (RevertPlan.Record does not count the author refused).
type AuthorPartlyRestoredError struct {
	AuthorID int
	Problems []string
}

func (e *AuthorPartlyRestoredError) Error() string {
	return fmt.Sprintf("author %d is restored, but %d of its aliases are not: %s",
		e.AuthorID, len(e.Problems), strings.Join(e.Problems, "; "))
}

// AuthorDeleteRestoreRefusal predicts the revert of an author delete row against
// the author as it is now: nil when it would restore (or already did), else the
// refusal. live is whether a row of snap.ID is live; nameHolder is the live
// author the name resolves to (0: none). mergedInto is the merge's keep author
// (0 for a reclassify). It is the preflight's half of
// database.AuthorNameIndexOnRestore, which the restore itself runs.
func AuthorDeleteRestoreRefusal(snap AuthorSnapshot, live bool, nameHolder, mergedInto int) (already bool, err error) {
	if live {
		return true, nil
	}
	if _, nerr := database.AuthorNameIndexOnRestore(!snap.SharedNameIndex, nameHolder, snap.ID, mergedInto); nerr != nil {
		return false, refuse(ReasonChangedSince, "author %d %q cannot be restored: %v", snap.ID, snap.Name, nerr)
	}
	return false, nil
}

// AuthorMergeDelete is NewValue of a ChangeTypeAuthorMergeDelete row.
type AuthorMergeDelete struct {
	IntoAuthorID int    `json:"into_author_id"`
	IntoName     string `json:"into_name"`
}

// AuthorReclassifyDelete is NewValue of a ChangeTypeAuthorReclassifyDelete row.
type AuthorReclassifyDelete struct {
	NarratorID   int    `json:"narrator_id"`
	NarratorName string `json:"narrator_name"`
}

// AuthorCreditsMove is NewValue of a ChangeTypeAuthorMergeCredits or
// ChangeTypeAuthorReclassifyCredits row.
type AuthorCreditsMove struct {
	FromAuthorID int `json:"from_author_id"`
	// IntoAuthorID is the keep author of a merge; 0 for a reclassify.
	IntoAuthorID int `json:"into_author_id,omitempty"`
	// CreditsAfter is the junction exactly as the write left it. The revert
	// restores the snapshot only while the book still holds exactly this: a
	// credit anyone added, removed or re-ordered since is a later change. A
	// pointer so an empty junction is told apart from a missing field.
	CreditsAfter *[]database.BookAuthor `json:"credits_after"`
	// PrimaryAfter is the primary AuthorID the write set when the book's primary
	// was the from-author; PrimaryChanged says it was repointed at all.
	PrimaryAfter   *int `json:"primary_after,omitempty"`
	PrimaryChanged bool `json:"primary_changed,omitempty"`
	// NarratorID and NarratorAdded: a reclassify's narrator link, and whether
	// THIS write added it (an existing link is left alone by the revert).
	NarratorID    int  `json:"narrator_id,omitempty"`
	NarratorAdded bool `json:"narrator_added,omitempty"`
}

func decodeCreditsMove(c *database.OperationChange) (TitleRelinkCreditsSnapshot, AuthorCreditsMove, error) {
	var snap TitleRelinkCreditsSnapshot
	var move AuthorCreditsMove
	if err := json.Unmarshal([]byte(c.OldValue), &snap); err != nil {
		return snap, move, &ReferentError{Reason: ReasonOldValueUnparsable, Detail: fmt.Sprintf("credits snapshot of change %s: %v", c.ID, err)}
	}
	if err := json.Unmarshal([]byte(c.NewValue), &move); err != nil || move.FromAuthorID <= 0 || move.CreditsAfter == nil {
		return snap, move, &ReferentError{Reason: ReasonOldValueUnparsable, Detail: fmt.Sprintf("credit move of change %s", c.ID)}
	}
	return snap, move, nil
}

// DecodeAuthorMergeCredits parses a ChangeTypeAuthorMergeCredits row.
func DecodeAuthorMergeCredits(c *database.OperationChange) (TitleRelinkCreditsSnapshot, AuthorCreditsMove, error) {
	snap, move, err := decodeCreditsMove(c)
	if err == nil && move.IntoAuthorID <= 0 {
		err = &ReferentError{Reason: ReasonOldValueUnparsable, Detail: fmt.Sprintf("credit move of change %s names no keep author", c.ID)}
	}
	return snap, move, err
}

// DecodeAuthorReclassifyCredits parses a ChangeTypeAuthorReclassifyCredits row.
func DecodeAuthorReclassifyCredits(c *database.OperationChange) (TitleRelinkCreditsSnapshot, AuthorCreditsMove, error) {
	snap, move, err := decodeCreditsMove(c)
	if err == nil && move.NarratorID <= 0 {
		err = &ReferentError{Reason: ReasonOldValueUnparsable, Detail: fmt.Sprintf("credit move of change %s names no narrator", c.ID)}
	}
	return snap, move, err
}

func decodeSnapshot(c *database.OperationChange) (AuthorSnapshot, error) {
	var snap AuthorSnapshot
	if err := json.Unmarshal([]byte(c.OldValue), &snap); err != nil || snap.ID <= 0 || snap.Name == "" {
		return snap, &ReferentError{Reason: ReasonOldValueUnparsable, Detail: fmt.Sprintf("deleted author of change %s", c.ID)}
	}
	return snap, nil
}

// DecodeAuthorMergeDelete parses a ChangeTypeAuthorMergeDelete row.
func DecodeAuthorMergeDelete(c *database.OperationChange) (AuthorSnapshot, AuthorMergeDelete, error) {
	snap, err := decodeSnapshot(c)
	if err != nil {
		return snap, AuthorMergeDelete{}, err
	}
	var v AuthorMergeDelete
	if err := json.Unmarshal([]byte(c.NewValue), &v); err != nil || v.IntoAuthorID <= 0 {
		return snap, v, &ReferentError{Reason: ReasonOldValueUnparsable, Detail: fmt.Sprintf("merge target of change %s", c.ID)}
	}
	return snap, v, nil
}

// DecodeAuthorReclassifyDelete parses a ChangeTypeAuthorReclassifyDelete row.
func DecodeAuthorReclassifyDelete(c *database.OperationChange) (AuthorSnapshot, AuthorReclassifyDelete, error) {
	snap, err := decodeSnapshot(c)
	if err != nil {
		return snap, AuthorReclassifyDelete{}, err
	}
	var v AuthorReclassifyDelete
	if err := json.Unmarshal([]byte(c.NewValue), &v); err != nil || v.NarratorID <= 0 {
		return snap, v, &ReferentError{Reason: ReasonOldValueUnparsable, Detail: fmt.Sprintf("narrator of change %s", c.ID)}
	}
	return snap, v, nil
}

// NewAuthorCreditsRow builds the journal row for one book's credit move. changeType
// is ChangeTypeAuthorMergeCredits or ChangeTypeAuthorReclassifyCredits. The caller
// sets ID and OperationID.
func NewAuthorCreditsRow(changeType, bookID string, before TitleRelinkCreditsSnapshot, move AuthorCreditsMove) (*database.OperationChange, error) {
	old, err := json.Marshal(before)
	if err != nil {
		return nil, err
	}
	nv, err := json.Marshal(move)
	if err != nil {
		return nil, err
	}
	return &database.OperationChange{
		BookID: bookID, ChangeType: changeType, FieldName: "book_authors",
		OldValue: string(old), NewValue: string(nv),
	}, nil
}

// NewAuthorDeleteRow builds the journal row for a deleted author. changeType is
// ChangeTypeAuthorMergeDelete (value an AuthorMergeDelete) or
// ChangeTypeAuthorReclassifyDelete (an AuthorReclassifyDelete).
func NewAuthorDeleteRow(changeType string, snap AuthorSnapshot, value any) (*database.OperationChange, error) {
	old, err := json.Marshal(snap)
	if err != nil {
		return nil, err
	}
	nv, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	return &database.OperationChange{
		ChangeType: changeType, FieldName: "author",
		OldValue: string(old), NewValue: string(nv),
	}, nil
}

// CheckJunctionIs is the revert's compare-and-set on a book's junction: it must
// hold EXACTLY want (what the journaled write left). Anything else is a later
// change the revert must not overwrite. what names the writer in the refusal.
func CheckJunctionIs(bookID string, current []database.BookAuthor, want *[]database.BookAuthor, what string) error {
	if want == nil || !SameJunction(current, *want) {
		return refuse(ReasonChangedSince, "book %s credits changed since the %s", bookID, what)
	}
	return nil
}

// CheckPrimaryAfterWrite is the compare-and-set on the primary, run BEFORE the
// junction is touched so a refused primary never leaves a half-reverted book. It
// returns ErrAlreadyRestored when the primary already holds the snapshot's value.
func CheckPrimaryAfterWrite(bookID string, current *int, before, after *int, changed bool, what string) error {
	if !changed {
		return nil
	}
	switch {
	case sameIntPointer(current, after):
		return nil
	case sameIntPointer(current, before):
		return ErrAlreadyRestored
	}
	return refuse(ReasonChangedSince, "book %s primary author changed since the %s", bookID, what)
}

// CheckAuthorLive refuses (ReasonChangedSince) unless authorID resolves to a live
// author row of that id. GetAuthorByID follows the tombstone, so an answer under
// another id means the author is still merged away.
func CheckAuthorLive(lookup func(int) (*database.Author, error), authorID int, what string) error {
	a, err := lookup(authorID)
	if err != nil {
		return refuse(ReasonFieldUnreadable, "look up author %d: %v", authorID, err)
	}
	if a == nil || a.ID != authorID {
		return refuse(ReasonChangedSince, "author %d is not restored, so its credit cannot go back (%s)", authorID, what)
	}
	return nil
}
