// file: internal/plugins/maintenance/retire_carry.go
// version: 1.1.0
// guid: 0b8d3f64-72a1-4c5e-8f19-6e4a9d2c7b35
// last-edited: 2026-10-21

package maintenance

import (
	"errors"
	"fmt"
	"slices"
	"sync"
	"time"

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

// carryIndexMaxAge bounds how long an apply op reuses one listing of the
// collections and playlists. A person who adds the retired book to a list
// after the listing, within this window, is the one case the index can miss
// (the write itself always re-reads the list under its lock).
const carryIndexMaxAge = 30 * time.Second

// carryIndex is one listing of every static collection and playlist, as
// book id -> the lists that name it. It is a hint only: carryCollectionsInto
// reads each list again before it writes, so a stale entry costs one read and
// never a wrong write.
type carryIndex struct {
	builtAt     time.Time
	collections map[string][]string
	playlists   map[string][]string
}

// carryIndexes holds one carryIndex per apply op (keyed by the Writer's op
// id), so a whole-library apply lists the collections and playlists once, not
// once per retired book (52 copies, 52 full scans). Entries are replaced
// after carryIndexMaxAge and dropped after twice that.
type carryIndexes struct {
	mu   sync.Mutex
	byOp map[string]*carryIndex
	// now is the clock (nil is time.Now); tests move it.
	now func() time.Time
}

func (ci *carryIndexes) clock() time.Time {
	if ci.now != nil {
		return ci.now()
	}
	return time.Now()
}

// listsOf returns the ids of the static collections and playlists that name
// book id, from op's index (built, or rebuilt when stale, by one listing).
func (ci *carryIndexes) listsOf(op string, cs carryStores, id string) (collections, playlists []string, err error) {
	ci.mu.Lock()
	defer ci.mu.Unlock()
	now := ci.clock()
	for k, e := range ci.byOp {
		if now.Sub(e.builtAt) > 2*carryIndexMaxAge {
			delete(ci.byOp, k)
		}
	}
	e := ci.byOp[op]
	if e == nil || now.Sub(e.builtAt) > carryIndexMaxAge {
		e, err = buildCarryIndex(cs, now)
		if err != nil {
			return nil, nil, err
		}
		if ci.byOp == nil {
			ci.byOp = map[string]*carryIndex{}
		}
		ci.byOp[op] = e
	}
	return slices.Clone(e.collections[id]), slices.Clone(e.playlists[id]), nil
}

func buildCarryIndex(cs carryStores, now time.Time) (*carryIndex, error) {
	e := &carryIndex{builtAt: now, collections: map[string][]string{}, playlists: map[string][]string{}}
	cols, _, err := cs.cols.ListCollections(database.CollectionTypeStatic, 0, 0)
	if err != nil {
		return nil, fmt.Errorf("list collections: %w", err)
	}
	for _, c := range cols {
		for _, b := range c.BookIDs {
			e.collections[b] = append(e.collections[b], c.ID)
		}
	}
	pls, _, err := cs.pls.ListUserPlaylists(database.UserPlaylistTypeStatic, 0, 0)
	if err != nil {
		return nil, fmt.Errorf("list playlists: %w", err)
	}
	for _, pl := range pls {
		for _, b := range pl.BookIDs {
			e.playlists[b] = append(e.playlists[b], pl.ID)
		}
	}
	return e, nil
}

// moved records that a carry put survivor in list listID in place of retired,
// so a later retire of survivor in the same op (a chain) finds it.
func (ci *carryIndexes) moved(op, kind, listID, retired, survivor string) {
	ci.mu.Lock()
	defer ci.mu.Unlock()
	e := ci.byOp[op]
	if e == nil {
		return
	}
	m := e.collections
	if kind == undo.UserContentPlaylist {
		m = e.playlists
	}
	m[retired] = slices.DeleteFunc(m[retired], func(l string) bool { return l == listID })
	if !slices.Contains(m[survivor], listID) {
		m[survivor] = append(m[survivor], listID)
	}
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
//     removal also journals a marker on the survivor (the survivor now stands
//     in for the retired book), which another operation's revert of the
//     survivor's own slot reads so that it does not take this book's place
//     with it (undo.SurvivorStandsIn).
func carryUserContentInto(p *Plugin, store OpsStore, cs carryStores, w *repairs.Writer, target, id string) (int, error) {
	steps := 0
	n, err := carryRatingsInto(store, w, target, id)
	steps += n
	if err != nil {
		return steps, fmt.Errorf("carry ratings of %s: %w", id, err)
	}
	cols, pls, err := p.carry.listsOf(w.OpID(), cs, id)
	if err != nil {
		return steps, fmt.Errorf("carry lists of %s: %w", id, err)
	}
	n, err = carryCollectionsInto(p, cs, w, target, id, cols)
	steps += n
	if err != nil {
		return steps, fmt.Errorf("carry collections of %s: %w", id, err)
	}
	n, err = carryPlaylistsInto(p, cs, w, target, id, pls)
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
// did (the list no longer matches rec). A removal (the survivor already
// listed) journals the survivor-side marker in the same step, so both rows are
// voided together when the write is refused.
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
	if !rec.SurvivorWasMember {
		return w.JournalStep(id, entry, func() error { return write(carry) })
	}
	marker := repairs.UndoEntry{ChangeType: undo.ChangeTypeUserContentFollow, Field: undo.UserContentField(undo.HeldKind(kind), listID), Old: oldV, New: id}
	// The restore of the retired book's row runs first (newest first), then
	// the marker's, so the marker is journaled first.
	return w.JournalSteps([]repairs.BookStep{{BookID: target, Entry: marker}, {BookID: id, Entry: entry}}, func() ([]bool, error) {
		if err := write(carry); err != nil {
			return nil, err
		}
		return []bool{true, true}, nil
	})
}

func carryCollectionsInto(p *Plugin, cs carryStores, w *repairs.Writer, target, id string, listIDs []string) (int, error) {
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
				p.carry.moved(w.OpID(), undo.UserContentCollection, colID, id, target)
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

func carryPlaylistsInto(p *Plugin, cs carryStores, w *repairs.Writer, target, id string, listIDs []string) (int, error) {
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
				p.carry.moved(w.OpID(), undo.UserContentPlaylist, plID, id, target)
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
