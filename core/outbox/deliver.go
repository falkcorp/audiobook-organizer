// file: core/outbox/deliver.go
// version: 1.1.0
// guid: 2a6ad2a4-6bc1-4f3b-b7f1-0ee9e2bb7d3f
// last-edited: 2026-11-13

package outbox

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/falkcorp/mediacore/internal/outboxstore"
	"github.com/falkcorp/mediacore/storage"
	"github.com/falkcorp/mediacore/storage/codec"
	"github.com/falkcorp/mediacore/tasks"
)

// maxTryUnordered bounds the messages one unordered (consumer, partition) may
// hold under retry: past it the pair stops taking new messages until some
// settle, so a consumer that fails everything cannot grow its try record
// without bound.
const maxTryUnordered = 64

// outcome is what became of one delivery step.
type outcome uint8

const (
	outAcked outcome = iota + 1 // the message is settled: acked, filtered or dead-lettered
	outRetry                    // it failed and a retry is scheduled
	outStale                    // another writer moved the pair first: re-read
	outHalt                     // canceled, or a fault: end this pair's pass
)

// errAbort ends a WriteRuns stage without committing it; the caller knows why.
var errAbort = errors.New("outbox: batch abandoned")

// panicError is a recovered handler panic. It keeps the Go type of the panic
// value and the functions that were running, never the value itself: a handler
// panics with whatever it was holding, and that can be the message's payload,
// which must not reach a log line, an error chain or a dead letter.
type panicError struct {
	typ    string   // the panic value's Go type (%T)
	frames []string // function:line of the panicking stack, innermost first
}

func (e *panicError) Error() string { return "outbox: handler panicked (" + e.typ + ")" }

// maxPanicFrames bounds the frames kept from one panic.
const maxPanicFrames = 16

// invokeSafe runs the consumer's handler, turning a panic into an error.
func invokeSafe(ctx context.Context, reg *consumerReg, tx *Tx, d delivery) (matched bool, err error) {
	defer func() {
		if p := recover(); p != nil {
			matched, err = true, &panicError{typ: fmt.Sprintf("%T", p), frames: panicFrames()}
		}
	}()
	return reg.invoke(ctx, tx, d)
}

// panicFrames names the functions of the panicking goroutine's stack, from the
// panic site out to invokeSafe: a function name and a source line each, which
// locate the fault and carry nothing the handler was working on (a
// runtime/debug.Stack dump would add every frame's raw argument words).
// It must be called from the deferred function that recovered.
func panicFrames() []string {
	pcs := make([]uintptr, 2*maxPanicFrames)
	n := runtime.Callers(3, pcs) // past Callers, panicFrames and the deferred function
	it := runtime.CallersFrames(pcs[:n])
	var out []string
	for len(out) < maxPanicFrames {
		f, more := it.Next()
		if strings.HasSuffix(f.Function, ".invokeSafe") {
			break
		}
		if f.Function != "" && !strings.HasPrefix(f.Function, "runtime.") {
			out = append(out, f.Function+":"+strconv.Itoa(f.Line))
		}
		if !more {
			break
		}
	}
	return out
}

// errType is the Go type of err, the one thing about a handler's failure that
// the relay logs: its text and its panic value can embed the payload (a schema
// validator echoes the instance it rejected), so neither is ever logged. The
// classified token (classify) says what kind of failure it was.
func errType(err error) string { return fmt.Sprintf("%T", err) }

// classify turns a handler error into the token stored with a retry or a dead
// letter (a closed set: no error text and no payload is persisted) and says
// whether retrying cannot help.
func classify(err error) (token string, permanent bool) {
	var pe *panicError
	switch {
	case errors.Is(err, ErrPermanent):
		return "permanent", true
	case errors.Is(err, ErrSchema):
		return "schema", true
	case errors.Is(err, tasks.ErrModeUnset):
		return "mode_unset", true
	case errors.Is(err, tasks.ErrWriteRunsNested):
		return "nested_write", true
	case errors.As(err, &pe):
		return "panic", false
	case errors.Is(err, context.DeadlineExceeded):
		return "timeout", false
	default:
		return "error", false
	}
}

// backoffDelay is the wait after the attempt-th failed delivery: uniform in
// [lo, cap) where cap doubles from lo with each attempt and never passes hi.
// u is a draw in [0, 1).
func backoffDelay(attempt int, lo, hi time.Duration, u float64) time.Duration {
	capd := lo
	for i := 1; i < attempt && capd < hi; i++ {
		if capd > maxDuration/2 {
			capd = hi
			break
		}
		capd *= 2
	}
	capd = min(capd, hi)
	return lo + time.Duration(u*float64(capd-lo))
}

