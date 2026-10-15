// file: internal/authormerge/authormerge.go
// version: 1.0.0
// guid: 7d3e9a51-2c84-4b6f-a0e7-5f19c3d82b46
// last-edited: 2026-10-14

// Package authormerge is the journaled write path every author merge shares:
// moving one book's credits off the merged author, and removing the merged author
// (database.MergeDeleteAuthorOf). Each write is journaled in the operation's undo
// ledger BEFORE it is made, as author_merge_credits / author_merge_delete rows the
// revert engine reverses (undo/author_merge.go), and a row whose write is refused
// is voided, so a row that stands always describes a write that happened.
//
// The Authors view merge (entities.author-merge, ai.author-scan-apply), the AI
// dedup apply (ai.author-merge-apply) and the maintenance merges
// (author-duplicate-merge, author-conjunction-repair, author-strip-merge,
// author-id-repair) all go through it, so every author merge is undoable the
// same way. Each caller still decides which books to move and what their credits
// become; this package only makes the write and its ledger row.
package authormerge

import (
	"encoding/json"
	"fmt"
	"slices"
	"time"

	"github.com/oklog/ulid/v2"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/undo"
)

// JournalStore writes undo-ledger rows.
type JournalStore interface {
	CreateOperationChange(change *database.OperationChange) error
}

// Step writes one undo-ledger row BEFORE the write it describes and returns the
// function that voids it when that write is refused. A failed journal write is
// returned: the caller must not make the write (an unjournaled merge cannot be
// undone, and must never happen silently).
func Step(store JournalStore, opID string, row *database.OperationChange) (void func() error, err error) {
	if opID == "" {
		return nil, fmt.Errorf("no operation id to journal the undo row under")
	}
	row.ID = ulid.Make().String()
	row.OperationID = opID
	if err := store.CreateOperationChange(row); err != nil {
		return nil, err
	}
	return func() error {
		voided := *row
		now := time.Now()
		voided.RevertedAt, voided.Voided = &now, true
		return store.CreateOperationChange(&voided)
	}, nil
}

// CreditsStore is what MoveCredits reads and writes.
type CreditsStore interface {
	JournalStore
	GetBookAuthors(bookID string) ([]database.BookAuthor, error)
	SetBookAuthors(bookID string, authors []database.BookAuthor) error
	ModifyBook(id string, fn func(*database.Book) error) (*database.Book, error)
}

// CreditMove is one book's credits moving off FromID onto IntoID.
type CreditMove struct {
	BookID         string
	FromID, IntoID int
	// Into, when set, is also written as the book's denormalized Author when its
	// primary is repointed (the maintenance merges kept it in step).
	Into *database.Author
	// Before is the book's junction as the caller read it; After is what the
	// caller wants it to be.
	Before, After []database.BookAuthor
	// PrimaryBefore is the book's primary AuthorID as the caller read it. The
	// primary is repointed only when it names FromID.
	PrimaryBefore *int
}

// MoveResult reports a MoveCredits that wrote the junction.
type MoveResult struct {
	// PrimaryRepointed: the primary named FromID and now names IntoID.
	PrimaryRepointed bool
	// Problems are failures after the junction moved: the primary could not be
	// repointed, or the row could not be updated to what was stored. The credits
	// did move; a caller that requires the primary to move counts these.
	Problems []string
	// PrimaryErr is set when the primary write failed (not when it was skipped
	// because the book is gone or another writer repointed it meanwhile). The
	// book then still names FromID as its primary, so a caller does not delete
	// FromID.
	PrimaryErr error
}

