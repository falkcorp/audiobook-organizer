// file: internal/database/pebble_store_author_merge_delete.go
// version: 1.0.0
// guid: 555c9709-4403-46af-b207-96b428052d33
// last-edited: 2026-10-14

package database

import (
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/cockroachdb/pebble/v2"
)

// ErrAuthorNotLive: a merge-delete named an author (to delete, or to merge into)
// that has no live row of that id.
var ErrAuthorNotLive = errors.New("author has no live row of that id")

// AuthorMergeDeleteResult is what MergeDeleteAuthor did that an undo needs to put
// back. Every field is read under the locks the delete holds.
type AuthorMergeDeleteResult struct {
	// MovedAliases are the merged author's aliases, now on the survivor under
	// their own ids (AuthorID already set to the survivor).
	MovedAliases []AuthorAlias
	// OwnedNameIndex: the merged author held its own author:name: entry. False
	// when another author whose name normalizes the same (a case or whitespace
	// variant) held it, in which case the delete left the entry alone and a
	// restore must not take it.
	OwnedNameIndex bool
	// InboundTombstones are the ids of authors merged INTO the merged author
	// earlier: their redirects still point at it. ResolveTombstoneChains may
	// flatten them onto the survivor while the merge stands, and a restore
	// points them back.
	InboundTombstones []int
}

// MergeDeleteAuthor implements AuthorMergeDeleter.
//
// Every merge path calls it: a merge that called DeleteAuthor and then
// CreateAuthorTombstone lost the merged author's aliases (DeleteAuthor cascades
// them), so a pen name that resolved to the merged author resolved to nobody.
func (p *PebbleStore) MergeDeleteAuthor(fromID, intoID int) (*AuthorMergeDeleteResult, error) {
	if fromID <= 0 || intoID <= 0 {
		return nil, fmt.Errorf("merge author %d into %d: invalid id", fromID, intoID)
	}
	return p.deleteAuthor(fromID, intoID)
}

// moveAuthorAliases stages, on batch, moving every alias of fromID onto intoID
// under the alias's own id: the row and its by-author index key are rewritten,
// and the author_alias:name: entry (name -> alias id) stays as it is. A by-author
// key whose alias row is gone is dropped. The caller holds nameIdx.alias.
func (p *PebbleStore) moveAuthorAliases(batch *GatedBatch, fromID, intoID int) ([]AuthorAlias, error) {
	prefix := []byte(fmt.Sprintf("author_alias:author:%d:", fromID))
	upper := []byte(fmt.Sprintf("author_alias:author:%d;", fromID))
	iter, err := p.db.NewIter(&pebble.IterOptions{LowerBound: prefix, UpperBound: upper})
	if err != nil {
		return nil, err
	}
	defer iter.Close()

	var moved []AuthorAlias
	for iter.First(); iter.Valid(); iter.Next() {
		// The alias id is the key's last segment; the value may be the alias JSON
		// or, for a legacy row, the bare id.
		key := string(iter.Key())
		aliasID, _ := strconv.Atoi(key[strings.LastIndexByte(key, ':')+1:])
		alias, err := p.getAuthorAliasByID(aliasID)
		if err != nil {
			return nil, err
		}
		if err := batch.Delete([]byte(key), nil); err != nil {
			return nil, fmt.Errorf("pebble Delete %s: %w", key, err)
		}
		if alias == nil {
			continue
		}
		alias.AuthorID = intoID
		if err := p.stageAuthorAliasRow(batch, alias); err != nil {
			return nil, err
		}
		moved = append(moved, *alias)
	}
	if err := iter.Error(); err != nil {
		return nil, err
	}
	return moved, nil
}

// stageAuthorAliasRow stages alias's row and its by-author index key (both carry
// the alias JSON, as CreateAuthorAlias writes them).
func (p *PebbleStore) stageAuthorAliasRow(batch *GatedBatch, alias *AuthorAlias) error {
	data, err := json.Marshal(alias)
	if err != nil {
		return err
	}
	if err := batch.Set([]byte(fmt.Sprintf("author_alias:%d", alias.ID)), data, nil); err != nil {
		return fmt.Errorf("pebble Set author_alias:%d: %w", alias.ID, err)
	}
	if err := batch.Set([]byte(fmt.Sprintf("author_alias:author:%d:%d", alias.AuthorID, alias.ID)), data, nil); err != nil {
		return fmt.Errorf("pebble Set author_alias:author index: %w", err)
	}
	return nil
}

// authorTombstonesPointingAt lists the ids whose tombstone redirects to authorID.
//
// Cost: one scan of the author_tombstone: keyspace (small fixed-size keys, one
// per author ever merged away) per call. There is no reverse index.
func (p *PebbleStore) authorTombstonesPointingAt(authorID int) ([]int, error) {
	iter, err := p.db.NewIter(&pebble.IterOptions{
		LowerBound: []byte("author_tombstone:"),
		UpperBound: []byte("author_tombstone;"),
	})
	if err != nil {
		return nil, fmt.Errorf("iterate author tombstones: %w", err)
	}
	defer iter.Close()
	want := strconv.Itoa(authorID)
	var out []int
	for iter.First(); iter.Valid(); iter.Next() {
		val, verr := iter.ValueAndErr()
		if verr != nil {
			return nil, fmt.Errorf("read author tombstone: %w", verr)
		}
		if string(val) != want {
			continue
		}
		oldID, aerr := strconv.Atoi(strings.TrimPrefix(string(iter.Key()), "author_tombstone:"))
		if aerr != nil {
			continue
		}
		out = append(out, oldID)
	}
	if err := iter.Error(); err != nil {
		return nil, fmt.Errorf("iterate author tombstones: %w", err)
	}
	return out, nil
}
