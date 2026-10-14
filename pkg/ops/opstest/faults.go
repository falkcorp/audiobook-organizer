// file: pkg/ops/opstest/faults.go
// version: 1.1.0
// guid: 3f7b1d94-6a25-4c08-9e3f-8d2b6a4c1e73
// last-edited: 2026-10-14

package opstest

import (
	"context"
	"fmt"
	"slices"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/pkg/ops"
)

// The fault injectors. Each item fault is keyed by an item's global start
// index within the attempt: 0 is the first item any worker starts. A fault
// that cannot apply (no such item, a chunk too small, a def that holds no
// stand-down) does nothing and says why in Outcome.Notes.

// ChunkSize overrides the def's ChunkSize. Conformance also runs 1, 3 and
// the default.
func ChunkSize(n int) RunOpt {
	return func(c *runCfg) {
		if n < 1 {
			c.err = fmt.Errorf("opstest: ChunkSize(%d): need at least 1", n)
			return
		}
		c.chunk = n
	}
}

// CancelAt cancels the run when item n starts, through the registry's
// Cancel: the run record moves to stopping, the fence is revoked, then the
// context is canceled. Item n still runs, on a canceled context. Finish runs
// with Canceled true and the partial Summary the ledger folded.
func CancelAt(item int) RunOpt { return func(c *runCfg) { c.cancelAt = item } }

// ShutdownAt shuts the registry down gracefully when item n starts, as a
// server restart does (Registry.Shutdown): the run's context is canceled with
// ErrShutdown, so the runner stops for resume (it skips Finish, leaving it to
// the resumed attempt) and the registry records the run interrupted. Unlike
// CrashAt nothing the stopping run commits is lost. Outcome.Resume() boots a
// new registry over the same store, which resumes the run under the same op
// id.
func ShutdownAt(item int) RunOpt { return func(c *runCfg) { c.shutdownAt = item } }

// CrashAt stops the world immediately after the item with global start index
// n returns: no further store write is accepted, from the registry or from
// anything it started, and the completions the ledger folded but had not
// committed yet are lost (the production commit throttle runs, so in a small
// run that is usually every completion so far). Outcome.Resume() boots a new
// registry, whose boot sweep resumes the same op id over the surviving
// store.
func CrashAt(item int) RunOpt { return func(c *runCfg) { c.crashAt = item } }

// CrashMidChunk stops the world while chunk c is leased and item i of it is
// done; Resume() must re-run chunk c from its first item, never re-run any
// chunk the ledger recorded, run every item >= 1 time and the items of chunk
// c <= 2 times, and leave Done == Total.
//
// The harness applies it in its strongest form: the worker holding chunk c
// waits, item i done, until every other worker has exited and the ledger has
// committed every chunk they completed, and only then does the world stop.
// Chunk c is then the only chunk the ledger does not record, so a correct
// resume leases c and nothing else, and every item outside c ran exactly
// once. i indexes into the chunk as leased (an oversized partition chunk
// included) and must not be its last item.
func CrashMidChunk(chunk, item int) RunOpt {
	return func(c *runCfg) { c.crashMid = &[2]int{chunk, item} }
}

// InsertBelowCursor proves an AppendOnly claim or fails it. While the run is
// in flight it inserts item into the World's books at a key that sorts
// before the cursor: inside a key range the ledger already recorded, once
// there is one holding two keys, and then the world stops, so Resume()'s
// chunk-ledger skip passes over the inserted item; failing that, inside a
// range of the run's final ledger, and the world stops once that ledger is
// committed. An item nil inserts a synthetic copy of
// the range's first book (this assumes the def keys books by id); otherwise
// item must be a database.Book whose id the caller placed. For a Snapshot or
// Unordered source it is a no-op, noted in Outcome.Notes.
func InsertBelowCursor(item any) RunOpt {
	return func(c *runCfg) { c.insert = &insertSpec{item: item} }
}

// AbandonAt makes item n's goroutine ignore its context: when it starts, the
// run is canceled and the goroutine keeps trying to write through the run's
// Writer, with a context that is never canceled, instead of returning, until
// the registry makes the run a zombie (stopping; it keeps its key). A second
// run of the same def is then queued; it must stay queued on the def's
// exclusive key until the abandoned goroutine returns (Outcome.Abandon).
// Every write the abandoned goroutine tries must be refused by the fence.
func AbandonAt(item int) RunOpt { return func(c *runCfg) { c.abandonAt = item } }

// LoseLeaseAt makes the registry's scan stand-down lease of the run lapse
// when item n starts (the stand-down lease, not a chunk lease: chunk leases
// are never reassigned while the owner lives). The registry's stand-down
// clock jumps past the lease, so the next renewal fails and the registry
// revokes the run's fence with no cancel behind it; no later write may
// commit. A run that holds no stand-down notes the fault as not tested.
func LoseLeaseAt(item int) RunOpt { return func(c *runCfg) { c.loseLeaseAt = item } }

