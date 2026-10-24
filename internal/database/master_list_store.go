// file: internal/database/master_list_store.go
// version: 1.2.0
// guid: e49355ec-8cf3-4b4d-b7a4-732d4c7f90e7
// last-edited: 2026-10-24

package database

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/cockroachdb/pebble/v2"
	"github.com/oklog/ulid/v2"
)

// Master-list store (design: docs/design/2026-10-23-master-list-import-design.md,
// section 3). An uploaded list (a CSV or JSON file of books with
// their ISBN, ASIN and read status) is kept so the Repairs plan can read it by
// id instead of taking the rows in a request body, and so every plan row has a
// durable source line.
//
// The store holds opaque rows: it does not read them. The row schema, its
// validation and its collapse live in internal/masterlist, which this package
// does not import.
//
// Like the author catalog, it is NOT a book table. Nothing here is read by the
// scanner, dedup, ABS, the search index or the library stats, and nothing here
// names a book row.
//
// Keyspace (new prefixes; dropping them removes the feature):
//
//	mlist:list:<list id>          list metadata JSON (MasterListMeta)
//	mlist:row:<list id>:<n>       one row's JSON, n zero-padded so a prefix scan
//	                              returns the rows in input order
//
// A list becomes visible when its mlist:list: key is written, which happens
// after every row: a failed upload leaves no list, and its rows are deleted
// again (best effort; the rows of a list nobody can name are unreachable).
// DeleteList takes a list out the other way round, metadata first, so a list
// that is visible always has all its rows.
//
// A list is the input of an import, not a record of one: deleting or pruning a
// list leaves everything an import wrote from it alone (user state, and the
// mlist:elsewhere: records of master_list_elsewhere.go).
const (
	masterListPrefix    = "mlist:list:"
	masterListRowPrefix = "mlist:row:"
	// masterListRowDigits is the zero padding of a row's number in its key:
	// wide enough for a million rows (masterlist.MaxRows is 50,000).
	masterListRowDigits = 7
	// masterListChunk is how many rows one write batch holds.
	masterListChunk = 2000
)

// ErrMasterListNotFound is returned for a list id that is not stored.
var ErrMasterListNotFound = errors.New("master list not found")

// MasterListMeta is a stored list's provenance. Every row of the list shares
// it; each row adds only its own line number.
type MasterListMeta struct {
	ID string `json:"id"`
	// Filename is the uploaded file's name as sent (display only).
	Filename string `json:"filename,omitempty"`
	Format   string `json:"format"`
	// UploadedBy is the id of the user who uploaded it.
	UploadedBy string    `json:"uploaded_by,omitempty"`
	UploadedAt time.Time `json:"uploaded_at"`
	RowCount   int       `json:"row_count"`
	// Bytes and SHA256 describe the uploaded file itself.
	Bytes  int64  `json:"bytes,omitempty"`
	SHA256 string `json:"sha256,omitempty"`
}

// MasterListStore is the Pebble-backed list store. Safe for concurrent use.
type MasterListStore struct {
	db  *GatedDB
	now func() time.Time
}

// NewMasterListStore builds the store on a shared PebbleDB handle.
func NewMasterListStore(db *GatedDB) *MasterListStore {
	return &MasterListStore{db: db, now: time.Now}
}

// NewMasterListStoreFromStore builds the store from any store that resolves to
// a *PebbleStore through the decorator chain, or returns nil.
func NewMasterListStoreFromStore(store any) *MasterListStore {
	ps := AsPebbleStore(store)
	if ps == nil {
		return nil
	}
	return NewMasterListStore(ps.DB())
}

func masterListRowKey(listID string, n int) []byte {
	return []byte(fmt.Sprintf("%s%s:%0*d", masterListRowPrefix, listID, masterListRowDigits, n))
}

func masterListRowsPrefix(listID string) []byte {
	return []byte(masterListRowPrefix + listID + ":")
}

// PutList stores a new list: rows (one JSON document each, in input order) and
// then its metadata, which gets a fresh id, the upload time and the row count.
// It returns the stored metadata.
func (s *MasterListStore) PutList(meta MasterListMeta, rows [][]byte) (MasterListMeta, error) {
	if len(rows) == 0 {
		return MasterListMeta{}, errors.New("master list: no rows")
	}
	meta.ID = ulid.Make().String()
	meta.UploadedAt = s.now().UTC()
	meta.RowCount = len(rows)
	cleanup := func() {
		p := masterListRowsPrefix(meta.ID)
		_ = s.db.DeleteRange(p, prefixUpperBound(p), pebble.Sync)
	}
	for start := 0; start < len(rows); start += masterListChunk {
		end := min(start+masterListChunk, len(rows))
		batch := s.db.NewBatch()
		for i := start; i < end; i++ {
			if err := batch.Set(masterListRowKey(meta.ID, i+1), rows[i], nil); err != nil {
				_ = batch.Close()
				cleanup()
				return MasterListMeta{}, fmt.Errorf("master list: stage row %d: %w", i+1, err)
			}
		}
		err := batch.Commit(pebble.NoSync)
		_ = batch.Close()
		if err != nil {
			cleanup()
			return MasterListMeta{}, fmt.Errorf("master list: write rows: %w", err)
		}
	}
	data, err := json.Marshal(meta)
	if err != nil {
		cleanup()
		return MasterListMeta{}, fmt.Errorf("master list: encode metadata: %w", err)
	}
	// The one synced write: it flushes the unsynced row batches before it in
	// the log, and publishes the list.
	if err := s.db.Set([]byte(masterListPrefix+meta.ID), data, pebble.Sync); err != nil {
		cleanup()
		return MasterListMeta{}, fmt.Errorf("master list: write metadata: %w", err)
	}
	return meta, nil
}

