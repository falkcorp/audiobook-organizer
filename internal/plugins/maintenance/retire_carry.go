// file: internal/plugins/maintenance/retire_carry.go
// version: 1.4.0
// guid: 0b8d3f64-72a1-4c5e-8f19-6e4a9d2c7b35
// last-edited: 2026-10-22

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
// longer describes the write. The write made nothing (the journal rows are
// voided) and the carry re-reads the list.
var errCarryStale = errors.New("list changed under the carry")

// errCarryGone: the playlist was deleted, or stopped being static, since the
// listing: nothing to carry.
var errCarryGone = errors.New("list is gone")

// errCarryJournalNotBatched: the op journal cannot write several rows in one
// commit (repairs.ChangeBatchJournal). A list step of the carry journals the
// survivor's marker and the retired book's row together; written one by one, a
// failure or a crash between the two would leave a marker standing on nothing,
// which a revert would read as a retire that holds the survivor in the list. The
// retire is refused before its first write, like a store without the list
// capabilities.
var errCarryJournalNotBatched = errors.New("the op journal cannot write a retire's rows in one commit, so a marker could be left without its row; nothing was written")

// carryStores are the list capabilities the carry writes through. The ratings
// need none (they are book fields, written through the Writer) and the
// listening state is checked by followUserStateInto, which is the first write
// of a retire.
type carryStores struct {
	cols    database.CollectionStore
	members database.CollectionMembershipStore
	pls     database.UserPlaylistStore
}

// resolveCarryStores resolves the collection and playlist capabilities of
// store, looking through its decorators (database.AsCapability). A store
// that lacks one cannot move a book's place in a list, and retireIntoWith asks
// BEFORE its first write: a retire that silently dropped a user's content is
// worse than one that stops, and a refusal here leaves the book live with
// nothing moved. It refuses every retire on such a store, not only a book that
// is in a list, because the lists cannot be read to tell.
func resolveCarryStores(store OpsStore) (carryStores, error) {
	var cs carryStores
	var ok bool
	if cs.cols, ok = database.AsCapability[database.CollectionStore](store); !ok {
		return cs, errors.New("collection store unavailable, so the book's places in collections cannot be carried; nothing was written")
	}
	if cs.members, ok = database.AsCapability[database.CollectionMembershipStore](store); !ok {
		return cs, errors.New("collection member store unavailable, so the book's places in collections cannot be carried; nothing was written")
	}
	if cs.pls, ok = database.AsCapability[database.UserPlaylistStore](store); !ok {
		return cs, errors.New("playlist store unavailable, so the book's places in playlists cannot be carried; nothing was written")
	}
	return cs, nil
}

// listsNaming returns the ids of the static collections and playlists that
// name book id, from one listing of each. It is read per retire, not cached
// across a run: a cached listing could miss a list the owner added the book to
// since, and the lists are few. It is a hint all the same: every list is read
// again before it is written (carryCollectionsInto, carryPlaylistsInto).
func listsNaming(cs carryStores, id string) (collections, playlists []string, err error) {
	cols, _, err := cs.cols.ListCollections(database.CollectionTypeStatic, 0, 0)
	if err != nil {
		return nil, nil, fmt.Errorf("list collections: %w", err)
	}
	for _, c := range cols {
		if slices.Contains(c.BookIDs, id) {
			collections = append(collections, c.ID)
		}
	}
	pls, _, err := cs.pls.ListUserPlaylists(database.UserPlaylistTypeStatic, 0, 0)
	if err != nil {
		return nil, nil, fmt.Errorf("list playlists: %w", err)
	}
	for _, pl := range pls {
		if slices.Contains(pl.BookIDs, id) {
			playlists = append(playlists, pl.ID)
		}
	}
	return collections, playlists, nil
}

