// file: core/internal/executor/publish.go
// version: 1.0.0
// guid: 0e933079-3bc4-4f76-856a-6f98f96ed99a
// last-edited: 2026-11-21

package executor

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"github.com/falkcorp/mediacore/events"
	"github.com/falkcorp/mediacore/storage"
	"github.com/falkcorp/mediacore/tasks"
)

// The executor publishes the three core.task.* topics (tasks.RunChangedTopic,
// LogAppendedTopic, FinishedTopic) after each run commit (05-tasks §5T.4 Run,
// step 3; F5-18 slice E6b). What is published, and when:
//
//   - run-changed: one per committed change to a run's row that a viewer can
//     see: every state transition (move), a queued run's insert, a queued
//     run's merge-fold, and each persisted progress tick. A deferral is not
//     committed, so it publishes nothing (seams.d/F5-18i.md).
//   - log-appended: one per run-log line the executor appends (rc.Note and the
//     notify_failed line), keyed by the run.
//   - finished: one per terminal transition, carrying the same payload as the
//     terminal run-changed.
//
// Every payload is built when the change is staged, from the view the executor
// already builds (view, never viewAs: the stream cannot resolve a blocker's
// kind, so it never carries blocked_by; see streamPayload), and is published
// only after the batch that carries the change has committed.
//
// # How a publish stays after its commit and in commit order
//
// WriteRuns gives each stage attempt a pending collector (pending, in the
// stage's context). A staging site adds an emission to it; the collector holds
// the run's sequencer gate from the moment of the first add. WriteRuns then
// commits. On success the collector appends its emissions to the publisher's
// FIFO and releases the gates; on any failure (a stage error, a conflict that
// retries, a commit error) it discards them and releases the gates, so a
// retried stage starts from nothing and a failed commit publishes nothing.
//
// Ordering guarantee: for one run, emissions reach the FIFO in the order their
// batches committed. Two batches that both emit for a run each hold that
// run's gate from before Commit until after the append, so the one that
// commits first appends first. One drainer goroutine empties the FIFO in
// order, so the sink sees each run's events in commit order. (Across runs
// there is no ordering promise and none is needed: the topics are keyed by run
// ID.) Changes that emit nothing (checkpoints, items, strikes) take no gate.
//
// What is never held across the publish: the drainer calls the sink with no
// lock held, and the commit path only appends to the FIFO (a short mutex, no
// sink call, no I/O), so a slow or stuck subscriber delays publication and
// nothing else: dispatch, starts and ledger writes never wait for it.
//
// Gates are leaves. The code that holds one is a stage's own Commit; nothing
// takes the executor's mu, startMu, an attempt's wmu or any other in-process
// lock while holding a gate, and a commit touches at most one pre-existing run
// besides the runs it creates (whose gates nobody else can reach before the
// commit), so taking a gate inside a stage cannot invert a lock order or
// deadlock. Gates are released before RetryOnConflict backs off.

// emitKind says which topic an emission is for.
type emitKind uint8

const (
	emitRunChanged emitKind = iota + 1
	emitLogAppended
	emitFinished
)

// emission is one pending publish: the topic, the run (the envelope key) and
// the typed payload's wire form.
type emission struct {
	kind emitKind
	run  tasks.RunID
	view tasks.RunPayload  // run-changed and finished
	line tasks.LogAppended // log-appended
	// seq is the run's Seq.Event at the commit that made the emission, for a
	// state transition; the other emissions carry the event sequence the run
	// had when they were staged. It never goes on the wire: tests read it.
	seq uint64
}

// maxPendingEmissions bounds the FIFO. A subscriber that never returns would
// otherwise grow it without bound; past the bound the newest emission is
// dropped and counted (the bus drops the same way, with its own counter).
const maxPendingEmissions = 1 << 14

// publishDrainTimeout bounds how long Shutdown waits for the FIFO to empty.
const publishDrainTimeout = 5 * time.Second

// publisher is the executor's outbound edge: gates, FIFO and drainer.
type publisher struct {
	sink func(emission) // never called with a lock held
	log  *slog.Logger

	mu      sync.Mutex
	gates   map[tasks.RunID]*gate
	q       []emission
	started bool
	closed  bool
	wake    chan struct{} // capacity 1: the FIFO has items or the publisher closed
	done    chan struct{} // closed when the drainer has returned

	dropped atomic.Int64

	// betweenCommitAndAppend is a test seam: it runs on the committing
	// goroutine after Commit and before the append, with the gates held. A
	// test uses it to widen the window an ordering bug needs.
	betweenCommitAndAppend func()
}

// gate serialises commit-then-append for one run. refs counts the collectors
// holding or waiting for it, so the entry is deleted when the last leaves.
type gate struct {
	mu   sync.Mutex
	refs int
}

