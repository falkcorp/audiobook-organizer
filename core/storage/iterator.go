// file: core/storage/iterator.go
// version: 1.0.0
// guid: c2ad05d0-dbee-4feb-9f79-5b43cf95dcd3
// last-edited: 2026-11-07

package storage

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"iter"
	"slices"
	"sort"
	"time"

	cpebble "github.com/cockroachdb/pebble/v2"
)

// Iterator is a cursor over one handle: a keyspace's keys (relative) or a
// legacy range's keys (absolute, clamped to the owner's claims). It is not
// safe for concurrent use. It holds an engine lease (or its snapshot's)
// until Close.
type Iterator struct {
	ctx      context.Context
	it       *cpebble.Iterator
	release  func()
	prefix   []byte // keyspace prefix, stripped from keys; nil for a legacy range
	spans    []span // the clamping ranges inside the bounds (legacy only)
	keysOnly bool
	err      error
	closed   bool
}

// newIterator opens an iterator over the handle. r is the read source (a
// snapshot); nil means the migration batch or the engine.
func (s handleOf) newIterator(ctx context.Context, opts ScanOptions, r reader) (*Iterator, error) {
	if err := s.check(); err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	lower, upper, spans, err := s.bounds(opts)
	if err != nil {
		return nil, err
	}
	release := func() {}
	if r == nil {
		rel, err := s.db.lease()
		if err != nil {
			return nil, err
		}
		release = rel
		r = s.db.eng.DB()
		if s.tx != nil {
			// A migration reads through its indexed batch, created on first use.
			if err := s.tx.b.ensure(); err != nil {
				release()
				return nil, err
			}
			r = s.tx.b.pb
		}
	}
	it := &Iterator{ctx: ctx, release: release, keysOnly: opts.KeysOnly}
	if s.legacy {
		it.spans = spans
	} else {
		it.prefix = s.prefix
	}
	if len(spans) == 0 || bytes.Compare(lower, upper) >= 0 {
		// Nothing in range: an iterator that is never valid.
		lower, upper = []byte{0}, []byte{0}
	}
	pit, err := r.NewIter(&cpebble.IterOptions{LowerBound: lower, UpperBound: upper})
	if err != nil {
		release()
		return nil, fmt.Errorf("storage: iterator: %w", err)
	}
	it.it = pit
	return it, nil
}

// bounds computes the absolute bounds of a scan and the clamping spans that
// fall inside them.
func (s handleOf) bounds(opts ScanOptions) (lower, upper []byte, spans []span, err error) {
	owned := s.ownedSpans()
	if len(owned) == 0 {
		return nil, nil, nil, nil
	}
	if s.legacy {
		lower, upper = owned[0].start, owned[len(owned)-1].end
		if opts.Start != nil && bytes.Compare(opts.Start, lower) > 0 {
			lower = opts.Start
		}
		if opts.End != nil && bytes.Compare(opts.End, upper) < 0 {
			upper = opts.End
		}
	} else {
		lower, upper = owned[0].start, owned[0].end
		if opts.Start != nil {
			lower = append(slices.Clone(s.prefix), opts.Start...)
		}
		if opts.End != nil {
			upper = append(slices.Clone(s.prefix), opts.End...)
		}
	}
	for _, sp := range owned {
		if sp.overlaps(lower, upper) {
			spans = append(spans, sp)
		}
	}
	return slices.Clone(lower), slices.Clone(upper), spans, nil
}

// settle moves a positioned iterator off keys outside the clamping spans:
// forward to the next span's start, backward below the previous span's end.
func (it *Iterator) settle(forward bool) bool {
	for it.it.Valid() {
		if it.ctx != nil {
			if err := it.ctx.Err(); err != nil {
				it.err = err
				return false
			}
		}
		if it.prefix != nil {
			return true // a keyspace's bounds are its one span
		}
		k := it.it.Key()
		i := sort.Search(len(it.spans), func(i int) bool { return bytes.Compare(it.spans[i].end, k) > 0 })
		if i < len(it.spans) && it.spans[i].contains(k) {
			return true
		}
		if forward {
			if i >= len(it.spans) {
				return it.invalidate()
			}
			it.it.SeekGE(it.spans[i].start)
			continue
		}
		// Backward: the last span that ends at or before k.
		j := i - 1
		if j < 0 {
			return it.invalidate()
		}
		it.it.SeekLT(it.spans[j].end)
	}
	return false
}

// invalidate leaves the iterator unpositioned.
func (it *Iterator) invalidate() bool {
	it.it.SeekLT([]byte{0})
	return false
}

func (it *Iterator) usable() bool { return it != nil && !it.closed && it.it != nil }

func (it *Iterator) First() bool { return it.usable() && it.pos(it.it.First(), true) }
func (it *Iterator) Last() bool  { return it.usable() && it.pos(it.it.Last(), false) }

// SeekGE positions at the first key >= key (relative for a keyspace).
func (it *Iterator) SeekGE(key []byte) bool {
	return it.usable() && it.pos(it.it.SeekGE(it.abs(key)), true)
}

