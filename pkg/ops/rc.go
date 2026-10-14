// file: pkg/ops/rc.go
// version: 1.5.0
// guid: 340c802d-854d-4fad-9e9f-cd5f9caae2b7
// last-edited: 2026-10-13

package ops

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"maps"
	"slices"
	"strings"
	"sync"
)

// Mode is Preview or Live, resolved by the framework, never read from a
// dry_run param by the op.
type Mode int

const (
	// Preview records writes as intents and refuses them (ErrPreview).
	Preview Mode = iota + 1
	// Live performs writes.
	Live
)

// String is "preview" or "live", the run record's spelling.
func (m Mode) String() string {
	if m == Live {
		return "live"
	}
	return "preview"
}

// Trigger says what started a run.
type Trigger string

// Triggers.
const (
	TriggerManual    Trigger = "manual"
	TriggerScheduled Trigger = "scheduled"
	TriggerChild     Trigger = "child"
	TriggerResume    Trigger = "resume"
)

// Actor is who caused a run: a user id and the verified auth method.
type Actor struct{ UserID, AuthMethod string }

// RunRef names a child run started with rc.Enqueue.
type RunRef struct{ ID string }

// Outcome is how a child run ended (rc.Await). State is the run's terminal
// status, Result its result payload and Err its error message.
type Outcome struct {
	State  string
	Result json.RawMessage
	Err    string
}

// EnqueueConfig is what EnqueueOpts set on a child run.
type EnqueueConfig struct {
	Priority Priority
	Stage    string // the pipeline stage name, when a Pipeline enqueues
}

// EnqueueOpt configures rc.Enqueue.
type EnqueueOpt func(*EnqueueConfig)

// WithPriority overrides the child def's priority.
func WithPriority(p Priority) EnqueueOpt { return func(c *EnqueueConfig) { c.Priority = p } }

// AsStage records the pipeline stage the child runs as.
func AsStage(name string) EnqueueOpt { return func(c *EnqueueConfig) { c.Stage = name } }

// Hold is a held scan stand-down (rc.StandDown). Release it when the write
// phase ends; the executor also releases it when the run ends.
type Hold interface{ Release() }

// Host is the executor's half of a run context. internal/operations/registry
// implements it; authors never do. done < 0 in Progress leaves the number
// alone (Note).
type Host interface {
	Progress(done int64, total Total, note string)
	Touch()
	Phase(name string)
	Checkpoint(state json.RawMessage) error
	Restore() (json.RawMessage, bool, error)
	Enqueue(def Definition, params json.RawMessage, cfg EnqueueConfig) (RunRef, error)
	Await(ctx context.Context, ref RunRef) (Outcome, error)
	StandDown(ctx context.Context) (Hold, error)
}

// RunEnv is what the executor supplies to build a run context (NewRC).
type RunEnv struct {
	Context context.Context
	OpID    string
	DefID   string
	Mode    Mode
	Trigger Trigger
	Actor   Actor
	Log     *slog.Logger
	// Writer is nil for a ReadOnly def.
	Writer Writer
	Host   Host
	// Resumed: an earlier attempt of this run (the same op id) ran before
	// this one, so writes under the op id may already exist (rc.Resumed).
	Resumed bool
}

// RC is what a run receives. It replaces v2's Reporter and the four side
// interfaces discovered on it by type assertion.
type RC struct {
	ctx      context.Context
	env      *RunEnv
	progress *Progress
}

// NewRC builds a run context. Executor only.
func NewRC(env RunEnv) *RC {
	if env.Context == nil {
		env.Context = context.Background()
	}
	if RefusalLatchFrom(env.Context) == nil {
		env.Context = WithRefusalLatch(env.Context, &RefusalLatch{})
	}
	if env.Log == nil {
		env.Log = slog.New(slog.DiscardHandler)
	}
	e := env
	return &RC{ctx: env.Context, env: &e, progress: &Progress{host: env.Host}}
}

// WithContext returns a copy of rc whose Context is ctx (the runner gives an
// item its own deadline this way).
func (rc *RC) WithContext(ctx context.Context) *RC {
	c := *rc
	c.ctx = ctx
	return &c
}

// Resumed reports whether an earlier attempt of this run ran before this
// one (RunEnv.Resumed).
func (rc *RC) Resumed() bool { return rc.env.Resumed }

// Context is canceled on cancel, timeout, quiesce and shutdown.
func (rc *RC) Context() context.Context { return rc.ctx }

// OpID is the run's id; always set.
func (rc *RC) OpID() string { return rc.env.OpID }

// DefID is the def's id.
func (rc *RC) DefID() string { return rc.env.DefID }

// Mode is Preview or Live.
func (rc *RC) Mode() Mode { return rc.env.Mode }

// Trigger says what started the run.
func (rc *RC) Trigger() Trigger { return rc.env.Trigger }

// Actor is who started the run.
func (rc *RC) Actor() Actor { return rc.env.Actor }