// beforeCommit is InsertBelowCursor's last chance: at the run's final
// commit, if no recorded range had room for the insert while items ran (a
// run of one chunk records none), the item goes inside a range of the ledger
// about to be written, and the world stops once that commit lands. Resume()
// then re-reads the source with every range recorded: a source that keeps
// its AppendOnly claim returns the item above them all, one that does not
// returns it inside a recorded range, where the resume skips it.
func (a *attempt) beforeCommit(l database.Ledger, final bool) bool {
	if !final || a.cfg.insert == nil {
		return false
	}
	a.mu.Lock()
	skip := a.inserted != "" || a.order != ops.AppendOnly
	a.mu.Unlock()
	if skip {
		return false
	}
	if a.insertInside(l.Ranges) {
		a.crashAfterCommit.Store(true)
	} else {
		a.note("InsertBelowCursor not applied: no completed range holds two keys to insert between")
	}
	return false
}

// crash stops the world: the generation is killed (no write in flight,
// none accepted after, from the dying registry or anything it leaked), the
// attempt's window closes and the op waits for Resume.
func (a *attempt) crash() {
	a.g.kill()
	if !a.crashed.CompareAndSwap(false, true) {
		return
	}
	a.w.mu.Lock()
	a.w.crashed[a.opID] = true
	a.w.mu.Unlock()
	a.close()
}

// shutdownRegistry is ShutdownAt: it marks the attempt stopped (so the op
// waits for Resume) and shuts the registry down off the item's goroutine,
// which Shutdown waits for. The item is held until Shutdown has canceled the
// run's context, so the stop lands mid-run rather than after the last item.
// Once the drain is over the generation is killed, so nothing the shut-down
// registry leaked can write, and the attempt's window closes.
func (a *attempt) shutdownRegistry(irc *ops.ItemRC) {
	if !a.crashed.CompareAndSwap(false, true) {
		return
	}
	a.w.mu.Lock()
	a.w.crashed[a.opID] = true
	a.w.mu.Unlock()
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), runLimit)
		defer cancel()
		if err := a.g.reg.Shutdown(ctx); err != nil {
			a.note("ShutdownAt: the registry shutdown did not drain: %v", err)
		}
		a.g.kill()
		a.close()
	}()
	select {
	case <-irc.Context().Done():
	case <-time.After(runLimit):
		a.w.t.Errorf("opstest: %s: ShutdownAt: the registry shutdown did not cancel the run within %s", a.opID,
			runLimit)
	}
}

// quiesce is CrashMidChunk's strong form: the worker holding chunk c, item i
// done, waits until every other worker has exited and the ledger has
// committed every chunk they completed. The world then stops with chunk c the
// only unrecorded chunk, so a correct resume leases c and only c. (The
// attempt's runner clock steps past the commit throttle on every reading,
// so each completion commits as it is folded.)
//
// The wait is bounded by runLimit, a deadline and not an ordering device:
// if the other workers have not all exited and been committed by then (a def
// whose Item blocks, a runner that never commits), the test fails saying so
// and the world stops where it is, instead of the test hanging.
func (a *attempt) quiesce(w int) {
	a.mu.Lock()
	s := a.sched
	a.mu.Unlock()
	if s != nil {
		s.exit(w) // let the others run while this worker holds its chunk
	}
	expired := false
	deadline := time.AfterFunc(runLimit, func() {
		a.mu.Lock()
		expired = true
		a.mu.Unlock()
		a.cond.Broadcast()
	})
	defer deadline.Stop()
	a.mu.Lock()
	defer a.mu.Unlock()
	for !expired && (a.exited < a.workers-1 || a.firstDone == nil || a.lastDone != *a.firstDone+a.delivered) {
		a.cond.Wait()
	}
	if expired {
		committed := "none"
		if a.firstDone != nil {
			committed = fmt.Sprint(a.lastDone - *a.firstDone)
		}
		a.w.t.Errorf("opstest: %s: CrashMidChunk waited %s for the other workers: %d of %d exited, %d chunks "+
			"delivered, %s committed; the world stops here", a.opID, runLimit, a.exited, a.workers-1,
			a.delivered, committed)
	}
}

// revokeWith runs revoke (the registry call that stops the run's writes)
// once the Writer calls in flight have returned, and logs the revoke in the
// commit sequence: a write logged after it can only be one that started
// after it. Every later Writer call that succeeds is a late write.
func (a *attempt) revokeWith(revoke func()) {
	if t := a.tracked; t != nil {
		t.drain.Lock()
		defer t.drain.Unlock()
		defer t.revoked.Store(true)
	}
	revoke()
	seq := a.w.log.next()
	a.mu.Lock()
	if a.revokedAt == 0 {
		a.revokedAt = seq
	}
	a.mu.Unlock()
}

// cancelRun is CancelAt: the registry's own Cancel, which moves the run to
// stopping, revokes its fence and cancels its context.
func (a *attempt) cancelRun() {
	a.revokeWith(func() {
		if err := a.g.reg.Cancel(a.opID); err != nil {
			a.w.t.Errorf("opstest: cancel %s: %v", a.opID, err)
		}
	})
}

// abandonProbe is the subject AbandonAt's ignored-context writes target.
var abandonProbe = ops.Subject{Type: ops.SubjectBook, ID: "opstest-abandon-probe"}

