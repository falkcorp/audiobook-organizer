// file: core/internal/executor/attempt.go
// version: 1.2.0
// guid: 4ad76723-6fc7-4bac-840b-f169142b4af9
// last-edited: 2026-11-10

package executor

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"runtime/debug"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/falkcorp/mediacore/entity"
	"github.com/falkcorp/mediacore/storage"
	"github.com/falkcorp/mediacore/tasks"
)

// Reserved checkpoint names: the executor's own records. storedName refuses
// a run body any name with the "core." prefix.
const (
	ckptZero     = "core.zero"     // the attempt from which earlier checkpoints are void (ResumeFromZero)
	ckptApproved = "core.approved" // a Fixer apply run's approved rows
	ckptRC       = "core.rc"       // RC.Checkpoint's value (the SDK passes the name "", which the store refuses)
	chunkAttempt = "core.attempt"  // Chunk.Counters entry: the attempt that wrote the chunk
)

// Stop reasons: the RunView.Reason tokens the executor writes.
const (
	reasonCancel   = "cancel"
	reasonShutdown = "shutdown"
	reasonQuiesce  = "quiesce"
	reasonTimeout  = "timeout"
	reasonError    = "error"
	reasonWatchdog = "watchdog"
)

// attempt is one claimed attempt of a run: the RunHost behind its RC and
// ItemRCs and the per-run writer (5T.7) its ledger writes go through.
type attempt struct {
	e      *executor
	d      *def
	id     tasks.RunID
	rec    tasks.RunRecord // as claimed
	n      int
	fence  uint64
	writes []entity.Ref
	log    *slog.Logger

	zeroFrom int // checkpoints and chunks of earlier attempts are void
	resumed  bool
	start    time.Time
	deadline time.Time

	ctx    context.Context
	cancel context.CancelCauseFunc
	done   chan struct{}

	fenced     atomic.Bool
	wmu        sync.Mutex // the per-run writer: every ledger write of this run
	lastReport atomic.Int64
	reported   atomic.Bool
	progressed atomic.Bool
	ckptd      atomic.Bool
	struckCkpt atomic.Bool
	seq        atomic.Uint64 // intent and log page numbering within this attempt

	pmu      sync.Mutex
	pDone    int
	pTotal   int
	pCurrent string
	pDirty   bool
	pLast    time.Time
	pending  map[string][]byte
	ckptLast time.Time

	lmu   sync.Mutex
	locks map[uint64]func()
	lnext uint64

	// Guarded by e.mu.
	stopping string // "", cancel, shutdown, quiesce, watchdog
	zombie   bool
	exited   bool
	holdLost bool
	live     bool // startAttempt published the fields above

	holds []*standHold
}

// ckptEnvelope wraps every checkpoint this executor writes with the attempt
// that wrote it, so ResumeFromZero can void earlier ones without deleting.
type ckptEnvelope struct {
	A int             `json:"core_attempt"`
	V json.RawMessage `json:"v"`
}

// --- tasks.RunHost ---

func (a *attempt) Context() context.Context { return a.ctx }
func (a *attempt) RunID() tasks.RunID       { return a.id }
func (a *attempt) Kind() tasks.Kind         { return a.d.kind }
func (a *attempt) Mode() tasks.Mode         { return a.rec.Mode }
func (a *attempt) Trigger() tasks.Trigger   { return a.rec.Trigger }
func (a *attempt) Actor() tasks.Actor       { return a.rec.Actor }
func (a *attempt) Log() *slog.Logger        { return a.log }
func (a *attempt) Writer() tasks.Writer     { return &writer{a: a} }
func (a *attempt) Resumed() bool            { return a.resumed }
func (a *attempt) Deadline() time.Time      { return a.deadline }

// ShouldYield: Deadline − now < max(10% of Timeout, 1 min) (5T.3.6).
func (a *attempt) ShouldYield() bool {
	return a.deadline.Sub(a.e.now()) < max(a.d.timeout/10, time.Minute)
}

func (a *attempt) Phase(name string) (end func()) {
	a.log.Debug("phase", "phase", name)
	return func() {}
}

