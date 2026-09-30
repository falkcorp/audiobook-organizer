// file: internal/scanlock/scanlock.go
// version: 1.1.0
// guid: 515417c0-cdec-4e8d-bf38-99b598b4115d
// last-edited: 2026-09-30

// Package scanlock is the per-BOOK lock that lets a library scan and a
// metadata apply run at the same time without either one reverting the other.
//
// Standing rule (owner, 2026-09-30): a library scan never blocks anything. The
// old request gate refused every single-book apply and write-back with 409
// SCAN_RUNNING for as long as ANY scan ran, because a rescan overlays the
// file's tags onto the book row and an apply that landed between the scanner's
// tag read and its row merge was silently reverted. This table replaces that
// library-wide refusal with the smallest scope that closes the race: the
// scanner holds the lock of the one book it is reading and merging, an apply
// holds the lock of the one book it is writing, and only two sides that name
// the same book ever wait for each other.
//
// KEYED, NEVER STRIPED. Every other lock the store and scanner own
// (pebble bookLocks, the scanner's lockBookPath and version-link stripes) hashes
// many keys onto a fixed array of mutexes. Those are held for microseconds
// across a read-modify-write, so a false collision costs nothing. This lock is
// held for the seconds a tag read or a tag write takes; a stripe collision
// would make an apply on one book wait for the scanner reading an unrelated
// one, which is exactly the complaint this package exists to remove.
//
// LOCK ORDER. This table is L0, the OUTERMOST lock in the process:
//
//	L0  scanlock.Books (this package)
//	L1  the server's writeBackPathLocks: book: -> vg: -> book:<copy> -> path
//	    (see metafetch lockBook)
//	L2  store and scanner stripes (pebble bookLocks, lockBookPath, ModifyBook)
//
// R1. A blocking acquire (LockSet) takes a whole sorted set, and the caller
//
//	must hold no L0 key while it waits. Adding a key while holding others
//	is only ever TryLockSet; on failure the caller releases everything and
//	starts over with the larger set.
//
// R2. No goroutine holding an L1 or L2 key ever takes an L0 key.
// R3. The scanner never takes an L1 key.
//
// Among L0 holders only goroutines that hold nothing ever wait, so no cycle can
// form inside L0; and L0 -> L1 -> L2 is the only nesting direction.
package scanlock

import (
	"context"
	"slices"
	"sync"
	"sync/atomic"
)

// Event is one trace record: Op is "+" (about to acquire, before any wait),
// "=" (acquired), "-" (released) or "!" (try failed / wait abandoned).
type Event struct {
	Op  string
	Key string
}

// Table is a keyed mutex over book IDs. The zero value is not usable; use New.
type Table struct {
	mu    sync.Mutex
	m     map[string]*entry
	trace func(Event)
}

type entry struct {
	// ch has capacity 1: a token in the channel means "held".
	ch   chan struct{}
	refs int // holders + waiters + pending marks; guarded by Table.mu
	// pending counts writers between their database write and their file
	// write (MarkPending). idle is non-nil while pending > 0 and is closed
	// when it drops to zero. Both guarded by Table.mu.
	pending int
	idle    chan struct{}
}

// New returns an empty table.
func New() *Table { return &Table{m: make(map[string]*entry)} }

// Books is the process-wide table shared by the scanner and every apply path.
// Package-level for the same reason the server's writeBackPathLocks is: two
// subsystems that never see each other's structs must share one table or the
// lock is worthless across them.
var Books = New()

// SetTrace installs (or with nil removes) a hook told of every lock event,
// called under the table mutex. Tests only.
func (t *Table) SetTrace(fn func(Event)) {
	t.mu.Lock()
	t.trace = fn
	t.mu.Unlock()
}

// Held returns the number of keys currently held or waited on. Tests only.
func (t *Table) Held() int {
	t.mu.Lock()
	defer t.mu.Unlock()
	return len(t.m)
}

func (t *Table) ref(id, op string) *entry {
	t.mu.Lock()
	e := t.m[id]
	if e == nil {
		e = &entry{ch: make(chan struct{}, 1)}
		t.m[id] = e
	}
	e.refs++
	if t.trace != nil {
		t.trace(Event{Op: op, Key: id})
	}
	t.mu.Unlock()
	return e
}

func (t *Table) unref(id string, e *entry, op string) {
	t.mu.Lock()
	e.refs--
	if e.refs == 0 {
		delete(t.m, id)
	}
	if t.trace != nil {
		t.trace(Event{Op: op, Key: id})
	}
	t.mu.Unlock()
}

func (t *Table) note(id, op string) {
	t.mu.Lock()
	if t.trace != nil {
		t.trace(Event{Op: op, Key: id})
	}
	t.mu.Unlock()
}

// normalize dedupes, drops empty IDs and sorts, so every blocking acquire
// takes its keys in one global order.
func normalize(ids []string) []string {
	out := make([]string, 0, len(ids))
	for _, id := range ids {
		if id != "" {
			out = append(out, id)
		}
	}
	slices.Sort(out)
	return slices.Compact(out)
}