// failurePlan decides what a failed delivery leads to: a dead letter at once
// for a permanent error, or when this was the last allowed attempt; otherwise
// the time of the next try (the handler's RetryAfter/RetryFree hint, else the
// backoff curve) and the attempts the message has used after this failure,
// which is one less than attempt for a RetryFree failure.
func (o *Outbox) failurePlan(reg *consumerReg, cfg Config, attempt int, err error) (dead bool, token string, next time.Time, used int) {
	token, permanent := classify(err)
	if permanent {
		return true, token, time.Time{}, attempt
	}
	hint, hinted := hintOf(err)
	if hinted && hint.free {
		// The delivery was never really tried: the message keeps the attempts
		// it had, and nothing here can reach the limit.
		return false, token, o.now().Add(hint.after), attempt - 1
	}
	limit := cfg.MaxAttempts
	if reg.cfg.maxAttempts > 0 {
		limit = reg.cfg.maxAttempts
	}
	if attempt >= limit {
		return true, token, time.Time{}, attempt
	}
	if hinted {
		return false, token, o.now().Add(hint.after), attempt
	}
	lo, hi := cfg.BackoffMin, cfg.BackoffMax
	if reg.cfg.backoffMin > 0 {
		lo, hi = reg.cfg.backoffMin, reg.cfg.backoffMax
	}
	return false, token, o.now().Add(backoffDelay(attempt, lo, hi, o.rnd())), attempt
}

// ackInto records that the pair took over the message at seq: the cursor moves
// to it and every retry entry at or below it is dropped.
func ackInto(st *outboxstore.PairState, seq uint64) {
	st.Cur.Seq = max(st.Cur.Seq, seq)
	st.Try.Entries = slices.DeleteFunc(slices.Clone(st.Try.Entries), func(e outboxstore.TryEntry) bool { return e.Seq <= seq })
}

func tryFor(t outboxstore.Try, seq uint64) *outboxstore.TryEntry {
	for i := range t.Entries {
		if t.Entries[i].Seq == seq {
			return &t.Entries[i]
		}
	}
	return nil
}

// ---- pair bookkeeping

func (r *relay) skip(p outboxstore.Pair, high uint64, now time.Time) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	if at, ok := r.pairStop[p]; ok && now.Sub(at) < stopRetryEvery {
		return true
	}
	if h, ok := r.idle[p]; ok && h == high {
		return true
	}
	if at, ok := r.blocked[p]; ok && now.Before(at) {
		return true
	}
	return false
}

func (r *relay) markIdle(p outboxstore.Pair, high uint64) {
	r.mu.Lock()
	r.idle[p] = high
	delete(r.blocked, p)
	delete(r.retries, p)
	r.mu.Unlock()
}

func (r *relay) block(p outboxstore.Pair, until time.Time) {
	r.mu.Lock()
	r.blocked[p] = until
	r.mu.Unlock()
}

// retryAt notes when an unordered pair's earliest retry is due. It only wakes
// the relay: an unordered pair keeps taking new messages meanwhile.
func (r *relay) retryAt(p outboxstore.Pair, at time.Time) {
	r.mu.Lock()
	r.retries[p] = at
	r.mu.Unlock()
}

func (r *relay) unblock(p outboxstore.Pair) {
	r.mu.Lock()
	delete(r.blocked, p)
	delete(r.retries, p)
	delete(r.idle, p)
	r.mu.Unlock()
}

// pairFault handles an error reading a pair's state or messages: a row of a
// newer build, or one that does not decode at all, stops the pair (never
// skipped, never marked done) and reports it; anything else is logged and tried
// again next loop.
func (r *relay) pairFault(ctx context.Context, p outboxstore.Pair, err error) {
	if err == nil || ctx.Err() != nil || r.stopped() {
		return
	}
	unknown := errors.Is(err, codec.ErrUnknownVersion)
	if unknown || errors.Is(err, outboxstore.ErrCorruptRow) {
		why := "corrupt_row"
		if unknown {
			why = "unknown_version"
		}
		r.mu.Lock()
		r.pairStop[p] = r.o.now()
		r.mu.Unlock()
		if r.o.health.stop(pairSubject(p), why) {
			r.log.Error("outbox: delivery stopped on a row it cannot read; it is not skipped",
				append([]any{slog.String("consumer", p.Consumer), slog.String("topic", p.Topic), slog.Int("partition", p.PP), slog.String("why", why)}, errAttrs(err)...)...)
		}
		return
	}
	r.fault(fmt.Sprintf("reading %s", pairSubject(p)), err)
}