// SeekLT positions at the last key < key (relative for a keyspace).
func (it *Iterator) SeekLT(key []byte) bool {
	return it.usable() && it.pos(it.it.SeekLT(it.abs(key)), false)
}

func (it *Iterator) Next() bool { return it.usable() && it.step(true) }
func (it *Iterator) Prev() bool { return it.usable() && it.step(false) }

func (it *Iterator) step(forward bool) bool {
	if it.err != nil {
		return false
	}
	if it.ctx != nil {
		if err := it.ctx.Err(); err != nil {
			it.err = err
			return false
		}
	}
	if forward {
		return it.pos(it.it.Next(), true)
	}
	return it.pos(it.it.Prev(), false)
}

func (it *Iterator) pos(valid, forward bool) bool {
	if !valid {
		return false
	}
	return it.settle(forward)
}

// abs maps a seek key to its absolute form.
func (it *Iterator) abs(key []byte) []byte {
	if it.prefix == nil {
		return key
	}
	return append(slices.Clone(it.prefix), key...)
}

func (it *Iterator) Valid() bool { return it.usable() && it.err == nil && it.it.Valid() }

// Key is the current key, relative for a keyspace; valid until the next step.
func (it *Iterator) Key() []byte {
	if !it.Valid() {
		return nil
	}
	return it.it.Key()[len(it.prefix):]
}

// Value is the current value, valid until the next step; nil for a KeysOnly
// iterator.
func (it *Iterator) Value() []byte {
	if !it.Valid() || it.keysOnly {
		return nil
	}
	v, err := it.it.ValueAndErr()
	if err != nil {
		it.err = fmt.Errorf("storage: value: %w", err)
		return nil
	}
	return v
}

func (it *Iterator) Err() error {
	if it == nil || it.it == nil {
		return nil
	}
	if it.err != nil {
		return it.err
	}
	if err := it.it.Error(); err != nil {
		return fmt.Errorf("storage: iterator: %w", err)
	}
	return nil
}

// Close releases the iterator and its lease; idempotent.
func (it *Iterator) Close() error {
	if it == nil || it.closed {
		return nil
	}
	it.closed = true
	var err error
	if it.it != nil {
		err = it.it.Close()
	}
	if it.release != nil {
		it.release()
	}
	if err != nil {
		return fmt.Errorf("storage: iterator close: %w", err)
	}
	return nil
}

// Snapshot pins a Pebble version. Snapshots are for bounded reads (a page, a
// verification pass); long passes must refresh (§5S.6).
type Snapshot struct {
	scope   handleOf
	snap    *cpebble.Snapshot
	release func()
	created time.Time
	closed  bool
}

func newSnapshot(s handleOf) (*Snapshot, error) {
	if err := s.check(); err != nil {
		return nil, err
	}
	release, err := s.db.lease()
	if err != nil {
		return nil, err
	}
	return &Snapshot{scope: s, snap: s.db.eng.DB().NewSnapshot(), release: release, created: s.db.clock()}, nil
}

// errSnapshotClosed: a read on a closed snapshot.
var errSnapshotClosed = errors.New("storage: snapshot closed")

func (s *Snapshot) Get(ctx context.Context, key []byte) ([]byte, error) {
	if s == nil || s.closed {
		return nil, errSnapshotClosed
	}
	if err := s.scope.check(); err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	abs, _, err := s.scope.absolute(key, opRead)
	if err != nil {
		return nil, err
	}
	v, ok, err := getFrom(s.snap, abs)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, ErrNotFound
	}
	return v, nil
}

func (s *Snapshot) Scan(ctx context.Context, opts ScanOptions) iter.Seq2[KV, error] {
	return func(yield func(KV, error) bool) {
		it, err := s.NewIterator(ctx, opts)
		if err != nil {
			yield(KV{}, err)
			return
		}
		defer func() { _ = it.Close() }()
		ok := it.First()
		if opts.Reverse {
			ok = it.Last()
		}
		for n := 0; ok; n++ {
			if opts.Limit > 0 && n >= opts.Limit {
				return
			}
			if !yield(KV{Key: it.Key(), Value: it.Value()}, nil) {
				return
			}
			if opts.Reverse {
				ok = it.Prev()
			} else {
				ok = it.Next()
			}
		}
		if err := it.Err(); err != nil {
			yield(KV{}, err)
		}
	}
}

func (s *Snapshot) NewIterator(ctx context.Context, opts ScanOptions) (*Iterator, error) {
	if s == nil || s.closed {
		return nil, errSnapshotClosed
	}
	return s.scope.newIterator(ctx, opts, s.snap)
}

// Age is how long the snapshot has been open.
func (s *Snapshot) Age() time.Duration { return s.scope.db.clock().Sub(s.created) }

// Close releases the snapshot and its lease; idempotent.
func (s *Snapshot) Close() error {
	if s == nil || s.closed {
		return nil
	}
	s.closed = true
	err := s.snap.Close()
	s.release()
	if err != nil {
		return fmt.Errorf("storage: snapshot close: %w", err)
	}
	return nil
}
