// file: core/audit/async.go
// version: 1.0.2
// guid: 9cae4d73-00cd-4cab-8d23-e3d19b224b56
// last-edited: 2026-11-10

package audit

import (
	"context"
	"time"

	"github.com/falkcorp/mediacore/entity"
)

// drainBudget bounds how long Stop lets the writer drain the queue (05-rbac
// §5 item 7); what is left after it is counted as dropped.
const drainBudget = 5 * time.Second

// sink persists the async writer's batches. It assigns each record its Seq,
// in order, and writes the records and their index rows together or not at
// all. storeSink is the store's; the tests that run the writer under
// testing/synctest, where Pebble's own goroutines could never be durably
// blocked, supply an in-memory one.
type sink interface {
	write(ctx context.Context, recs []Record) error
}

// Emit queues e for the writer and returns at once (05-rbac §7.3). It never
// blocks: a full queue, a stopped store or an event that fails the action or
// outcome rules drops the event and counts it in audit_dropped_total
// (fail-open: availability under a credential-stuffing burst beats
// completeness; the throttle and the access log still see it). The writer
// takes the seq, so Emit never waits on a sequence lease.
func (s *Store) Emit(ctx context.Context, e Event) {
	rec, err := s.build(ctx, e)
	if err != nil {
		s.ctr.drop(1)
		s.log.WarnContext(ctx, "audit: event refused and dropped", "action", e.Action, "err", err)
		return
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.state == stateStopped {
		s.ctr.drop(1)
		return
	}
	select {
	case s.q <- rec:
	default:
		s.ctr.drop(1)
	}
}

// Start launches the one async writer goroutine (05-rbac §5 item 7). It runs
// until Stop, or until ctx ends (the app context), which stops it as Stop
// would: the queue is drained, bounded by drainBudget. A store starts once.
func (s *Store) Start(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	switch s.state {
	case stateRunning:
		return ErrStarted
	case stateStopped:
		return ErrStopped
	}
	s.state = stateRunning
	s.ran = true
	go s.run(ctx)
	return nil
}

// Stop ends the writer: no later Emit is queued, the queue is drained and
// written, bounded by drainBudget (5 s), and what is left is counted as
// dropped. It returns when the writer has exited, or when ctx ends first (the
// writer then drops what it has not written and exits on its own). Stop on a
// store that never started counts its queue as dropped. Stop may be called
// again; it waits for the same exit.
func (s *Store) Stop(ctx context.Context) error {
	s.mu.Lock()
	first := s.state != stateStopped
	ran := s.ran
	s.state = stateStopped
	s.mu.Unlock()
	if !ran {
		if first {
			s.ctr.drop(len(s.q))
			for len(s.q) > 0 {
				<-s.q
			}
		}
		return nil
	}
	s.stopOnce.Do(func() { close(s.stop) })
	select {
	case <-s.done:
		s.abandonCancel()
		return nil
	case <-ctx.Done():
		s.abandonCancel()
		return ctx.Err()
	}
}

// requestStop is what the writer does when Start's context ends: the same as
// Stop's first half.
func (s *Store) requestStop() {
	s.mu.Lock()
	s.state = stateStopped
	s.mu.Unlock()
	s.stopOnce.Do(func() { close(s.stop) })
}

// run is the writer goroutine.
func (s *Store) run(startCtx context.Context) {
	defer close(s.done)
	defer s.abandonCancel()
	w := &writer{s: s, folds: map[foldKey]*fold{}}
	defer w.stopTimers()
	for {
		select {
		case rec := <-s.q:
			w.accept(s.abandonCtx, rec)
		case <-w.batchC():
			w.batchOn = false
			w.flush(s.abandonCtx)
		case <-w.foldC():
			w.foldOn = false
			w.expire()
		case <-s.stop:
			w.drain()
			return
		case <-startCtx.Done():
			s.requestStop()
			w.drain()
			return
		}
		w.rearm()
	}
}

// foldKey is what makes two denials identical: 05-rbac §7.3's actor, route,
// permission, outcome, target and reason, with the actor as user, credential
// and address (so anonymous callers are told apart) and the action beside
// them (a login denial and a role denial never fold). Target is in the key
// because a route is a pattern: without it a caller walking many targets
// through one route inside the window would show only the first.
type foldKey struct {
	action, userID, credential, addr, route, permission, reason string
	target                                                      entity.Ref
	outcome                                                     Outcome
}

func foldKeyOf(r Record) foldKey {
	return foldKey{
		action: r.Action, userID: r.Actor.UserID, credential: r.Actor.CredentialID, addr: r.Actor.ClientAddr,
		route: r.Route, permission: r.Permission, reason: r.Reason, target: r.Target, outcome: r.Outcome,
	}
}

// fold is a run of identical denials held until its window ends.
type fold struct {
	rec      Record // the first one's
	n        uint32
	deadline time.Time
}

// writer is the writer goroutine's state; only that goroutine touches it.
type writer struct {
	s     *Store
	ready []Record // finished records not yet written
	folds map[foldKey]*fold
	foldQ []*fold // the open folds, oldest first: one window, so deadline order

	batchT, foldT   *time.Timer
	batchOn, foldOn bool
}

func (w *writer) batchC() <-chan time.Time {
	if w.batchOn {
		return w.batchT.C
	}
	return nil
}

func (w *writer) foldC() <-chan time.Time {
	if w.foldOn {
		return w.foldT.C
	}
	return nil
}

func (w *writer) stopTimers() {
	if w.batchT != nil {
		w.batchT.Stop()
	}
	if w.foldT != nil {
		w.foldT.Stop()
	}
}

// accept takes one queued record: a denial joins its open fold, or opens one;
// anything else is ready to write.
func (w *writer) accept(ctx context.Context, rec Record) {
	s := w.s
	if rec.Outcome == Denied && s.opts.CoalesceWindow > 0 {
		w.expire()
		k := foldKeyOf(rec)
		if f, ok := w.folds[k]; ok {
			f.n++
			return
		}
		// The open folds are bounded by AsyncBuffer, so memory stays bounded
		// whatever the cardinality of the keys (a flood of distinct addresses,
		// credentials or routes): at the cap the oldest fold ends early and is
		// written as it stands. That loses no record; it only folds fewer
		// denials into it.
		for len(w.foldQ) >= s.opts.AsyncBuffer {
			oldest := w.foldQ[0]
			w.foldQ[0] = nil
			w.foldQ = w.foldQ[1:]
			w.finish(oldest)
		}
		f := &fold{rec: rec, n: 1, deadline: rec.Time.Add(s.opts.CoalesceWindow)}
		w.folds[k] = f
		w.foldQ = append(w.foldQ, f)
		if len(w.ready) >= s.opts.BatchMax {
			w.flush(ctx)
		}
		return
	}
	w.ready = append(w.ready, rec)
	if len(w.ready) >= s.opts.BatchMax {
		w.flush(ctx)
	}
}

// expire finishes every fold whose window has ended: one record carrying how
// many denials it stands for.
func (w *writer) expire() {
	now := w.s.now()
	for len(w.foldQ) > 0 && !w.foldQ[0].deadline.After(now) {
		w.finish(w.foldQ[0])
		w.foldQ = w.foldQ[1:]
	}
}

// finish moves a fold into the ready list. Coalesced is set only when denials
// were folded (n > 1); a lone denial is an ordinary record.
func (w *writer) finish(f *fold) {
	rec := f.rec
	if f.n > 1 {
		rec.Coalesced = f.n
	}
	if w.folds[foldKeyOf(rec)] == f {
		delete(w.folds, foldKeyOf(rec))
	}
	w.ready = append(w.ready, rec)
}

// rearm sets the two timers from the state: the batch timer runs while
// records wait, the fold timer until the oldest open fold ends.
func (w *writer) rearm() {
	s := w.s
	if len(w.ready) > 0 && !w.batchOn {
		w.batchOn = true
		if w.batchT == nil {
			w.batchT = time.NewTimer(s.opts.BatchWait)
		} else {
			w.batchT.Reset(s.opts.BatchWait)
		}
	}
	if len(w.foldQ) == 0 {
		if w.foldOn {
			w.foldT.Stop()
			w.foldOn = false
		}
		return
	}
	d := max(w.foldQ[0].deadline.Sub(s.now()), 0)
	w.foldOn = true
	if w.foldT == nil {
		w.foldT = time.NewTimer(d)
	} else {
		w.foldT.Reset(d)
	}
}

// flush writes the ready records in batches of at most BatchMax. A batch that
// fails is lost, not retried: the failure is counted and logged, and the
// events in it are counted as dropped (fail-open, §8).
func (w *writer) flush(ctx context.Context) {
	if w.batchOn {
		w.batchT.Stop()
		w.batchOn = false
	}
	recs := w.ready
	w.ready = nil
	for len(recs) > 0 {
		n := min(len(recs), w.s.opts.BatchMax)
		w.write(ctx, recs[:n])
		recs = recs[n:]
	}
}

// write sends one batch to the sink and counts the outcome.
func (w *writer) write(ctx context.Context, recs []Record) {
	s := w.s
	if err := s.sink.write(ctx, recs); err != nil {
		s.ctr.writeError(pathAsync)
		s.ctr.drop(len(recs))
		s.log.WarnContext(ctx, "audit: async batch lost", "records", len(recs), "err", err)
		return
	}
	for _, r := range recs {
		s.ctr.record(r)
	}
}

// drain is the writer's last act: everything queued is taken (denials still
// fold), every open fold is finished whatever its window, and all of it is
// written, within drainBudget. What the budget or an abandoned Stop leaves is
// counted as dropped.
func (w *writer) drain() {
	s := w.s
	ctx, cancel := context.WithTimeout(s.abandonCtx, drainBudget)
	defer cancel()
	for done := false; !done; {
		select {
		case rec := <-s.q:
			w.accept(ctx, rec)
		default:
			done = true
		}
	}
	for _, f := range w.foldQ {
		w.finish(f)
	}
	w.foldQ = nil
	recs := w.ready
	w.ready = nil
	for len(recs) > 0 {
		if ctx.Err() != nil {
			s.ctr.drop(len(recs))
			s.log.Warn("audit: shutdown drain ended with records unwritten", "dropped", len(recs))
			return
		}
		n := min(len(recs), s.opts.BatchMax)
		w.write(ctx, recs[:n])
		recs = recs[n:]
	}
}