func (r *relay) pairHealthy(p outboxstore.Pair) {
	r.mu.Lock()
	_, was := r.pairStop[p]
	delete(r.pairStop, p)
	r.mu.Unlock()
	if was {
		r.o.health.clear(pairSubject(p))
	}
}

func pairSubject(p outboxstore.Pair) string {
	return fmt.Sprintf("pair %s/%s/%02d", p.Consumer, p.Topic, p.PP)
}

// ---- the delivery loop

// deliverAll gives every (consumer, partition) that may have work one pass,
// through one bounded tasks.RunItems: the pairs are the items, a pair is never
// in two workers at once (each appears once per call and the call returns
// before the next), and a consumer's messages of one partition stay in order
// because a pair's pass is sequential.
func (r *relay) deliverAll(ctx context.Context) {
	o := r.o
	regs := make(map[string]*consumerReg, len(r.regs))
	highs := map[string]uint64{}
	refused := map[string]bool{} // topics whose partition count changed
	var pairs []outboxstore.Pair
	now := o.now()
	for _, reg := range r.regs {
		regs[reg.name] = reg
		if !r.inited[reg.name] {
			continue
		}
		ti, err := o.topic(reg.topic)
		if err != nil {
			continue
		}
		hi, ok := highs[ti.name]
		if !ok {
			h, found, err := o.store.ReadHigh(ctx, ti.name)
			if err != nil {
				r.intakeFault(ctx, ti.name, "topic "+ti.name, err)
				continue
			}
			hi = h.High
			highs[ti.name] = hi
			// Messages sequenced over another partition count are not found by
			// pairs of this one: deliver nothing from the topic until they agree.
			refused[ti.name] = !r.partitionsOK(ti, h, found)
		}
		if refused[ti.name] {
			continue
		}
		for pp := range ti.partitions {
			p := outboxstore.Pair{Consumer: reg.name, Topic: ti.name, PP: pp}
			if !r.skip(p, hi, now) {
				pairs = append(pairs, p)
			}
		}
	}
	if len(pairs) == 0 || ctx.Err() != nil || r.stopped() {
		return
	}
	workers := max(1, min(len(pairs), r.cfg.RelayWorkers))
	_, err := tasks.RunItems(r.rc, tasks.Fixed(workers, "outbox relay workers"), pairs,
		func(ic *tasks.ItemRC, p outboxstore.Pair) error {
			r.pair(ic, regs[p.Consumer], p, highs[p.Topic])
			return nil
		}, tasks.ItemsOptions[outboxstore.Pair]{OnError: tasks.Collect})
	if err != nil {
		r.fault("delivery", err)
	}
}

type peekResult uint8

const (
	peekNone peekResult = iota
	peekWork
)

// pair runs one pass over one (consumer, partition). A pair with nothing
// deliverable costs three reads and no write; one with work does its pass
// inside one fenced write of the run.
func (r *relay) pair(ic *tasks.ItemRC, reg *consumerReg, p outboxstore.Pair, high uint64) {
	ctx := ic.Context()
	ti, err := r.o.topic(p.Topic)
	if err != nil || reg == nil {
		return
	}
	if r.peek(ctx, reg, p, high) == peekNone {
		return
	}
	pass := func(ctx context.Context) {
		if reg.cfg.unordered {
			r.passUnordered(ctx, reg, ti, p, high)
		} else {
			r.passOrdered(ctx, reg, ti, p, high)
		}
	}
	w := ic.Writer()
	if w == nil {
		pass(ctx)
		return
	}
	err = w.Do(ctx, tasks.Write{Name: "outbox.deliver", Resource: tasks.Res("core", "outbox"), Subject: tasks.Res("core", "outbox")},
		func(ctx context.Context) ([]tasks.Change, error) { pass(ctx); return nil, nil })
	if errors.Is(err, tasks.ErrFenced) || errors.Is(err, tasks.ErrPreview) {
		r.setStop(err)
	}
}