// Checkpoint stages v under name, throttled by min_checkpoint_interval: the
// newest value of each name waits in memory until the interval has passed
// and is flushed with the next write or when the attempt ends. A write
// error fails a ResumeContinue run (resume would be unsound) and is a WARN
// otherwise (5T.5).
func (a *attempt) Checkpoint(name string, v any) error {
	name, err := storedName(name)
	if err != nil {
		return err
	}
	raw, err := json.Marshal(v)
	if err != nil {
		return fmt.Errorf("executor: checkpoint %q: %w", name, err)
	}
	env, err := json.Marshal(ckptEnvelope{A: a.n, V: raw})
	if err != nil {
		return err
	}
	a.pmu.Lock()
	if a.pending == nil {
		a.pending = map[string][]byte{}
	}
	a.pending[name] = env
	due := a.e.now().Sub(a.ckptLast) >= a.e.config().MinCheckpointInterval
	a.pmu.Unlock()
	a.ckptd.Store(true)
	if !due {
		return nil
	}
	return a.ckptResult(a.flushLedger(a.ctx))
}

// storedName maps a run body's checkpoint name to the stored one: "" (the
// name RC.Checkpoint and RC.Restore pass) is ckptRC, and a name in the
// executor's reserved "core." space is refused, so a body can neither
// forge nor read the executor's own records (an approval, the zero mark).
func storedName(name string) (string, error) {
	switch {
	case name == "":
		return ckptRC, nil
	case strings.HasPrefix(name, "core."):
		return "", fmt.Errorf("%w: %q", ErrReservedCheckpoint, name)
	}
	return name, nil
}

func (a *attempt) ckptResult(err error) error {
	if err == nil {
		return nil
	}
	if a.d.c.Resume == tasks.ResumeContinue {
		return fmt.Errorf("executor: checkpoint write: %w", err)
	}
	a.log.Warn("checkpoint write failed; the run continues (its resume policy is not continue)", "err", err)
	return nil
}

// Restore reads name: this attempt's newest pending value first, then the
// stored one unless a ResumeFromZero voided it.
func (a *attempt) Restore(name string, v any) (bool, error) {
	name, err := storedName(name)
	if err != nil {
		return false, err
	}
	a.pmu.Lock()
	env, ok := a.pending[name]
	a.pmu.Unlock()
	if !ok {
		raw, found, err := a.e.runs.Checkpoint(a.ctx, a.id, name)
		if err != nil {
			return false, err
		}
		if !found || len(raw) == 0 {
			return false, nil
		}
		env = raw
	}
	data, attemptOf, wrapped := unwrapCkpt(env)
	if wrapped && attemptOf < a.zeroFrom || !wrapped && a.zeroFrom > 0 {
		return false, nil
	}
	if err := json.Unmarshal(data, v); err != nil {
		return false, fmt.Errorf("executor: restore %q: %w", name, err)
	}
	return true, nil
}

// unwrapCkpt returns a checkpoint's data and writing attempt; a value this
// executor did not wrap (a run the cutover imported, 5T.3.6) is returned as
// is with wrapped false.
func unwrapCkpt(raw []byte) (data []byte, attemptOf int, wrapped bool) {
	var env ckptEnvelope
	if json.Unmarshal(raw, &env) == nil && env.A > 0 && env.V != nil {
		return env.V, env.A, true
	}
	return raw, 0, false
}

// ReportProgress records progress; the per-run writer persists it at most
// once per ProgressInterval (the newest numbers win) and at attempt end.
func (a *attempt) ReportProgress(done int64, total tasks.Total) {
	n, known := total.Value()
	a.progress(int(done), func() int {
		if known {
			return int(n)
		}
		return 0
	}(), "", false)
}

func (a *attempt) ItemsProgress(done, total int64, current string) {
	a.progress(int(done), int(total), current, true)
}

func (a *attempt) progress(done, total int, current string, setCurrent bool) {
	a.Touch()
	if done > a.rec.Done {
		a.progressed.Store(true) // past this attempt's starting point: ErrContinueInPlace's first proof
	}
	now := a.e.now()
	a.pmu.Lock()
	a.pDone, a.pTotal, a.pDirty = done, total, true
	if setCurrent {
		a.pCurrent = current
	}
	due := now.Sub(a.pLast) >= a.e.config().ProgressInterval
	if due {
		a.pLast = now
	}
	a.pmu.Unlock()
	if due {
		a.tick(a.ctx)
	}
}