// Log is the run's logger, with op_id and def_id attached by the executor.
func (rc *RC) Log() *slog.Logger { return rc.env.Log }

// Progress is the run's progress handle.
func (rc *RC) Progress() *Progress { return rc.progress }

// Phase declares a phase and returns the function that closes it.
func (rc *RC) Phase(name string) func() {
	if rc.env.Host == nil {
		return func() {}
	}
	rc.env.Host.Phase(name)
	return func() { rc.env.Host.Phase("") }
}

// Writer is the run's only write path: nil for a ReadOnly def, record-only in
// Preview.
func (rc *RC) Writer() Writer { return rc.env.Writer }

// Checkpoint saves a Task's resume state. Batch and Fixer checkpoint through
// the chunk ledger automatically.
func (rc *RC) Checkpoint(v any) error {
	if rc.env.Host == nil {
		return fmt.Errorf("ops: %s: no executor to checkpoint to", rc.env.DefID)
	}
	raw, err := json.Marshal(v)
	if err != nil {
		return fmt.Errorf("ops: %s: checkpoint does not encode: %w", rc.env.DefID, err)
	}
	return rc.env.Host.Checkpoint(raw)
}

// Restore loads the state the previous attempt checkpointed into v. It
// reports false when there is none (a first attempt).
func (rc *RC) Restore(v any) (bool, error) {
	if rc.env.Host == nil {
		return false, nil
	}
	raw, ok, err := rc.env.Host.Restore()
	if err != nil || !ok {
		return false, err
	}
	if err := json.Unmarshal(raw, v); err != nil {
		return false, fmt.Errorf("ops: %s: checkpoint does not decode: %w", rc.env.DefID, err)
	}
	return true, nil
}

// Enqueue starts a child run of def with a parent link to this run.
func (rc *RC) Enqueue(def Definition, params any, opts ...EnqueueOpt) (RunRef, error) {
	if rc.env.Host == nil {
		return RunRef{}, fmt.Errorf("ops: %s: no executor to enqueue on", rc.env.DefID)
	}
	raw, err := json.Marshal(params)
	if err != nil {
		return RunRef{}, fmt.Errorf("ops: %s: child params do not encode: %w", rc.env.DefID, err)
	}
	var cfg EnqueueConfig
	for _, o := range opts {
		o(&cfg)
	}
	return rc.env.Host.Enqueue(def, raw, cfg)
}

// Await follows a child to its end, relaying its liveness to this run.
func (rc *RC) Await(ref RunRef) (Outcome, error) {
	if rc.env.Host == nil {
		return Outcome{}, fmt.Errorf("ops: %s: no executor to await on", rc.env.DefID)
	}
	return rc.env.Host.Await(rc.ctx, ref)
}

// StandDown pauses library.scan for a write phase (v2 HoldScanStandDown). The
// Writer renews the lease on every write while the hold is held.
func (rc *RC) StandDown() (Hold, error) {
	if rc.env.Host == nil {
		return nil, fmt.Errorf("ops: %s: no executor to stand the scan down", rc.env.DefID)
	}
	return rc.env.Host.StandDown(rc.ctx)
}

// Canceled reports whether the run was asked to stop. Finish sees true after
// a cancel.
func (rc *RC) Canceled() bool { return rc.ctx.Err() != nil }

// Tally is one item's buffered counters. The runner gives every item a fresh
// Tally and folds it into the chunk's buffer when the item returns; the
// chunk's buffer reaches the ledger only when the whole chunk completes, so a
// crash never leaves half a chunk's counts in Done. Executor only.
type Tally struct {
	mu       sync.Mutex
	counters map[string]int64
	skip     string
	marks    []string
	refused  error
	// sealed: the item returned. A refusal noted after that is a late write
	// (a goroutine the item left behind) and goes to the run's latch, never
	// to this item's outcome.
	sealed bool
}

// Seal marks the item returned: NoteRefusal no longer flags this Tally.
// The runner gives each item a fresh Tally and seals it when the item
// returns. Executor only.
func (t *Tally) Seal() {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.sealed = true
}

// Refused is the first write the Writer refused for this item because the
// run is fenced or lost its stand-down lease (NoteRefusal), or nil. The
// runner does not record the item's chunk done when it is set, even if the
// item swallowed the error and returned nil.
func (t *Tally) Refused() error {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.refused
}

type tallyKey struct{}

// RefusalLatch is a run's record of a write the Writer refused (fenced or
// lost stand-down lease) that it could not pin on a live item: one made with
// the run's own context, with an unrelated context such as
// context.Background(), or after its item returned. The runner checks it
// after every item and fails the run closed once it is set, since it cannot
// tell which chunk the refused write belonged to. NewRC installs one on the
// run's context; the executor hands the same latch to the run's Writer
// (opswriter.Config.RunLatch) so a write on an unrelated context still
// reaches it. Executor only.
type RefusalLatch struct {
	mu  sync.Mutex
	err error
}

