// file: core/tasks/items.go
// version: 1.3.0
// guid: 4939aca5-9ac9-469d-bb84-fc497d308458
// last-edited: 2026-11-10

package tasks

import (
	"context"
	"errors"
	"fmt"
	"iter"
	"log/slog"
	"runtime/debug"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/falkcorp/mediacore/entity"
)

// RunItems runs fn over items inside a run. It is the in-body loop for Task
// shapes; Batch/Fixer use their runner. Semantics in the table of 5T.3.2:
// a fixed set of min(workers, len(items)) goroutines pulls indexes from an
// atomic cursor, checks the run context before taking an index and again
// before calling fn, and parks on the executor's pause gate, ref locks and
// CPU budget through rc before the item clock starts.
func RunItems[T any](rc *RC, par Parallelism, items []T,
	fn func(ic *ItemRC, item T) error, opts ...ItemsOptions[T]) (ItemsResult, error) {
	if rc == nil {
		return ItemsResult{}, refusal(ErrNoRunContext)
	}
	o, err := oneOptions(opts)
	if err != nil {
		return ItemsResult{}, refusal(err)
	}
	if err := par.Validate(); err != nil {
		return ItemsResult{}, refusal(err)
	}
	l := newLoop(rc.ctx, rc.h, par, o)
	n := len(items)
	l.total = int64(n)
	l.exhausted.Store(true) // a slice: every item is counted in pulled
	if err := l.restoreIndex(); err != nil {
		return ItemsResult{}, refusal(err)
	}
	var cursor atomic.Int64
	cursor.Store(int64(l.resumed))
	take := func() (int, T, bool) {
		i := int(cursor.Add(1) - 1)
		if i >= n {
			var zero T
			return 0, zero, false
		}
		return i, items[i], true
	}
	l.run(min(l.workers(), n-l.resumed), take, fn)
	l.pulled = int64(n - l.resumed)
	return l.result()
}

// RunSeq is RunItems over a stream whose length may be unknown. It cannot
// take Watermark (no stable index) unless Key is set.
func RunSeq[T any](rc *RC, par Parallelism, items iter.Seq[T], total Total,
	fn func(ic *ItemRC, item T) error, opts ...ItemsOptions[T]) (ItemsResult, error) {
	if rc == nil {
		return ItemsResult{}, refusal(ErrNoRunContext)
	}
	o, err := oneOptions(opts)
	if err != nil {
		return ItemsResult{}, refusal(err)
	}
	if err := par.Validate(); err != nil {
		return ItemsResult{}, refusal(err)
	}
	if o.Watermark != "" && o.Key == nil {
		return ItemsResult{}, refusal(errWatermarkNeedsKey)
	}
	if items == nil {
		return ItemsResult{}, nil
	}
	l := newLoop(rc.ctx, rc.h, par, o)
	if n, ok := total.Value(); ok {
		l.total = n
	}
	next, stop := iter.Pull(items)
	defer stop()
	var mu sync.Mutex // iter.Pull's next must never run concurrently
	idx := 0
	take := func() (int, T, bool) {
		mu.Lock()
		defer mu.Unlock()
		item, ok := next()
		if !ok {
			l.exhausted.Store(true)
			var zero T
			return 0, zero, false
		}
		i := idx
		idx++
		l.pulledSeq.Add(1)
		return i, item, true
	}
	if err := l.restoreKeyed(take); err != nil {
		return ItemsResult{}, refusal(err)
	}
	workers := l.workers()
	if n, ok := total.Value(); ok {
		workers = min(workers, max(int(n)-l.resumed, 1))
	}
	l.run(workers, take, fn)
	l.pulled = l.pulledSeq.Load() - int64(l.resumed)
	if n, ok := total.Value(); ok && l.halted() {
		l.pulled = max(l.pulled, n-int64(l.resumed))
	}
	return l.result()
}