// Note sets the run's current label and appends a log line.
func (a *attempt) Note(msg string) {
	a.Touch()
	a.pmu.Lock()
	a.pCurrent, a.pDirty = msg, true
	a.pmu.Unlock()
	line := tasks.LogLine{At: a.e.now(), Level: "info", Message: msg}
	_ = a.ledger(a.ctx, func(ctx context.Context, b *storage.Batch) error {
		return a.e.runs.AppendLog(ctx, b, a.id, a.nextSeq(), line)
	})
}

// Touch is liveness only (the watchdog's input).
func (a *attempt) Touch() {
	a.lastReport.Store(a.e.now().UnixNano())
	a.reported.Store(true)
}

// nextSeq numbers an intent or log entry: the attempt in the high half, so
// entries of different attempts never collide and none needs a stored
// counter.
func (a *attempt) nextSeq() uint64 { return uint64(a.n)<<32 | a.seq.Add(1) }

func (a *attempt) Enqueue(kind tasks.Kind, params any, opts ...tasks.StartOption) (tasks.RunRef, error) {
	raw, err := json.Marshal(params)
	if err != nil {
		return tasks.RunRef{}, fmt.Errorf("executor: enqueue %s: params: %w", kind, err)
	}
	req := tasks.StartRequest{Params: raw, Parent: a.id, Actor: a.rec.Actor, Trigger: tasks.TriggerParent}
	for _, o := range opts {
		o(&req)
	}
	if _, ok := req.Mode.Mode(); !ok {
		req.Mode = tasks.ModeRequestLive
		if a.rec.Mode == tasks.ModePreview {
			req.Mode = tasks.ModeRequestPreview
		}
	}
	var ref tasks.RunRef
	err = a.e.startLocked(a.ctx, kind, req, nil, modeRequired, func(p prepared) error {
		return a.e.write(a.ctx, func(ctx context.Context, b *storage.Batch) error {
			var err error
			ref, err = a.e.stageStart(ctx, b, p)
			return err
		})
	})
	if err == nil {
		a.e.wakeDispatcher()
	}
	return ref, err
}

func (a *attempt) Await(ref tasks.RunRef) (tasks.Outcome, error) { return a.e.await(a.ctx, ref.ID) }

func (a *attempt) StandDown(key tasks.Key) (tasks.Hold, error) { return a.e.standDown(a, key) }

// LockRefs takes refs in the executor's table; the attempt releases any
// lock still held when it ends (a zombie's only when its goroutine exits).
func (a *attempt) LockRefs(ctx context.Context, refs ...entity.Ref) (func(), error) {
	start := a.e.now()
	release, err := a.e.refs.lock(ctx, refs)
	a.e.ins.refLockWait(a.d.kind, a.e.now().Sub(start))
	if err != nil {
		return nil, err
	}
	a.lmu.Lock()
	a.lnext++
	tok := a.lnext
	if a.locks == nil {
		a.locks = map[uint64]func(){}
	}
	a.locks[tok] = release
	a.lmu.Unlock()
	return sync.OnceFunc(func() {
		a.lmu.Lock()
		delete(a.locks, tok)
		a.lmu.Unlock()
		release()
	}), nil
}

func (a *attempt) releaseLocks() {
	a.lmu.Lock()
	ls := a.locks
	a.locks = nil
	a.lmu.Unlock()
	for _, release := range ls {
		release()
	}
}

func (a *attempt) Lane(ctx context.Context, id tasks.LaneID) (tasks.LaneLease, error) {
	ln, ok := a.e.lanes[id]
	if !ok || !containsLane(a.d.c.Lanes, id) {
		return nil, fmt.Errorf("%w: %s (kind %s)", ErrLaneNotDeclared, id, a.d.kind)
	}
	return ln.acquire(ctx)
}

func containsLane(ls []tasks.LaneID, id tasks.LaneID) bool {
	for _, l := range ls {
		if l == id {
			return true
		}
	}
	return false
}

// CapWorkers applies core.tasks.kinds[kind].parallelism_cap (COR3-11).
func (a *attempt) CapWorkers(declared int) int {
	if dial, ok := a.e.config().Kinds[a.d.kind]; ok && dial.ParallelismCap > 0 {
		return min(declared, dial.ParallelismCap)
	}
	return declared
}