// peek decides, from reads alone, whether the pair has anything to deliver now;
// when it has not, it records why (idle until the topic grows, or blocked until
// a retry is due).
func (r *relay) peek(ctx context.Context, reg *consumerReg, p outboxstore.Pair, high uint64) peekResult {
	o := r.o
	st, err := o.store.ReadPair(ctx, p)
	if err != nil {
		r.pairFault(ctx, p, err)
		return peekNone
	}
	if !st.Has {
		return peekNone
	}
	now := o.now()
	var earliest time.Time
	for _, e := range st.Try.Entries {
		if !e.NextAt.After(now) {
			if reg.cfg.unordered {
				return peekWork
			}
			continue
		}
		if earliest.IsZero() || e.NextAt.Before(earliest) {
			earliest = e.NextAt
		}
	}
	room := !reg.cfg.unordered || len(st.Try.Entries) < maxTryUnordered
	if room {
		rows, err := o.store.MessagesAfter(ctx, p.Topic, p.PP, st.Cur.Seq, 1)
		if err != nil {
			r.pairFault(ctx, p, err)
			if len(rows) == 0 {
				return peekNone
			}
		}
		if len(rows) > 0 {
			if reg.cfg.unordered {
				return peekWork
			}
			// Ordered: the head is the first message; it waits if it is under retry.
			if e := tryFor(st.Try, rows[0].Seq); e != nil && e.NextAt.After(now) {
				r.block(p, e.NextAt)
				return peekNone
			}
			r.pairHealthy(p)
			return peekWork
		}
	}
	r.pairHealthy(p)
	if !earliest.IsZero() {
		if reg.cfg.unordered {
			r.retryAt(p, earliest)
		} else {
			r.block(p, earliest)
		}
		return peekNone
	}
	r.markIdle(p, high)
	return peekNone
}

// ---- ordered pass

// passOrdered delivers the pair's messages one at a time in seq order. A
// failing head message blocks the rest of the partition until it succeeds or
// dead-letters (head-of-line: the cost of per-key order).
func (r *relay) passOrdered(ctx context.Context, reg *consumerReg, ti *topicInfo, p outboxstore.Pair, high uint64) {
	o := r.o
	start := o.now()
	var rows []outboxstore.MsgRow
	for n := 0; n < passMaxMessages && ctx.Err() == nil && !r.stopped(); n++ {
		if n > 0 && o.now().Sub(start) > passBudget {
			return
		}
		st, err := o.store.ReadPair(ctx, p)
		if err != nil {
			r.pairFault(ctx, p, err)
			return
		}
		if !st.Has {
			return
		}
		for len(rows) > 0 && rows[0].Seq <= st.Cur.Seq {
			rows = rows[1:]
		}
		if len(rows) == 0 {
			var rerr error
			rows, rerr = o.store.MessagesAfter(ctx, p.Topic, p.PP, st.Cur.Seq, prefetchRows)
			if rerr != nil {
				r.pairFault(ctx, p, rerr)
			}
			if len(rows) == 0 {
				if rerr == nil {
					r.markIdle(p, high)
				}
				return
			}
		}
		row := rows[0]
		attempt := 1
		if e := tryFor(st.Try, row.Seq); e != nil {
			if e.NextAt.After(o.now()) {
				r.block(p, e.NextAt)
				return
			}
			attempt = e.Attempt + 1
		}
		switch r.deliverOne(ctx, reg, ti, p, row, attempt) {
		case outAcked:
			r.unblock(p)
			r.moved.Add(1)
			r.rc.Progress().Tick()
		default:
			return
		}
	}
}

// execResult is what running a handler on one message came to.
type execResult struct {
	matched bool        // the handler ran (a Where did not reject the message)
	skipped bool        // a retried dead letter addressed to another consumer
	err     error       // the handler's failure
	seq     uint64      // the message's seq
	folded  tasks.RunID // the existing run a Tx.StartRun folded into, if one did
}

// execute runs the consumer on one message: the retried-dead-letter address
// check, the raw schema check, then the handler under handler_timeout, with
// panics recovered. tx is non-nil for the batch forms.
func (r *relay) execute(ctx context.Context, reg *consumerReg, ti *topicInfo, row outboxstore.MsgRow, attempt int, tx *Tx) execResult {
	if row.Msg.To != "" && row.Msg.To != reg.name {
		return execResult{skipped: true}
	}
	d := delivery{topic: ti.name, key: row.Msg.Key, id: row.Msg.ID, producer: row.Msg.Producer, seq: row.Seq,
		attempt: attempt, enqueuedAt: row.Msg.StagedAt, payload: row.Msg.Payload}
	if reg.raw() && ti.check != nil {
		if ti.check.validate(d.payload) != nil {
			// Opaque on purpose: the validator's text echoes the values of the
			// instance it rejected, so it is dropped here, before it can reach an
			// error chain, a log line or a dead letter. The topic and the
			// "schema" token say what failed; the payload itself is in the
			// message row an operator can read.
			return execResult{err: fmt.Errorf("%w: %s", ErrSchema, ti.name)}
		}
	}
	hctx, cancel := context.WithTimeout(ctx, r.cfg.HandlerTimeout)
	defer cancel()
	matched, err := invokeSafe(hctx, reg, tx, d)
	if err == nil {
		if h := r.o.hooks.get(); h.afterHandler != nil {
			h.afterHandler(reg.name, row.Seq)
		}
	} else if pe := (*panicError)(nil); errors.As(err, &pe) {
		// The panic value is never logged, only its type and the frames (see
		// panicError): the events bus logs a recovered subscriber the same way.
		r.log.Error("outbox: handler panicked", slog.String("consumer", reg.name), slog.String("topic", ti.name),
			slog.Uint64("seq", row.Seq), slog.String("panic_type", pe.typ), slog.String("frames", strings.Join(pe.frames, " <- ")))
	}
	res := execResult{matched: matched, err: err, seq: row.Seq}
	if tx != nil {
		res.folded = tx.folded
	}
	return res
}

