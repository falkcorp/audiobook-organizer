// file: core/internal/taskstore/store.go
// version: 1.1.0
// guid: fde9d96f-27b6-454c-8ddb-e85d358661e7
// last-edited: 2026-11-10

package taskstore

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"iter"
	"slices"
	"time"

	"github.com/falkcorp/mediacore/storage"
	"github.com/falkcorp/mediacore/storage/keys"
)

// ErrNilBatch: a staging call was handed no batch.
var ErrNilBatch = errors.New("taskstore: nil batch")

// ErrNotIndexed: a read-modify-write was handed a batch that is not indexed.
// Such a batch reads only the committed state, so a second read of a key the
// batch already wrote would miss that write and the commit would lose it.
var ErrNotIndexed = errors.New("taskstore: batch is not indexed (a read-modify-write needs Keyspace.NewIndexedBatch)")

// Store is the raw staging layer over one c/tasks/ keyspace. It holds the
// keyspace and the strike sequence and nothing mutable of its own, so it is
// safe for concurrent use: every write is staged into the caller's batch,
// and every read goes to storage.
type Store struct {
	ks      storage.Keyspace
	strikes *storage.Sequence
}

// New is the staging layer over ks (the c/tasks/ keyspace).
func New(ks storage.Keyspace) *Store {
	return &Store{ks: ks, strikes: ks.Sequence("strike")}
}

// Keyspace is the keyspace the store writes.
func (s *Store) Keyspace() storage.Keyspace { return s.ks }

// NewBatch is an indexed batch whose origin is the store's keyspace: the
// batch a build that owns no other handle (the executor's own writes, a
// test) stages into, so that a later read of the same build sees an earlier
// write of it.
func (s *Store) NewBatch() *storage.Batch { return s.ks.NewIndexedBatch() }

// Bind binds the store's keyspace to b (participant binding, 05-storage
// §5S.3.2); binding an already bound handle is a no-op. A Bind that storage
// refuses poisons b, so the next stage or the Commit returns its error.
func (s *Store) Bind(b *storage.Batch) error {
	if b == nil {
		return ErrNilBatch
	}
	b.Bind(s.ks)
	return nil
}

// Get reads one committed key; found is false when it is absent.
func (s *Store) Get(ctx context.Context, key []byte) (value []byte, found bool, err error) {
	v, err := s.ks.Get(ctx, key)
	if errors.Is(err, storage.ErrNotFound) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	return v, true, nil
}

// ReadForUpdate binds the keyspace to b, reads a guard key through b (its own
// writes first) and records the commit-time precondition that the key still
// holds the committed value read (absent when it was absent). found is false
// when the key is absent as b sees it. Only Serialized keys accept it, and
// only an indexed batch (ErrNotIndexed): every caller stages a write derived
// from the read, and a later read in the same batch must see that write.
func (s *Store) ReadForUpdate(ctx context.Context, b *storage.Batch, key []byte) (value []byte, found bool, err error) {
	if err := s.Bind(b); err != nil {
		return nil, false, err
	}
	if !b.Indexed() {
		return nil, false, ErrNotIndexed
	}
	v, err := b.GetForUpdate(ctx, s.ks, key)
	if errors.Is(err, storage.ErrNotFound) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	return v, true, nil
}

// ExpectValue records that a guard key must hold value at commit (nil: be
// absent), without reading it.
func (s *Store) ExpectValue(b *storage.Batch, key, value []byte) error {
	if err := s.Bind(b); err != nil {
		return err
	}
	return b.Expect(s.ks, key, value)
}

// Set stages key = value into b through the keyspace.
func (s *Store) Set(b *storage.Batch, key, value []byte) error {
	if err := s.Bind(b); err != nil {
		return err
	}
	return b.In(s.ks).Set(key, value)
}

// Delete stages the point delete of key into b.
func (s *Store) Delete(b *storage.Batch, key []byte) error {
	if err := s.Bind(b); err != nil {
		return err
	}
	return b.In(s.ks).Delete(key)
}