// ForEach is the same loop for code that holds no *RC (COR-23): a library call
// reached through an API that takes only a ctx (e.g. an indexer pool's fan-out,
// 05-indexer; an acquire decision pass, 10). Same worker shape, cancellation,
// error shape, Label and ExclusiveRefs rules. When ctx descends from an RC
// context, ForEach joins that run's CPU budget, pause gate, ref-lock table and
// liveness; otherwise it is a plain bounded pool. Watermark is refused
// (ErrNoRunContext): checkpointing needs a run.
//
// Nested CPU loops: a CPU() item holds one core.tasks.cpu_slots slot, and its
// ItemRC context is marked as holding it. A CPU() ForEach (or RunItems) whose
// context carries that mark runs on the inherited slot, with one worker, and
// never calls AcquireCPU: an item that waited for a second slot while holding
// one could deadlock the budget. A nested loop must therefore be given the
// item's context (ic.Context()); a RunItems over the outer *RC sees no mark.
//
// Nested ref locks: an item that took ExclusiveRefs carries, in its context,
// the refs it holds and one lock table of its own. Every nested lock under
// that item (a nested loop's ExclusiveRefs, ic.LockRefs, LockRefs) is taken
// in the item's table, so two nested loops or goroutines of one item still
// exclude each other, and may name only refs the item holds or a held ref
// covers. Anything else is refused, never waited for: a ref that overlaps a
// held one without being covered by it (a whole kind while the item holds
// one ID) with ErrHeldOverlap, because the item would wait on itself; any
// other ref with ErrNestedLockNotHeld, because taking locks in stages (hold
// X, then wait for Y) can deadlock against a sibling item holding Y and
// waiting for X. An item declares every ref it needs in its own
// ExclusiveRefs. The item's table is not re-entrant: one goroutine must
// release a ref before locking it again.
//
// Both marks live on in every context derived from the item's, so a
// goroutine started by an item must not outlive it (F2-17's tasklint is
// where that is checked).
func ForEach[T any](ctx context.Context, par Parallelism, items []T,
	fn func(ctx context.Context, item T) error, opts ...ItemsOptions[T]) (ItemsResult, error) {
	o, err := oneOptions(opts)
	if err != nil {
		return ItemsResult{}, refusal(err)
	}
	if err := par.Validate(); err != nil {
		return ItemsResult{}, refusal(err)
	}
	if o.Watermark != "" {
		return ItemsResult{}, refusal(ErrNoRunContext)
	}
	if ctx == nil {
		ctx = context.Background()
	}
	l := newLoop(ctx, hostFrom(ctx), par, o)
	n := len(items)
	l.total = int64(n)
	l.exhausted.Store(true) // a slice: every item is counted in pulled
	var cursor atomic.Int64
	take := func() (int, T, bool) {
		i := int(cursor.Add(1) - 1)
		if i >= n {
			var zero T
			return 0, zero, false
		}
		return i, items[i], true
	}
	l.run(min(l.workers(), n), take, func(ic *ItemRC, item T) error { return fn(ic.ctx, item) })
	l.pulled = int64(n)
	return l.result()
}

// ItemsOptions tunes RunItems, RunSeq and ForEach. Pass at most one.
type ItemsOptions[T any] struct {
	Label         func(T) string       // pure; receives only the item (cannot read counters)
	OnError       ErrorMode            // FailFast or Collect; zero = FailFast
	ItemTimeout   time.Duration        // per item; the clock starts after the pause gate, ref locks and CPU slot
	ExclusiveRefs func(T) []entity.Ref // overlapping items never run at the same time (5T.3.4)
	// Watermark, when non-empty, checkpoints the contiguous-completion
	// watermark under this name and resumes from it (requires a stable item
	// order).
	Watermark       string
	CheckpointEvery int            // 0 = 64 items
	ProgressBase    int            // absolute offset when items is a slice of a larger set
	ProgressTotal   int            // absolute denominator
	Key             func(T) string // RunSeq + Watermark: stable key instead of index
}

// DefaultCheckpointEvery is ItemsOptions.CheckpointEvery when it is zero.
// Name coined by F2-01.
const DefaultCheckpointEvery = 64

// ErrorMode is what an item error does to the loop.
type ErrorMode uint8

const (
	FailFast ErrorMode = iota // stop dispatching on the first error
	Collect                   // run every item
)

// ItemsResult is a loop's outcome. Done counts items that completed in this
// call or, after a Watermark resume, in an earlier attempt.
type ItemsResult struct {
	Done, Failed, Skipped, NotStarted int64
	Counters                          map[string]int64 // folded from ItemRC.Count
	Watermark                         int              // absolute contiguous-completion prefix
}