// deliverOne delivers one message to an ordered consumer and settles it.
func (r *relay) deliverOne(ctx context.Context, reg *consumerReg, ti *topicInfo, p outboxstore.Pair, row outboxstore.MsgRow, attempt int) outcome {
	if reg.needsBatch() {
		return r.deliverBatch(ctx, reg, ti, p, row, attempt)
	}
	res := r.execute(ctx, reg, ti, row, attempt, nil)
	if res.err != nil {
		if ctx.Err() != nil {
			return outHalt // shutdown, not a failed attempt: the message is redelivered
		}
		return r.settleFail(ctx, reg, ti, p, row, attempt, res.err)
	}
	return r.settleAck(ctx, reg, ti, p, row, res)
}

// settleAck commits the cursor advance for a message the consumer took over.
// The batch reads the pair's cursor with GetForUpdate and rewrites it, so a
// writer that got there first (a zombie run, an operator's purge) fails this
// commit instead of being overwritten.
func (r *relay) settleAck(ctx context.Context, reg *consumerReg, ti *topicInfo, p outboxstore.Pair, row outboxstore.MsgRow, res execResult) outcome {
	o := r.o
	out := outHalt
	err := storage.RetryOnConflict(ctx, settleAttempts, func(ctx context.Context) (err error) {
		b := o.store.OpenBatch()
		defer closeBatch(b, &err)
		st, err := o.store.LoadPair(ctx, b, p)
		if err != nil {
			return err
		}
		if !st.Has || st.Cur.Seq >= row.Seq {
			out = outStale
			return nil
		}
		ackInto(&st, row.Seq)
		if err := o.store.StagePair(b, p, st, o.now()); err != nil {
			return err
		}
		h := o.hooks.get()
		if h.beforeAckCommit != nil {
			h.beforeAckCommit(reg.name, row.Seq)
		}
		if err := b.Commit(ctx, storage.Sync); err != nil {
			return err
		}
		out = outAcked
		if h.afterAckCommit != nil {
			h.afterAckCommit(reg.name, row.Seq)
		}
		return nil
	})
	if err != nil {
		r.fault("ack of "+reg.name, err)
		return outHalt
	}
	if out == outAcked {
		r.countSettled(reg, ti, res)
	}
	return out
}

func (r *relay) countSettled(reg *consumerReg, ti *topicInfo, res execResult) {
	switch {
	case res.skipped:
	case !res.matched:
		r.o.inst.filtered(ti.name, reg.name)
	default:
		r.o.inst.delivered(ti.name, reg.name)
		if res.folded != "" {
			// The delivery is acked (the run it asked for exists) but queued no
			// run of its own: say so, so a consumer whose runs keep folding is
			// visible rather than a silent loss of what came after the first.
			r.o.inst.folded(ti.name, reg.name)
			r.log.Info("outbox: run start folded into an existing run", slog.String("consumer", reg.name),
				slog.String("topic", ti.name), slog.Uint64("seq", res.seq), slog.String("run", string(res.folded)))
		}
	}
}