// abandon is AbandonAt, on the item's own goroutine: the run is canceled
// and the goroutine, ignoring its context, keeps trying to write through
// the run's Writer instead of returning, until the registry gives up on it
// (the abandon grace) and makes the run a zombie that keeps its key. A
// second run of the same def is then queued; a whole dispatch cycle must
// see it and leave it queued while the goroutine is still here. Then the
// goroutine writes once more and returns, the zombie ends, the key frees
// and the second run starts (Outcome.Abandon).
func (a *attempt) abandon(irc *ops.ItemRC) {
	a.cancelRun()
	// The goroutine ignores its context: its writes carry a context that
	// is never canceled, so the fence alone stands between them and the
	// store.
	ctx := context.WithoutCancel(irc.Context())
	a.probeWrites(irc, ctx)
	select {
	case <-a.zombie:
	case <-time.After(runLimit):
		a.w.t.Errorf("opstest: %s: the registry never made the abandoned run a zombie within %s", a.opID, runLimit)
		return
	}
	sec := a.g.newAttempt(a.def, a.c, a.params, newRunCfg(a.cfg.carry()))
	a.g.expectAttempt(sec)
	rep := &AbandonReport{}
	id, err := a.g.reg.EnqueueOp(context.Background(), a.c.ID, a.params)
	switch {
	case err != nil:
		a.w.t.Errorf("opstest: %s: enqueue the second run: %v", a.opID, err)
		return
	case id == a.opID:
		a.w.t.Errorf("opstest: %s: the second run was deduplicated onto the abandoned run", a.opID)
		return
	}
	if err := a.g.bind(id, sec); err != nil {
		a.w.t.Errorf("%v", err)
		return
	}
	rep.SecondQueued = a.g.waitListed(id)
	rep.SecondStartedWhileHeld = a.g.reg.IsRunning(id) || sec.started()
	a.mu.Lock()
	a.second, a.secondAt = rep, sec
	a.mu.Unlock()
	a.probeWrites(irc, ctx) // still ignoring ctx, still refused
}

// started reports whether the registry started the attempt.
func (a *attempt) started() bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.startSeq != 0
}

// probeWrites is the abandoned goroutine writing after the cancel: an Effect
// whose fn would record a write. The fence must refuse every one.
func (a *attempt) probeWrites(irc *ops.ItemRC, ctx context.Context) {
	wr := irc.Writer()
	if wr == nil {
		return
	}
	for range 2 {
		_ = wr.Effect(ctx, ops.EffectSpec{Name: "opstest.abandon-probe", Subject: abandonProbe},
			func() ([]ops.HistoryRow, error) {
				return nil, a.w.books.PutValue(abandonProbe, "written", "after-cancel")
			})
	}
}

// loseLease is LoseLeaseAt: the registry's stand-down clock jumps past the
// lease, so the holder's next renewal (the Writer renews on every write)
// finds it lapsed, and the registry revokes the run's fence, as it does on
// a lost stand-down lease, with no cancel behind it.
func (a *attempt) loseLease() {
	if !a.g.reg.ScanStandDownValid(a.opID) {
		a.note("LoseLeaseAt not tested: the run holds no scan stand-down")
		return
	}
	a.revokeWith(func() { a.g.sdSkew.Add(int64(2 * standDownLease)) })
}

// insertBelowCommitted is InsertBelowCursor while items run: once a range the
// ledger recorded holds two keys, a book is inserted strictly inside it
// (below the cursor), and the caller stops the world, so the resume's ledger
// skips the range and the inserted item with it.
func (a *attempt) insertBelowCommitted() bool {
	a.mu.Lock()
	if a.inserted != "" || a.committed == nil {
		a.mu.Unlock()
		return false
	}
	ranges := slices.Clone(a.committed.Ranges)
	a.mu.Unlock()
	return a.insertInside(ranges)
}

// insertInside inserts the fault's item strictly inside one of ranges and
// reports whether it did. The generated item is a copy of the range's first
// book with an id that sorts just after it; it assumes the def keys books by
// id (pass an item to InsertBelowCursor otherwise).
func (a *attempt) insertInside(ranges []database.KeyRange) bool {
	for _, r := range ranges {
		cand := r.First + "~"
		if !(r.First < cand && cand < r.Last) {
			continue
		}
		var book database.Book
		switch v := a.cfg.insert.item.(type) {
		case database.Book:
			book = v
		case *database.Book:
			book = *v
		case nil:
			src, err := a.w.st().GetBookByID(r.First)
			if err != nil || src == nil {
				a.note("InsertBelowCursor not applied: range key %q is not a book id; pass the item to insert", r.First)
				return false
			}
			book = *src
			book.ID, book.Title = cand, "Inserted below the cursor"
			book.FilePath = "/library/synthetic/" + cand
		default:
			a.note("InsertBelowCursor not applied: item %T is not a database.Book", v)
			return false
		}
		if err := a.w.books.InsertBook(book); err != nil {
			a.note("InsertBelowCursor not applied: insert %s: %v", book.ID, err)
			return false
		}
		a.mu.Lock()
		a.inserted = book.ID
		a.mu.Unlock()
		return true
	}
	return false
}