// ItemsError is the ONLY non-nil error type RunItems/RunSeq/ForEach return.
type ItemsError struct {
	First      error   // the first item error to COMPLETE (completion order, both modes)
	FirstIndex int     // -1 when no item ran (ErrNoParallelism, ErrNoRunContext)
	Errs       []error // every item error observed before the loop stopped, completion order
	Cause      error   // context.Cause(rc.Context()) when the run's end changed the outcome (items left unstarted, or an item failed after the end), else nil
}

// Error summarises the first error, how many were observed and the cause.
func (e *ItemsError) Error() string {
	var b strings.Builder
	b.WriteString("tasks: items")
	if e.First != nil {
		fmt.Fprintf(&b, ": %v", e.First)
		if e.FirstIndex >= 0 {
			fmt.Fprintf(&b, " (item %d)", e.FirstIndex)
		}
		if n := len(e.Errs); n > 1 {
			fmt.Fprintf(&b, "; %d item errors", n)
		}
	}
	if e.Cause != nil {
		fmt.Fprintf(&b, "; run stopped: %v", e.Cause)
	}
	return b.String()
}

// Unwrap yields First, Errs and Cause: errors.Is(err, context.Canceled) and
// errors.Is(err, ErrNoParallelism) work.
func (e *ItemsError) Unwrap() []error {
	out := make([]error, 0, len(e.Errs)+2)
	if e.First != nil {
		out = append(out, e.First)
	}
	out = append(out, e.Errs...)
	if e.Cause != nil {
		out = append(out, e.Cause)
	}
	return out
}

// refusal is the one return shape of a loop that touched no item.
func refusal(err error) *ItemsError { return &ItemsError{First: err, FirstIndex: -1} }

var (
	errWatermarkNeedsKey = errors.New("tasks: RunSeq with a Watermark needs ItemsOptions.Key")
	errManyOptions       = errors.New("tasks: pass at most one ItemsOptions")
)

func oneOptions[T any](opts []ItemsOptions[T]) (ItemsOptions[T], error) {
	switch len(opts) {
	case 0:
		return ItemsOptions[T]{}, nil
	case 1:
		return opts[0], nil
	default:
		return ItemsOptions[T]{}, errManyOptions
	}
}

// ItemRC is one item's context. Executor-owned counters: callers keep none.
type ItemRC struct {
	ctx      context.Context
	h        RunHost // nil inside a plain ForEach
	counters *counters
	skip     atomic.Pointer[string]
	returned atomic.Bool
}

// Context is derived from the run ctx and carries ItemTimeout.
func (ic *ItemRC) Context() context.Context { return ic.ctx }

// Count adds delta to the named counter, atomically; folded into
// ItemsResult/Summary. A count made after the item returned is dropped.
func (ic *ItemRC) Count(name string, delta int64) {
	if ic.returned.Load() {
		return
	}
	ic.counters.add(name, delta)
}

// Skip marks the item skipped with reason (counted as skipped:<reason>)
// unless it returns an error.
func (ic *ItemRC) Skip(reason string) {
	if !ic.returned.Load() {
		ic.skip.Store(&reason)
	}
}

// Log is the run's logger; a discarding logger inside a plain ForEach.
func (ic *ItemRC) Log() *slog.Logger {
	if ic.h == nil {
		return slog.New(slog.DiscardHandler)
	}
	return ic.h.Log()
}

// Writer is the run's Writer (nil for ReadOnly defs and inside a plain ForEach).
func (ic *ItemRC) Writer() Writer {
	if ic.h == nil {
		return nil
	}
	return ic.h.Writer()
}

// Lane leases capacity of a lane the definition lists in Common.Lanes.
func (ic *ItemRC) Lane(id LaneID) (LaneLease, error) {
	if ic.h == nil {
		return nil, ErrNoRunContext
	}
	return ic.h.Lane(ic.ctx, id)
}

