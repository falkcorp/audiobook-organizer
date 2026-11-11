// file: core/internal/executor/dispatch.go
// version: 1.4.0
// guid: 159fa4ea-e5b1-45c1-abd9-61611bd6e318
// last-edited: 2026-11-11

package executor

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"time"

	"github.com/falkcorp/mediacore/entity"
	"github.com/falkcorp/mediacore/health"
	"github.com/falkcorp/mediacore/tasks"
)

// Dispatch deferral reasons: <ns>.ops.dispatch_deferred's reason values
// (5T.11).
const (
	deferKey       = "key"
	deferWriteSet  = "writeset"
	deferReady     = "ready"
	deferModuleCap = "module_cap"
	deferZombieCap = "zombie_cap"
	deferStandDown = "standdown"
	deferPaused    = "paused"
	deferHeld      = "held"
)

// passLimit bounds the queued runs one dispatch pass reads.
const passLimit = 1000

// Run implements tasks.Executor.Run (5T.4 step 2): the pause marker, the
// stand-down markers, the resume sweep, then the dispatcher and watchdog.
// It returns once they are started; they run until Shutdown (ctx only
// bounds the boot writes).
func (e *executor) Run(ctx context.Context) error {
	e.mu.Lock()
	if e.runCalled {
		e.mu.Unlock()
		return ErrAlreadyRunning
	}
	e.runCalled = true
	e.mu.Unlock()
	p, paused, err := e.opts.Stores.Pause.Get(ctx)
	if err != nil {
		return err
	}
	if paused {
		e.setPaused(true)
		e.log.Info("task executor starts paused", "reason", p.Reason)
	}
	if err := e.consultStandDowns(ctx); err != nil {
		return err
	}
	if err := e.resumeSweep(ctx); err != nil {
		return err
	}
	e.bg.Add(2)
	go e.dispatcher()
	go e.watchdog()
	e.wakeDispatcher()
	return nil
}

func (e *executor) dispatcher() {
	defer e.bg.Done()
	t := time.NewTicker(e.config().DispatchTick)
	defer t.Stop()
	for {
		select {
		case <-e.bgCtx.Done():
			return
		case <-t.C:
		case <-e.wake:
		}
		e.dispatchOnce(e.bgCtx)
	}
}

// candidate is a queued run read by one pass.
type candidate struct {
	rec tasks.RunRecord
	d   *def
}

// dispatchOnce is one pass over the queue in dispatch order (priority, then
// enqueue time): every gate of 5T.3.4/5T.4, then the claim.
func (e *executor) dispatchOnce(ctx context.Context) {
	e.reapHolds(ctx)
	e.releaseWaitingDeps(ctx)
	var cands []candidate
	depth := map[[2]string]int64{}
	for rec, err := range e.runs.Queued(ctx) {
		if err != nil {
			e.ins.undecodable("run")
			continue
		}
		depth[[2]string{rec.Kind.Module(), prioName(rec.Priority)}]++
		cands = append(cands, candidate{rec: rec, d: e.defs[rec.Kind]})
		if len(cands) >= passLimit {
			break
		}
	}
	e.ins.queueDepth(depth)
	for _, c := range cands {
		if ctx.Err() != nil {
			return
		}
		if c.d == nil {
			if e.held(c.rec.Kind) {
				e.deferRun(c.rec, deferHeld)
			}
			continue // unknown kind: the resume sweep's to drop at next boot
		}
		if e.held(c.rec.Kind) {
			e.deferRun(c.rec, deferHeld)
			continue
		}
		ws, err := e.writeSet(c.d, c.rec.Params)
		if err != nil {
			ws = c.d.writes
		}
		if reason, blocked := e.gate(c, ws); blocked {
			if reason == "notwhile" {
				e.toWaitingDeps(ctx, c.rec)
				continue
			}
			e.deferRun(c.rec, reason)
			continue
		}
		e.claim(ctx, c, ws)
	}
}

