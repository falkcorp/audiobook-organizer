// file: core/log/handler.go
// version: 1.0.5
// guid: 00d59f68-e217-4796-9d74-36dcbf58a4ed
// last-edited: 2026-11-08

package log

import (
	"context"
	"fmt"
	"log/slog"
	"slices"
	"sync"
	"sync/atomic"
	"time"

	"go.opentelemetry.io/otel/trace"
)

// handler is the one slog.Handler behind Root, Module, Scope and Handler. It
// implements 05-logging §3's pipeline, in its normative order:
//
//  1. level gate: the scope's *slog.LevelVar, one atomic load (Enabled, and
//     again in Handle for adapters that call Handle directly);
//  2. the context's attributes (WithAttrs) and the trace and span IDs of
//     trace.SpanContextFromContext(ctx) are added;
//  3. every attribute and the message go through the pipeline's Redactor;
//     the logger's own attributes (WithAttrs) and its open groups (WithGroup)
//     are first assembled into the same attribute tree a slog.Group value
//     would make, and that tree is redacted as one, so WithGroup("password")
//     and slog.Group("password", ...) take the same code path;
//  4. fan-out: the primary stdlib Text or JSON handler, then each tap at or
//     above its minimum level. Taps therefore only ever see redacted records.
//
// There is no message sanitizer: the stdlib handlers quote control characters
// in messages and values (TestHandler_EscapesControlChars).
//
// A handler is immutable; WithAttrs and WithGroup return a new one that keeps
// the scope (and so its level gate).
type handler struct {
	l      *Logging
	sc     *scope
	pre    []slog.Attr // WithAttrs before any WithGroup, unredacted
	groups []openGroup // WithGroup names, each with the attrs added after it
	// process marks Root and ProcessHandler and what is derived from them:
	// Record.Process. Handler is the same scope with process false.
	process bool
}

// openGroup is one WithGroup and the (unredacted) attrs added while it was
// the innermost open group.
type openGroup struct {
	name  string
	attrs []slog.Attr
}

// Enabled is the level gate: one atomic load of the scope's LevelVar.
func (h *handler) Enabled(_ context.Context, level slog.Level) bool {
	return level >= h.sc.gate.Level()
}

// WithAttrs keeps attrs, unredacted, inside the innermost open group. They
// are redacted per record in Handle, together with the groups around them,
// so a group name the deny-list matches hides them exactly as it hides the
// members of a slog.Group value; a LogValuer among them is resolved at log
// time.
//
// A subsystem attribute (KeySubsystem, a non-empty string) added outside any
// group is not kept as an attribute: it moves the returned logger to that
// module's subsystem scope, as Scope(module, subsystem) would, so a logger
// that only has a *slog.Logger (the books shim, 05-logging §11 item 2) is
// gated by Levels["module.subsystem"]. The primary line and the taps then
// carry it as the scope's one subsystem, never twice. The last one added wins.
func (h *handler) WithAttrs(attrs []slog.Attr) slog.Handler {
	if len(attrs) == 0 {
		return h
	}
	nh := *h
	red := make([]slog.Attr, 0, len(attrs))
	for _, a := range attrs {
		if sub, ok := subsystemAttr(a); ok && len(h.groups) == 0 {
			nh.sc = h.l.scope(h.sc.module, sub)
			continue
		}
		red = append(red, a)
	}
	if len(red) == 0 {
		return &nh
	}
	if len(h.groups) == 0 {
		nh.pre = append(slices.Clip(h.pre), red...)
		return &nh
	}
	nh.groups = slices.Clone(h.groups)
	last := &nh.groups[len(nh.groups)-1]
	last.attrs = append(slices.Clip(last.attrs), red...)
	return &nh
}

// subsystemAttr reports a's value when it is a subsystem attribute with a
// non-empty string value.
func subsystemAttr(a slog.Attr) (string, bool) {
	if a.Key != KeySubsystem {
		return "", false
	}
	v := a.Value.Resolve()
	if v.Kind() != slog.KindString || v.String() == "" {
		return "", false
	}
	return v.String(), true
}