// LockRefs takes the executor's keyed ref locks for refs (5T.3.4). Inside an
// item that holds refs it may name only those, and locks them in the item's
// own table (see ForEach).
func (ic *ItemRC) LockRefs(refs ...entity.Ref) (release func(), err error) {
	if ic.h == nil {
		return nil, ErrNoRunContext
	}
	return lockFor(ic.ctx, ic.h, refs)
}

// heldRefsKey marks an item context with the item's *itemHold.
type heldRefsKey struct{}

// itemHold is what an item that took ExclusiveRefs holds: its refs, and the
// one table every nested lock under the item is taken in.
type itemHold struct {
	refs  []entity.Ref
	table *refTable
}

func holdOf(ctx context.Context) *itemHold {
	h, _ := ctx.Value(heldRefsKey{}).(*itemHold)
	return h
}

// covers reports that holding h includes r: the same ref, or r is one ID of
// the whole kind h.
func covers(h, r entity.Ref) bool {
	return h.Module == r.Module && h.Kind == r.Kind && (h.ID == "" || h.ID == r.ID)
}

// ErrHeldOverlap refuses a lock on a ref that overlaps one the calling item
// already holds without being covered by it. Name coined by F2-01.
var ErrHeldOverlap = errors.New("tasks: the lock overlaps a ref this item holds without being covered by it; it would wait on itself")

// ErrNestedLockNotHeld refuses a nested lock on a ref the calling item
// neither holds nor covers: taking locks in stages can deadlock against a
// sibling item. Declare the ref in the item's own ExclusiveRefs. Name coined
// by F2-01's follow-up.
var ErrNestedLockNotHeld = errors.New("tasks: a nested lock may name only refs the item holds; declare every ref the item needs in its ExclusiveRefs")

// check refuses any ref the hold does not cover (see ForEach).
func (h *itemHold) check(refs []entity.Ref) error {
	for _, r := range refs {
		switch {
		case slices.ContainsFunc(h.refs, func(have entity.Ref) bool { return covers(have, r) }):
		case slices.ContainsFunc(h.refs, func(have entity.Ref) bool { return Overlaps(have, r) }):
			return fmt.Errorf("%w: %s", ErrHeldOverlap, FormatRef(r))
		default:
			return fmt.Errorf("%w: %s", ErrNestedLockNotHeld, FormatRef(r))
		}
	}
	return nil
}

// lock takes refs, all covered by the hold, in the item's table.
func (h *itemHold) lock(ctx context.Context, refs []entity.Ref) (func(), error) {
	if err := h.check(refs); err != nil {
		return nil, err
	}
	if len(refs) == 0 {
		return func() {}, nil
	}
	return h.table.lock(ctx, refs)
}

// lockFor is ic.LockRefs and LockRefs: inside an item that holds refs, the
// item's table; otherwise the run's.
func lockFor(ctx context.Context, h RunHost, refs []entity.Ref) (func(), error) {
	if hold := holdOf(ctx); hold != nil {
		return hold.lock(ctx, refs)
	}
	return h.LockRefs(ctx, refs...)
}

// PanicError is the item error a recovered panic in fn, Label, Key or
// ExclusiveRefs becomes. Error is one line (the panic value); the stack is in
// Stack and, inside a run, logged once through the run's logger at Error
// level. Name coined by F2-01.
type PanicError struct {
	Value any    // what was passed to panic
	Stack []byte // the panicking goroutine's stack (runtime/debug.Stack)

	// logged is set by the loop that logs the stack, so a panic that
	// surfaces again through an enclosing loop's item error is not logged
	// twice. Written and read by one loop's worker, then by enclosing loops
	// only after the nested loop returned.
	logged bool
}

// Error is "panic: <value>" on one line.
func (e *PanicError) Error() string {
	return "panic: " + strings.ReplaceAll(fmt.Sprint(e.Value), "\n", " ")
}

// Unwrap yields Value when it is an error, so errors.Is sees through a
// panic(err).
func (e *PanicError) Unwrap() error {
	err, _ := e.Value.(error)
	return err
}

// counters is a loop's named counters: a map guarded for insertion, each
// value an atomic.
type counters struct {
	mu sync.Mutex
	m  map[string]*atomic.Int64
}

func (c *counters) add(name string, delta int64) {
	c.mu.Lock()
	v := c.m[name]
	if v == nil {
		v = new(atomic.Int64)
		c.m[name] = v
	}
	c.mu.Unlock()
	v.Add(delta)
}