// DeletePrefix stages a DeleteRange over every key that starts with prefix.
// Storage refuses it over a Serialized (guard) family (ErrBlindWrite).
func (s *Store) DeletePrefix(b *storage.Batch, prefix []byte) error {
	if err := s.Bind(b); err != nil {
		return err
	}
	return b.In(s.ks).DeleteRange(prefix, keys.PrefixEnd(prefix))
}

// NextStrike allocates the next strike number for use in b (a durable,
// crash-safe sequence: no number is ever issued twice).
func (s *Store) NextStrike(ctx context.Context, b *storage.Batch) (uint64, error) {
	if err := s.Bind(b); err != nil {
		return 0, err
	}
	return s.strikes.NextIn(ctx, b)
}

// RunIndex is what the run's derived keys are computed from: the queue
// entry, the kind, state and done indexes.
type RunIndex struct {
	ID       string
	Kind     string
	State    string
	Queued   bool // the run has a dispatch queue entry
	Priority uint8
	Enqueued time.Time
	Terminal bool // the run has a done-index entry
	Finished time.Time
}

func (x RunIndex) queueKey() []byte { return QueueKey(x.Priority, x.Enqueued, x.ID) }
func (x RunIndex) doneKey() []byte  { return DoneIndexKey(x.Finished, x.ID) }

// PutRun stages the run row value at run/<next.ID> and moves its derived
// keys from prev (nil for a new run) to next: only the keys that differ are
// deleted and written. The caller has already recorded the guard
// precondition on run/<id> (ReadForUpdate or ExpectValue): storage refuses
// the commit otherwise (ErrBlindWrite).
func (s *Store) PutRun(b *storage.Batch, value []byte, prev *RunIndex, next RunIndex) error {
	if next.ID == "" {
		return errors.New("taskstore: run without an ID")
	}
	if prev != nil && prev.ID != next.ID {
		return fmt.Errorf("taskstore: run %q staged over the index of run %q", next.ID, prev.ID)
	}
	if err := s.Set(b, RunKey(next.ID), value); err != nil {
		return err
	}
	type derived struct {
		had, has   bool
		old, fresh []byte
	}
	var d []derived
	if prev == nil {
		d = []derived{
			{has: true, fresh: KindIndexKey(next.Kind, next.ID)},
			{has: true, fresh: StateIndexKey(next.State, next.ID)},
			{has: next.Queued, fresh: next.queueKey()},
			{has: next.Terminal, fresh: next.doneKey()},
		}
	} else {
		d = []derived{
			{had: true, has: true, old: KindIndexKey(prev.Kind, prev.ID), fresh: KindIndexKey(next.Kind, next.ID)},
			{had: true, has: true, old: StateIndexKey(prev.State, prev.ID), fresh: StateIndexKey(next.State, next.ID)},
			{had: prev.Queued, has: next.Queued, old: prev.queueKey(), fresh: next.queueKey()},
			{had: prev.Terminal, has: next.Terminal, old: prev.doneKey(), fresh: next.doneKey()},
		}
	}
	for _, k := range d {
		same := k.had && k.has && bytes.Equal(k.old, k.fresh)
		if k.had && !same {
			if err := s.Delete(b, k.old); err != nil {
				return err
			}
		}
		if k.has && !same {
			if err := s.Set(b, k.fresh, nil); err != nil {
				return err
			}
		}
	}
	return nil
}

// runFamilies are the per-run families a DeleteRange removes with a run.
func runFamilies(id string) [][]byte {
	return [][]byte{
		keys.Prefix(keys.Str(famPage), keys.Str(id)),
		CheckpointPrefix(id),
		ChunkPrefix(id),
		RowPrefix(id),
		MarkPrefix(id),
		StrikePrefix(id),
	}
}

