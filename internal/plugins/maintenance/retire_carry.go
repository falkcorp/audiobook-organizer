// file: internal/plugins/maintenance/retire_carry.go
// version: 1.0.0
// guid: 0b8d3f64-72a1-4c5e-8f19-6e4a9d2c7b35
// last-edited: 2026-10-21

package maintenance

import (
	"errors"
	"fmt"
	"slices"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/repairs"
	"github.com/falkcorp/audiobook-organizer/internal/undo"
)

// A retire hides a book and points merged_into_book_id at the survivor. What
// a person attached to the retired book himself (a rating, a place in a
// collection, a place in a playlist) would stay on a hidden book and be lost
// to him, so retireIntoWith carries it onto the survivor with the listening
// state (carryUserContentInto). Every move is journaled first as
// undo.ChangeTypeUserContentFollow and restored by the op revert.
//
// This is not retire-specific policy: merge.Service carries none of the
// three, which is why every Repairs retire (fragment-consolidation,
// duplicate-copies, consolidation-leftovers) had the gap.

// carryAttempts bounds the re-read when a collection or playlist changes
// between the read that planned a carry and the locked write that makes it.
const carryAttempts = 3

// errCarryStale: the list changed under the carry, so what was journaled no
// longer describes the write. The write made nothing (the journal row is
// voided) and the carry re-reads the list.
var errCarryStale = errors.New("list changed under the carry")

// carryUserContentInto carries the retired book id's user ratings, collection
// memberships and playlist entries onto target, in that order. It returns the
// journaled steps written, also on an error (a step that landed counts).
//
//   - Ratings: the survivor keeps every rating field it has; a field it left
//     empty takes the retired book's. Nothing is overwritten.
//   - Collections and playlists (static ones; a dynamic or smart list names
//     its members by query): the retired book is replaced in its slot by the
//     survivor, or just removed when the survivor is already listed.
//
// A store that cannot edit collections or playlists refuses the retire (the
// same rule as the listening state: a retire that silently dropped a user's
// content is worse than one that stops). It runs before the first journaled
// step of the retire's destructive half, so a refusal here leaves the book
// live.
func carryUserContentInto(store OpsStore, w *repairs.Writer, target, id string) (int, error) {
	steps := 0
	n, err := carryRatingsInto(store, w, target, id)
	steps += n
	if err != nil {
		return steps, fmt.Errorf("carry ratings of %s: %w", id, err)
	}
	n, err = carryCollectionsInto(store, w, target, id)
	steps += n
	if err != nil {
		return steps, fmt.Errorf("carry collections of %s: %w", id, err)
	}
	n, err = carryPlaylistsInto(store, w, target, id)
	steps += n
	if err != nil {
		return steps, fmt.Errorf("carry playlists of %s: %w", id, err)
	}
	return steps, nil
}

func carryRatingsInto(store OpsStore, w *repairs.Writer, target, id string) (int, error) {
	child, err := store.GetBookByID(id)
	if err != nil {
		return 0, err
	}
	survivor, err := store.GetBookByID(target)
	if err != nil {
		return 0, err
	}
	if child == nil || survivor == nil {
		return 0, fmt.Errorf("%w: book %s or %s vanished", repairs.ErrChangedSincePlan, id, target)
	}
	before := undo.RatingOf(survivor)
	after := before.FillFrom(undo.RatingOf(child))
	if after.Equal(before) {
		return 0, nil
	}
	oldV, err := undo.EncodeRating(before)
	if err != nil {
		return 0, err
	}
	newV, err := undo.EncodeRating(after)
	if err != nil {
		return 0, err
	}
	err = w.JournalStep(id, repairs.UndoEntry{ChangeType: undo.ChangeTypeUserContentFollow, Field: undo.UserContentField(undo.UserContentRating, target), Old: oldV, New: newV}, func() error {
		_, err := w.Modify(target, func(cur *database.Book) error {
			if !undo.RatingOf(cur).Equal(before) {
				return fmt.Errorf("%w: book %s ratings changed during the retire", repairs.ErrChangedSincePlan, target)
			}
			after.Apply(cur)
			return nil
		})
		return err
	})
	if err != nil {
		return 0, err
	}
	return 1, nil
}