// settleFail records a failed delivery in its own small batch: a retry entry
// with the next time, or, for a permanent error or the last attempt, the dead
// letter and the cursor advance together (the message is never both lost and
// unrecorded). The error text is not stored; the token is.
func (r *relay) settleFail(ctx context.Context, reg *consumerReg, ti *topicInfo, p outboxstore.Pair, row outboxstore.MsgRow, attempt int, herr error) outcome {
	o := r.o
	out := outHalt
	var dead bool
	var next time.Time
	var token string
	var used int
	err := storage.RetryOnConflict(ctx, settleAttempts, func(ctx context.Context) (err error) {
		b := o.store.OpenBatch()
		defer closeBatch(b, &err)
		st, err := o.store.LoadPair(ctx, b, p)
		if err != nil {
			return err
		}
		if !st.Has || st.Cur.Seq >= row.Seq {
			out = outStale
			return nil
		}
		dead, token, next, used = o.failurePlan(reg, r.cfg, attempt, herr)
		if dead {
			d := outboxstore.Dead{Msg: row.Msg, Part: p.PP, Token: token, Attempts: attempt, At: o.now().UTC()}
			if err := o.store.StageDead(ctx, b, reg.name, ti.name, row.Seq, d); err != nil {
				return err
			}
			ackInto(&st, row.Seq)
		} else {
			st.Try.Entries = []outboxstore.TryEntry{{Seq: row.Seq, Attempt: used, NextAt: next.UTC(), Token: token}}
		}
		if err := o.store.StagePair(b, p, st, o.now()); err != nil {
			return err
		}
		if err := b.Commit(ctx, storage.Sync); err != nil {
			return err
		}
		out = outRetry
		if dead {
			out = outAcked
		}
		return nil
	})
	if err != nil {
		r.fault("recording a failure of "+reg.name, err)
		return outHalt
	}
	switch {
	case out == outRetry:
		r.block(p, next)
		o.inst.retried(ti.name, reg.name)
		// token is one of classify's constants (a closed set that no handler
		// input reaches), so it is safe to log; herr's text is not, only its type.
		r.log.Warn("outbox: delivery failed; will retry", slog.String("consumer", reg.name), slog.String("topic", ti.name),
			slog.Uint64("seq", row.Seq), slog.Int("attempt", attempt), slog.String("token", token), slog.String("err_type", errType(herr)))
	case out == outAcked && dead:
		o.health.addDead(reg.name, o.now())
		r.log.Error("outbox: message dead-lettered", slog.String("consumer", reg.name), slog.String("topic", ti.name),
			slog.Uint64("seq", row.Seq), slog.Int("attempts", attempt), slog.String("token", token), slog.String("err_type", errType(herr)))
	}
	return out
}

// deliverBatch delivers to a ConsumeTx or ConsumeIntoTask consumer: the
// handler stages its effect (and a run, through Tx.StartRun) into a batch the
// relay opened on the outbox keyspace, and the relay stages the cursor advance
// in the SAME batch, which Executor.WriteRuns commits under RetryOnConflict
// with a fresh batch per attempt. The effect and the ack commit together or
// not at all.
func (r *relay) deliverBatch(ctx context.Context, reg *consumerReg, ti *topicInfo, p outboxstore.Pair, row outboxstore.MsgRow, attempt int) outcome {
	o := r.o
	runs := o.runsOrNil()
	if runs == nil {
		r.fault("delivery to "+reg.name, ErrNoRuns)
		return outHalt
	}
	var (
		herr error
		res  execResult
		out  = outAcked
	)
	err := runs.WriteRuns(ctx, o.store.OpenBatch, func(sctx context.Context, b *storage.Batch) error {
		herr, res, out = nil, execResult{}, outAcked
		st, err := o.store.LoadPair(sctx, b, p)
		if err != nil {
			return err
		}
		if !st.Has || st.Cur.Seq >= row.Seq {
			out = outStale
			return errAbort
		}
		res = r.execute(sctx, reg, ti, row, attempt, &Tx{ctx: sctx, b: b, runs: runs})
		if res.err != nil {
			herr = res.err
			return errAbort
		}
		ackInto(&st, row.Seq)
		if err := o.store.StagePair(b, p, st, o.now()); err != nil {
			return err
		}
		if h := o.hooks.get(); h.beforeAckCommit != nil {
			h.beforeAckCommit(reg.name, row.Seq)
		}
		return nil
	})
	switch {
	case err == nil:
		if h := o.hooks.get(); h.afterAckCommit != nil {
			h.afterAckCommit(reg.name, row.Seq)
		}
		r.countSettled(reg, ti, res)
		return outAcked
	case errors.Is(err, errAbort) && herr != nil:
		if ctx.Err() != nil {
			return outHalt
		}
		return r.settleFail(ctx, reg, ti, p, row, attempt, herr)
	case errors.Is(err, errAbort):
		return out
	case errors.Is(err, tasks.ErrWriteRunsNested):
		return r.settleFail(ctx, reg, ti, p, row, attempt, err)
	default:
		r.fault("committing "+reg.name, err)
		return outHalt
	}
}