// LockSet acquires every id, in sorted order, waiting as long as ctx allows.
// On ctx expiry it releases whatever it had taken and returns ctx.Err(). The
// caller must hold no key of this table while it waits (R1). An empty set
// returns an empty, valid hold.
func (t *Table) LockSet(ctx context.Context, ids []string) (*Hold, error) {
	keys := normalize(ids)
	h := &Hold{t: t}
	for _, id := range keys {
		e := t.ref(id, "+")
		select {
		case e.ch <- struct{}{}:
			h.keys = append(h.keys, id)
			h.entries = append(h.entries, e)
			t.note(id, "=")
		case <-ctx.Done():
			t.unref(id, e, "!")
			h.releaseAll()
			return nil, ctx.Err()
		}
	}
	h.shares.Store(1)
	return h, nil
}

// TryLockSet acquires every id or none, never waiting.
func (t *Table) TryLockSet(ids []string) (*Hold, bool) {
	keys := normalize(ids)
	h := &Hold{t: t}
	for _, id := range keys {
		e := t.ref(id, "+")
		select {
		case e.ch <- struct{}{}:
			h.keys = append(h.keys, id)
			h.entries = append(h.entries, e)
			t.note(id, "=")
		default:
			t.unref(id, e, "!")
			h.releaseAll()
			return nil, false
		}
	}
	h.shares.Store(1)
	return h, true
}

// MarkPending records that a writer has committed book id's metadata to the
// database and has file work (tag write, rename, cover embed) still to run.
// It does not exclude other writers -- a second apply of the book need not
// wait for the first one's file job, metafetch's own book lock serializes
// those -- but the SCANNER treats a pending book as busy (LockSetIdle,
// TryLockSetIdle). Without it the scanner could read the book's old tags
// after the apply's database write and before its tag write, overlay them
// onto the applied row, and the file job would then write the reverted
// values into the files.
//
// A writer marks pending while it still holds the book's exclusive lock, so
// the scanner can never slip in between the database write and the mark. The
// returned func clears the mark; it is idempotent.
func (t *Table) MarkPending(id string) func() {
	if id == "" {
		return func() {}
	}
	e := t.ref(id, "p+")
	t.mu.Lock()
	e.pending++
	if e.idle == nil {
		e.idle = make(chan struct{})
	}
	t.mu.Unlock()
	var once sync.Once
	return func() {
		once.Do(func() {
			t.mu.Lock()
			e.pending--
			if e.pending == 0 && e.idle != nil {
				close(e.idle)
				e.idle = nil
			}
			t.mu.Unlock()
			t.unref(id, e, "p-")
		})
	}
}

// firstPendingIdle returns the idle channel of the first held key with
// pending file work, or nil when none has any.
func (h *Hold) firstPendingIdle() <-chan struct{} {
	h.t.mu.Lock()
	defer h.t.mu.Unlock()
	for _, e := range h.entries {
		if e.pending > 0 {
			return e.idle
		}
	}
	return nil
}

// LockSetIdle is LockSet for the scanner: it also waits until no key has
// pending file work (MarkPending). While it waits for a pending job it holds
// nothing, so it never blocks the writer that is about to finish.
func (t *Table) LockSetIdle(ctx context.Context, ids []string) (*Hold, error) {
	for {
		h, err := t.LockSet(ctx, ids)
		if err != nil {
			return nil, err
		}
		idle := h.firstPendingIdle()
		if idle == nil {
			return h, nil
		}
		h.Release()
		select {
		case <-idle:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
}

// TryLockSetIdle is TryLockSet for the scanner: it also fails when any key
// has pending file work.
func (t *Table) TryLockSetIdle(ids []string) (*Hold, bool) {
	h, ok := t.TryLockSet(ids)
	if !ok {
		return nil, false
	}
	if h.firstPendingIdle() != nil {
		h.Release()
		return nil, false
	}
	return h, true
}

// Hold is a set of held keys. It is released once every share is released:
// the acquirer's own, plus one per Retain.
type Hold struct {
	t       *Table
	keys    []string
	entries []*entry
	shares  atomic.Int32
	once    sync.Once
}

// IDs returns the held keys, sorted.
func (h *Hold) IDs() []string {
	if h == nil {
		return nil
	}
	return slices.Clone(h.keys)
}

// Has reports whether id is one of the held keys.
func (h *Hold) Has(id string) bool {
	if h == nil {
		return false
	}
	_, found := slices.BinarySearch(h.keys, id)
	return found
}

// Retain adds a share for work that outlives the caller (a background file
// job). The returned func releases that share; it is idempotent.
func (h *Hold) Retain() func() {
	if h == nil {
		return func() {}
	}
	h.shares.Add(1)
	var once sync.Once
	return func() { once.Do(h.dropShare) }
}

// Release drops the acquirer's share. Idempotent; safe on nil.
func (h *Hold) Release() {
	if h == nil {
		return
	}
	h.once.Do(h.dropShare)
}

func (h *Hold) dropShare() {
	if h.shares.Add(-1) == 0 {
		h.releaseAll()
	}
}

func (h *Hold) releaseAll() {
	for i := len(h.keys) - 1; i >= 0; i-- {
		<-h.entries[i].ch
		h.t.unref(h.keys[i], h.entries[i], "-")
	}
	h.keys, h.entries = nil, nil
}

type holdKey struct{}

// WithHold returns ctx carrying h, so code deep inside a held span (the
// scanner's saveBook) can check which rows it is allowed to write.
func WithHold(ctx context.Context, h *Hold) context.Context {
	return context.WithValue(ctx, holdKey{}, h)
}

// HoldFrom returns the hold WithHold attached, or nil.
func HoldFrom(ctx context.Context) *Hold {
	if ctx == nil {
		return nil
	}
	h, _ := ctx.Value(holdKey{}).(*Hold)
	return h
}
