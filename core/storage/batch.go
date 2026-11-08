// file: core/storage/batch.go
// version: 1.0.0
// guid: 95428f24-dcaf-4b52-bcb8-b5a04189097c
// last-edited: 2026-11-07

package storage

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"slices"

	cpebble "github.com/cockroachdb/pebble/v2"

	"github.com/falkcorp/mediacore/storage/keys"
)

// noCopy makes go vet's copylocks check flag a Batch copied by value.
type noCopy struct{}

func (*noCopy) Lock()   {}
func (*noCopy) Unlock() {}

// Batch is atomic across every handle bound to it: one Pebble batch.
// A batch created from handle H can write H. Bind adds another handle; the
// caller must HOLD that handle — the handle value is the capability, so a
// participant can only touch what it bound. This is how a module writes its
// row and an outbox entry (c/outbox/) or an index-dirty mark in one commit,
// and how a core subsystem joins a module's batch (participant pattern below).
// Batch embeds a noCopy marker: it is only ever passed as *Batch (COR2-12).
//
// Storage holds no lock, lease or reservation for an open batch: its Pebble
// batch is created on the first write, stripes exist only inside Commit, and
// an engine lease is held only for the duration of a read or the Commit. A
// batch that is never closed costs only its own memory (§5S.3.2 item 3).
type Batch struct {
	noCopy noCopy //nolint:unused // the marker go vet's copylocks reads

	db      *DB
	indexed bool
	pb      *cpebble.Batch
	bound   []handleOf

	expects    map[string]expectation // absolute key -> committed value it must have
	serialKeys map[string]bool        // keyspace writes in a Serialized range: each needs an Expect
	stripeKeys map[string]bool        // keys whose stripes Commit takes
	allStripes bool                   // a DeleteRange over a Serialized range: every stripe
	writes     int

	err  error // the first staging error: Commit applies nothing
	done bool  // committed or closed

	// exclusive marks a boot migration step's batch: the DAG runs before any
	// service holds a handle, so its keyspace writes in a Serialized range
	// need no Expect (MigrationTx has none). Stripes are still taken.
	exclusive bool
}

// expectation is one precondition: the committed value must equal value, or
// the key must be absent.
type expectation struct {
	value  []byte
	absent bool
}

func newBatch(origin Handle, indexed bool) *Batch {
	s, _ := scopeOf(origin)
	b := &Batch{db: s.db, indexed: indexed}
	if s.db == nil {
		b.err = ErrInvalidKeyspace
		return b
	}
	if s.tx != nil {
		// A migration step's handle: this batch would commit outside the
		// step's one batch, so every stage and the Commit refuse.
		b.err = errTxKeyspaceWrite
		return b
	}
	b.bound = append(b.bound, s)
	return b
}

// poison records the first staging error; Commit then applies nothing.
func (b *Batch) poison(err error) {
	if b.err == nil {
		b.err = err
	}
}

// ensure creates the Pebble batch under a short lease.
func (b *Batch) ensure() error {
	if b.pb != nil {
		return nil
	}
	release, err := b.db.lease()
	if err != nil {
		return err
	}
	defer release()
	if b.indexed {
		b.pb = b.db.eng.DB().NewIndexedBatch()
	} else {
		b.pb = b.db.eng.DB().NewBatch()
	}
	return nil
}

// Bind adds a handle the caller holds to the batch.
func (b *Batch) Bind(h Handle) *Batch {
	s, ok := scopeOf(h)
	switch {
	case !ok:
		b.poison(fmt.Errorf("storage: Bind of a zero handle: %w", ErrInvalidKeyspace))
	case s.db != b.db:
		b.poison(fmt.Errorf("storage: Bind of a handle of another store: %w", ErrOutsideKeyspace))
	case s.tx != nil:
		b.poison(fmt.Errorf("storage: Bind of a migration step's handle: %w", errTxKeyspaceWrite))
	case b.boundScope(h) == nil:
		b.bound = append(b.bound, s)
	}
	return b
}

// boundScope returns the bound scope of h, or nil.
func (b *Batch) boundScope(h Handle) *handleOf {
	s, ok := scopeOf(h)
	if !ok {
		return nil
	}
	for i := range b.bound {
		if b.bound[i].same(s) {
			return &b.bound[i]
		}
	}
	return nil
}

// In is the writer of a bound handle; it panics if h is not bound.
func (b *Batch) In(h Handle) BatchWriter {
	if b.boundScope(h) == nil {
		panic("storage: Batch.In of a handle that is not bound to the batch")
	}
	return batchWriter{b: b, h: h}
}

// Set is shorthand for b.In(origin).Set.
func (b *Batch) Set(key, value []byte) error { return b.stage(b.origin(), opSet, key, value) }