// DeleteRun stages the removal of a run and every record it owns: the guard
// key by a point delete (the caller has recorded its precondition), the
// queue entry and index keys by point deletes, and the pages, checkpoints,
// chunks, rows, marks and strikes by DeleteRange, which storage allows
// outside the guard families (5T.6.1).
func (s *Store) DeleteRun(b *storage.Batch, x RunIndex) error {
	if x.ID == "" {
		return errors.New("taskstore: delete of a run without an ID")
	}
	point := [][]byte{RunKey(x.ID), KindIndexKey(x.Kind, x.ID), StateIndexKey(x.State, x.ID)}
	if x.Queued {
		point = append(point, x.queueKey())
	}
	if x.Terminal {
		point = append(point, x.doneKey())
	}
	for _, k := range point {
		if err := s.Delete(b, k); err != nil {
			return err
		}
	}
	for _, p := range runFamilies(x.ID) {
		if err := s.DeletePrefix(b, p); err != nil {
			return err
		}
	}
	return nil
}

// Scan yields the committed pairs whose keys start with prefix and sort
// after after (exclusive; nil = from the start of the prefix), in key order,
// at most limit of them (0 = all). Each pair is a copy the caller owns.
func (s *Store) Scan(ctx context.Context, prefix, after []byte, limit int) iter.Seq2[storage.KV, error] {
	return func(yield func(storage.KV, error) bool) {
		start := prefix
		if after != nil {
			start = append(slices.Clone(after), 0x00) // the smallest key above after
		}
		n := 0
		for kv, err := range s.ks.Scan(ctx, storage.ScanOptions{Start: start, End: keys.PrefixEnd(prefix)}) {
			if err != nil {
				yield(storage.KV{}, err)
				return
			}
			if !yield(kv.Clone(), nil) {
				return
			}
			n++
			if limit > 0 && n >= limit {
				return
			}
		}
	}
}

// IDs yields the last segment of every key under prefix (a run ID for the
// index and queue families), in key order.
func (s *Store) IDs(ctx context.Context, prefix []byte) iter.Seq2[string, error] {
	return func(yield func(string, error) bool) {
		for kv, err := range s.ks.Scan(ctx, storage.ScanOptions{Start: prefix, End: keys.PrefixEnd(prefix), KeysOnly: true}) {
			if err != nil {
				yield("", err)
				return
			}
			seg := lastSeg(kv.Key)
			if seg == nil {
				if !yield("", fmt.Errorf("taskstore: malformed key under %q", prefix)) {
					return
				}
				continue
			}
			if !yield(string(seg), nil) {
				return
			}
		}
	}
}

// DoneEntry is one done-index entry: its key and what the key names.
type DoneEntry struct {
	Key      []byte
	ID       string
	Finished time.Time
}

// DoneBefore returns up to limit done-index entries finished before before,
// oldest first, after the key after (exclusive; nil = from the oldest).
func (s *Store) DoneBefore(ctx context.Context, before time.Time, after []byte, limit int) ([]DoneEntry, error) {
	start := DoneIndexPrefix()
	if after != nil {
		start = append(slices.Clone(after), 0x00)
	}
	end := keys.Join(keys.Str(famIndex), keys.Str(ixDone), keys.Time(before))
	var out []DoneEntry
	for kv, err := range s.ks.Scan(ctx, storage.ScanOptions{Start: start, End: end, KeysOnly: true, Limit: limit}) {
		if err != nil {
			return nil, err
		}
		finished, id, err := ParseDoneIndexKey(kv.Key)
		if err != nil {
			return nil, err
		}
		out = append(out, DoneEntry{Key: slices.Clone(kv.Key), ID: id, Finished: finished})
	}
	return out, nil
}

// StateCounts counts the keys under each ix/state/<state>/ prefix: the
// census is derived from the state index, never from a counter row
// (5T.6.1, DATA7-2).
func (s *Store) StateCounts(ctx context.Context) (map[string]int64, error) {
	out := map[string]int64{}
	prefix := StateIndexPrefix("")
	for kv, err := range s.ks.Scan(ctx, storage.ScanOptions{Start: prefix, End: keys.PrefixEnd(prefix), KeysOnly: true}) {
		if err != nil {
			return nil, err
		}
		state, _, err := ParseStateIndexKey(kv.Key)
		if err != nil {
			return nil, err
		}
		out[state]++
	}
	return out, nil
}