// ---- unordered pass

// roundItem is one message of an unordered round.
type roundItem struct {
	row     outboxstore.MsgRow
	attempt int
	retry   bool // a message already under retry (below the cursor)
	done    bool
	res     execResult
}

// passUnordered delivers an unordered pair a round at a time: it takes the
// messages due for retry and the next messages after the cursor, runs them on
// up to unordered_parallelism workers, then settles the whole round in ONE
// guarded batch: the cursor moves past every message of the round, failures go
// into the pair's try record (bounded by maxTryUnordered) or become dead
// letters. A failing message therefore never blocks the ones after it. The
// cost is that a crash before a round's settle batch redelivers the whole
// round (at-least-once).
func (r *relay) passUnordered(ctx context.Context, reg *consumerReg, ti *topicInfo, p outboxstore.Pair, high uint64) {
	o := r.o
	par := max(1, r.cfg.UnorderedParallelism)
	window := par * 4
	start := o.now()
	for round := 0; round < 8 && ctx.Err() == nil && !r.stopped(); round++ {
		if round > 0 && o.now().Sub(start) > passBudget {
			return
		}
		st, err := o.store.ReadPair(ctx, p)
		if err != nil {
			r.pairFault(ctx, p, err)
			return
		}
		if !st.Has {
			return
		}
		now := o.now()
		var items []*roundItem
		var gone []uint64 // retry entries whose message is no longer in the store
		var earliest time.Time
		for _, e := range st.Try.Entries {
			if e.NextAt.After(now) {
				if earliest.IsZero() || e.NextAt.Before(earliest) {
					earliest = e.NextAt
				}
				continue
			}
			if len(items) >= window {
				continue
			}
			row, ok, err := r.messageAt(ctx, p, e.Seq)
			if err != nil {
				r.pairFault(ctx, p, err)
				return
			}
			if !ok {
				gone = append(gone, e.Seq) // the settle batch drops the entry
				continue
			}
			items = append(items, &roundItem{row: row, attempt: e.Attempt + 1, retry: true})
		}
		var newMax uint64
		// New messages are capped so that even if every one of them fails the
		// pair's try record holds at most maxTryUnordered entries: bounding only
		// the decision to take more would let a round overshoot by a window.
		if room := min(window-len(items), maxTryUnordered-len(st.Try.Entries)); room > 0 {
			rows, rerr := o.store.MessagesAfter(ctx, p.Topic, p.PP, st.Cur.Seq, room)
			if rerr != nil {
				r.pairFault(ctx, p, rerr)
			}
			for _, row := range rows {
				items = append(items, &roundItem{row: row, attempt: 1})
				newMax = row.Seq
			}
			if len(rows) == 0 && rerr != nil {
				return
			}
		}
		if len(items) == 0 && len(gone) == 0 {
			switch {
			case len(st.Try.Entries) > 0 && !earliest.IsZero():
				r.retryAt(p, earliest)
			case len(st.Try.Entries) == 0:
				r.markIdle(p, high)
			}
			return
		}
		r.pairHealthy(p)
		r.runRound(ctx, reg, ti, items, par)
		if ctx.Err() != nil {
			return // nothing is settled: the round is delivered again
		}
		if out := r.settleRound(ctx, reg, ti, p, items, gone, newMax); out != outAcked {
			return
		}
		r.unblock(p)
		r.moved.Add(int64(len(items)))
		r.rc.Progress().Tick()
	}
}

// runRound runs the items on at most par goroutines, each pulling the next
// index; the goroutines end with the round.
func (r *relay) runRound(ctx context.Context, reg *consumerReg, ti *topicInfo, items []*roundItem, par int) {
	var next atomic.Int64
	var wg sync.WaitGroup
	for range min(par, len(items)) {
		wg.Go(func() {
			for ctx.Err() == nil {
				i := int(next.Add(1) - 1)
				if i >= len(items) {
					return
				}
				it := items[i]
				it.res = r.execute(ctx, reg, ti, it.row, it.attempt, nil)
				it.done = true
			}
		})
	}
	wg.Wait()
}

// messageAt reads the one message at seq of the pair's partition.
func (r *relay) messageAt(ctx context.Context, p outboxstore.Pair, seq uint64) (outboxstore.MsgRow, bool, error) {
	if seq == 0 {
		return outboxstore.MsgRow{}, false, nil
	}
	rows, err := r.o.store.MessagesAfter(ctx, p.Topic, p.PP, seq-1, 1)
	if err != nil || len(rows) == 0 || rows[0].Seq != seq {
		return outboxstore.MsgRow{}, false, err
	}
	return rows[0], true, nil
}

