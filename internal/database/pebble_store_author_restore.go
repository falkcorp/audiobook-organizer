// file: internal/database/pebble_store_author_restore.go
// version: 1.0.0
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
// own id, and drop the redirect the delete left. It is not on Store (every other
// writer of authors goes through CreateAuthor, which mints a new id); resolve it
// with AsCapability. *PebbleStore implements it.
type AuthorRestorer interface {
	// RestoreAuthor recreates the author row with exactly this id and name. It
	// reports restored=false, and writes nothing, when a live row of that id
	// already exists (an earlier revert got this far). It returns
	// ErrAuthorNameTaken when a different author holds the name.
	RestoreAuthor(id int, name string) (restored bool, err error)
	// DeleteAuthorTombstone removes the redirect from a merged-away id. Absent is not an error.
	DeleteAuthorTombstone(oldID int) error
}

// RestoreAuthor implements AuthorRestorer.
func (p *PebbleStore) RestoreAuthor(id int, name string) (bool, error) {
	if id <= 0 {
		return false, fmt.Errorf("restore author: invalid id %d", id)
	}
	if err := CheckAuthorNameForCreation(name); err != nil {
		return false, err
	}
	// The same lock every writer of author:name: holds (see nameIndexLocks).
	p.nameIdx.author.Lock()
	defer p.nameIdx.author.Unlock()

	key := []byte(fmt.Sprintf("author:%d", id))
	if _, closer, err := p.db.Get(key); err == nil {
		closer.Close()
		return false, nil
	} else if err != pebble.ErrNotFound {
		return false, err
	}
	// GetAuthorByName reads the index straight from Pebble. A hit on another id is
	// a name somebody else owns now.
	if holder, err := p.GetAuthorByName(name); err != nil {
		return false, err
	} else if holder != nil && holder.ID != id {
		return false, fmt.Errorf("%w: %q is author %d", ErrAuthorNameTaken, name, holder.ID)
	}

	author := &Author{ID: id, Name: name}
	data, err := json.Marshal(author)
	if err != nil {
		return false, err
	}
	batch := p.db.NewBatch()
	if err := batch.Set(key, data, nil); err != nil {
		batch.Close()
		return false, err
	}
	indexKey := []byte("author:name:" + util.NormalizeAuthor(name))
	if err := batch.Set(indexKey, []byte(strconv.Itoa(id)), nil); err != nil {
		batch.Close()
		return false, err
	}
	if err := batch.Commit(pebble.Sync); err != nil {
		return false, err
	}
	p.UpsertAuthorToMemDB(author)
	p.notifyAuthorRenamed(id)
	return true, nil
}

// DeleteAuthorTombstone implements AuthorRestorer.
func (p *PebbleStore) DeleteAuthorTombstone(oldID int) error {
	return p.db.Delete([]byte(fmt.Sprintf("author_tombstone:%d", oldID)), pebble.Sync)
}
