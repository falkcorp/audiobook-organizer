// file: internal/database/master_list_elsewhere.go
// version: 1.0.0
// guid: 5b7e2c94-0d3a-4e61-9a8f-c1d6e3f47b20
// last-edited: 2026-10-23

package database

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"sort"
	"sync"
	"time"

	"github.com/cockroachdb/pebble/v2"
)

// "Read elsewhere" records (owner decision 2026-10-23, option A). A row of an
// imported master list that matches no book in the library is still a book the
// owner has read or owns; it is kept as one small record, never as a stub book.
// Nothing here is a book table: no scanner, dedup, ABS, search or stats path
// reads these keys, and a record names no book row.
//
// Keyspace (a new prefix; dropping it removes the feature):
//
//	mlist:elsewhere:<id>   one record's JSON (MasterListElsewhere)
//
// The id is derived from the user and the plan row's key (ElsewhereID), so the
// same list row imported twice, or from a newer list, is one record: creating
// it again changes nothing. A record is created only by the master-list-import
// fixer's apply, which journals the creation (undo.ChangeTypeMasterListElsewhereCreate)
// so the apply's revert deletes it again while it is still what the apply made.
const masterListElsewherePrefix = "mlist:elsewhere:"

// ErrElsewhereExists is returned by CreateElsewhere when a record with the id
// is already stored; nothing is written.
var ErrElsewhereExists = errors.New("read-elsewhere record already exists")

// ErrElsewhereNotFound is returned for an id that is not stored.
var ErrElsewhereNotFound = errors.New("read-elsewhere record not found")

// MasterListElsewhereSource is one provenance entry of a record: where the
// list row says the book came from (a source and its id, such as an iTunes
// persistent id).
type MasterListElsewhereSource struct {
	Source   string `json:"source"`
	SourceID string `json:"source_id,omitempty"`
}

// MasterListElsewhere is one book the owner has read or owns that the library
// does not hold.
type MasterListElsewhere struct {
	ID string `json:"id"`
	// UserID is the user whose reading it is (the import's target user).
	UserID string `json:"user_id"`
	// ListID is the uploaded list the row came from, and Lines the input lines
	// it stood for. UploadedBy is that list's uploader, which is who put the
	// book on record, not necessarily the user it is for.
	ListID     string `json:"list_id"`
	UploadedBy string `json:"uploaded_by,omitempty"`
	Lines      []int  `json:"lines,omitempty"`

	Title   string                      `json:"title,omitempty"`
	Authors []string                    `json:"authors,omitempty"`
	ISBN10  string                      `json:"isbn10,omitempty"`
	ISBN13  string                      `json:"isbn13,omitempty"`
	ASIN    string                      `json:"asin,omitempty"`
	Sources []MasterListElsewhereSource `json:"sources,omitempty"`

	// Status and FinishedAt are what the list says about the reading (a status
	// word, and an RFC 3339 time with a zone), verbatim.
	Status     string `json:"status,omitempty"`
	FinishedAt string `json:"finished_at,omitempty"`
	// Issues are the codes of the findings the list row carried
	// (bad_timestamp, identifier_conflict, ...): the data is kept, flagged.
	Issues    []string  `json:"issues,omitempty"`
	CreatedAt time.Time `json:"created_at"`
}

// ElsewhereID is the record id for a user and a plan row key: a hash, so the
// key is bounded and holds no title or name.
func ElsewhereID(userID, rowKey string) string {
	sum := sha256.Sum256([]byte(userID + "\x00" + rowKey))
	return hex.EncodeToString(sum[:])[:32]
}

// SameElsewhere reports whether two records are the same record: every field
// but the creation time, which a retry stamps afresh.
func SameElsewhere(a, b MasterListElsewhere) bool {
	a.CreatedAt, b.CreatedAt = time.Time{}, time.Time{}
	return reflect.DeepEqual(a, b)
}

// elsewhereMu makes create-if-absent atomic: the store has no compare-and-set,
// and two applies of overlapping plans must not both create one record.
var elsewhereMu sync.Mutex