// carryUserContentInto carries the retired book id's user ratings, collection
// memberships and playlist entries onto target, in that order. cs is
// resolveCarryStores, asked before the retire's first write. It returns the
// journaled steps written, also on an error (a step that landed counts).
//
//   - Ratings: the survivor keeps every rating field it has; a field it left
//     empty takes the retired book's. Nothing is overwritten.
//   - Collections and playlists (static ones; a dynamic or smart list names
//     its members by query): the retired book is replaced in its slot by the
//     survivor, or just removed when the survivor is already listed. A
//     removal journals a "held" marker on the survivor (it now stands in for
//     the retired book), and a replace journals a "took" marker (it entered
//     the list in that book's slot). Another operation's revert reads both so
//     that, whichever of two retires into the survivor is reverted first, the
//     survivor leaves the list with the last of them and never before or after
//     (undo.SurvivorStandOf).
func carryUserContentInto(store OpsStore, cs carryStores, w *repairs.Writer, target, id string) (int, error) {
	steps := 0
	n, err := carryRatingsInto(store, w, target, id)
	steps += n
	if err != nil {
		return steps, fmt.Errorf("carry ratings of %s: %w", id, err)
	}
	cols, pls, err := listsNaming(cs, id)
	if err != nil {
		return steps, fmt.Errorf("carry lists of %s: %w", id, err)
	}
	n, err = carryCollectionsInto(cs, w, target, id, cols)
	steps += n
	if err != nil {
		return steps, fmt.Errorf("carry collections of %s: %w", id, err)
	}
	n, err = carryPlaylistsInto(cs, w, target, id, pls)
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

// carryMembership journals and makes one carry on one list. write applies fn
// to the list under the list's own lock and must return errCarryStale when fn
// did (the list no longer matches rec). It journals a survivor-side marker in
// the same step as the retired book's row, so both are voided together when
// the write is refused: a "held" marker on a removal (the survivor was already
// listed and now also stands in for the retired book) and a "took" marker on a
// replace (the survivor entered the list in the retired book's slot). The
// markers write nothing; a later operation's revert reads them to decide
// whether the survivor stays in the list (undo.SurvivorStandOf).
func carryMembership(w *repairs.Writer, kind, listID, target, id string, rec undo.MembershipRecord, write func(carry func(cur []string) ([]string, error)) error) error {
	oldV, err := undo.EncodeMembership(rec)
	if err != nil {
		return err
	}
	carry := func(cur []string) ([]string, error) {
		n, r, ok := undo.CarryMembership(cur, id, target)
		if !ok || r != rec {
			return nil, errCarryStale
		}
		return n, nil
	}
	entry := repairs.UndoEntry{ChangeType: undo.ChangeTypeUserContentFollow, Field: undo.UserContentField(kind, listID), Old: oldV, New: target}
	markerKind := undo.TookKind(kind)
	if rec.SurvivorWasMember {
		markerKind = undo.HeldKind(kind)
	}
	marker := repairs.UndoEntry{ChangeType: undo.ChangeTypeUserContentFollow, Field: undo.UserContentField(markerKind, listID), Old: oldV, New: id}
	// The restore of the retired book's row runs first (newest first), then
	// the marker's, so the marker is journaled first.
	//
	// The list lock covers journal and write together (undo.LockList): a
	// revert of another operation reading the survivor's journal must not see
	// this marker before the write it describes, nor the write before it.
	defer undo.LockList(kind, listID)()
	return w.JournalSteps([]repairs.BookStep{{BookID: target, Entry: marker}, {BookID: id, Entry: entry}}, func() ([]bool, error) {
		if err := write(carry); err != nil {
			return nil, err
		}
		return []bool{true, true}, nil
	})
}

func carryCollectionsInto(cs carryStores, w *repairs.Writer, target, id string, listIDs []string) (int, error) {
	steps := 0
	for _, colID := range listIDs {
		col, err := cs.cols.GetCollection(colID)
		if err != nil {
			return steps, fmt.Errorf("collection %s: %w", colID, err)
		}
		if col == nil || col.Type != database.CollectionTypeStatic {
			continue // deleted or changed kind since the listing: nothing to carry
		}
		cur := col.BookIDs
		for attempt := 0; ; attempt++ {
			_, rec, ok := undo.CarryMembership(cur, id, target)
			if !ok {
				break
			}
			err := carryMembership(w, undo.UserContentCollection, colID, target, id, rec, func(carry func([]string) ([]string, error)) error {
				_, err := cs.members.UpdateCollectionBooks(colID, carry)
				return err
			})
			if err == nil {
				steps++
				break
			}
			if errors.Is(err, database.ErrCollectionNotFound) || errors.Is(err, database.ErrCollectionNotStatic) {
				break
			}
			if !errors.Is(err, errCarryStale) || attempt+1 >= carryAttempts {
				return steps, fmt.Errorf("collection %s: %w", colID, err)
			}
			fresh, gerr := cs.cols.GetCollection(colID)
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

func carryPlaylistsInto(cs carryStores, w *repairs.Writer, target, id string, listIDs []string) (int, error) {
	steps := 0
	for _, plID := range listIDs {
		pl, err := cs.pls.GetUserPlaylist(plID)
		if err != nil {
			return steps, fmt.Errorf("playlist %s: %w", plID, err)
		}
		if pl == nil || pl.Type != database.UserPlaylistTypeStatic {
			continue
		}
		cur := pl.BookIDs
		for attempt := 0; ; attempt++ {
			_, rec, ok := undo.CarryMembership(cur, id, target)
			if !ok {
				break
			}
			err := carryMembership(w, undo.UserContentPlaylist, plID, target, id, rec, func(carry func([]string) ([]string, error)) error {
				fresh, err := cs.pls.GetUserPlaylist(plID)
				if err != nil {
					return err
				}
				if fresh == nil || fresh.Type != database.UserPlaylistTypeStatic {
					return errCarryGone
				}
				n, err := carry(fresh.BookIDs)
				if err != nil {
					return err
				}
				fresh.BookIDs = n
				if err := cs.pls.UpdateUserPlaylist(fresh); err != nil {
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
				return steps, fmt.Errorf("playlist %s: %w", plID, err)
			}
			fresh, gerr := cs.pls.GetUserPlaylist(plID)
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
