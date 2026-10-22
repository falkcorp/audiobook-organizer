// file: internal/database/pebble_store_opchange_list_index.go
// version: 1.0.0
// guid: 5d0a7e13-9c24-4b6f-8a51-3f7e2c9b40d8
// last-edited: 2026-10-22

// The opchange_by_list: secondary index over the operation-change journal: the
// carry rows of retired books into collections and playlists, filed by list.
//
//	opchange_by_list:<kind>:<listID>:<opID>:<changeID>   -> (empty value)
//
// kind is "collection" or "playlist". A list's journal is the rows of type
// user_content_follow whose field is the list's own ("collection:<id>": the
// retired book's row) or one of its survivor-side markers ("collection-held:<id>"
// and "collection-took:<id>"): every row any retire into the list wrote, on the
// books of every survivor and every retired book alike. The key is the list
// followed by the primary key with its "opchange:" prefix removed, so within one
// list the index sorts in the primary key's order.
//
// WHY. Where a retired book goes back in a list is decided by the records of
// EVERY carry into that list (undo.MembershipGraph), and those records live in
// the journals of many books: the survivors' markers and the retired books'
// rows. Reading them book by book needs a rule for which books to read, and a
// rule can stop short; reading the list's own journal in one call cannot.
//
// Indexable list ids: non-empty and free of ':', for the reason the by-book
// index has (opChangeIndexable): a ':' in the id would make two lists' keys
// collide. GetListChanges scans the whole journal for an id that fails the test,
// so it returns every row either way.
//
// Invariant (completeness): for every user_content_follow row with a list
// field of an indexable list whose JSON decodes, the entry exists. Extras are
// allowed (the reader point-gets each row and keeps it only when it still
// names the list). Every writer keeps the entry in the row's own batch:
// CreateOperationChange Sets it (and deletes the entry a rewritten row had under
// another list), markOperationChangesRevertedWith re-Sets it, and
// PruneOperationChanges deletes it. The rows are never pruned in practice
// (OpChangeTypesKeptByPrune), the deletion is for the invariant's sake.
//
// No trust gate and no backfill: the carry's rows are written only by builds
// that stage the entry (nothing earlier wrote them), so there is nothing to
// catch up on. A row of another build's would be invisible to GetListChanges.

package database

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/cockroachdb/pebble/v2"
)

const (
	opChangeByListPrefix = "opchange_by_list:"
	// opChangeFollowType is undo.ChangeTypeUserContentFollow (the database
	// package cannot import undo).
	opChangeFollowType = "user_content_follow"
)

// List kinds, as undo.UserContentCollection and undo.UserContentPlaylist.
const (
	opChangeListCollection = "collection"
	opChangeListPlaylist   = "playlist"
)

// opChangeListRef is the list a journal row belongs to: its kind ("collection"
// or "playlist", whatever the marker variety) and id. ok is false for every row
// that is not part of a list's journal.
func opChangeListRef(c *OperationChange) (kind, listID string, ok bool) {
	if c == nil || c.ChangeType != opChangeFollowType {
		return "", "", false
	}
	field, id, found := strings.Cut(c.FieldName, ":")
	if !found || id == "" {
		return "", "", false
	}
	switch field {
	case opChangeListCollection, opChangeListCollection + "-held", opChangeListCollection + "-took":
		return opChangeListCollection, id, true
	case opChangeListPlaylist, opChangeListPlaylist + "-held", opChangeListPlaylist + "-took":
		return opChangeListPlaylist, id, true
	}
	return "", "", false
}

// opChangeListIndexable: the list id is free of ':' (see the file comment).
func opChangeListIndexable(listID string) bool { return !strings.Contains(listID, ":") }

func opChangeByListListPrefix(kind, listID string) []byte {
	return []byte(opChangeByListPrefix + kind + ":" + listID + ":")
}