func (c *counters) snapshot() map[string]int64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make(map[string]int64, len(c.m))
	for k, v := range c.m {
		out[k] = v.Load()
	}
	return out
}

// watermark is the checkpointed value of ItemsOptions.Watermark: N items, in
// item order, completed contiguously (absolute for RunItems), and the Key of
// the Nth for RunSeq.
type watermark struct {
	N   int    `json:"n"`
	Key string `json:"key,omitempty"`
}

// loop is one RunItems/RunSeq/ForEach call. It holds no package state: every
// field belongs to this call (03 R5).
type loop[T any] struct {
	ctx      context.Context // the run (or ForEach) context
	h        RunHost         // nil: a plain bounded pool
	par      Parallelism
	opts     ItemsOptions[T]
	itemsCtx context.Context // child of ctx, canceled with ErrItemsStopped on a FailFast stop
	stop     context.CancelCauseFunc
	stopped  atomic.Bool
	locks    *refTable // a plain pool's own ref locks (ExclusiveRefs without a run or an enclosing item's hold)
	counters counters

	started   atomic.Int64
	pulledSeq atomic.Int64
	exhausted atomic.Bool // every item was handed out or counted (a slice, or a drained stream)
	pulled    int64       // items handed to the loop (all of a slice), set after run
	resumed   int         // items skipped because an earlier attempt completed them
	total     int64       // item count when known, else 0

	mu         sync.Mutex // completion order: errors, counts, progress, watermark
	errs       []error
	firstIdx   int
	done       int64
	failed     int64
	skipped    int64
	completed  int64
	finished   map[int]string // completed indexes beyond the prefix -> key
	prefix     int            // contiguous completed items from 0
	prefixKey  string
	sinceCkpt  int
	ckptFailed bool
	// failedAfterEnd: an item error was recorded after the run's context
	// ended, so the run's end, not only the item, stopped it.
	failedAfterEnd bool
}

func newLoop[T any](ctx context.Context, h RunHost, par Parallelism, o ItemsOptions[T]) *loop[T] {
	itemsCtx, stop := context.WithCancelCause(ctx)
	l := &loop[T]{ctx: ctx, h: h, par: par, opts: o, itemsCtx: itemsCtx, stop: stop, firstIdx: -1}
	l.counters.m = make(map[string]*atomic.Int64)
	if o.ExclusiveRefs != nil && h == nil {
		l.locks = &refTable{}
	}
	if o.Watermark != "" {
		l.finished = make(map[int]string)
	}
	return l
}

func (l *loop[T]) workers() int {
	if l.inheritsCPUSlot() {
		return 1 // runs on the enclosing item's slot (see ForEach)
	}
	n := l.par.Workers()
	if l.h != nil {
		n = l.h.CapWorkers(n)
	}
	return max(n, 1)
}

// cpuSlotKey marks an item context whose item holds a CPU slot.
type cpuSlotKey struct{}

// inheritsCPUSlot reports a CPU() loop nested inside a CPU() item of a run:
// it uses the item's slot instead of acquiring one.
func (l *loop[T]) inheritsCPUSlot() bool {
	if l.h == nil || !l.par.UsesCPUBudget() {
		return false
	}
	held, _ := l.ctx.Value(cpuSlotKey{}).(bool)
	return held
}

// halted reports a done run context or a FailFast stop.
func (l *loop[T]) halted() bool { return l.ctx.Err() != nil || l.stopped.Load() }

// restoreIndex reads RunItems' watermark and skips the items it covers.
func (l *loop[T]) restoreIndex() error {
	if l.opts.Watermark == "" {
		return nil
	}
	var w watermark
	ok, err := l.h.Restore(l.opts.Watermark, &w)
	if err != nil {
		return fmt.Errorf("tasks: restore watermark %q: %w", l.opts.Watermark, err)
	}
	if ok {
		l.resumed = min(max(w.N-l.opts.ProgressBase, 0), int(l.total))
		l.prefix = l.resumed
		l.done = int64(l.resumed)
		l.completed = int64(l.resumed)
	}
	return nil
}