func (a *attempt) AwaitUnpaused(ctx context.Context) error { return a.e.awaitUnpaused(ctx) }

func (a *attempt) AcquireCPU(ctx context.Context) (func(), error) { return a.e.cpu.acquire(ctx) }

// RenewHold renews every stand-down hold this run holds; a lost one fails
// the caller's next write (5T.3.7).
func (a *attempt) RenewHold(ctx context.Context) error { return a.e.renewHolds(ctx, a) }

// --- the per-run writer ---

// errClosed refuses a ledger write: the run is terminal, or another
// attempt or fence owns it (5T.7's closed-ledger rule).
var errClosed = errors.New("executor: ledger closed for this attempt")

// errDrop drops a bare progress tick whose fresh read shows a state change.
var errDrop = errors.New("executor: progress tick dropped")

// ledger runs one ledger write (checkpoint, chunk, row mark, page, item,
// strike) through the per-run writer: re-staged on a storage conflict while
// the fresh read is non-terminal under this attempt and fence (stopping
// included), refused otherwise with tasks.ErrFenced.
func (a *attempt) ledger(ctx context.Context, stage func(ctx context.Context, b *storage.Batch) error) error {
	a.wmu.Lock()
	defer a.wmu.Unlock()
	return a.ledgerLocked(ctx, stage)
}

func (a *attempt) ledgerLocked(ctx context.Context, stage func(ctx context.Context, b *storage.Batch) error) error {
	if a.fenced.Load() {
		return tasks.ErrFenced
	}
	err := a.e.write(context.WithoutCancel(ctx), func(ctx context.Context, b *storage.Batch) error {
		cur, err := a.e.runs.Get(ctx, a.id)
		if err != nil {
			return err
		}
		if cur.State.Terminal() || cur.Attempt != a.n || cur.FenceEpoch != a.fence {
			return errClosed
		}
		return stage(ctx, b)
	})
	if errors.Is(err, errClosed) || errors.Is(err, tasks.ErrRunConflict) {
		return fmt.Errorf("%w: %v", tasks.ErrFenced, err)
	}
	return err
}

// tick persists the newest progress numbers: a bare tick, dropped when a
// retry's fresh read shows a state change or another attempt.
func (a *attempt) tick(ctx context.Context) {
	a.wmu.Lock()
	defer a.wmu.Unlock()
	a.tickLocked(ctx)
}

func (a *attempt) tickLocked(ctx context.Context) {
	if a.fenced.Load() {
		return
	}
	a.pmu.Lock()
	done, total, cur, dirty := a.pDone, a.pTotal, a.pCurrent, a.pDirty
	a.pDirty = false
	a.pmu.Unlock()
	if !dirty {
		return
	}
	var first tasks.RunState
	err := a.e.write(context.WithoutCancel(ctx), func(ctx context.Context, b *storage.Batch) error {
		rec, err := a.e.runs.Get(ctx, a.id)
		if err != nil {
			return err
		}
		if first == "" {
			first = rec.State
		} else if rec.State != first {
			return errDrop
		}
		if rec.Attempt != a.n || rec.FenceEpoch != a.fence {
			return errDrop
		}
		return a.e.runs.Progress(ctx, b, a.id, done, total, cur)
	})
	if err != nil && !errors.Is(err, errDrop) && !errors.Is(err, tasks.ErrRunConflict) {
		a.log.Warn("progress tick failed", "err", err)
	}
}

// flushLedger writes every pending checkpoint together with the newest
// progress numbers in one batch (5T.3.6: same batch as the progress record).
func (a *attempt) flushLedger(ctx context.Context) error {
	a.wmu.Lock()
	defer a.wmu.Unlock()
	a.pmu.Lock()
	pend := a.pending
	a.pending = nil
	done, total, cur := a.pDone, a.pTotal, a.pCurrent
	a.pDirty = false
	a.ckptLast = a.e.now()
	a.pmu.Unlock()
	if len(pend) == 0 {
		return nil
	}
	err := a.ledgerLocked(ctx, func(ctx context.Context, b *storage.Batch) error {
		for name, v := range pend {
			if err := a.e.runs.SaveCheckpoint(ctx, b, a.id, name, v); err != nil {
				return err
			}
		}
		return a.e.runs.Progress(ctx, b, a.id, done, total, cur)
	})
	if err != nil {
		a.pmu.Lock() // keep what did not land, unless a newer value replaced it
		if a.pending == nil {
			a.pending = map[string][]byte{}
		}
		for name, v := range pend {
			if _, newer := a.pending[name]; !newer {
				a.pending[name] = v
			}
		}
		a.pmu.Unlock()
	}
	return err
}