// roundCounts are what a committed round did, for metrics and health.
type roundCounts struct {
	delivered, filtered, retried, dead, gone int
	next                                     time.Time
}

// settleRound commits a round. It reads the pair's cursor with GetForUpdate, so
// a concurrent writer fails the commit; on a retry it re-reads and drops every
// item another writer already settled.
func (r *relay) settleRound(ctx context.Context, reg *consumerReg, ti *topicInfo, p outboxstore.Pair, items []*roundItem, gone []uint64, newMax uint64) outcome {
	o := r.o
	out := outHalt
	var cnt roundCounts
	err := storage.RetryOnConflict(ctx, settleAttempts, func(ctx context.Context) (err error) {
		b := o.store.OpenBatch()
		defer closeBatch(b, &err)
		st, err := o.store.LoadPair(ctx, b, p)
		if err != nil {
			return err
		}
		if !st.Has {
			out = outStale
			return nil
		}
		cnt = roundCounts{}
		entries := map[uint64]outboxstore.TryEntry{}
		for _, e := range st.Try.Entries {
			entries[e.Seq] = e
		}
		// An entry whose message is gone can never be retried: leaving it would
		// hold a slot of the pair's bounded try list, and keep the pair awake,
		// for good.
		for _, seq := range gone {
			if _, ok := entries[seq]; ok {
				delete(entries, seq)
				cnt.gone++
			}
		}
		for _, it := range items {
			seq := it.row.Seq
			if it.retry {
				if _, ok := entries[seq]; !ok {
					continue // settled by another writer meanwhile
				}
			} else if seq <= st.Cur.Seq {
				continue
			}
			if !it.done {
				continue
			}
			if it.res.err == nil {
				delete(entries, seq)
				switch {
				case it.res.skipped:
				case !it.res.matched:
					cnt.filtered++
				default:
					cnt.delivered++
				}
				continue
			}
			dead, token, next, used := o.failurePlan(reg, r.cfg, it.attempt, it.res.err)
			if dead {
				d := outboxstore.Dead{Msg: it.row.Msg, Part: p.PP, Token: token, Attempts: it.attempt, At: o.now().UTC()}
				if err := o.store.StageDead(ctx, b, reg.name, ti.name, seq, d); err != nil {
					return err
				}
				delete(entries, seq)
				cnt.dead++
				continue
			}
			entries[seq] = outboxstore.TryEntry{Seq: seq, Attempt: used, NextAt: next.UTC(), Token: token}
			cnt.retried++
			if cnt.next.IsZero() || next.Before(cnt.next) {
				cnt.next = next
			}
		}
		st.Cur.Seq = max(st.Cur.Seq, newMax)
		st.Try.Entries = st.Try.Entries[:0]
		for _, e := range entries {
			st.Try.Entries = append(st.Try.Entries, e)
		}
		slices.SortFunc(st.Try.Entries, func(a, b outboxstore.TryEntry) int {
			switch {
			case a.Seq < b.Seq:
				return -1
			case a.Seq > b.Seq:
				return 1
			}
			return 0
		})
		if err := o.store.StagePair(b, p, st, o.now()); err != nil {
			return err
		}
		h := o.hooks.get()
		if h.beforeAckCommit != nil {
			h.beforeAckCommit(reg.name, newMax)
		}
		if err := b.Commit(ctx, storage.Sync); err != nil {
			return err
		}
		out = outAcked
		if h.afterAckCommit != nil {
			h.afterAckCommit(reg.name, newMax)
		}
		return nil
	})
	if err != nil {
		r.fault("settling a round of "+reg.name, err)
		return outHalt
	}
	if out == outAcked {
		for range cnt.delivered {
			o.inst.delivered(ti.name, reg.name)
		}
		for range cnt.filtered {
			o.inst.filtered(ti.name, reg.name)
		}
		for range cnt.retried {
			o.inst.retried(ti.name, reg.name)
		}
		if cnt.dead > 0 {
			o.health.addDead(reg.name, o.now())
		}
		if cnt.gone > 0 {
			r.log.Warn("outbox: messages under retry were gone from the store; their retry entries were dropped",
				slog.String("consumer", reg.name), slog.String("topic", ti.name), slog.Int("partition", p.PP), slog.Int("entries", cnt.gone))
		}
		if !cnt.next.IsZero() {
			r.retryAt(p, cnt.next)
		}
	}
	return out
}