// restoreKeyed reads RunSeq's watermark and drains the items it covers,
// checking that the stream's order is the one the checkpoint recorded.
func (l *loop[T]) restoreKeyed(take func() (int, T, bool)) error {
	if l.opts.Watermark == "" {
		return nil
	}
	var w watermark
	ok, err := l.h.Restore(l.opts.Watermark, &w)
	if err != nil {
		return fmt.Errorf("tasks: restore watermark %q: %w", l.opts.Watermark, err)
	}
	// The checkpoint is absolute (ProgressBase + prefix, checkpointLocked);
	// this call's stream starts at ProgressBase, as in restoreIndex.
	n := w.N - l.opts.ProgressBase
	if !ok || n <= 0 {
		return nil
	}
	var last string
	for i := range n {
		_, item, more := take()
		if !more {
			return fmt.Errorf("tasks: watermark %q: the stream ended after %d items, the checkpoint recorded %d", l.opts.Watermark, i, n)
		}
		key, err := guarded("Key", func() (string, error) { return l.opts.Key(item), nil })
		if err != nil {
			l.logPanic(-1, err)
			return fmt.Errorf("tasks: watermark %q: %w", l.opts.Watermark, err)
		}
		last = key
	}
	if last != w.Key {
		return fmt.Errorf("tasks: watermark %q: item %d has key %q, the checkpoint recorded %q; the order is not stable", l.opts.Watermark, n, last, w.Key)
	}
	l.resumed = n
	l.prefix = n
	l.prefixKey = w.Key
	l.done = int64(n)
	l.completed = int64(n)
	return nil
}

// run starts n workers (wg.Go) over take and waits for them.
func (l *loop[T]) run(n int, take func() (int, T, bool), fn func(ic *ItemRC, item T) error) {
	defer l.stop(nil)
	var wg sync.WaitGroup
	for range n {
		wg.Go(func() {
			for !l.halted() { // checked BEFORE taking an index
				i, item, ok := take()
				if !ok {
					return
				}
				l.one(i, item, fn)
			}
		})
	}
	wg.Wait()
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.opts.Watermark != "" && l.sinceCkpt > 0 && !l.ckptFailed {
		l.checkpointLocked()
	}
}

// one runs a single item: pause gate, ref locks, CPU slot, the second
// context check, then the item clock and fn.
//
// A gate (AwaitUnpaused, LockRefs, AcquireCPU) that returns an error while
// the loop's context is still live is not a cancellation: the item fails with
// that error and the loop stops, in both error modes, because the run's
// pause gate, ref-lock table or CPU budget is broken. With the context done
// the item is simply not started. A panic in fn, Label, Key or ExclusiveRefs
// fails the item with the panic value and its stack.
func (l *loop[T]) one(i int, item T, fn func(ic *ItemRC, item T) error) {
	ctx := l.itemsCtx
	var releases []func()
	defer func() {
		for _, release := range slices.Backward(releases) {
			release()
		}
	}()
	fail := func(err error, gate bool) {
		l.started.Add(1)
		l.complete(i, "", "", err, nil)
		if gate {
			l.halt()
		}
	}
	gateErr := func(what string, err error) bool {
		if err == nil {
			return false
		}
		if l.itemsCtx.Err() == nil {
			fail(fmt.Errorf("tasks: %s: %w", what, err), true)
		}
		return true // not started when the context is done
	}
	if l.h != nil && gateErr("pause gate", l.h.AwaitUnpaused(ctx)) {
		return
	}
	if l.opts.ExclusiveRefs != nil {
		refs, err := guarded("ExclusiveRefs", func() ([]entity.Ref, error) { return l.opts.ExclusiveRefs(item), nil })
		if err != nil {
			fail(err, false)
			return
		}
		release, err := l.lockRefs(ctx, refs)
		if gateErr("ref locks", err) {
			return
		}
		releases = append(releases, release)
		if len(refs) > 0 { // nested locks under this item use its own table and may name only these refs
			ctx = context.WithValue(ctx, heldRefsKey{}, &itemHold{refs: slices.Clone(refs), table: &refTable{}})
		}
	}
	if l.h != nil && l.par.UsesCPUBudget() && !l.inheritsCPUSlot() {
		release, err := l.h.AcquireCPU(ctx)
		if gateErr("CPU slot", err) {
			return
		}
		releases = append(releases, release)
		ctx = context.WithValue(ctx, cpuSlotKey{}, true) // a nested CPU() loop inherits this slot
	}
	if l.halted() { // checked again BEFORE calling fn
		return
	}
	l.started.Add(1)
	var label string
	err := l.guard("Label", func() error {
		if l.opts.Label != nil {
			label = l.opts.Label(item)
		}
		return nil
	})
	itemCtx, cancel := ctx, context.CancelFunc(func() {})
	if l.opts.ItemTimeout > 0 {
		itemCtx, cancel = context.WithTimeout(ctx, l.opts.ItemTimeout)
	}
	ic := &ItemRC{ctx: itemCtx, h: l.h, counters: &l.counters}
	if err == nil && l.h != nil {
		err = l.h.RenewHold(itemCtx) // a lost stand-down hold fails the item before its write
	}
	if err == nil {
		err = l.guard("fn", func() error { return fn(ic, item) })
	}
	ic.returned.Store(true)
	cancel()
	var key string
	if err == nil && l.opts.Watermark != "" && l.opts.Key != nil {
		key, err = guarded("Key", func() (string, error) { return l.opts.Key(item), nil })
	}
	l.complete(i, key, label, err, ic.skip.Load())
}