// Err is the first refusal latched, or nil.
func (l *RefusalLatch) Err() error {
	if l == nil {
		return nil
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.err
}

// Note latches err (the first one is kept).
func (l *RefusalLatch) Note(err error) {
	if l == nil || err == nil {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.err == nil {
		l.err = err
	}
}

type latchKey struct{}

// WithRefusalLatch returns ctx carrying l. Executor only.
func WithRefusalLatch(ctx context.Context, l *RefusalLatch) context.Context {
	return context.WithValue(ctx, latchKey{}, l)
}

// RefusalLatchFrom returns the run latch ctx carries, or nil.
func RefusalLatchFrom(ctx context.Context) *RefusalLatch {
	if ctx == nil {
		return nil
	}
	l, _ := ctx.Value(latchKey{}).(*RefusalLatch)
	return l
}

// NoteRefusal records a write refused because the run is fenced or lost its
// stand-down lease. On a live item's context (or one derived from it) it is
// flagged on that item's outcome (Tally.Refused); otherwise, when ctx
// carries the run's RefusalLatch, it is latched there. It reports whether
// either took it; a Writer that gets false latches its own Config.RunLatch.
// Writer implementations only: an op reports its own errors by returning
// them.
func NoteRefusal(ctx context.Context, err error) bool {
	if ctx == nil || err == nil {
		return false
	}
	if t, ok := ctx.Value(tallyKey{}).(*Tally); ok && t != nil {
		t.mu.Lock()
		live := !t.sealed
		if live && t.refused == nil {
			t.refused = err
		}
		t.mu.Unlock()
		if live {
			return true
		}
	}
	if l := RefusalLatchFrom(ctx); l != nil {
		l.Note(err)
		return true
	}
	return false
}

// Counters returns a copy of the item's op-defined counters.
func (t *Tally) Counters() map[string]int64 {
	t.mu.Lock()
	defer t.mu.Unlock()
	return maps.Clone(t.counters)
}

// SkipReason is the reason given to rc.Skip, or "".
func (t *Tally) SkipReason() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.skip
}

// Marks returns the names the item was marked with (rc.Mark).
func (t *Tally) Marks() []string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return slices.Clone(t.marks)
}

// Reset clears the tally for the next item.
func (t *Tally) Reset() {
	t.mu.Lock()
	defer t.mu.Unlock()
	clear(t.counters)
	t.skip = ""
	t.marks = t.marks[:0]
	t.refused = nil
	t.sealed = false
}

// ItemRC is RC plus the item's own deadline and counters. An item must not
// write after it returns: its context is canceled then (ErrItemReturned),
// so a write a goroutine it left behind makes is refused and, when the run
// is fenced, fails the run closed through the run's RefusalLatch.
type ItemRC struct {
	*RC
	tally *Tally
	touch func()
}

// NewItemRC builds an item's context. ctx carries the item's deadline; touch
// heartbeats the worker's lease and the run's liveness. The item's context
// also carries t, so a Writer write made with it can flag a refusal on the
// item's outcome (NoteRefusal). Executor only.
func NewItemRC(rc *RC, ctx context.Context, t *Tally, touch func()) *ItemRC {
	if t == nil {
		t = &Tally{}
	}
	ctx = context.WithValue(ctx, tallyKey{}, t)
	return &ItemRC{RC: rc.WithContext(ctx), tally: t, touch: touch}
}

// Count adds delta to an op-defined counter. Buffered per chunk and folded
// into the ledger when the chunk completes.
func (rc *ItemRC) Count(counter string, delta int64) {
	rc.tally.mu.Lock()
	defer rc.tally.mu.Unlock()
	if rc.tally.counters == nil {
		rc.tally.counters = map[string]int64{}
	}
	rc.tally.counters[counter] += delta
}

// Skip marks the item skipped; it is counted under skipped:<reason> when Item
// returns nil.
func (rc *ItemRC) Skip(reason string) {
	rc.tally.mu.Lock()
	defer rc.tally.mu.Unlock()
	if reason == "" {
		reason = "unspecified"
	}
	rc.tally.skip = reason
}

// Mark records the item's key under name. Like the counters, marks reach
// the ledger only when the item's chunk completes, and they survive a
// resume; Finish reads them as Summary.Marks, every one of them (each is
// stored once, as its own key, so there is no cap). Meant for the sets a
// Finish must list (a Fixer's applied rows). name is a short identifier:
// empty or holding a NUL byte is a programming error and panics, which the
// runner reports as this item's error.
func (rc *ItemRC) Mark(name string) {
	if name == "" || strings.ContainsRune(name, 0) {
		panic(fmt.Sprintf("ops: mark name %q is empty or holds a NUL", name))
	}
	rc.tally.mu.Lock()
	defer rc.tally.mu.Unlock()
	if !slices.Contains(rc.tally.marks, name) {
		rc.tally.marks = append(rc.tally.marks, name)
	}
}

// Touch is liveness inside one long item (v2 TouchLiveness). Call it after
// real work, never from a timer.
func (rc *ItemRC) Touch() {
	if rc.touch != nil {
		rc.touch()
	}
}