// strike stages one watchdog strike through the per-run writer.
func (a *attempt) strike(ctx context.Context, kind, detail string) {
	s := tasks.Strike{Kind: kind, Detail: detail, At: a.e.now()}
	if err := a.ledger(ctx, func(ctx context.Context, b *storage.Batch) error {
		return a.e.runs.Strike(ctx, b, a.id, s)
	}); err != nil {
		a.log.Warn("watchdog strike not recorded", "strike", kind, "err", err)
	}
}

// --- the attempt's life ---

// run is the attempt goroutine: body, then the terminal (or yield) CAS.
func (a *attempt) run() {
	defer a.e.attemptExited(a)
	res, err := a.body()
	a.finish(res, err)
}

func (a *attempt) body() (res json.RawMessage, err error) {
	defer func() {
		if r := recover(); r != nil {
			err = &tasks.PanicError{Value: r, Stack: debug.Stack()}
		}
	}()
	p, err := a.d.decode(a.rec.Params)
	if err != nil {
		return nil, err
	}
	rc := tasks.NewRC(a)
	switch {
	case a.d.task != nil:
		return a.d.task.Run(rc, p)
	case a.d.batch != nil:
		return a.runBatch(rc, p)
	default:
		return a.runFixer(rc, p)
	}
}

// outcome maps an attempt's return to a state (5T.3.6): by the run
// context's cause when the run was stopped, else by the error.
func (a *attempt) outcome(err error) (tasks.RunState, string) {
	cause := context.Cause(a.ctx)
	if a.ctx.Err() == nil {
		cause = nil
	}
	a.e.mu.Lock()
	stopping := a.stopping
	a.e.mu.Unlock()
	switch {
	case err == nil || errors.Is(err, tasks.ErrPreview):
		return tasks.StateSucceeded, ""
	case errors.Is(cause, tasks.ErrTimedOut):
		return tasks.StateTimedOut, reasonTimeout
	case errors.Is(cause, tasks.ErrCanceled):
		if stopping == reasonWatchdog {
			return tasks.StateCanceled, reasonWatchdog
		}
		return tasks.StateCanceled, reasonCancel
	case errors.Is(cause, tasks.ErrShutdown):
		return tasks.StateInterrupted, reasonShutdown
	case errors.Is(cause, tasks.ErrQuiesced):
		return tasks.StateInterruptedQuiesced, reasonQuiesce
	default:
		return tasks.StateFailed, reasonError
	}
}

// finish commits the attempt's end: its pending ledger first, then one CAS
// from running/stopping under this attempt and fence. A fenced attempt (its
// grace ran out, or shutdown stamped it) writes nothing more.
func (a *attempt) finish(res json.RawMessage, err error) {
	if a.fenced.Load() {
		return
	}
	ctx := context.WithoutCancel(a.ctx)
	if ferr := a.flushLedger(ctx); ferr != nil && !errors.Is(ferr, tasks.ErrFenced) {
		if err == nil && a.d.c.Resume == tasks.ResumeContinue {
			err = fmt.Errorf("executor: final checkpoint write: %w", ferr)
		}
	}
	a.tick(ctx)
	state, reason := a.outcome(err)
	yield := errors.Is(err, tasks.ErrContinueInPlace) && a.ctx.Err() == nil
	if state == tasks.StateInterruptedQuiesced && !a.e.keyHeld(a.d.key) {
		state, reason = tasks.StateQueued, "" // the hold was released while this attempt wound down
	}
	a.pmu.Lock()
	done, total, current := a.pDone, a.pTotal, a.pCurrent
	a.pmu.Unlock()
	out, mErr := a.e.move(ctx, a.id, func(cur tasks.RunRecord) (tasks.RunRecord, tasks.RunEvent, error) {
		if cur.Attempt != a.n || cur.FenceEpoch != a.fence || (cur.State != tasks.StateRunning && cur.State != tasks.StateStopping) {
			return cur, tasks.RunEvent{}, errStale(cur, "attempt end")
		}
		next := cur
		next.Done, next.Total, next.Current = done, total, current
		ev := tasks.RunEvent{}
		if yield {
			return a.yieldNext(cur, next)
		}
		next.State, next.Reason = state, reason
		switch state {
		case tasks.StateSucceeded:
			next.Result, next.Error = res, ""
			ev.Event = "complete"
		case tasks.StateFailed, tasks.StateTimedOut:
			next.Error = errText(err)
			ev.Event = "fail"
		case tasks.StateCanceled:
			ev.Event = "cancel"
		default:
			ev.Event = "interrupt"
		}
		ev.Reason = reason
		if state.Terminal() {
			next.Finished = a.e.now()
		}
		return next, ev, nil
	}, nil)
	if mErr != nil && !errors.Is(mErr, tasks.ErrRunConflict) {
		a.log.Error("attempt end not committed; the resume sweep decides at next boot", "err", mErr)
	}
	if mErr == nil && yield && out.State == tasks.StateQueued {
		a.e.ins.yielded(a.d.kind)
	}
}