// WithGroup opens a group: the record's attributes, and attrs added later,
// nest inside it. The context's attributes and the trace and span IDs stay at
// the top level, where the journal and a log search look for run_id or
// trace_id. An empty name is ignored, as slog requires.
func (h *handler) WithGroup(name string) slog.Handler {
	if name == "" {
		return h
	}
	nh := *h
	nh.groups = append(slices.Clip(h.groups), openGroup{name: name})
	return &nh
}

// Handle runs one record through the pipeline. A primary write error loses
// the record for that sink, is counted and reported on stderr at most once a
// minute, and is not returned: logging never fails its caller (05-logging §6).
func (h *handler) Handle(ctx context.Context, r slog.Record) error {
	if r.Level < h.sc.gate.Level() {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	l := h.l

	// Step 2: the record's own attrs, nested in the open groups, then the
	// context's attrs; trace and span IDs from the span context.
	recAttrs := make([]slog.Attr, 0, r.NumAttrs())
	r.Attrs(func(a slog.Attr) bool {
		recAttrs = append(recAttrs, a)
		return true
	})
	var traceID, spanID string
	if sc := trace.SpanContextFromContext(ctx); sc.IsValid() {
		traceID, spanID = sc.TraceID().String(), sc.SpanID().String()
	}

	// Step 3: redact. The logger's attrs, its open groups and the record's
	// attrs form one tree, redacted top-level attr by top-level attr through
	// Redactor.attr (which recurses into groups and applies the key rule to
	// group names). The message is redacted under the empty key: no key rule
	// applies to it, and the text scan (URLs, paths, key=value pairs, auth
	// schemes) does.
	tree := append(slices.Clone(h.pre), h.nest(recAttrs)...)
	body := l.redactAll(tree[:0], tree)
	ctxRed := l.redactAll(nil, ctxAttrs(ctx))
	msg, mk := l.redactor.StringKinds("", r.Message)
	l.countRedactions(mk)
	t := r.Time
	if l.now != nil {
		t = l.now()
	}
	l.countRecord(h.sc.module, r.Level)

	// Step 4a: the primary output.
	out := slog.NewRecord(t, r.Level, msg, r.PC)
	// The process default logger (Root, ProcessHandler; Handler is the same
	// scope) has no module, so its
	// records carry no module attribute; every other scope's do, first.
	if h.sc.module != processModule {
		out.AddAttrs(slog.String(KeyModule, h.sc.module))
	}
	if h.sc.subsystem != "" {
		out.AddAttrs(slog.String(KeySubsystem, h.sc.subsystem))
	}
	out.AddAttrs(body...)
	out.AddAttrs(ctxRed...)
	if traceID != "" {
		out.AddAttrs(slog.String(KeyTraceID, traceID), slog.String(KeySpanID, spanID))
	}
	if err := l.primary.Handle(ctx, out); err != nil {
		l.primaryFailed(err)
	}

	// Step 4b: the taps. flat is built once and never handed out: every tap
	// gets its own clone, so a tap that reorders or edits its Attrs (on its
	// own goroutine) cannot race with, or show through to, another tap.
	taps := *l.taps.Load()
	var flat []slog.Attr
	built := false
	for _, tp := range taps {
		if r.Level < tp.min {
			continue
		}
		if !built {
			flat = appendFlat(appendFlat(nil, body), ctxRed)
			built = true
		}
		tp.offer(Record{
			Time:      t,
			Level:     r.Level,
			Module:    h.sc.module,
			Subsystem: h.sc.subsystem,
			Message:   msg,
			Attrs:     slices.Clone(flat),
			TraceID:   traceID,
			SpanID:    spanID,
			Process:   h.process,
		})
	}
	return nil
}

// nest wraps the record's attrs in the open groups, each group holding the
// attrs added while it was innermost, then the next group: the tree a
// slog.Group value of the same shape would be. A group left with no members
// is dropped, as the stdlib handlers drop it, so an unused WithGroup prints
// nothing (and no "[REDACTED]" for an empty denied group).
func (h *handler) nest(recAttrs []slog.Attr) []slog.Attr {
	inner := recAttrs
	for _, g := range slices.Backward(h.groups) {

		if len(g.attrs) == 0 && len(inner) == 0 {
			continue
		}
		children := append(slices.Clone(g.attrs), inner...)
		inner = []slog.Attr{{Key: g.name, Value: slog.GroupValue(children...)}}
	}
	return inner
}

// redactAll appends the redaction of each of in to dst, counting the kinds.
// dst may alias in's backing array at the same index (in-place redaction).
// It is the only place the pipeline redacts attributes.
func (l *Logging) redactAll(dst, in []slog.Attr) []slog.Attr {
	for _, a := range in {
		ra, k := l.redactor.attr(a)
		l.countRedactions(k)
		dst = append(dst, ra)
	}
	return dst
}

// appendFlat appends attrs to dst the way the stdlib handlers print them: an
// empty attr is dropped, an empty-key group is inlined, an empty group is
// dropped, and group values are rebuilt the same way recursively. Values are
// already resolved by the Redactor.
func appendFlat(dst, attrs []slog.Attr) []slog.Attr {
	for _, a := range attrs {
		if a.Equal(slog.Attr{}) {
			continue
		}
		if a.Value.Kind() != slog.KindGroup {
			dst = append(dst, a)
			continue
		}
		children := appendFlat(nil, a.Value.Group())
		if len(children) == 0 {
			continue
		}
		if a.Key == "" {
			dst = append(dst, children...)
			continue
		}
		dst = append(dst, slog.Attr{Key: a.Key, Value: slog.GroupValue(children...)})
	}
	return dst
}

// Tap registers an asynchronous consumer of redacted records at or above min
// (05-logging §3, §7). The tap owns one goroutine and one bounded channel of
// queue records (at least 1); the caller never blocks: when the queue is full
// the record is dropped. A panic in fn is recovered and is a drop too. Every
// drop is counted under name in Counters().Dropped, and announced to the tap
// by a summary record "N dropped": queued ahead of the next record that fits,
// or delivered last when Close drains the tap. A summary is itself lost only
// when its own delivery panics or stop (or Close's deadline) discards it.
// Taps sharing a name share that counter.
//
// fn receives a context that is cancelled when the tap is stopped or Close's
// deadline passes; fn should return promptly once it is. stop is idempotent:
// it stops delivery (records still queued are discarded, not counted as
// drops), cancels fn's context and waits for the goroutine to exit. It must
// not be called from fn. After Close, with a nil fn, or with the reserved name
// "primary" (the primary output's sink in Counters().Dropped, which a tap
// must not merge into), Tap starts nothing and returns a no-op stop; the
// reserved name is also reported on stderr, since Tap has no error result.
func (l *Logging) Tap(name string, min slog.Level, queue int, fn func(ctx context.Context, r Record)) (stop func()) {
	if fn == nil {
		return func() {}
	}
	if name == primarySink {
		return refusedTap(fmt.Fprintf(l.stderr, "mediacore/log: Tap refused: %q is the primary output's sink name\n", name))
	}
	if queue < 1 {
		queue = 1
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.closed {
		return func() {}
	}
	drops := l.drops[name]
	if drops == nil {
		drops = new(atomic.Uint64)
		l.drops[name] = drops
	}
	ctx, cancel := context.WithCancel(context.Background())
	t := &tap{
		name:   name,
		min:    min,
		fn:     fn,
		ch:     make(chan queued, queue),
		drops:  drops,
		ctx:    ctx,
		cancel: cancel,
		now:    l.clock,
		quit:   make(chan struct{}),
		drain:  make(chan struct{}),
		done:   make(chan struct{}),
	}
	cur := *l.taps.Load()
	next := append(slices.Clip(cur), t)
	l.taps.Store(&next)
	go t.run()
	return func() { t.stop(l) }
}

// refusedTap is the no-op stop of a refused Tap. It takes the result of the
// stderr line that explains the refusal and deliberately ignores it: stderr
// is the last place a failure could be reported, and the refusal (the no-op
// stop) stands either way. Written this way because core's errcheck ratchet
// checks blank assignments (check-blank), so `_, _ = fmt.Fprintf(...)` would
// count as a new finding.
func refusedTap(int, error) (stop func()) { return func() {} }

// tap is one asynchronous consumer.
type tap struct {
	name    string
	min     slog.Level
	fn      func(ctx context.Context, r Record)
	ch      chan queued
	drops   *atomic.Uint64 // Counters().Dropped[name], shared by taps of one name
	pending atomic.Uint64  // drops not yet reported by a summary record

	ctx    context.Context
	cancel context.CancelFunc
	now    func() time.Time // the Logging's clock, for the closing summary

	quit      chan struct{} // closed by stop: exit without delivering the queue
	drain     chan struct{} // closed by Close: deliver the queue, then exit
	done      chan struct{} // closed when run returns
	stopOnce  sync.Once
	drainOnce sync.Once
}

// offer queues rec without blocking. When earlier records were dropped, a
// summary record goes first; if it does not fit, rec is dropped too.
func (t *tap) offer(rec Record) {
	if t.pending.Load() > 0 {
		if n := t.pending.Swap(0); n > 0 {
			select {
			case t.ch <- queued{rec: t.summary(n, rec.Time), summary: true}:
			default:
				t.pending.Add(n)
				t.dropped()
				return
			}
		}
	}
	select {
	case t.ch <- queued{rec: rec}:
	default:
		t.dropped()
	}
}

func (t *tap) dropped() {
	t.drops.Add(1)
	t.pending.Add(1)
}

// summary is the record that tells a tap n records were dropped. It is at
// least warn, and at least the tap's minimum, so the tap does not filter it.
func (t *tap) summary(n uint64, at time.Time) Record {
	return Record{
		Time:      at,
		Level:     max(LevelWarn, t.min),
		Module:    rootModule,
		Subsystem: "log",
		Message:   fmt.Sprintf("%d dropped", n),
		Attrs:     []slog.Attr{slog.String("sink", t.name), slog.Uint64("dropped", n)},
	}
}

// run is the tap's goroutine.
func (t *tap) run() {
	defer close(t.done)
	for {
		select {
		case <-t.quit:
			return
		case <-t.drain:
			for {
				select {
				case q := <-t.ch:
					if t.ctx.Err() != nil {
						return
					}
					t.deliver(q)
				default:
					// The queue is empty: announce drops no later record
					// will carry a summary for (fan-out has stopped).
					if n := t.pending.Swap(0); n > 0 && t.ctx.Err() == nil {
						t.deliver(queued{rec: t.summary(n, t.now()), summary: true})
					}
					return
				}
			}
		case q := <-t.ch:
			select {
			case <-t.quit:
				return
			default:
			}
			t.deliver(q)
		}
	}
}

// queued is one entry of a tap's channel; summary marks a drop summary,
// which is not a log record, so a panic while delivering it is not a drop.
type queued struct {
	rec     Record
	summary bool
}

// deliver calls fn, counting a panic on a log record as a drop (announced in
// the next summary): a broken tap must not take the process down with it.
func (t *tap) deliver(q queued) {
	defer func() {
		if recover() != nil && !q.summary {
			t.dropped()
		}
	}()
	t.fn(t.ctx, q.rec)
}

// stop removes the tap from the fan-out, discards its queue and joins its
// goroutine. Idempotent.
func (t *tap) stop(l *Logging) {
	t.stopOnce.Do(func() {
		l.mu.Lock()
		cur := *l.taps.Load()
		if i := slices.Index(cur, t); i >= 0 {
			next := slices.Delete(slices.Clone(cur), i, i+1)
			l.taps.Store(&next)
		}
		l.mu.Unlock()
		close(t.quit)
		t.cancel()
	})
	<-t.done
}

// beginDrain tells the goroutine to deliver what is queued and exit.
func (t *tap) beginDrain() {
	t.drainOnce.Do(func() { close(t.drain) })
}
