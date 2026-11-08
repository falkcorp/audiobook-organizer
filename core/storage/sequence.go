// file: core/storage/sequence.go
// version: 1.0.0
// guid: b5e7e777-b040-4058-924d-a4c858a083cc
// last-edited: 2026-11-07

package storage

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"slices"
	"sync"
	"sync/atomic"
)

// Sequence hands out unique, increasing uint64 IDs from leases of
// core.storage.sequence.lease IDs (default 64). A refill is its OWN Sync
// commit of hw = max(hw, start+lease), made under the sequence's mutex BEFORE
// any ID of the new lease is returned. A crash can therefore only skip IDs,
// never reissue one; the cost is one fsync per lease (ABO's nextID pays one
// per ID, pebble_store.go:966-990). IDs increase in allocation order, not in
// commit order.
//
// The stored high-water mark is the first ID no lease has handed out; IDs
// start at 1. Next is lock-free inside a lease (an atomic counter on the
// current lease); a refill takes the mutex.
type Sequence struct {
	ks    Keyspace
	key   []byte // relative
	lease uint64
	err   error // an invalid sequence: every call returns it

	mu  sync.Mutex // serializes refills and AdvancePast
	cur atomic.Pointer[seqLease]
}

// seqLease is one durable lease [next, limit). The zero lease is empty.
// limit is atomic because AdvancePast zeroes a lease that a Next may already
// have loaded: Next checks it after taking an ID, so a stale lease yields
// nothing.
type seqLease struct {
	next  atomic.Uint64
	limit atomic.Uint64
}

func newSequence(ks Keyspace, key []byte, lease uint64) *Sequence {
	s := &Sequence{ks: ks, key: key, lease: max(lease, 1)}
	s.cur.Store(&seqLease{})
	return s
}

// Next returns the next ID.
func (s *Sequence) Next(ctx context.Context) (uint64, error) {
	if s.err != nil {
		return 0, s.err
	}
	for {
		if err := ctx.Err(); err != nil {
			return 0, err
		}
		l := s.cur.Load()
		if h := s.ks.db.hooks.seqLoaded; h != nil {
			h()
		}
		if n := l.next.Add(1) - 1; n < l.limit.Load() {
			return n, nil
		}
		if err := s.refill(ctx, l); err != nil {
			return 0, err
		}
	}
}

// NextIn returns an ID from a durable lease for use in b's keys or values.
// It writes nothing into b; if a refill is needed it commits on its own first.
func (s *Sequence) NextIn(ctx context.Context, b *Batch) (uint64, error) {
	if b == nil {
		return 0, errors.New("storage: Sequence.NextIn with a nil batch")
	}
	return s.Next(ctx)
}

// AdvancePast makes every later ID greater than n: under the sequence's mutex
// it commits hw = max(hw, n+1) on its own (Sync) and drops the in-memory lease.
// Used after a restore, before records carrying IDs from another copy of the
// store are written back (05-rbac audit copy-forward; DATA2-16, SEC2-12). Like
// a refill, a crash after it can only skip IDs.
func (s *Sequence) AdvancePast(ctx context.Context, n uint64) error {
	if s.err != nil {
		return s.err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, err := s.commitHW(ctx, func(hw uint64) uint64 { return max(hw, n+1) }); err != nil {
		return err
	}
	// The dropped lease's unissued IDs are skipped, never reissued: the next
	// refill starts at the stored mark, which is past both n and that lease.
	// Its limit is zeroed first, so a Next that loaded it before the swap
	// and takes its ID after finds it spent and refills (review NIT-2).
	s.cur.Load().limit.Store(0)
	s.cur.Store(&seqLease{})
	return nil
}

// refill takes a new durable lease when l is still the current one.
func (s *Sequence) refill(ctx context.Context, l *seqLease) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cur.Load() != l {
		return nil // another caller refilled
	}
	var start uint64
	hw, err := s.commitHW(ctx, func(hw uint64) uint64 {
		start = max(hw, l.limit.Load(), 1)
		return start + s.lease
	})
	if err != nil {
		return err
	}
	nl := &seqLease{}
	nl.limit.Store(hw)
	nl.next.Store(start)
	s.cur.Store(nl)
	return nil
}

// commitHW reads the stored high-water mark, computes the new one and commits
// it with Sync, conditionally (GetForUpdate) when the key lies in a
// Serialized range, retrying on a conflict.
func (s *Sequence) commitHW(ctx context.Context, next func(hw uint64) uint64) (uint64, error) {
	if s.ks.ro {
		return 0, ErrReadOnly
	}
	var out uint64
	err := RetryOnConflict(ctx, 8, func(ctx context.Context) (err error) {
		b := s.ks.NewBatch()
		defer func() { err = errors.Join(err, b.Close()) }()
		abs := append(slices.Clone(s.ks.prefix), s.key...)
		info := s.ks.db.owners[s.ks.owner]
		var raw []byte
		if info != nil && info.isSerialized(abs) {
			raw, err = b.GetForUpdate(ctx, s.ks, s.key)
		} else {
			raw, err = s.ks.Get(ctx, s.key)
		}
		var hw uint64
		switch {
		case errors.Is(err, ErrNotFound):
			hw = 1
		case err != nil:
			return err
		case len(raw) != 8:
			return fmt.Errorf("storage: sequence %q high-water mark is %d bytes, want 8", s.key, len(raw))
		default:
			hw = binary.BigEndian.Uint64(raw)
		}
		nhw := next(hw)
		if err := b.Set(s.key, binary.BigEndian.AppendUint64(nil, nhw)); err != nil {
			return err
		}
		if err := b.Commit(ctx, Sync); err != nil {
			return err
		}
		out = nhw
		return nil
	})
	return out, err
}
