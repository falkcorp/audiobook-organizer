// file: core/outbox/relay.go
// version: 1.0.0
// guid: 77b6d1de-9cf2-4c52-8f0b-7e9a5c2b6d11
// last-edited: 2026-11-12

package outbox

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"sync"
	"sync/atomic"
	"time"

	"github.com/falkcorp/mediacore/internal/outboxstore"
	"github.com/falkcorp/mediacore/storage"
	"github.com/falkcorp/mediacore/storage/codec"
	"github.com/falkcorp/mediacore/tasks"
)

// Limits that bound what one pass of the relay holds in memory or spends.
const (
	intakeChunkRows     = 256     // intake rows sequenced per batch
	intakeChunkBytes    = 1 << 20 // payload bytes per intake batch
	intakeChunksPerLoop = 8       // chunks per topic before delivery gets its turn
	prefetchRows        = 32      // messages read ahead for one (consumer, partition)
	passMaxMessages     = 256     // messages one pair delivers per pass
	passBudget          = 2 * time.Second
	stopRetryEvery      = 30 * time.Second // a topic or pair stopped on an unreadable row is looked at again
	statusEvery         = 15 * time.Second
	settleAttempts      = 5
)

// relay is one relay run's working state. Only the run's goroutine and the
// pair workers it starts touch it; mu guards what they share.
type relay struct {
	o    *Outbox
	rc   *tasks.RC
	log  *slog.Logger
	cfg  Config
	regs []*consumerReg

	mu        sync.Mutex
	idle      map[outboxstore.Pair]uint64    // pair -> the topic's high-water it was drained at
	blocked   map[outboxstore.Pair]time.Time // ordered pair -> when its head message's retry is due (skipped until then)
	retries   map[outboxstore.Pair]time.Time // unordered pair -> its earliest retry (a wake-up, never a skip)
	pairStop  map[outboxstore.Pair]time.Time // pair -> when it stopped on an unreadable row
	topicStop map[string]time.Time
	epoch     uint64 // Outbox.opsEpoch the idle and blocked maps were built under

	inited   map[string]bool
	fast     []time.Duration
	lastSwp  time.Time
	lastStat time.Time
	moved    atomic.Int64 // sequenced + delivered, for the loop's "busy" test
	stopErr  error
}

// relayBody is the run of core.outbox.relay (05-events §5E.5.3): it sequences
// intake, delivers to every consumer, sweeps retention and reports health,
// until relay_rotate has passed, then succeeds. Everything durable it writes
// is a batch that went through the run's fenced writer, and every state it
// changes carries its own guard, so a zombie run can only fail its commits.
func (o *Outbox) relayBody(rc *tasks.RC, _ struct{}) (struct{}, error) {
	ctx := rc.Context()
	if rc.Mode() != tasks.ModeLive {
		rc.Progress().Note("the relay delivers in Live mode only; this run did nothing")
		return struct{}{}, nil
	}
	cfg := o.config()
	if !cfg.RelayEnabled {
		o.health.setDisabled(true)
		o.health.report(o.now())
		rc.Progress().Note("core.outbox.relay_enabled is false")
		return struct{}{}, nil
	}
	o.running.Store(true)
	defer o.running.Store(false)

	r := &relay{
		o: o, rc: rc, log: rc.Log(), cfg: cfg, regs: o.consumers(),
		idle: map[outboxstore.Pair]uint64{}, blocked: map[outboxstore.Pair]time.Time{}, retries: map[outboxstore.Pair]time.Time{},
		pairStop: map[outboxstore.Pair]time.Time{}, topicStop: map[string]time.Time{},
		inited: map[string]bool{}, epoch: o.opsEpoch.Load(),
	}
	o.health.setDisabled(false)
	deadline := o.now().Add(min(cfg.RelayRotate, tasks.MaxTimeout-relayTimeoutSlack))
	for {
		if err := ctx.Err(); err != nil {
			return struct{}{}, context.Cause(ctx)
		}
		r.cfg = o.config()
		if !r.cfg.RelayEnabled {
			o.health.setDisabled(true)
			o.health.report(o.now())
			return struct{}{}, nil
		}
		o.health.setDisabled(false)
		rc.Progress().Tick()
		r.syncEpoch()
		before := r.moved.Load()
		r.ensureCursors(ctx)
		r.intakeAll(ctx)
		r.deliverAll(ctx)
		r.maintain(ctx)
		if r.stopErr != nil {
			return struct{}{}, r.stopErr
		}
		if !o.now().Before(deadline) {
			return struct{}{}, nil
		}
		r.wait(ctx, deadline, r.moved.Load() != before)
	}
}

