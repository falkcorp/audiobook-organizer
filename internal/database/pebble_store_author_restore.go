// file: internal/database/pebble_store_author_restore.go
// version: 1.1.0
// guid: 4e8a1c37-95b2-4d60-a3f9-7c2d0b6e1f58
// last-edited: 2026-10-14

package database

import (
	"encoding/json"
	"errors"
	"fmt"
	"strconv"

	"github.com/cockroachdb/pebble/v2"

	"github.com/falkcorp/audiobook-organizer/internal/util"
)

// ErrAuthorNameTaken: RestoreAuthor was asked to bring back an author whose name
// another author has taken since.
var ErrAuthorNameTaken = errors.New("author name taken by another author")

// AuthorRestorer is the part of the author store the undo of an author merge or
// reclassify needs and nothing else does: bring a deleted author back under its
// own id, with what the delete moved or redirected. It is not on Store (every
// other writer of authors goes through CreateAuthor, which mints a new id);
// resolve it with AsCapability. *PebbleStore implements it.
type AuthorRestorer interface {
	// RestoreAuthor recreates the author row with exactly this id and name, and
	// puts back what opts lists, in one batch. A live row of that id already
	// there is not an error: the row is left alone (Restored false) and the rest
	// still runs, so a revert that stopped part way finishes on a retry. It
	// returns ErrAuthorNameTaken, and writes nothing, when AuthorNameIndexOnRestore
	// refuses the name.
	RestoreAuthor(id int, name string, opts AuthorRestoreOptions) (*AuthorRestoreResult, error)
}

// AuthorRestoreOptions is what a delete did besides removing the row.
type AuthorRestoreOptions struct {
	// MergedInto is the author a merge folded this one into (0: not a merge).
	MergedInto int
	// OwnedNameIndex: the author held its own author:name: entry when it was
	// deleted (AuthorMergeDeleteResult.OwnedNameIndex).
	OwnedNameIndex bool
	// AliasIDs are aliases the merge moved onto MergedInto; each one still there
	// is moved back.
	AliasIDs []int
	// InboundTombstones are ids that redirected to this author when it was
	// deleted. One flattened onto MergedInto's chain since (ResolveTombstoneChains)
	// is pointed back at this author.
	InboundTombstones []int
	// DropTombstone removes this author's own redirect.
	DropTombstone bool
}

// AuthorRestoreResult reports what RestoreAuthor did.
type AuthorRestoreResult struct {
	// Restored: this call wrote the row (false: a live row was already there).
	Restored bool
	// AliasesNotBack are listed aliases that are not on the author after the
	// call, with why: deleted since, or moved to another author since.
	AliasesNotBack []AuthorAliasNotBack
}

// AuthorAliasNotBack is one alias RestoreAuthor could not move back.
type AuthorAliasNotBack struct {
	AliasID int
	// HeldBy is the author the alias is on now; 0 when the alias row is gone.
	HeldBy int
}

// AuthorNameIndexOnRestore decides the author:name: entry of an author being
// restored. ownedAtDelete is whether the author held the entry when it was
// deleted; holder is the live author that holds it now (0: none); mergedInto is
// the author a merge folded it into (0: not a merge).
//
// Names that normalize the same (a case or whitespace variant) share ONE entry,
// and two such authors are exactly what a dedup merge folds together. So:
//
//   - the author did not hold the entry: write nothing, refuse nothing. Its row
//     never resolved by name, and the holder is not "somebody who took the name".
//   - it held the entry, and nobody (or itself) holds it now: write it back.
//   - it held the entry, and the author it was merged into holds it now (renamed
//     onto it since): the survivor keeps the entry, the row comes back without it.
//   - it held the entry, and any other author holds it: ErrAuthorNameTaken.
//
// The store's restore and the undo preflight both call it, so the preflight
// predicts what the restore does.
func AuthorNameIndexOnRestore(ownedAtDelete bool, holder, restoredID, mergedInto int) (write bool, err error) {
	switch {
	case !ownedAtDelete:
		return false, nil
	case holder == 0 || holder == restoredID:
		return true, nil
	case mergedInto > 0 && holder == mergedInto:
		return false, nil
	}
	return false, fmt.Errorf("%w: author %d holds it", ErrAuthorNameTaken, holder)
}