func elsewhereKey(id string) []byte { return []byte(masterListElsewherePrefix + id) }

// CreateElsewhere stores rec (its ID and UserID are required; CreatedAt is set
// to now when zero) unless a record with that id exists, in which case it
// returns ErrElsewhereExists and writes nothing.
func (s *MasterListStore) CreateElsewhere(rec MasterListElsewhere) error {
	if rec.ID == "" || rec.UserID == "" || rec.ListID == "" {
		return errors.New("read-elsewhere record: id, user and list are required")
	}
	if rec.CreatedAt.IsZero() {
		rec.CreatedAt = s.now()
	}
	rec.CreatedAt = rec.CreatedAt.UTC()
	data, err := json.Marshal(rec)
	if err != nil {
		return fmt.Errorf("read-elsewhere record %s: encode: %w", rec.ID, err)
	}
	elsewhereMu.Lock()
	defer elsewhereMu.Unlock()
	if _, closer, err := s.db.Get(elsewhereKey(rec.ID)); err == nil {
		_ = closer.Close()
		return ErrElsewhereExists
	} else if !errors.Is(err, pebble.ErrNotFound) {
		return fmt.Errorf("read-elsewhere record %s: %w", rec.ID, err)
	}
	if err := s.db.Set(elsewhereKey(rec.ID), data, pebble.Sync); err != nil {
		return fmt.Errorf("read-elsewhere record %s: write: %w", rec.ID, err)
	}
	return nil
}

// GetElsewhere returns one record, or ErrElsewhereNotFound.
func (s *MasterListStore) GetElsewhere(id string) (*MasterListElsewhere, error) {
	val, closer, err := s.db.Get(elsewhereKey(id))
	if errors.Is(err, pebble.ErrNotFound) {
		return nil, ErrElsewhereNotFound
	}
	if err != nil {
		return nil, err
	}
	defer closer.Close()
	var rec MasterListElsewhere
	if err := json.Unmarshal(val, &rec); err != nil {
		return nil, fmt.Errorf("read-elsewhere record %s: decode: %w", id, err)
	}
	return &rec, nil
}

// DeleteElsewhere removes a record: the undo of CreateElsewhere. It returns
// ErrElsewhereNotFound when there is none.
func (s *MasterListStore) DeleteElsewhere(id string) error {
	elsewhereMu.Lock()
	defer elsewhereMu.Unlock()
	if _, err := s.GetElsewhere(id); err != nil {
		return err
	}
	if err := s.db.Delete(elsewhereKey(id), pebble.Sync); err != nil {
		return fmt.Errorf("read-elsewhere record %s: delete: %w", id, err)
	}
	return nil
}

// ListElsewhere returns the records, newest first; userID "" lists every
// user's.
func (s *MasterListStore) ListElsewhere(userID string) ([]MasterListElsewhere, error) {
	p := []byte(masterListElsewherePrefix)
	iter, err := s.db.NewIter(&pebble.IterOptions{LowerBound: p, UpperBound: prefixUpperBound(p)})
	if err != nil {
		return nil, err
	}
	defer iter.Close()
	out := []MasterListElsewhere{}
	for iter.First(); iter.Valid(); iter.Next() {
		var rec MasterListElsewhere
		if err := json.Unmarshal(iter.Value(), &rec); err != nil {
			return nil, fmt.Errorf("read-elsewhere record %s: decode: %w", bytes.TrimPrefix(iter.Key(), p), err)
		}
		if userID == "" || rec.UserID == userID {
			out = append(out, rec)
		}
	}
	if err := iter.Error(); err != nil {
		return nil, err
	}
	sort.Slice(out, func(i, j int) bool {
		if !out[i].CreatedAt.Equal(out[j].CreatedAt) {
			return out[i].CreatedAt.After(out[j].CreatedAt)
		}
		return out[i].ID > out[j].ID
	})
	return out, nil
}
