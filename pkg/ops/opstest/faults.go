// file: pkg/ops/opstest/faults.go
// version: 1.0.0
// guid: 3f7b1d94-6a25-4c08-9e3f-8d2b6a4c1e73
// last-edited: 2026-10-14

package opstest

import (
	"fmt"
	"slices"
	"sync"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/operations/state"
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

// CancelAt cancels the run when item n starts, the way the registry does: the
// run record moves to stopping, the fence is revoked, then the context is
// canceled. Item n still runs, on a canceled context. Finish runs with
// Canceled true and the partial Summary the ledger folded.
func CancelAt(item int) RunOpt { return func(c *runCfg) { c.cancelAt = item } }

// CrashAt stops the world immediately after the item with global start index
// n returns: every goroutine is released, no further store write is accepted
// and the chunk counters buffered in memory are lost. Outcome.Resume()
// continues with a new attempt of the same op id over the surviving store.
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
// run is canceled (stopping, zombie), and the goroutine keeps trying to write
// through the run's Writer instead of returning. While it does, a second run
// of the same def is started; it must stay queued on the def's exclusive key
// until the abandoned goroutine returns (Outcome.Abandon). Every write the
// abandoned goroutine tries must be refused by the fence.
func AbandonAt(item int) RunOpt { return func(c *runCfg) { c.abandonAt = item } }

// LoseLeaseAt makes the fake scan stand-down report the holder's lease lost
// when item n starts (the stand-down lease, not a chunk lease: chunk leases
// are never reassigned while the owner lives). As the registry does, the
// run's fence is revoked with no cancel behind it; no later write may
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
	if !final || a.cfg.insert == nil || a.order != ops.AppendOnly {
		return false
	}
	a.mu.Lock()
	done := a.inserted != ""
	a.mu.Unlock()
	if done {
		return false
	}
	if a.insertInside(l.Ranges) {
		a.crashAfterCommit.Store(true)
	} else {
		a.note("InsertBelowCursor not applied: no completed range holds two keys to insert between")
	}
	return false
}

// crash stops the world: no write in flight, none accepted after.
func (a *attempt) crash() {
	a.w.freeze()
	a.crashed.Store(true)
}

// quiesce is CrashMidChunk's strong form: the worker holding chunk c, item i
// done, waits until every other worker has exited and the ledger has
// committed every chunk they completed. The world then stops with chunk c the
// only unrecorded chunk, so a correct resume leases c and only c.
func (a *attempt) quiesce(w int) {
	if a.sched != nil {
		a.sched.exit(w) // let the others run while this worker holds its chunk
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	for a.exited < a.workers-1 || a.firstDone == nil || a.lastDone != *a.firstDone+a.delivered {
		a.cond.Wait()
	}
}

// stop requests a stop the way the registry does: the run record moves to
// stopping, the fence is revoked (after the Writer calls in flight drain),
// then the context is canceled.
func (a *attempt) stop(reason string, zombie bool) {
	a.mu.Lock()
	if a.stopped {
		a.mu.Unlock()
		return
	}
	a.stopped = true
	a.mu.Unlock()
	if _, err := a.w.transition(a.opID, database.TransitionRequest{Event: state.EvStopRequested, StopReason: reason,
		Zombie: zombie}); err != nil {
		a.w.t.Errorf("opstest: stop %s: %v", a.opID, err)
	}
	a.revokeFence()
	a.cancel()
}

// revokeFence revokes the run's fence once the Writer calls in flight have
// returned, and logs the revoke in the commit sequence.
func (a *attempt) revokeFence() {
	if a.tracked != nil {
		a.tracked.drain.Lock()
		defer a.tracked.drain.Unlock()
	}
	a.revoke()
	seq := a.w.log.next()
	a.mu.Lock()
	if a.revokedAt == 0 {
		a.revokedAt = seq
	}
	a.mu.Unlock()
}

// abandonProbe is the subject AbandonAt's ignored-context writes target.
var abandonProbe = ops.Subject{Type: ops.SubjectBook, ID: "opstest-abandon-probe"}

// abandon is AbandonAt: on the item's own goroutine, the run is canceled and
// the goroutine keeps trying to write through the run's Writer instead of
// returning. While it does, a second run of the same def is started and must
// wait queued for the exclusive key.
func (a *attempt) abandon(irc *ops.ItemRC) {
	a.stop(state.StopUser, true)
	a.probeWrites(irc)
	rep := &secondRun{done: make(chan secondResult, 1)}
	a.mu.Lock()
	a.second = rep
	a.mu.Unlock()
	queued, started := make(chan struct{}), make(chan struct{})
	var qOnce, sOnce sync.Once
	def, params := a.def, a.params
	opID := fmt.Sprintf("opstest-%04d", a.w.opSeq.Add(1))
	go func() {
		out, err := a.w.attempt(def, params, opID, false, newRunCfg(a.cfg.carry()),
			func() { rep.queued.Store(true); qOnce.Do(func() { close(queued) }) },
			func() { sOnce.Do(func() { close(started) }) })
		rep.done <- secondResult{o: out, err: err}
	}()
	select {
	case <-queued:
	case <-started:
	}
	select {
	case <-started:
		rep.startedWhileHeld = true
	default:
	}
	a.probeWrites(irc) // still ignoring ctx, still refused
}

// probeWrites is the abandoned goroutine writing after the cancel: an Effect
// whose fn would record a write. The fence must refuse every one.
func (a *attempt) probeWrites(irc *ops.ItemRC) {
	wr := irc.Writer()
	if wr == nil {
		return
	}
	for range 2 {
		_ = wr.Effect(irc.Context(), ops.EffectSpec{Name: "opstest.abandon-probe", Subject: abandonProbe},
			func() ([]ops.HistoryRow, error) {
				return nil, a.w.books.PutValue(abandonProbe, "written", "after-cancel")
			})
	}
}

// loseLease is LoseLeaseAt: the fake stand-down reports the lease lost and,
// as the registry does on a lost stand-down lease, the run's fence is revoked
// (with no cancel behind it).
func (a *attempt) loseLease() {
	if !a.sd.isHeld() {
		a.note("LoseLeaseAt not tested: the run holds no scan stand-down")
		return
	}
	a.sd.lose()
	a.revokeFence()
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