func newPublisher(sink func(emission), log *slog.Logger) *publisher {
	return &publisher{sink: sink, log: log, gates: map[tasks.RunID]*gate{},
		wake: make(chan struct{}, 1), done: make(chan struct{})}
}

// busSink is the default sink: events.Publish of the typed payload, keyed by
// the run ID. The context carries nothing: the bus reads it only for the
// actor, originating run and trace of the envelope, and a publish made by the
// executor's drainer has none of its own.
func busSink(pub events.Publisher) func(emission) {
	ctx := context.Background()
	return func(em emission) {
		key := string(em.run)
		switch em.kind {
		case emitRunChanged:
			events.Publish(pub, ctx, tasks.RunChangedTopic(), key, em.view)
		case emitFinished:
			events.Publish(pub, ctx, tasks.FinishedTopic(), key, em.view)
		case emitLogAppended:
			events.Publish(pub, ctx, tasks.LogAppendedTopic(), key, em.line)
		}
	}
}

// newPending starts a collector for one stage attempt.
func (p *publisher) newPending() *pending { return &pending{p: p, held: map[tasks.RunID]*gate{}} }

// acquire takes run's gate (creating it) and returns it.
func (p *publisher) acquire(run tasks.RunID) *gate {
	p.mu.Lock()
	g, ok := p.gates[run]
	if !ok {
		g = &gate{}
		p.gates[run] = g
	}
	g.refs++
	p.mu.Unlock()
	g.mu.Lock()
	return g
}

// release gives run's gate back and drops its entry when nobody else wants it.
func (p *publisher) release(run tasks.RunID, g *gate) {
	g.mu.Unlock()
	p.mu.Lock()
	if g.refs--; g.refs == 0 {
		delete(p.gates, run)
	}
	p.mu.Unlock()
}

// enqueue appends em to the FIFO and starts the drainer on first use. It is
// called with the run's gate held and takes only p.mu. After close, emissions
// are dropped: Shutdown has stopped the work that makes them.
func (p *publisher) enqueue(ems []emission) {
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		return
	}
	for _, em := range ems {
		if len(p.q) >= maxPendingEmissions {
			if n := p.dropped.Add(1); n == 1 || n%1000 == 0 {
				p.log.Warn("task event dropped: the publish queue is full (a subscriber is not returning)", "dropped", n, "topic_kind", em.kind)
			}
			continue
		}
		p.q = append(p.q, em)
	}
	if !p.started {
		p.started = true
		go p.drain()
	}
	p.mu.Unlock()
	select {
	case p.wake <- struct{}{}:
	default:
	}
}

// drain is the one goroutine that calls the sink, in FIFO order, with no lock
// held. It returns after close once the FIFO is empty.
func (p *publisher) drain() {
	defer close(p.done)
	for {
		p.mu.Lock()
		batch, closed := p.q, p.closed
		p.q = nil
		p.mu.Unlock()
		for _, em := range batch {
			p.deliver(em)
		}
		if len(batch) > 0 {
			continue
		}
		if closed {
			return
		}
		<-p.wake
	}
}

// deliver calls the sink; a panicking sink loses that event, not the drainer.
func (p *publisher) deliver(em emission) {
	defer func() {
		if r := recover(); r != nil {
			p.log.Error("task event sink panicked; the event is lost", "run", em.run, "panic", r)
		}
	}()
	p.sink(em)
}

// close stops accepting emissions and waits for the FIFO to empty, at most
// until ctx ends or publishDrainTimeout passes. A drainer that never started
// has nothing to wait for.
func (p *publisher) close(ctx context.Context) {
	p.mu.Lock()
	started := p.started
	p.closed = true
	p.mu.Unlock()
	if !started {
		return
	}
	select {
	case p.wake <- struct{}{}:
	default:
	}
	t := time.NewTimer(publishDrainTimeout)
	defer t.Stop()
	select {
	case <-p.done:
	case <-ctx.Done():
	case <-t.C:
		p.log.Warn("task events still unpublished at shutdown", "timeout", publishDrainTimeout)
	}
}

// pending is one stage attempt's collector: the emissions staged so far and
// the gates held for them. It is used by one goroutine (the stage).
type pending struct {
	p     *publisher
	held  map[tasks.RunID]*gate
	items []emission
}

// stage records em, taking its run's gate first if this attempt does not hold
// it. The gate is held until commit or discard.
func (c *pending) stage(em emission) {
	if _, ok := c.held[em.run]; !ok {
		c.held[em.run] = c.p.acquire(em.run)
	}
	c.items = append(c.items, em)
}

// commit hands the emissions to the FIFO (the batch has committed) and
// releases the gates. It must run after Commit returned nil.
func (c *pending) commit() {
	if c.p.betweenCommitAndAppend != nil && len(c.items) > 0 {
		c.p.betweenCommitAndAppend()
	}
	if len(c.items) > 0 {
		c.p.enqueue(c.items)
	}
	c.releaseAll()
}

