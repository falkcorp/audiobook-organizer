// file: internal/merge/hard_delete_keeping_state.go
// version: 1.0.0
// guid: 051a1636-d299-45a5-80d2-9b72255cffcf
// last-edited: 2026-10-05

package merge

import (
	"errors"
	"fmt"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// Hard deletes a user asks for (owner decision 2026-10-05, 19:15): the
// single-book delete / the trash page's "Purge now" and the batch hard
// delete behave like the nightly purge. A book users have listening state
// on (positions, status, progress, listened time, hide, bookmarks) is
// deleted only after that state is carried to a version of the book
// Audiobookshelf lists; with no such version the delete is refused, and the
// owner can drop the state on purpose with "Discard progress and purge"
// (DiscardUserStateThenHardDelete).

// ErrBookHasListeningState is returned by HardDeleteKeepingUserState when
// users have listening state on the book and no other version of it that
// Audiobookshelf lists exists to carry the state to. Nothing was deleted or
// moved.
var ErrBookHasListeningState = errors.New("the book has listening progress and there is no other copy of it in the Audiobookshelf library to move it to")

// ErrCarryTargetUnreadable is returned when the version chosen to receive
// the state could not be read (looking for it, or re-checking it under the
// merge lock). Nothing was deleted or moved; it is a failure, not a refusal.
var ErrCarryTargetUnreadable = errors.New("the copy chosen to receive the listening progress could not be read")

// VersionGroupReader reads books and version groups, to pick a carry target.
type VersionGroupReader interface {
	GetBookByID(id string) (*database.Book, error)
	GetBooksByVersionGroup(groupID string) ([]database.Book, error)
}

// IsCarryTarget reports whether b may receive another book's users' state:
// it is out of the trash and Audiobookshelf lists it
// (database.ABSLibraryFilter: the organized, non-quarantined primary).
// State carried onto a book ABS does not list -- an imported or iTunes-only
// copy, a non-primary version, a quarantined one -- is stranded where the
// user cannot see it.
func IsCarryTarget(b *database.Book) bool {
	return b != nil && !database.IsInTrash(b) && database.ABSLibraryFilter().Matches(b)
}

// ListedLiveSibling picks the member of book's version group its users'
// state may be carried to (IsCarryTarget), the lowest id among several. ""
// when the book has no group or no member qualifies. An error means the
// group could not be read.
func ListedLiveSibling(books VersionGroupReader, book *database.Book) (string, error) {
	if book == nil || book.VersionGroupID == nil || *book.VersionGroupID == "" {
		return "", nil
	}
	members, err := books.GetBooksByVersionGroup(*book.VersionGroupID)
	if err != nil {
		return "", fmt.Errorf("list version group %s: %w", *book.VersionGroupID, err)
	}
	best := ""
	for i := range members {
		m := &members[i]
		if m.ID == "" || m.ID == book.ID || !IsCarryTarget(m) {
			continue
		}
		if best == "" || m.ID < best {
			best = m.ID
		}
	}
	return best, nil
}

// CarryTargetPrecheck is the precheck CarryStateThenHardDelete runs under
// the merge lock for a target picked by ListedLiveSibling: the target is
// re-read and must still be a carry target. A failed read wraps
// ErrCarryTargetUnreadable; a target no longer listed wraps
// ErrBookHasListeningState.
func CarryTargetPrecheck(books VersionGroupReader, targetID string) func() error {
	return func() error {
		cur, err := books.GetBookByID(targetID)
		if err != nil {
			return fmt.Errorf("%w: re-read %s: %w", ErrCarryTargetUnreadable, targetID, err)
		}
		if !IsCarryTarget(cur) {
			return fmt.Errorf("%w: %s is no longer listed in Audiobookshelf", ErrBookHasListeningState, targetID)
		}
		return nil
	}
}

// HardDeleteKeepingUserState runs del (the caller's hard delete of book)
// only where no listening state is lost:
//
//   - no user has state on the book (UserStateProbe.Has, bookmarks
//     included): del runs under the merge lock after the same check is made
//     again there (HardDeleteWithoutUserState);
//   - state, and a version of the book Audiobookshelf lists exists
//     (ListedLiveSibling): the state is carried there and del runs, in one
//     hold of the merge lock (CarryStateThenHardDelete), carriedTo naming it;
//   - state and no such version: ErrBookHasListeningState, nothing done.
//
// Errors: a probe that cannot be made wraps ErrUserStateCheckFailed; an
// unreadable target ErrCarryTargetUnreadable; a carry that does not complete
// ErrStateCarryIncomplete (put back, del not run); del's own error is
// returned wrapped (after a carry the state is on carriedTo by then).
//
// The caller must hold neither the merge lock nor a version-group lock.
func HardDeleteKeepingUserState(db UserProgressMerger, books VersionGroupReader, book *database.Book, del func() error) (carriedTo string, err error) {
	if book == nil || book.ID == "" {
		return "", fmt.Errorf("%w: no book", ErrUserStateCheckFailed)
	}
	probe, err := NewUserStateProbe(db)
	if err != nil {
		return "", fmt.Errorf("%w: %s: %w", ErrUserStateCheckFailed, book.ID, err)
	}
	has, err := probe.Has(book.ID)
	if err != nil {
		return "", fmt.Errorf("%w: %s: %w", ErrUserStateCheckFailed, book.ID, err)
	}
	if !has {
		return "", HardDeleteWithoutUserState(db, book.ID, del)
	}
	target, err := ListedLiveSibling(books, book)
	if err != nil {
		return "", fmt.Errorf("%w: %w", ErrCarryTargetUnreadable, err)
	}
	if target == "" {
		return "", fmt.Errorf("%w (%s)", ErrBookHasListeningState, book.ID)
	}
	if err := CarryStateThenHardDelete(db, target, book.ID, CarryTargetPrecheck(books, target), del); err != nil {
		if errors.Is(err, ErrCarryPrecheckRefused) || errors.Is(err, ErrStateCarryIncomplete) {
			return "", err
		}
		return target, err
	}
	return target, nil
}
