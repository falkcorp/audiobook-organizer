// file: internal/dedup/split_book_storage.go
// version: 1.0.1
// guid: 2a4b6c8e-1f3d-5a7b-9c0e-2d4f6a8b0c1d
// last-edited: 2026-10-11

// Split-book candidate storage. The detector is purely analytical;
// these helpers persist its results so the operator-facing CLI and the
// HTTP list endpoint have something to read.
//
// DedupCandidate is pairwise (entity_a / entity_b) and doesn't fit an
// N-ary chapter cluster, so we use a dedicated Pebble keyspace instead
// of shoehorning into the existing table.
//
// Keys:
//
//	split_book_candidate:<ulid>            → JSON-encoded SplitBookCandidate
//
// The detector replaces the entire keyspace on each run (DeleteAll +
// Upsert) so the candidate list reflects only the latest scan.

package dedup

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/cockroachdb/pebble/v2"
	ulid "github.com/oklog/ulid/v2"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// splitBookKeyPrefix is the Pebble key prefix for stored candidates.
// ';' is the next byte after ':' in ASCII — used as the upper bound for
// prefix iteration in the same style as the rest of pebble_store.go.
const (
	splitBookKeyPrefix = "split_book_candidate:"
	splitBookKeyUpper  = "split_book_candidate;"
)

// SplitBookStore is implemented by anything that exposes a *database.GatedDB.
// EmbeddingStore satisfies this via its (currently unexported) db field;
// we wrap it with a thin helper rather than adding methods directly to
// avoid bloating EmbeddingStore's API surface.
type SplitBookStore struct {
	db *database.GatedDB
}

// NewSplitBookStore constructs a SplitBookStore backed by the given
// Pebble database. The DB is shared; Close is a no-op.
func NewSplitBookStore(db *database.GatedDB) *SplitBookStore {
	return &SplitBookStore{db: db}
}

// SaveAll replaces every existing split-book candidate row with the
// supplied list. Each candidate gets a fresh ULID and CreatedAt as its
// persisted ID is otherwise meaningless to the detector.
func (s *SplitBookStore) SaveAll(cands []SplitBookCandidate) error {
	if s == nil || s.db == nil {
		return fmt.Errorf("SplitBookStore: nil db")
	}
	// Clear existing rows first.
	if err := s.deleteAll(); err != nil {
		return fmt.Errorf("clear existing: %w", err)
	}
	now := time.Now()
	batch := s.db.NewBatch()
	defer batch.Close()
	for i := range cands {
		c := cands[i]
		if c.ID == "" {
			c.ID = ulid.Make().String()
		}
		type wrapper struct {
			SplitBookCandidate
			CreatedAt time.Time `json:"created_at"`
		}
		data, err := json.Marshal(wrapper{SplitBookCandidate: c, CreatedAt: now})
		if err != nil {
			return fmt.Errorf("marshal candidate %d: %w", i, err)
		}
		if err := batch.Set([]byte(splitBookKeyPrefix+c.ID), data, nil); err != nil {
			return fmt.Errorf("batch set %s: %w", c.ID, err)
		}
	}
	return batch.Commit(pebble.Sync)
}

// List returns every stored split-book candidate. Results are sorted by
// the underlying key (ULID lexicographic = chronological).
func (s *SplitBookStore) List() ([]SplitBookCandidate, error) {
	if s == nil || s.db == nil {
		return nil, fmt.Errorf("SplitBookStore: nil db")
	}
	iter, err := s.db.NewIter(&pebble.IterOptions{
		LowerBound: []byte(splitBookKeyPrefix),
		UpperBound: []byte(splitBookKeyUpper),
	})
	if err != nil {
		return nil, fmt.Errorf("new iter: %w", err)
	}
	defer iter.Close()
	var out []SplitBookCandidate
	for iter.First(); iter.Valid(); iter.Next() {
		var w struct {
			SplitBookCandidate
			CreatedAt time.Time `json:"created_at"`
		}
		if err := json.Unmarshal(iter.Value(), &w); err != nil {
			// Skip corrupt rows but continue — operator-facing CLI
			// shouldn't fail wholesale because of one bad record.
			continue
		}
		out = append(out, w.SplitBookCandidate)
	}
	return out, iter.Error()
}

// Get fetches a single candidate by ID, or (nil, nil) if not found.
func (s *SplitBookStore) Get(id string) (*SplitBookCandidate, error) {
	if s == nil || s.db == nil {
		return nil, fmt.Errorf("SplitBookStore: nil db")
	}
	key := []byte(splitBookKeyPrefix + id)
	val, closer, err := s.db.Get(key)
	if err == pebble.ErrNotFound {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer closer.Close()
	var w struct {
		SplitBookCandidate
		CreatedAt time.Time `json:"created_at"`
	}
	if err := json.Unmarshal(val, &w); err != nil {
		return nil, err
	}
	return &w.SplitBookCandidate, nil
}

// Delete removes a candidate by ID. Returns nil if not present.
func (s *SplitBookStore) Delete(id string) error {
	if s == nil || s.db == nil {
		return fmt.Errorf("SplitBookStore: nil db")
	}
	return s.db.Delete([]byte(splitBookKeyPrefix+id), pebble.Sync)
}

// deleteAll wipes every candidate row.
func (s *SplitBookStore) deleteAll() error {
	return s.db.DeleteRange(
		[]byte(splitBookKeyPrefix),
		[]byte(splitBookKeyUpper),
		pebble.Sync,
	)
}

// PebbleDB exposes the underlying Pebble handle for callers (e.g.
// EmbeddingStore) that want to construct a SplitBookStore from a
// pre-existing shared DB. Returns nil if the receiver is nil.
func (s *SplitBookStore) PebbleDB() *database.GatedDB {
	if s == nil {
		return nil
	}
	return s.db
}