func carryCollectionsInto(store OpsStore, w *repairs.Writer, target, id string) (int, error) {
	cols, ok := database.AsCapability[database.CollectionStore](store)
	if !ok {
		return 0, errors.New("collection store unavailable")
	}
	members, ok := database.AsCapability[database.CollectionMembershipStore](store)
	if !ok {
		return 0, errors.New("collection member store unavailable")
	}
	list, _, err := cols.ListCollections(database.CollectionTypeStatic, 0, 0)
	if err != nil {
		return 0, err
	}
	steps := 0
	for _, col := range list {
		if !slices.Contains(col.BookIDs, id) {
			continue
		}
		cur := col.BookIDs
		for attempt := 0; ; attempt++ {
			_, rec, ok := undo.CarryMembership(cur, id, target)
			if !ok {
				break
			}
			oldV, err := undo.EncodeMembership(rec)
			if err != nil {
				return steps, err
			}
			err = w.JournalStep(id, repairs.UndoEntry{ChangeType: undo.ChangeTypeUserContentFollow, Field: undo.UserContentField(undo.UserContentCollection, col.ID), Old: oldV, New: target}, func() error {
				_, err := members.UpdateCollectionBooks(col.ID, func(now []string) ([]string, error) {
					n, r, ok := undo.CarryMembership(now, id, target)
					if !ok || r != rec {
						return nil, errCarryStale
					}
					return n, nil
				})
				return err
			})
			if err == nil {
				steps++
				break
			}
			if errors.Is(err, database.ErrCollectionNotFound) || errors.Is(err, database.ErrCollectionNotStatic) {
				break // deleted or changed kind since the listing: nothing to carry
			}
			if !errors.Is(err, errCarryStale) || attempt+1 >= carryAttempts {
				return steps, fmt.Errorf("collection %s: %w", col.ID, err)
			}
			fresh, gerr := cols.GetCollection(col.ID)
			if gerr != nil {
				return steps, gerr
			}
			if fresh == nil || fresh.Type != database.CollectionTypeStatic {
				break
			}
			cur = fresh.BookIDs
		}
	}
	return steps, nil
}

func carryPlaylistsInto(store OpsStore, w *repairs.Writer, target, id string) (int, error) {
	pls, ok := database.AsCapability[database.UserPlaylistStore](store)
	if !ok {
		return 0, errors.New("playlist store unavailable")
	}
	list, _, err := pls.ListUserPlaylists(database.UserPlaylistTypeStatic, 0, 0)
	if err != nil {
		return 0, err
	}
	steps := 0
	for _, pl := range list {
		if !slices.Contains(pl.BookIDs, id) {
			continue
		}
		cur := pl.BookIDs
		for attempt := 0; ; attempt++ {
			_, rec, ok := undo.CarryMembership(cur, id, target)
			if !ok {
				break
			}
			oldV, err := undo.EncodeMembership(rec)
			if err != nil {
				return steps, err
			}
			err = w.JournalStep(id, repairs.UndoEntry{ChangeType: undo.ChangeTypeUserContentFollow, Field: undo.UserContentField(undo.UserContentPlaylist, pl.ID), Old: oldV, New: target}, func() error {
				fresh, err := pls.GetUserPlaylist(pl.ID)
				if err != nil {
					return err
				}
				if fresh == nil || fresh.Type != database.UserPlaylistTypeStatic {
					return errCarryGone
				}
				n, r, ok := undo.CarryMembership(fresh.BookIDs, id, target)
				if !ok || r != rec {
					return errCarryStale
				}
				fresh.BookIDs = n
				if err := pls.UpdateUserPlaylist(fresh); err != nil {
					if errors.Is(err, database.ErrUserPlaylistVersionConflict) {
						return errCarryStale
					}
					return err
				}
				return nil
			})
			if err == nil {
				steps++
				break
			}
			if errors.Is(err, errCarryGone) {
				break
			}
			if !errors.Is(err, errCarryStale) || attempt+1 >= carryAttempts {
				return steps, fmt.Errorf("playlist %s: %w", pl.ID, err)
			}
			fresh, gerr := pls.GetUserPlaylist(pl.ID)
			if gerr != nil {
				return steps, gerr
			}
			if fresh == nil || fresh.Type != database.UserPlaylistTypeStatic {
				break
			}
			cur = fresh.BookIDs
		}
	}
	return steps, nil
}

// errCarryGone: the playlist was deleted, or stopped being static, since the
// listing: nothing to carry.
var errCarryGone = errors.New("list is gone")