// gate runs fn as one write of the run, through its fenced writer: a canceled,
// abandoned or timed-out run refuses it with tasks.ErrFenced, which ends the
// relay. A run without a writer (a unit test's host) runs fn as it is.
func (r *relay) gate(ctx context.Context, name, subject string, fn func(ctx context.Context) error) error {
	w := r.rc.Writer()
	if w == nil {
		return fn(ctx)
	}
	err := w.Do(ctx, tasks.Write{Name: name, Resource: tasks.Res("core", "outbox"), Subject: tasks.Res("core", "outbox")},
		func(ctx context.Context) ([]tasks.Change, error) { return nil, fn(ctx) })
	if errors.Is(err, tasks.ErrFenced) || errors.Is(err, tasks.ErrPreview) {
		r.setStop(err)
	}
	return err
}

func (r *relay) setStop(err error) {
	r.mu.Lock()
	if r.stopErr == nil {
		r.stopErr = err
	}
	r.mu.Unlock()
}

func (r *relay) stopped() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.stopErr != nil
}

// syncEpoch forgets what the relay believed about idle and blocked pairs when
// an operator action (retry, discard, purge) changed the stores under it.
func (r *relay) syncEpoch() {
	e := r.o.opsEpoch.Load()
	r.mu.Lock()
	defer r.mu.Unlock()
	if e != r.epoch {
		r.epoch = e
		clear(r.idle)
		clear(r.blocked)
		clear(r.retries)
	}
}

// wait sleeps until there may be something to do: a nudge, the next intake
// poll, the earliest retry or the rotation, and not at all after a loop that
// moved messages (drain first). A nudge is followed by two short rescans,
// because Stage nudges before its batch commits.
func (r *relay) wait(ctx context.Context, deadline time.Time, busy bool) {
	if busy {
		return
	}
	o := r.o
	d := r.cfg.IntakePoll
	if len(r.fast) > 0 {
		d, r.fast = min(d, r.fast[0]), r.fast[1:]
	}
	if at := r.earliestBlocked(); !at.IsZero() {
		d = min(d, at.Sub(o.now()))
	}
	d = min(d, deadline.Sub(o.now()))
	d = max(d, time.Millisecond)
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
	case <-t.C:
	case <-o.wake:
		r.fast = []time.Duration{10 * time.Millisecond, 100 * time.Millisecond}
	}
}

func (r *relay) earliestBlocked() time.Time {
	r.mu.Lock()
	defer r.mu.Unlock()
	// A time already past is "due, look again next loop", never a reason to
	// spin: only a future time shortens the wait.
	now := r.o.now()
	var at time.Time
	for _, m := range []map[outboxstore.Pair]time.Time{r.blocked, r.retries} {
		for _, t := range m {
			if t.After(now) && (at.IsZero() || t.Before(at)) {
				at = t
			}
		}
	}
	return at
}

// ---- cursors