// gate applies the dispatch gates to one queued run. A gate that holds the
// run returns its deferral reason.
func (e *executor) gate(c candidate, ws []entity.Ref) (string, bool) {
	cfg := e.config()
	if !e.ready(c.d) {
		return deferReady, true
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.paused {
		return deferPaused, true
	}
	if _, inProc := e.running[c.rec.ID]; inProc {
		return "", true // a zombie of this run's last attempt, or a claim in flight
	}
	if h := e.holds[c.d.key]; h != nil && h.liveLocked(e.now()) {
		return deferStandDown, true
	}
	live, nonSystem, perModule := 0, 0, 0
	for _, a := range e.running {
		if a.d.key == c.d.key {
			return deferKey, true // zombies keep their key until their goroutine exits
		}
		if overlapsSet(a.writes, ws) {
			return deferWriteSet, true
		}
		if a.zombie {
			continue
		}
		live++
		if a.rec.Priority != tasks.PrioritySystem {
			nonSystem++
		}
		if a.d.module == c.d.module {
			perModule++
		}
	}
	if limit := cfg.ModuleMaxConcurrent[c.d.module]; limit > 0 && perModule >= limit {
		return deferModuleCap, true
	}
	if e.zombies[c.d.module] >= cfg.ZombieCap {
		return deferZombieCap, true
	}
	for _, a := range e.running {
		if !a.zombie && slices.Contains(c.d.c.NotWhile, a.d.kind) {
			return "notwhile", true
		}
	}
	if live >= cfg.Workers {
		return "", true
	}
	if c.rec.Priority != tasks.PrioritySystem && nonSystem >= cfg.Workers-cfg.SystemWorkers {
		return "", true
	}
	return "", false
}

// overlapsSet is the write-set conflict rule (5T.3.4): some ref of one set
// overlaps some ref of the other.
func overlapsSet(a, b []entity.Ref) bool {
	for _, x := range a {
		for _, y := range b {
			if tasks.Overlaps(x, y) {
				return true
			}
		}
	}
	return false
}

// deferRun counts a deferral once per run and reason (a reason change
// counts again), so a run held for an hour is one sample, not 36,000.
func (e *executor) deferRun(rec tasks.RunRecord, reason string) {
	if reason == "" {
		return
	}
	e.mu.Lock()
	same := e.deferred[rec.ID] == reason
	e.deferred[rec.ID] = reason
	e.mu.Unlock()
	if !same {
		e.ins.deferredRun(reason)
	}
}

// ready reports whether every Ready signal of d is Up or Degraded now
// (health.Registry.Wait on a done context answers without blocking). A
// pending set gets one waiter goroutine that wakes the dispatcher when it
// is satisfied; waiting runs hold no worker, key or clock (5T.4 step 2).
func (e *executor) ready(d *def) bool {
	reg := e.opts.Health
	if reg == nil {
		return true
	}
	ids := d.c.Ready
	if ids == nil {
		ids = []tasks.ReadySignal{tasks.SignalStorage}
	}
	done, cancel := context.WithCancel(context.Background())
	cancel()
	if reg.Wait(done, ids...) == nil {
		return true
	}
	parts := make([]string, len(ids))
	for i, id := range ids {
		parts[i] = string(id)
	}
	slices.Sort(parts)
	key := strings.Join(parts, "\x00")
	e.mu.Lock()
	if e.waiting[key] || e.shutting {
		e.mu.Unlock()
		return false
	}
	e.waiting[key] = true
	e.bg.Add(1)
	e.mu.Unlock()
	go func(ids []health.ComponentID) {
		defer e.bg.Done()
		err := reg.Wait(e.bgCtx, ids...)
		e.mu.Lock()
		delete(e.waiting, key)
		e.mu.Unlock()
		if err == nil {
			e.wakeDispatcher()
		}
	}(slices.Clone(ids))
	return false
}

// claim reserves the run in process (so the gates see its key and write
// set at once), then commits queued→running with attempt+1 and fence+1
// (5T.4 step 3) and starts the attempt. Losing the CAS drops the
// reservation.
func (e *executor) claim(ctx context.Context, c candidate, ws []entity.Ref) {
	a := &attempt{e: e, d: c.d, id: c.rec.ID, writes: ws, done: make(chan struct{})}
	e.mu.Lock()
	if e.shutting {
		e.mu.Unlock()
		return
	}
	e.running[a.id] = a
	delete(e.deferred, a.id)
	e.mu.Unlock()
	now := e.now()
	rec, err := e.move(ctx, a.id, func(cur tasks.RunRecord) (tasks.RunRecord, tasks.RunEvent, error) {
		if cur.State != tasks.StateQueued {
			return cur, tasks.RunEvent{}, errStale(cur, "claim")
		}
		next := cur
		next.State, next.Reason = tasks.StateRunning, ""
		next.Attempt, next.FenceEpoch = cur.Attempt+1, cur.FenceEpoch+1
		if next.Started.IsZero() {
			next.Started = now
		}
		return next, tasks.RunEvent{Event: "claim", At: now}, nil
	}, nil)
	if err != nil {
		e.mu.Lock()
		delete(e.running, a.id)
		e.mu.Unlock()
		if !errors.Is(err, tasks.ErrRunConflict) && ctx.Err() == nil {
			e.log.Warn("claim failed", "run", a.id, "err", err)
		}
		return
	}
	e.startAttempt(a, rec) // releases the claim instead when Shutdown overtook it
}

// release returns a claimed run that never started to the queue.
func (e *executor) release(a *attempt, rec tasks.RunRecord) {
	_, err := e.move(context.Background(), a.id, func(cur tasks.RunRecord) (tasks.RunRecord, tasks.RunEvent, error) {
		if cur.State != tasks.StateRunning || cur.Attempt != rec.Attempt || cur.FenceEpoch != rec.FenceEpoch {
			return cur, tasks.RunEvent{}, errStale(cur, "release")
		}
		next := cur
		next.State = tasks.StateQueued
		next.Attempt = cur.Attempt - 1
		return next, tasks.RunEvent{Event: "release"}, nil
	}, nil)
	if err != nil {
		e.log.Warn("claimed run not released; the resume sweep re-queues it at next boot", "run", a.id, "err", err)
	}
	e.mu.Lock()
	delete(e.running, a.id)
	e.mu.Unlock()
}

// startAttempt builds the attempt's context, clock and resume state and
// starts its goroutine and supervisor. A claim that Shutdown or a
// stand-down of its key overtook is released to the queue instead
// (claimed-not-started, 5T.4 step 4; the dispatch gate then defers it while
// the hold lives). Both checks and a.live are one decision under e.mu, the
// lock Shutdown takes its view of the live attempts under and standDown
// sets its hold and picks its victims under: an attempt is either in that
// view (so it is canceled, or quiesced, like any live run) or never goes
// live. A check made before this critical section would let an attempt go
// live just after the view and run on past Shutdown, or beside a hold on
// its key, with its fence intact.
func (e *executor) startAttempt(a *attempt, rec tasks.RunRecord) {
	start := e.now()
	deadline := start.Add(a.d.timeout)
	zeroFrom := e.zeroFrom(a.id, rec.Attempt)
	base, cancel := context.WithCancelCause(context.WithoutCancel(e.bgCtx))
	ctx, stop := context.WithDeadlineCause(base, deadline, tasks.ErrTimedOut)
	a.lastReport.Store(start.UnixNano())
	// Publish every field the gates, cancel, the watchdog and the metrics
	// callback read in one critical section: they read them under e.mu
	// (or after taking the attempt from e.running under it).
	e.mu.Lock()
	if h := e.holds[a.d.key]; e.shutting || h != nil && h.liveLocked(start) {
		e.mu.Unlock()
		cancel(nil)
		stop()
		e.release(a, rec)
		e.wakeDispatcher() // the gate decides again: deferred while the hold lives
		return
	}
	a.rec, a.n, a.fence = rec, rec.Attempt, rec.FenceEpoch
	a.log = e.log.With("run_id", string(a.id), "kind", string(a.d.kind), "attempt", rec.Attempt)
	a.start, a.deadline, a.zeroFrom = start, deadline, zeroFrom
	a.resumed = rec.Attempt > 1 && rec.Attempt != zeroFrom
	a.ctx = ctx
	a.cancel = func(cause error) { cancel(cause); stop() }
	a.live = true
	a.pmu.Lock()
	// The run's stored progress is where a resumed attempt starts, so one
	// that reports nothing does not zero it at its end (ResumeFromZero's
	// requeue has already zeroed it).
	a.pDone, a.pTotal, a.pCurrent = rec.Done, rec.Total, rec.Current
	a.pmu.Unlock()
	pending := a.stopCause // a stop that committed while the attempt was claimed but not live
	e.mu.Unlock()
	if pending != nil {
		a.cancel(pending)
	}
	e.ins.runStarted(a)
	e.bg.Add(1)
	go a.supervise()
	go a.run(pending == nil)
}

// zeroFrom is the attempt from which ResumeFromZero voids earlier
// checkpoints (0: none voided). A marker that cannot be read or decoded
// voids every checkpoint before this attempt, so a broken marker can only
// redo work, never resume from voided state.
func (e *executor) zeroFrom(id tasks.RunID, attempt int) int {
	raw, ok, err := e.runs.Checkpoint(context.Background(), id, ckptZero)
	if err == nil && !ok {
		return 0
	}
	var n int
	if err == nil {
		err = json.Unmarshal(unwrapData(raw), &n)
	}
	if err != nil {
		e.log.Warn("from-zero marker unreadable; earlier checkpoints are void for this attempt", "run", string(id), "err", err)
		return attempt
	}
	return n
}

// unwrapData is a checkpoint's data, wrapped or not.
func unwrapData(raw []byte) []byte {
	d, _, _ := unwrapCkpt(raw)
	return d
}

// attemptExited runs when an attempt goroutine returns: the run leaves the
// in-process set (a zombie frees its key, write set and zombie-cap slot
// now), its ref locks and stand-down holds are released.
func (e *executor) attemptExited(a *attempt) {
	a.releaseLocks()
	e.releaseHolds(a)
	e.mu.Lock()
	a.exited = true
	if e.running[a.id] == a {
		delete(e.running, a.id)
	}
	if a.zombie {
		e.zombies[a.d.module]--
		if e.zombies[a.d.module] <= 0 {
			delete(e.zombies, a.d.module)
		}
	}
	e.mu.Unlock()
	close(a.done)
	a.cancel(context.Canceled)
	e.wakeDispatcher()
}

// abandon revokes an attempt's fence and ends its row when its body did not
// honour a stop within the grace (5T.3.6): the in-process fence first, under
// the per-run writer, so no ledger write is in flight; then the CAS with
// FenceEpoch+1, so a stale write of this attempt fails the store's check
// too; the goroutine is a zombie until it exits.
func (e *executor) abandon(a *attempt, state tasks.RunState, reason string) {
	a.wmu.Lock()
	already := a.fenced.Swap(true)
	a.wmu.Unlock()
	if already {
		return
	}
	e.mu.Lock()
	if a.exited {
		e.mu.Unlock()
		return
	}
	a.zombie = true
	e.zombies[a.d.module]++
	e.mu.Unlock()
	_, err := e.move(context.Background(), a.id, func(cur tasks.RunRecord) (tasks.RunRecord, tasks.RunEvent, error) {
		if cur.State.Terminal() || cur.Attempt != a.n || cur.FenceEpoch != a.fence {
			return cur, tasks.RunEvent{}, errStale(cur, "abandon")
		}
		next := cur
		next.State, next.Reason = state, reason
		next.FenceEpoch = cur.FenceEpoch + 1
		if state.Terminal() {
			next.Finished = e.now()
		}
		return next, tasks.RunEvent{Event: "abandon", Reason: reason}, nil
	}, nil)
	if err != nil && !errors.Is(err, tasks.ErrRunConflict) {
		a.log.Error("abandoned attempt's row not ended", "err", err)
	}
	a.log.Warn("attempt abandoned after its grace; its goroutine is a zombie until it returns", "state", state, "grace", a.d.grace)
	e.wakeDispatcher()
}

// toWaitingDeps moves a queued run whose NotWhile kinds are running to
// waiting_deps; releaseWaitingDeps moves it back when they are not.
func (e *executor) toWaitingDeps(ctx context.Context, rec tasks.RunRecord) {
	_, err := e.move(ctx, rec.ID, func(cur tasks.RunRecord) (tasks.RunRecord, tasks.RunEvent, error) {
		if cur.State != tasks.StateQueued {
			return cur, tasks.RunEvent{}, errStale(cur, "wait")
		}
		next := cur
		next.State, next.Reason = tasks.StateWaitingDeps, "not_while"
		return next, tasks.RunEvent{Event: "wait", Reason: "not_while"}, nil
	}, nil)
	if err != nil && !errors.Is(err, tasks.ErrRunConflict) {
		e.log.Warn("run not moved to waiting_deps; the next dispatch pass retries", "run", string(rec.ID), "err", err)
	}
}

func (e *executor) releaseWaitingDeps(ctx context.Context) {
	q := tasks.RunQuery{States: []tasks.RunState{tasks.StateWaitingDeps}, Limit: passLimit}
	var ids []tasks.RunID
	for rec, err := range e.runs.List(ctx, q) {
		if err != nil {
			e.ins.undecodable("run")
			continue
		}
		d := e.defs[rec.Kind]
		if d == nil || e.held(rec.Kind) {
			continue
		}
		e.mu.Lock()
		blocked := false
		for _, a := range e.running {
			if !a.zombie && slices.Contains(d.c.NotWhile, a.d.kind) {
				blocked = true
			}
		}
		e.mu.Unlock()
		if !blocked {
			ids = append(ids, rec.ID)
		}
	}
	for _, id := range ids {
		_, err := e.move(ctx, id, func(cur tasks.RunRecord) (tasks.RunRecord, tasks.RunEvent, error) {
			if cur.State != tasks.StateWaitingDeps {
				return cur, tasks.RunEvent{}, errStale(cur, "unwait")
			}
			next := cur
			next.State, next.Reason = tasks.StateQueued, ""
			return next, tasks.RunEvent{Event: "unwait"}, nil
		}, nil)
		if err != nil && !errors.Is(err, tasks.ErrRunConflict) {
			e.log.Warn("waiting run not released; the next dispatch pass retries", "run", string(id), "err", err)
		}
	}
}

// keyHeld reports whether a live stand-down hold covers key.
func (e *executor) keyHeld(key tasks.Key) bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	h := e.holds[key]
	return h != nil && h.liveLocked(e.now())
}

// setPaused flips the in-process pause gate.
func (e *executor) setPaused(p bool) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if p == e.paused {
		return
	}
	e.paused = p
	if p {
		e.unpaused = make(chan struct{})
	} else {
		close(e.unpaused)
	}
}

// awaitUnpaused parks a caller (an item before its clock starts) while the
// executor is paused.
func (e *executor) awaitUnpaused(ctx context.Context) error {
	e.mu.Lock()
	ch := e.unpaused
	e.mu.Unlock()
	select {
	case <-ch:
		return nil
	case <-ctx.Done():
		return context.Cause(ctx)
	}
}

// await is rc.Await (HEAD's childop, re-implemented): it returns the run's
// outcome once it is terminal; ctx ends the wait.
func (e *executor) await(ctx context.Context, id tasks.RunID) (tasks.Outcome, error) {
	for {
		sig := e.termSignal()
		rec, err := e.runs.Get(ctx, id)
		if err != nil {
			return tasks.Outcome{}, err
		}
		if rec.State.Terminal() {
			return tasks.Outcome{State: rec.State, Result: rec.Result, Err: rec.Error}, nil
		}
		select {
		case <-sig:
		case <-ctx.Done():
			return tasks.Outcome{}, context.Cause(ctx)
		}
	}
}