// Delete is shorthand for b.In(origin).Delete.
func (b *Batch) Delete(key []byte) error { return b.stage(b.origin(), opDelete, key, nil) }

// DeleteRange is shorthand for b.In(origin).DeleteRange.
func (b *Batch) DeleteRange(start, end []byte) error { return b.stageRange(b.origin(), start, end) }

func (b *Batch) origin() Handle {
	if len(b.bound) == 0 {
		return Keyspace{}
	}
	return b.bound[0].handle()
}

// Get reads through the batch's origin, its own writes first: indexed
// batches only.
func (b *Batch) Get(ctx context.Context, key []byte) ([]byte, error) {
	if !b.indexed {
		return nil, errors.New("storage: Get on a batch that is not indexed")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if len(b.bound) == 0 {
		return nil, ErrInvalidKeyspace
	}
	s := b.bound[0]
	if err := s.check(); err != nil {
		return nil, err
	}
	abs, _, err := s.absolute(key, opRead)
	if err != nil {
		return nil, err
	}
	v, ok, err := b.getView(abs)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, ErrNotFound
	}
	return v, nil
}

// getView reads an absolute key as the batch sees it: an indexed batch's own
// writes over the committed state, else the committed state.
func (b *Batch) getView(abs []byte) ([]byte, bool, error) {
	if !b.indexed {
		return b.db.getCommitted(abs)
	}
	if err := b.ensure(); err != nil {
		return nil, false, err
	}
	release, err := b.db.lease()
	if err != nil {
		return nil, false, err
	}
	defer release()
	return getFrom(b.pb, abs)
}

// Expect adds a precondition evaluated at Commit: the key's committed value
// must equal value byte for byte (nil = the key must be absent). Allowed only
// for keys in Serialized ranges (§5S.3.4) of a bound handle; else ErrNotSerialized.
func (b *Batch) Expect(h Handle, key, value []byte) error {
	abs, err := b.expectable(h, key)
	if err != nil {
		return err
	}
	b.expect(abs, value, value == nil)
	return nil
}

// GetForUpdate reads key through h (the batch's own writes first) and records
// Expect(h, key, <value read>): optimistic read-modify-write. The recorded
// precondition is the committed value, so a write this batch staged earlier
// does not make its own Expect fail.
func (b *Batch) GetForUpdate(ctx context.Context, h Handle, key []byte) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	abs, err := b.expectable(h, key)
	if err != nil {
		return nil, err
	}
	committed, ok, err := b.db.getCommitted(abs)
	if err != nil {
		b.poison(err)
		return nil, err
	}
	b.expect(abs, committed, !ok)
	view, vok := committed, ok
	if b.indexed {
		if view, vok, err = b.getView(abs); err != nil {
			b.poison(err)
			return nil, err
		}
	}
	if !vok {
		return nil, ErrNotFound
	}
	return view, nil
}

// expectable checks that a precondition may be recorded on key through h.
func (b *Batch) expectable(h Handle, key []byte) ([]byte, error) {
	if b.err != nil {
		return nil, b.err
	}
	s := b.boundScope(h)
	if s == nil {
		err := fmt.Errorf("storage: Expect through a handle that is not bound: %w", ErrOutsideKeyspace)
		b.poison(err)
		return nil, err
	}
	if err := s.check(); err != nil {
		b.poison(err)
		return nil, err
	}
	abs, serialized, err := s.absolute(key, opRead)
	if err != nil {
		b.poison(err)
		return nil, err
	}
	if !serialized {
		err := fmt.Errorf("storage: Expect on %q: %w", abs, ErrNotSerialized)
		b.poison(err)
		return nil, err
	}
	return abs, nil
}

func (b *Batch) expect(abs, value []byte, absent bool) {
	if b.expects == nil {
		b.expects = make(map[string]expectation)
	}
	b.expects[string(abs)] = expectation{value: slices.Clone(value), absent: absent}
}

// Participants are the owners of the bound handles, for logs and metrics.
func (b *Batch) Participants() []Owner {
	var out []Owner
	for _, s := range b.bound {
		if !slices.Contains(out, s.owner) {
			out = append(out, s.owner)
		}
	}
	return out
}

// Len is the encoded size in bytes, for callers that chunk.
func (b *Batch) Len() int {
	if b.pb == nil {
		return 0
	}
	return b.pb.Len()
}

// Count is the number of entries.
func (b *Batch) Count() int { return b.writes }

// stage applies one Set or Delete through h after resolving and checking it.
func (b *Batch) stage(h Handle, op opKind, key, value []byte) error {
	if b.err != nil {
		return b.err
	}
	if b.done {
		return errBatchDone
	}
	s := b.boundScope(h)
	if s == nil {
		err := fmt.Errorf("storage: write through a handle that is not bound: %w", ErrOutsideKeyspace)
		b.poison(err)
		return err
	}
	if err := b.stageChecked(*s, op, key, value); err != nil {
		b.poison(err)
		return err
	}
	return nil
}