// yieldNext is ErrContinueInPlace (5T.3.6): re-queued under the same run ID
// (the next attempt gets a fresh Timeout) when the kind resumes with
// ResumeContinue and this attempt both reported progress past its starting
// point and checkpointed; failed otherwise, with the missing proof named
// (HEAD continue_in_place.go's rule), and failed once core.tasks.max_yields
// consecutive yields are reached.
func (a *attempt) yieldNext(cur, next tasks.RunRecord) (tasks.RunRecord, tasks.RunEvent, error) {
	ev := tasks.RunEvent{Event: "yield"}
	var missing []string
	if !a.progressed.Load() {
		missing = append(missing, "reported no progress past its starting point")
	}
	if !a.ckptd.Load() {
		missing = append(missing, "did not checkpoint")
	}
	switch {
	case a.d.c.Resume != tasks.ResumeContinue:
		next.State, next.Reason, next.Finished = tasks.StateFailed, "yield_not_continue", a.e.now()
		next.Error = fmt.Sprintf("tasks: continue in place is honoured only for ResumeContinue (this kind resumes %s)", a.d.c.Resume)
		ev.Event = "fail"
	case len(missing) > 0:
		next.State, next.Reason, next.Finished = tasks.StateFailed, "yield_without_progress", a.e.now()
		next.Error = "tasks: yielded to continue in place, but the attempt " + strings.Join(missing, " and ")
		ev.Event = "fail"
	case cur.Yields+1 > a.e.config().MaxYields:
		next.State, next.Reason, next.Finished = tasks.StateFailed, "max_yields", a.e.now()
		next.Error = fmt.Sprintf("tasks: %d consecutive yields reached core.tasks.max_yields", cur.Yields)
		ev.Event = "fail"
	default:
		next.State, next.Reason = tasks.StateQueued, ""
		next.Yields = cur.Yields + 1
	}
	ev.Reason = next.Reason
	return next, ev, nil
}

// errText is a run's stored error text.
func errText(err error) string {
	if err == nil {
		return ""
	}
	var pe *tasks.PanicError
	if errors.As(err, &pe) {
		return "panic: " + fmt.Sprint(pe.Value)
	}
	return err.Error()
}

// supervise watches an attempt for a stop that its body does not honour:
// once the run context ends (cancel, timeout, quiesce, watchdog) it waits
// the contract's grace, then abandons the attempt (fence revoked, the row
// ended, the goroutine counted a zombie until it exits). A shutdown stop is
// the shutdown path's.
func (a *attempt) supervise() {
	defer a.e.bg.Done()
	select {
	case <-a.done:
		return
	case <-a.ctx.Done():
	case <-a.e.bgCtx.Done():
		return
	}
	cause := context.Cause(a.ctx)
	if errors.Is(cause, tasks.ErrShutdown) {
		return
	}
	t := time.NewTimer(a.d.grace)
	defer t.Stop()
	select {
	case <-a.done:
		return
	case <-a.e.bgCtx.Done():
		return
	case <-t.C:
	}
	state, reason := a.outcome(cause)
	a.e.abandon(a, state, reason)
}