// ensureCursors creates, in the run's first loop, the cursors of every
// registered consumer that has none: at the topic's high-water mark ("now"), or
// at zero with FromBeginning. It runs before the first intake scan, so a
// message sequenced afterwards is above every new cursor and none is missed.
func (r *relay) ensureCursors(ctx context.Context) {
	o := r.o
	for _, reg := range r.regs {
		if r.inited[reg.name] || ctx.Err() != nil || r.stopped() {
			continue
		}
		ti, err := o.topic(reg.topic)
		if err != nil {
			r.log.Error("outbox: consumer's topic is unusable", slog.String("consumer", reg.name), slog.Any("err", err))
			continue
		}
		err = r.gate(ctx, "outbox.cursors", reg.name, func(ctx context.Context) error {
			return storage.RetryOnConflict(ctx, settleAttempts, func(ctx context.Context) (err error) {
				b := o.store.OpenBatch()
				defer closeBatch(b, &err)
				n, err := o.store.StageCursorInit(ctx, b, reg.name, ti.name, ti.partitions, reg.cfg.fromBeginning, o.now())
				if err != nil || n == 0 {
					return err
				}
				return b.Commit(ctx, storage.Sync)
			})
		})
		if err != nil {
			r.fault("cursors of "+reg.name, err)
			continue
		}
		r.inited[reg.name] = true
	}
}

// ---- intake

// intakeAll sequences the intake rows of every topic that has some, one batch
// per chunk, topics independently: a topic that cannot be read stops alone.
func (r *relay) intakeAll(ctx context.Context) {
	o := r.o
	topics, err := o.store.IntakeTopics(ctx)
	if err != nil {
		r.fault("intake topics", err)
		return
	}
	for _, topic := range topics {
		if ctx.Err() != nil || r.stopped() {
			return
		}
		r.intakeTopic(ctx, topic)
	}
}

func (r *relay) intakeTopic(ctx context.Context, topic string) {
	o := r.o
	subject := "topic " + topic
	r.mu.Lock()
	since, isStopped := r.topicStop[topic]
	r.mu.Unlock()
	if isStopped && o.now().Sub(since) < stopRetryEvery {
		return
	}
	ti, err := o.topic(topic)
	if err != nil {
		// Intake of a topic nobody declares (a removed topic) stays where it is.
		r.stopTopic(topic, subject, "undeclared_topic", err)
		return
	}
	for range intakeChunksPerLoop {
		rows, _, scanErr := o.store.ScanIntake(ctx, topic, o.now(), intakeChunkRows, intakeChunkBytes)
		if len(rows) > 0 {
			if err := r.sequence(ctx, ti, rows); err != nil {
				r.intakeFault(ctx, topic, subject, err)
				return
			}
			r.moved.Add(int64(len(rows)))
			r.rc.Progress().Tick()
		}
		if scanErr != nil {
			r.intakeFault(ctx, topic, subject, scanErr)
			return
		}
		if len(rows) < intakeChunkRows {
			break
		}
	}
	if isStopped {
		r.mu.Lock()
		delete(r.topicStop, topic)
		r.mu.Unlock()
		o.health.clear(subject)
	}
}

// sequence commits one chunk: the intake rows become m/ rows with consecutive
// seqs, the intake rows are deleted, and seq/<topic> advances, all in one batch
// whose read of seq/<topic> is checked at commit.
func (r *relay) sequence(ctx context.Context, ti *topicInfo, rows []outboxstore.IntakeRow) error {
	o := r.o
	return r.gate(ctx, "outbox.intake", ti.name, func(ctx context.Context) (err error) {
		b := o.store.OpenBatch()
		defer closeBatch(b, &err)
		if _, err := o.store.StageSequence(ctx, b, ti.name, ti.partitions, rows); err != nil {
			return err
		}
		h := o.hooks.get()
		if h.beforeIntakeCommit != nil {
			h.beforeIntakeCommit(ti.name)
		}
		if err := b.Commit(ctx, storage.Sync); err != nil {
			return err
		}
		if h.afterIntakeCommit != nil {
			h.afterIntakeCommit(ti.name)
		}
		return nil
	})
}