// GetList returns one list's metadata, or ErrMasterListNotFound.
func (s *MasterListStore) GetList(id string) (*MasterListMeta, error) {
	val, closer, err := s.db.Get([]byte(masterListPrefix + id))
	if errors.Is(err, pebble.ErrNotFound) {
		return nil, ErrMasterListNotFound
	}
	if err != nil {
		return nil, err
	}
	defer closer.Close()
	var m MasterListMeta
	if err := json.Unmarshal(val, &m); err != nil {
		return nil, fmt.Errorf("master list %s: decode: %w", id, err)
	}
	return &m, nil
}

// ListLists returns every stored list's metadata, newest first.
func (s *MasterListStore) ListLists() ([]MasterListMeta, error) {
	p := []byte(masterListPrefix)
	iter, err := s.db.NewIter(&pebble.IterOptions{LowerBound: p, UpperBound: prefixUpperBound(p)})
	if err != nil {
		return nil, err
	}
	defer iter.Close()
	out := []MasterListMeta{}
	for iter.First(); iter.Valid(); iter.Next() {
		var m MasterListMeta
		if err := json.Unmarshal(iter.Value(), &m); err != nil {
			return nil, fmt.Errorf("master list %s: decode: %w", bytes.TrimPrefix(iter.Key(), p), err)
		}
		out = append(out, m)
	}
	if err := iter.Error(); err != nil {
		return nil, err
	}
	sort.Slice(out, func(i, j int) bool {
		if !out[i].UploadedAt.Equal(out[j].UploadedAt) {
			return out[i].UploadedAt.After(out[j].UploadedAt)
		}
		return out[i].ID > out[j].ID
	})
	return out, nil
}

// DeleteList removes a list and its rows. The metadata key goes first, in its
// own synced write: from then on nothing can name the list, and a crash before
// the row range is deleted leaves unreachable rows rather than a visible list
// whose ScanRows fails on the missing rows. Rows that outlive such a crash are
// unreachable (list ids are never reused), which is the cost of the ordering.
// It returns ErrMasterListNotFound for an unknown id.
//
// Whether the list exists is decided by its key, not by decoding it: a list
// whose metadata no longer decodes is exactly the one an admin needs to be able
// to remove, and GetList would answer with the decode error instead.
func (s *MasterListStore) DeleteList(id string) error {
	_, closer, err := s.db.Get([]byte(masterListPrefix + id))
	if errors.Is(err, pebble.ErrNotFound) {
		return ErrMasterListNotFound
	}
	if err != nil {
		return fmt.Errorf("master list %s: read metadata: %w", id, err)
	}
	_ = closer.Close()
	if err := s.db.Delete([]byte(masterListPrefix+id), pebble.Sync); err != nil {
		return fmt.Errorf("master list %s: delete metadata: %w", id, err)
	}
	p := masterListRowsPrefix(id)
	if err := s.db.DeleteRange(p, prefixUpperBound(p), pebble.Sync); err != nil {
		return fmt.Errorf("master list %s: delete rows (the list itself is gone): %w", id, err)
	}
	return nil
}

// DefaultMasterListRetention is how many lists one uploader keeps when the
// setting is unset or unusable.
const DefaultMasterListRetention = 10

// PruneLists deletes the uploader's lists beyond the newest keep (the list
// just uploaded counts as one of them, being the newest), oldest first, and
// returns the ids it deleted. Lists of other uploaders are never touched; a
// list with no recorded uploader (an upload by a key that names no user) is
// its own group. keep below 1 is refused: a bad setting must never be able to
// prune the list that was just stored.
//
// It stops at the first list it cannot delete and returns that error with the
// ids already deleted, so the caller can say how far it got.
func (s *MasterListStore) PruneLists(uploadedBy string, keep int) ([]string, error) {
	if keep < 1 {
		return nil, fmt.Errorf("master list retention must keep at least 1 list per uploader, got %d", keep)
	}
	all, err := s.ListLists() // newest first
	if err != nil {
		return nil, err
	}
	var mine []MasterListMeta
	for _, m := range all {
		if m.UploadedBy == uploadedBy {
			mine = append(mine, m)
		}
	}
	if len(mine) <= keep {
		return nil, nil
	}
	var deleted []string
	// Oldest first: a failure part-way leaves the newest of the excess lists.
	for i := len(mine) - 1; i >= keep; i-- {
		if err := s.DeleteList(mine[i].ID); err != nil {
			if errors.Is(err, ErrMasterListNotFound) {
				continue // deleted by someone else since the listing
			}
			return deleted, err
		}
		deleted = append(deleted, mine[i].ID)
	}
	return deleted, nil
}

// ScanRows calls fn with every row of the list in input order: n is the row's
// 1-based number in the list, raw its stored JSON (valid only during the call).
// fn's error stops the scan and is returned. It returns ErrMasterListNotFound
// for an unknown list, and an error when the rows found are not the
// RowCount the list was stored with.
func (s *MasterListStore) ScanRows(id string, fn func(n int, raw []byte) error) error {
	meta, err := s.GetList(id)
	if err != nil {
		return err
	}
	p := masterListRowsPrefix(id)
	iter, err := s.db.NewIter(&pebble.IterOptions{LowerBound: p, UpperBound: prefixUpperBound(p)})
	if err != nil {
		return err
	}
	defer iter.Close()
	n := 0
	for iter.First(); iter.Valid(); iter.Next() {
		n++
		if err := fn(n, iter.Value()); err != nil {
			return err
		}
	}
	if err := iter.Error(); err != nil {
		return err
	}
	if n != meta.RowCount {
		return fmt.Errorf("master list %s: stored %d rows but the list says %d", id, n, meta.RowCount)
	}
	return nil
}