// discard drops the emissions (the stage failed or the commit did not land)
// and releases the gates.
func (c *pending) discard() {
	c.items = nil
	c.releaseAll()
}

func (c *pending) releaseAll() {
	for run, g := range c.held {
		c.p.release(run, g)
		delete(c.held, run)
	}
}

// pendingKey is the stage context key of the attempt's collector.
type pendingKey struct{}

// stageEmit adds em to the collector of the stage whose context is ctx. It is a
// no-op when the executor has no publisher or ctx is not a WriteRuns stage
// context (a StartIn made outside the executor's WriteRuns commits in a batch
// this package does not see, so it cannot publish).
func stageEmit(ctx context.Context, em emission) {
	if c, ok := ctx.Value(pendingKey{}).(*pending); ok {
		c.stage(em)
	}
}

// streamPayload is the wire form of r for the stream: the view the executor
// builds with no caller, whose BlockedBy is blank. view never names a blocker
// (viewAs does, for a caller who may see the blocker's kind), and the stream
// has no caller kind to check, so BlockedBy is blanked here again: a payload
// built from a view that had BlockedBy set would put another kind's run ID on
// every subscriber's stream. The topic's Redact (tasks.RunViewRedactor,
// applied per subscriber by realtime's stream.Deliver) blanks it a second time.
func (e *executor) streamPayload(r tasks.RunRecord) tasks.RunPayload {
	v := e.view(r)
	v.BlockedBy = ""
	return tasks.NewRunPayload(v)
}

// runChangedOf is the run-changed emission for r as it stands in the batch.
func (e *executor) runChangedOf(r tasks.RunRecord) emission {
	return emission{kind: emitRunChanged, run: r.ID, view: e.streamPayload(r), seq: r.Seq.Event}
}

// finishedOf is the finished emission for the terminal run-changed em.
func finishedOf(em emission) emission {
	em.kind = emitFinished
	return em
}

// logAppendedOf is the log-appended emission for one line of run id's log.
func logAppendedOf(id tasks.RunID, l tasks.LogLine, seq uint64) emission {
	return emission{kind: emitLogAppended, run: id, line: tasks.NewLogAppended(l), seq: seq}
}

// emitRunChanged stages the run-changed emission for r into ctx's stage.
func (e *executor) emitRunChanged(ctx context.Context, r tasks.RunRecord) {
	if e.pub == nil {
		return
	}
	stageEmit(ctx, e.runChangedOf(r))
}

// emitLog stages the log-appended emission for one line of run id's log.
func (e *executor) emitLog(ctx context.Context, id tasks.RunID, l tasks.LogLine, seq uint64) {
	if e.pub == nil {
		return
	}
	stageEmit(ctx, logAppendedOf(id, l, seq))
}

// emitMove stages what a committed move publishes: run-changed for next, and,
// for the one transition into a terminal state, finished beside it. terminal is
// move's own test (next is terminal and cur was not), so a run's terminal state
// is announced once however many times the stage is re-run.
func (e *executor) emitMove(ctx context.Context, next tasks.RunRecord, terminal bool) {
	if e.pub == nil {
		return
	}
	em := e.runChangedOf(next)
	stageEmit(ctx, em)
	if terminal {
		stageEmit(ctx, finishedOf(em))
	}
}

// commitIf and discardIf are commit and discard on a possibly nil collector
// (an executor with no publisher stages none).
func (c *pending) commitIf() {
	if c != nil {
		c.commit()
	}
}

func (c *pending) discardIf() {
	if c != nil {
		c.discard()
	}
}

// emitProgress stages the run-changed emission of a progress write: rec, the
// run as the stage read it, with the numbers the write set.
//
// Progress rewrites the row it reads inside the batch, and rec is a plain read
// taken earlier, so a move that committed between the two would leave rec
// describing a state the committed row no longer has (a cancel's stopping row
// published as running). Every move bumps Seq.Event, so a second read that
// still shows rec's sequence, state, attempt and fence proves no move landed
// in between and rec is what the batch rewrites; otherwise the stage fails with
// a conflict and re-runs from fresh reads (a tick whose state changed is then
// dropped, as tick always has). A move that lands after the second read fails
// the batch's own commit check the same way.
func (e *executor) emitProgress(ctx context.Context, rec tasks.RunRecord, done, total int, current string) error {
	if e.pub == nil {
		return nil
	}
	again, err := e.runs.Get(ctx, rec.ID)
	if err != nil {
		return err
	}
	if again.Seq.Event != rec.Seq.Event || again.State != rec.State || again.Attempt != rec.Attempt || again.FenceEpoch != rec.FenceEpoch {
		return fmt.Errorf("%w: run %s moved while its progress was staged", storage.ErrConflict, rec.ID)
	}
	rec.Done, rec.Total, rec.Current = done, total, current
	stageEmit(ctx, e.runChangedOf(rec))
	return nil
}