// guard runs f, turning a panic into a *PanicError (see guarded).
func (l *loop[T]) guard(what string, f func() error) error {
	_, err := guarded(what, func() (struct{}, error) { return struct{}{}, f() })
	return err
}

// guarded runs f, turning a panic into a *PanicError, wrapped with what
// panicked.
func guarded[V any](what string, f func() (V, error)) (v V, err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("tasks: %s: %w", what, &PanicError{Value: r, Stack: debug.Stack()})
		}
	}()
	return f()
}

// lockRefs takes an item's ExclusiveRefs, all at once (never in stages):
// under an enclosing item that holds refs, in that item's table and only refs
// it covers; otherwise in the run's table, or the loop's own without a run.
// The run's table therefore never sees a holder ask again for a ref it holds.
func (l *loop[T]) lockRefs(ctx context.Context, refs []entity.Ref) (func(), error) {
	if len(refs) == 0 {
		return func() {}, nil
	}
	if hold := holdOf(ctx); hold != nil {
		return hold.lock(ctx, refs)
	}
	if l.h == nil {
		return l.locks.lock(ctx, refs)
	}
	return l.h.LockRefs(ctx, refs...)
}

// complete records one finished item in completion order, under one lock:
// counts, errors, progress delivery and the watermark.
func (l *loop[T]) complete(i int, key, label string, err error, skip *string) {
	l.logPanic(i, err)
	l.mu.Lock()
	switch {
	case err != nil:
		if l.ctx.Err() != nil {
			l.failedAfterEnd = true // see result: the run's end stopped this item
		}
		l.failed++
		l.errs = append(l.errs, err)
		if len(l.errs) == 1 {
			l.firstIdx = i
		}
	case skip != nil:
		l.skipped++
		l.counters.add("skipped:"+*skip, 1)
	default:
		l.done++
	}
	l.completed++
	if l.h != nil {
		l.h.ItemsProgress(int64(l.opts.ProgressBase)+l.completed, l.progressTotal(), label)
	}
	if l.finished != nil && err == nil { // failed items never advance the watermark
		l.advanceLocked(i, key)
	}
	stop := err != nil && l.opts.OnError == FailFast
	if stop {
		l.stopped.Store(true) // before unlocking: no worker takes another index after this error
	}
	l.mu.Unlock()
	if stop {
		l.stop(ErrItemsStopped)
	}
}

// logPanic logs a recovered panic's stack once, through the run's logger at
// Error level: the innermost loop inside a run logs it and marks it logged,
// so an enclosing loop that sees it again in its item's error does not (a
// plain ForEach has no run logger and leaves it to the enclosing run's loop).
func (l *loop[T]) logPanic(i int, err error) {
	var pe *PanicError
	if l.h == nil || !errors.As(err, &pe) || pe.logged {
		return
	}
	pe.logged = true
	l.h.Log().Error("tasks: item panicked", "item", i, "error", err.Error(), "stack", string(pe.Stack))
}