// intakeFault decides what a failed intake means: a lost race is retried by
// the next loop (the rows are still there to be read again); a partition count
// that changed or a row of a newer build stops the topic, loudly, and nothing
// else.
func (r *relay) intakeFault(ctx context.Context, topic, subject string, err error) {
	switch {
	case ctx.Err() != nil || r.stopped():
	case errors.Is(err, storage.ErrConflict):
		r.log.Debug("outbox: intake lost a race; rescanning", slog.String("topic", topic))
	case errors.Is(err, outboxstore.ErrPartitionsChanged):
		r.stopTopic(topic, subject, "partitions_changed", err)
	case errors.Is(err, codec.ErrUnknownVersion):
		r.stopTopic(topic, subject, "unknown_version", err)
	default:
		r.fault("intake of "+topic, err)
	}
}

func (r *relay) stopTopic(topic, subject, why string, err error) {
	r.mu.Lock()
	r.topicStop[topic] = r.o.now()
	r.mu.Unlock()
	if r.o.health.stop(subject, why) {
		r.log.Error("outbox: topic stopped; it is not skipped", slog.String("topic", topic), slog.String("why", why), slog.Any("err", err))
	}
}

// fault logs an error that is neither a cancellation nor an expected race.
func (r *relay) fault(what string, err error) {
	if err == nil || r.rc.Context().Err() != nil || r.stopped() {
		return
	}
	r.log.Warn("outbox: "+what+" failed", slog.Any("err", err))
}

// ---- maintenance

// maintain sweeps retention every sweepEvery and refreshes the status gauges
// and the health component every statusEvery.
func (r *relay) maintain(ctx context.Context) {
	o := r.o
	now := o.now()
	if now.Sub(r.lastSwp) >= o.sweepEvery {
		r.lastSwp = now
		if err := r.sweep(ctx); err != nil {
			r.fault("retention sweep", err)
		}
	}
	if now.Sub(r.lastStat) >= statusEvery {
		r.lastStat = now
		o.RefreshStatus(ctx)
	}
}

// RefreshStatus recomputes the status the gauges and core.outbox report. The
// relay calls it every 15 seconds; operator actions call it after they change
// anything.
func (o *Outbox) RefreshStatus(ctx context.Context) {
	st, err := o.Status(ctx)
	if err != nil {
		o.log.Warn("outbox: status failed", slog.Any("err", err))
	}
	o.inst.publish(&st)
	dead := make(map[string]int, len(st.Consumers))
	for _, c := range st.Consumers {
		if c.Dead > 0 {
			dead[c.Name] = c.Dead
		}
	}
	o.health.setDead(dead)
	o.health.report(o.now())
}

// sortedTopics lists the durable topics of the catalog by name.
func (o *Outbox) sortedTopics() []string {
	var out []string
	for t := range o.cat.All() {
		if t.Durable != nil {
			out = append(out, t.Name)
		}
	}
	slices.Sort(out)
	return out
}

// topic resolves a durable topic of the catalog; the result is cached, the
// catalog being frozen once the App is built.
func (o *Outbox) topic(name string) (*topicInfo, error) {
	o.topicsMu.Lock()
	defer o.topicsMu.Unlock()
	if ti, ok := o.topicsMem[name]; ok {
		return ti, nil
	}
	spec, ok := o.cat.Lookup(name)
	if !ok {
		return nil, fmt.Errorf("%w: %s", ErrUnknownTopic, name)
	}
	if spec.Durable == nil {
		return nil, fmt.Errorf("%w: %s", ErrNotDurable, name)
	}
	n := outboxstore.Partitions(spec.Durable.Partitions)
	if !outboxstore.ValidPartitions(n) {
		return nil, fmt.Errorf("%w: %s declares %d", ErrBadPartitions, name, spec.Durable.Partitions)
	}
	chk, err := newSchemaCheck(spec.Schema)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", name, err)
	}
	ti := &topicInfo{name: name, partitions: n, spec: spec, check: chk}
	o.topicsMem[name] = ti
	return ti, nil
}