// stageOpChangeListIndex adds c's list entry to batch, for a row that belongs
// to an indexable list; any other row gets none.
func stageOpChangeListIndex(batch *GatedBatch, c *OperationChange, primary []byte) error {
	kind, id, ok := opChangeListRef(c)
	if !ok || !opChangeListIndexable(id) {
		return nil
	}
	key := append(opChangeByListListPrefix(kind, id), primary[len(opChangeKeyPrefix):]...)
	if err := batch.Set(key, nil, nil); err != nil {
		return fmt.Errorf("stage opchange_by_list entry: %w", err)
	}
	return nil
}

// unstageOpChangeListIndex adds the deletion of c's list entry to batch.
func unstageOpChangeListIndex(batch *GatedBatch, c *OperationChange, primary []byte) error {
	kind, id, ok := opChangeListRef(c)
	if !ok || !opChangeListIndexable(id) {
		return nil
	}
	key := append(opChangeByListListPrefix(kind, id), primary[len(opChangeKeyPrefix):]...)
	if err := batch.Delete(key, nil); err != nil {
		return fmt.Errorf("stage opchange_by_list delete: %w", err)
	}
	return nil
}

// storedOpChange reads the row at primary. found is false when the row does not
// exist or does not decode (an undecodable row has no readable book or list, so
// there is no entry to move).
func (p *PebbleStore) storedOpChange(primary []byte) (old *OperationChange, found bool, err error) {
	v, closer, err := p.db.Get(primary)
	if errors.Is(err, pebble.ErrNotFound) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, fmt.Errorf("read existing opchange row: %w", err)
	}
	defer closer.Close()
	var row OperationChange
	if json.Unmarshal(v, &row) != nil {
		return nil, false, nil
	}
	return &row, true, nil
}

// GetListChanges returns every journal row of list listID of kind ("collection"
// or "playlist"), in primary key order: the retired books' rows of the carries
// into it and the survivors' markers, voided, reverted or not. It reads the
// opchange_by_list: index from one snapshot (a consistent view, as
// getBookChangesIndexed), and fails on an undecodable row as GetBookChanges does.
// It is not on Store: resolve it with database.AsCapability.
func (p *PebbleStore) GetListChanges(kind, listID string) ([]*OperationChange, error) {
	if kind != opChangeListCollection && kind != opChangeListPlaylist {
		return nil, fmt.Errorf("list changes: unknown list kind %q", kind)
	}
	if listID == "" {
		return nil, errors.New("list changes: empty list id")
	}
	if !opChangeListIndexable(listID) {
		var out []*OperationChange
		err := p.ScanOperationChanges(func(c *OperationChange) error {
			if k, id, ok := opChangeListRef(c); ok && k == kind && id == listID {
				out = append(out, c)
			}
			return nil
		})
		return out, err
	}
	snap := p.db.NewSnapshot()
	defer snap.Close()
	if err := opChangeUndecodableGate(snap); err != nil {
		return nil, err
	}
	prefix := opChangeByListListPrefix(kind, listID)
	iter, err := snap.NewIter(&pebble.IterOptions{LowerBound: prefix, UpperBound: prefixEnd(prefix)})
	if err != nil {
		return nil, err
	}
	defer iter.Close()
	var changes []*OperationChange
	primary := make([]byte, 0, 96)
	for iter.First(); iter.Valid(); iter.Next() {
		primary = append(append(primary[:0], opChangeKeyPrefix...), iter.Key()[len(prefix):]...)
		v, closer, err := snap.Get(primary)
		if errors.Is(err, pebble.ErrNotFound) {
			continue // stale entry: the row was pruned
		}
		if err != nil {
			return nil, err
		}
		var c OperationChange
		uerr := json.Unmarshal(v, &c)
		closer.Close()
		if uerr != nil {
			return nil, uerr
		}
		if k, id, ok := opChangeListRef(&c); !ok || k != kind || id != listID {
			continue // stale entry: the row now belongs to another list
		}
		changes = append(changes, &c)
	}
	return changes, iter.Error()
}