func (l *loop[T]) halt() {
	l.stopped.Store(true)
	l.stop(ErrItemsStopped)
}

func (l *loop[T]) progressTotal() int64 {
	if l.opts.ProgressTotal > 0 {
		return int64(l.opts.ProgressTotal)
	}
	if l.total == 0 {
		return 0 // unknown
	}
	return int64(l.opts.ProgressBase) + l.total
}

// advanceLocked folds a completed index into the contiguous prefix and
// checkpoints every CheckpointEvery advances.
func (l *loop[T]) advanceLocked(i int, key string) {
	l.finished[i] = key
	moved := false
	for {
		k, ok := l.finished[l.prefix]
		if !ok {
			break
		}
		delete(l.finished, l.prefix)
		l.prefix++
		l.prefixKey = k
		l.sinceCkpt++
		moved = true
	}
	every := l.opts.CheckpointEvery
	if every <= 0 {
		every = DefaultCheckpointEvery
	}
	if moved && l.sinceCkpt >= every && !l.ckptFailed {
		l.checkpointLocked()
	}
}

// checkpointLocked persists the watermark. A persist error stops the loop
// and is reported like an item error (FirstIndex -1 when it is the first):
// the executor's host decides whether the error is fatal for the run's
// ResumePolicy by returning it or not (5T.5).
func (l *loop[T]) checkpointLocked() {
	l.sinceCkpt = 0
	w := watermark{N: l.opts.ProgressBase + l.prefix, Key: l.prefixKey}
	if err := l.h.Checkpoint(l.opts.Watermark, w); err != nil {
		l.errs = append(l.errs, fmt.Errorf("tasks: checkpoint watermark %q: %w", l.opts.Watermark, err))
		l.ckptFailed = true
		l.halt()
	}
}

func (l *loop[T]) result() (ItemsResult, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	res := ItemsResult{
		Done:       l.done,
		Failed:     l.failed,
		Skipped:    l.skipped,
		NotStarted: max(l.pulled-l.started.Load(), 0),
		Counters:   l.counters.snapshot(),
		Watermark:  l.opts.ProgressBase + l.prefix,
	}
	var cause error
	// Cause is reported when the run's end changed the loop's outcome: items
	// were left unstarted, a stream was not drained, or an item failed after
	// the run's context ended (typically returning its canceled context's
	// error). An item that failed on its own before the end, or a loop whose
	// every item succeeded, gets no Cause.
	if l.ctx.Err() != nil && (res.NotStarted > 0 || !l.exhausted.Load() || l.failedAfterEnd) {
		cause = context.Cause(l.ctx)
	}
	if len(l.errs) == 0 && cause == nil {
		return res, nil
	}
	e := &ItemsError{FirstIndex: l.firstIdx, Errs: l.errs, Cause: cause}
	if len(l.errs) > 0 {
		e.First = l.errs[0]
	}
	return res, e
}

// refTable is a plain ForEach's own ref locks: the executor's table rule
// (sorted, all-or-wait, cancellable) over one call's items. Inside a run the
// executor's table is used instead.
type refTable struct {
	mu   sync.Mutex
	held map[*refHold][]entity.Ref
}

type refHold struct {
	done chan struct{}
	once sync.Once
}

func (t *refTable) lock(ctx context.Context, refs []entity.Ref) (func(), error) {
	for {
		t.mu.Lock()
		var wait chan struct{}
	scan:
		for h, rs := range t.held {
			for _, have := range rs {
				for _, want := range refs {
					if Overlaps(have, want) {
						wait = h.done
						break scan
					}
				}
			}
		}
		if wait == nil {
			h := &refHold{done: make(chan struct{})}
			if t.held == nil {
				t.held = make(map[*refHold][]entity.Ref)
			}
			t.held[h] = refs
			t.mu.Unlock()
			return func() {
				h.once.Do(func() {
					t.mu.Lock()
					delete(t.held, h)
					t.mu.Unlock()
					close(h.done)
				})
			}, nil
		}
		t.mu.Unlock()
		select {
		case <-wait:
		case <-ctx.Done():
			return nil, context.Cause(ctx)
		}
	}
}