// RestoreAuthor implements AuthorRestorer.
func (p *PebbleStore) RestoreAuthor(id int, name string, opts AuthorRestoreOptions) (*AuthorRestoreResult, error) {
	if id <= 0 {
		return nil, fmt.Errorf("restore author: invalid id %d", id)
	}
	if err := CheckAuthorNameForCreation(name); err != nil {
		return nil, err
	}
	// The same locks every writer of author:name: and author_alias: holds, in
	// their order (see nameIndexLocks).
	p.nameIdx.author.Lock()
	defer p.nameIdx.author.Unlock()
	p.nameIdx.alias.Lock()
	defer p.nameIdx.alias.Unlock()

	res := &AuthorRestoreResult{}
	batch := p.db.NewBatch()
	fail := func(err error) (*AuthorRestoreResult, error) {
		batch.Close()
		return nil, err
	}

	key := []byte(fmt.Sprintf("author:%d", id))
	var author *Author
	if _, closer, err := p.db.Get(key); err == nil {
		closer.Close()
	} else if err != pebble.ErrNotFound {
		return fail(err)
	} else {
		holder, err := p.liveAuthorNameHolder(name)
		if err != nil {
			return fail(err)
		}
		writeIndex, err := AuthorNameIndexOnRestore(opts.OwnedNameIndex, holder, id, opts.MergedInto)
		if err != nil {
			return fail(fmt.Errorf("restore author %d %q: %w", id, name, err))
		}
		author = &Author{ID: id, Name: name}
		data, err := json.Marshal(author)
		if err != nil {
			return fail(err)
		}
		if err := batch.Set(key, data, nil); err != nil {
			return fail(err)
		}
		if writeIndex {
			if err := batch.Set([]byte(authorNameIndexKey(util.NormalizeAuthor(name))), []byte(strconv.Itoa(id)), nil); err != nil {
				return fail(err)
			}
		}
		res.Restored = true
	}

	// The aliases the merge moved, while they are still on the survivor.
	var movedBack []AuthorAlias
	for _, aliasID := range opts.AliasIDs {
		alias, err := p.getAuthorAliasByID(aliasID)
		if err != nil {
			return fail(err)
		}
		switch {
		case alias == nil:
			res.AliasesNotBack = append(res.AliasesNotBack, AuthorAliasNotBack{AliasID: aliasID})
		case alias.AuthorID == id:
			// Back already.
		case opts.MergedInto > 0 && alias.AuthorID == opts.MergedInto:
			if err := batch.Delete([]byte(fmt.Sprintf("author_alias:author:%d:%d", alias.AuthorID, alias.ID)), nil); err != nil {
				return fail(err)
			}
			alias.AuthorID = id
			if err := p.stageAuthorAliasRow(batch, alias); err != nil {
				return fail(err)
			}
			movedBack = append(movedBack, *alias)
		default:
			res.AliasesNotBack = append(res.AliasesNotBack, AuthorAliasNotBack{AliasID: aliasID, HeldBy: alias.AuthorID})
		}
	}

	// Redirects that pointed at this author and were flattened onto the
	// survivor's chain while the merge stood.
	if opts.MergedInto > 0 && len(opts.InboundTombstones) > 0 {
		chain, err := p.authorTombstoneChain(opts.MergedInto)
		if err != nil {
			return fail(err)
		}
		for _, oldID := range opts.InboundTombstones {
			if oldID == id {
				continue
			}
			target, err := p.GetAuthorTombstone(oldID)
			if err != nil {
				return fail(err)
			}
			if target == 0 || target == id || !chain[target] {
				continue // restored since, untouched, or redirected by a later merge
			}
			if live, err := p.liveAuthorRow(oldID); err != nil {
				return fail(err)
			} else if live {
				continue
			}
			if err := batch.Set(authorTombstoneKey(oldID), []byte(strconv.Itoa(id)), nil); err != nil {
				return fail(err)
			}
		}
	}
	if opts.DropTombstone {
		if err := batch.Delete(authorTombstoneKey(id), nil); err != nil {
			return fail(err)
		}
	}
	if err := batch.Commit(pebble.Sync); err != nil {
		return nil, err
	}
	if author != nil {
		p.UpsertAuthorToMemDB(author)
	}
	for i := range movedBack {
		p.UpsertAuthorAliasToMemDB(&movedBack[i])
	}
	p.notifyAuthorRenamed(id)
	return res, nil
}

// liveAuthorNameHolder is the id of the live author that holds name's
// author:name: entry, or 0. An entry naming an id with no live row of its own (a
// deleted author's leftover) holds nothing.
func (p *PebbleStore) liveAuthorNameHolder(name string) (int, error) {
	v, err := p.nameIndexGet(authorNameIndexKey, name)
	if err != nil || v == nil {
		return 0, err
	}
	holder, err := strconv.Atoi(string(v))
	if err != nil {
		return 0, nil
	}
	live, err := p.liveAuthorRow(holder)
	if err != nil || !live {
		return 0, err
	}
	return holder, nil
}

// liveAuthorRow reports whether author:<id> exists (not following a tombstone).
func (p *PebbleStore) liveAuthorRow(id int) (bool, error) {
	_, closer, err := p.db.Get([]byte(fmt.Sprintf("author:%d", id)))
	if err == pebble.ErrNotFound {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	closer.Close()
	return true, nil
}

// authorTombstoneChain is id and every id its redirects lead to.
func (p *PebbleStore) authorTombstoneChain(id int) (map[int]bool, error) {
	chain := map[int]bool{}
	for cur := id; cur != 0 && !chain[cur]; {
		chain[cur] = true
		next, err := p.GetAuthorTombstone(cur)
		if err != nil {
			return nil, err
		}
		cur = next
	}
	return chain, nil
}

// DeleteAuthorTombstone removes the redirect from a merged-away id. Absent is not an error.
func (p *PebbleStore) DeleteAuthorTombstone(oldID int) error {
	return p.db.Delete(authorTombstoneKey(oldID), pebble.Sync)
}