func (b *Batch) stageChecked(s handleOf, op opKind, key, value []byte) error {
	if err := s.check(); err != nil {
		return err
	}
	if s.ro {
		return ErrReadOnly
	}
	abs, serialized, err := s.absolute(key, op)
	if err != nil {
		return err
	}
	if err := b.ensure(); err != nil {
		return err
	}
	switch op {
	case opSet:
		err = b.pb.Set(abs, value, nil)
	default:
		err = b.pb.Delete(abs, nil)
	}
	if err != nil {
		return fmt.Errorf("storage: stage: %w", err)
	}
	b.writes++
	if serialized {
		b.mark(&b.stripeKeys, abs)
		if !s.legacy {
			b.mark(&b.serialKeys, abs)
		}
	}
	return nil
}

func (b *Batch) mark(m *map[string]bool, abs []byte) {
	if *m == nil {
		*m = make(map[string]bool)
	}
	(*m)[string(abs)] = true
}

// stageRange stages a DeleteRange through h. In a keyspace a range over a
// Serialized sub-prefix is refused (ErrBlindWrite); in a legacy range the
// whole range must resolve to the handle with a mode that admits deletes,
// and a range over a Serialized claim takes every stripe at Commit.
func (b *Batch) stageRange(h Handle, start, end []byte) error {
	if b.err != nil {
		return b.err
	}
	if b.done {
		return errBatchDone
	}
	s := b.boundScope(h)
	if s == nil {
		err := fmt.Errorf("storage: DeleteRange through a handle that is not bound: %w", ErrOutsideKeyspace)
		b.poison(err)
		return err
	}
	if err := b.stageRangeChecked(*s, start, end); err != nil {
		b.poison(err)
		return err
	}
	return nil
}

func (b *Batch) stageRangeChecked(s handleOf, start, end []byte) error {
	if err := s.check(); err != nil {
		return err
	}
	if s.ro {
		return ErrReadOnly
	}
	var lo, hi []byte
	if s.legacy {
		lo, hi = slices.Clone(start), slices.Clone(end)
	} else {
		lo = append(slices.Clone(s.prefix), start...)
		if end == nil {
			hi = keys.PrefixEnd(s.prefix)
		} else {
			hi = append(slices.Clone(s.prefix), end...)
		}
	}
	if bytes.Compare(lo, hi) >= 0 {
		return fmt.Errorf("storage: DeleteRange [%q, %q) is empty or inverted", lo, hi)
	}
	allStripes := false
	if s.legacy {
		serial, err := s.db.coverage(s.owner, lo, hi)
		if err != nil {
			return err
		}
		allStripes = serial
	} else if info := s.db.owners[s.owner]; info != nil {
		for _, sp := range info.serialized {
			if sp.overlaps(lo, hi) {
				return fmt.Errorf("storage: DeleteRange [%q, %q) over the Serialized range %q: %w", lo, hi, sp.start, ErrBlindWrite)
			}
		}
	}
	if err := b.ensure(); err != nil {
		return err
	}
	if err := b.pb.DeleteRange(lo, hi, nil); err != nil {
		return fmt.Errorf("storage: stage: %w", err)
	}
	b.writes++
	b.allStripes = b.allStripes || allStripes
	return nil
}

// coverage checks that every byte of [lo, hi) lies in intervals of owner's
// claims, contiguously, with modes that admit deletes, and reports whether any
// part is Serialized. It walks every interval the range overlaps; a gap, an
// interval of another owner, or any overlap with the reserved roots [c/, c0)
// and [m/, m0) refuses the whole range with ErrOutsideKeyspace, and nothing
// is staged.
//
// The shadow dial (core.storage.claims.mode=shadow, §5S.0 D-S2) never applies
// here: it exists to let a point access through while a claim set is being
// corrected, and a range delete that ran on past the owner's claims would
// remove rows the owner does not hold, which no dial may allow.
func (db *DB) coverage(owner Owner, lo, hi []byte) (serialized bool, err error) {
	refuse := func(at []byte, why string) error {
		return fmt.Errorf("storage: DeleteRange [%q, %q) by %s %s at %q: %w", lo, hi, owner, why, at, ErrOutsideKeyspace)
	}
	for _, r := range []span{{start: []byte("c/"), end: []byte("c0")}, {start: []byte("m/"), end: []byte("m0")}} {
		if r.overlaps(lo, hi) {
			return false, refuse(r.start, "covers a reserved range")
		}
	}
	at := lo
	for _, iv := range db.claims.intervals {
		if bytes.Compare(iv.end, at) <= 0 {
			continue // wholly before the uncovered remainder
		}
		if bytes.Compare(iv.start, at) > 0 {
			return false, refuse(at, "runs into keys no claim holds")
		}
		if iv.owner != owner {
			return false, refuse(at, "runs into a claim of "+string(iv.owner))
		}
		if !iv.mode.allows(opDelete) {
			return false, fmt.Errorf("storage: DeleteRange over a %s claim of %s: %w", iv.mode, owner, ErrClaimMode)
		}
		serialized = serialized || iv.serialized
		at = iv.end
		if bytes.Compare(at, hi) >= 0 {
			return serialized, nil
		}
	}
	return false, refuse(at, "runs past the last claim")
}