// MoveCredits journals the move as an author_merge_credits row, writes After,
// then repoints the primary when it names FromID. An error means nothing was
// written (the row could not be built or recorded, or the junction write was
// refused and the row voided).
func MoveCredits(store CreditsStore, opID string, m CreditMove) (MoveResult, error) {
	var res MoveResult
	primaryChanged := m.PrimaryBefore != nil && *m.PrimaryBefore == m.FromID
	after := slices.Clone(m.After)
	move := undo.AuthorCreditsMove{FromAuthorID: m.FromID, IntoAuthorID: m.IntoID, CreditsAfter: &after, PrimaryChanged: primaryChanged}
	if primaryChanged {
		k := m.IntoID
		move.PrimaryAfter = &k
	}
	var primaryBefore *int
	if m.PrimaryBefore != nil {
		v := *m.PrimaryBefore
		primaryBefore = &v
	}
	row, err := undo.NewAuthorCreditsRow(undo.ChangeTypeAuthorMergeCredits, m.BookID,
		undo.TitleRelinkCreditsSnapshot{AuthorID: primaryBefore, Credits: slices.Clone(m.Before)}, move)
	if err != nil {
		return res, fmt.Errorf("undo row not built: %w", err)
	}
	void, err := Step(store, opID, row)
	if err != nil {
		return res, fmt.Errorf("undo row not recorded: %w", err)
	}
	if err := store.SetBookAuthors(m.BookID, m.After); err != nil {
		if verr := void(); verr != nil {
			return res, fmt.Errorf("set book authors: %w (and its undo row could not be voided: %v)", err, verr)
		}
		return res, fmt.Errorf("set book authors: %w", err)
	}
	rewrite := func(what string) {
		nv, merr := json.Marshal(move)
		if merr == nil {
			row.NewValue = string(nv)
			merr = store.CreateOperationChange(row)
		}
		if merr != nil {
			res.Problems = append(res.Problems, fmt.Sprintf("undo row not updated %s: %v", what, merr))
		}
	}
	// The store may normalize what it was given (positions); the revert is a
	// compare-and-set against the stored junction, so journal what is stored.
	if stored, gerr := store.GetBookAuthors(m.BookID); gerr == nil && !undo.SameJunction(stored, after) {
		after = append(after[:0], stored...)
		rewrite("to the stored credits")
	}
	if !primaryChanged {
		return res, nil
	}
	// Under the book's write lock, only when it still names FromID, so a column
	// another writer commits meanwhile is not reverted (audit A1#15).
	wrote := false
	_, upErr := store.ModifyBook(m.BookID, func(cur *database.Book) error {
		if cur.AuthorID == nil || *cur.AuthorID != m.FromID {
			return database.ErrSkipBookWrite
		}
		id := m.IntoID
		cur.AuthorID = &id
		if m.Into != nil {
			into := *m.Into
			cur.Author = &into
		}
		wrote = true
		return nil
	})
	if upErr == nil && wrote {
		res.PrimaryRepointed = true
		return res, nil
	}
	if upErr != nil {
		res.PrimaryErr = upErr
		res.Problems = append(res.Problems, fmt.Sprintf("credits moved but its primary author could not be repointed: %v", upErr))
	}
	// A row that still claimed the repoint made the revert read the untouched
	// primary as "already back" and then refuse the junction as changed since.
	move.PrimaryChanged, move.PrimaryAfter = false, nil
	rewrite("after its primary author was not repointed")
	return res, nil
}

// DeleteStore is what MergeDelete reads and writes. The merge-delete itself is
// the store's database.AuthorMergeDeleter capability.
type DeleteStore interface {
	JournalStore
	GetAuthorAliases(authorID int) ([]database.AuthorAlias, error)
	GetAuthorByName(name string) (*database.Author, error)
}

// DeleteResult reports a MergeDelete that removed the author.
type DeleteResult struct {
	Deleted *database.AuthorMergeDeleteResult
	// Problems: the row could not be updated to what the delete moved. The
	// author is gone; the undo then lacks what the snapshot missed.
	Problems []string
}

// MergeDelete journals an author_merge_delete row, merge-deletes from into into
// (database.MergeDeleteAuthorOf) and rewrites the row with exactly what the
// delete moved (only the delete knows the inbound redirects). An error means the
// author was not deleted; a refused delete voids its row.
func MergeDelete(store DeleteStore, opID string, from, into database.Author) (DeleteResult, error) {
	var out DeleteResult
	aliases, err := store.GetAuthorAliases(from.ID)
	if err != nil {
		return out, fmt.Errorf("its aliases could not be read for the undo row: %w", err)
	}
	snap := undo.AuthorSnapshot{ID: from.ID, Name: from.Name}
	for _, al := range aliases {
		snap.Aliases = append(snap.Aliases, undo.AuthorAliasSnapshot{ID: al.ID, Name: al.AliasName, Type: al.AliasType})
	}
	holder, err := store.GetAuthorByName(from.Name)
	if err != nil {
		return out, fmt.Errorf("its name entry could not be read for the undo row: %w", err)
	}
	snap.SharedNameIndex = holder != nil && holder.ID != from.ID
	value := undo.AuthorMergeDelete{IntoAuthorID: into.ID, IntoName: into.Name}
	row, err := undo.NewAuthorDeleteRow(undo.ChangeTypeAuthorMergeDelete, snap, value)
	if err != nil {
		return out, fmt.Errorf("undo row not built: %w", err)
	}
	void, err := Step(store, opID, row)
	if err != nil {
		return out, fmt.Errorf("undo row not recorded: %w", err)
	}
	res, err := database.MergeDeleteAuthorOf(store, from.ID, into.ID)
	if err != nil {
		if verr := void(); verr != nil {
			return out, fmt.Errorf("%w (and its undo row could not be voided: %v)", err, verr)
		}
		return out, err
	}
	out.Deleted = res
	if exact := undo.MergeSnapshotFrom(from, res); !SameSnapshot(exact, snap) {
		fixed, ferr := undo.NewAuthorDeleteRow(undo.ChangeTypeAuthorMergeDelete, exact, value)
		if ferr == nil {
			row.OldValue = fixed.OldValue
			ferr = store.CreateOperationChange(row)
		}
		if ferr != nil {
			out.Problems = append(out.Problems, fmt.Sprintf("undo row not updated to what the delete moved: %v", ferr))
		}
	}
	return out, nil
}

// SameSnapshot compares two delete snapshots field by field.
func SameSnapshot(a, b undo.AuthorSnapshot) bool {
	return a.ID == b.ID && a.Name == b.Name && a.SharedNameIndex == b.SharedNameIndex &&
		slices.Equal(a.Aliases, b.Aliases) && slices.Equal(a.InboundTombstones, b.InboundTombstones)
}