// errBatchDone: a write or commit on a batch already committed or closed.
var errBatchDone = errors.New("storage: batch already committed or closed")

// Commit applies the batch atomically or not at all. ErrConflict: a
// precondition failed; ErrOutsideKeyspace / ErrClaimMode: a write resolves
// outside the bound handles or against the claim's mode.
//
// Procedure (§5S.3.2): every write was resolved and checked when it was
// staged, and a staging error poisoned the batch; every keyspace write in a
// Serialized range must carry an Expect on its key (else ErrBlindWrite);
// then the stripes of every expected key and every Serialized write are
// taken in ascending order (all of them for a DeleteRange over a Serialized
// claim), every Expect is evaluated against the committed state, the batch
// is applied with its durability, and the stripes are released.
func (b *Batch) Commit(ctx context.Context, d Durability) error {
	if b.done {
		return errBatchDone
	}
	defer b.finish()
	if err := ctx.Err(); err != nil {
		return err
	}
	if b.err != nil {
		return b.err
	}
	if d != Sync && d != NoSync {
		return fmt.Errorf("storage: Commit with durability %d", d)
	}
	if b.writes == 0 && len(b.expects) == 0 {
		return nil
	}
	if b.writes > 0 && b.db.readOnly {
		return ErrReadOnly
	}
	if int64(b.Len()) > b.db.set.maxBatchBytes {
		return fmt.Errorf("storage: batch of %d bytes (limit %d): %w", b.Len(), b.db.set.maxBatchBytes, ErrBatchTooLarge)
	}
	for k := range b.serialKeys {
		if _, ok := b.expects[k]; !ok && !b.exclusive {
			return fmt.Errorf("storage: write of %q in a Serialized range without an Expect on it: %w", k, ErrBlindWrite)
		}
	}
	lockKeys := make([][]byte, 0, len(b.expects)+len(b.stripeKeys))
	for k := range b.expects {
		lockKeys = append(lockKeys, []byte(k))
	}
	for k := range b.stripeKeys {
		lockKeys = append(lockKeys, []byte(k))
	}
	var unlock func()
	if b.allStripes || len(lockKeys) > 0 {
		unlock = b.db.commitStripes(lockKeys, b.allStripes)
		defer unlock()
	} else if b.db.hooks.stripes != nil {
		b.db.hooks.stripes(0)
	}
	release, err := b.db.lease()
	if err != nil {
		return err
	}
	defer release()
	for k, want := range b.expects {
		got, ok, err := getFrom(b.db.eng.DB(), []byte(k))
		if err != nil {
			return err
		}
		if ok == want.absent || (ok && !bytes.Equal(got, want.value)) {
			return fmt.Errorf("storage: precondition on %q: %w", k, ErrConflict)
		}
	}
	if b.db.hooks.beforeApply != nil {
		b.db.hooks.beforeApply()
	}
	if b.pb == nil {
		return nil
	}
	opts := cpebble.Sync
	if d == NoSync {
		opts = cpebble.NoSync
	}
	if err := b.db.eng.DB().Apply(b.pb, opts); err != nil {
		return fmt.Errorf("storage: commit: %w", err)
	}
	return nil
}

// finish releases the Pebble batch; the Batch is done.
func (b *Batch) finish() {
	b.done = true
	if b.pb != nil {
		_ = b.pb.Close()
		b.pb = nil
	}
}

// Close discards an uncommitted batch; idempotent.
func (b *Batch) Close() error {
	if b == nil || b.done {
		return nil
	}
	b.finish()
	return nil
}

// BatchWriter writes one bound handle of a batch.
type BatchWriter interface {
	Set(key, value []byte) error
	Delete(key []byte) error
	DeleteRange(start, end []byte) error
}

type batchWriter struct {
	b *Batch
	h Handle
}

func (w batchWriter) Set(key, value []byte) error         { return w.b.stage(w.h, opSet, key, value) }
func (w batchWriter) Delete(key []byte) error             { return w.b.stage(w.h, opDelete, key, nil) }
func (w batchWriter) DeleteRange(start, end []byte) error { return w.b.stageRange(w.h, start, end) }
