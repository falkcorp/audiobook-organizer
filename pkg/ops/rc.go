// file: pkg/ops/rc.go
// version: 1.0.0
// guid: 340c802d-854d-4fad-9e9f-cd5f9caae2b7
// last-edited: 2026-10-13

package ops

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"maps"
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

// Reset clears the tally for the next item.
func (t *Tally) Reset() {
	t.mu.Lock()
	defer t.mu.Unlock()
	clear(t.counters)
	t.skip = ""
}

// ItemRC is RC plus the item's own deadline and counters.
type ItemRC struct {
	*RC
	tally *Tally
	touch func()
}

// NewItemRC builds an item's context. ctx carries the item's deadline; touch
// heartbeats the worker's lease and the run's liveness. Executor only.
func NewItemRC(rc *RC, ctx context.Context, t *Tally, touch func()) *ItemRC {
	if t == nil {
		t = &Tally{}
	}
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

// Touch is liveness inside one long item (v2 TouchLiveness). Call it after
// real work, never from a timer.
func (rc *ItemRC) Touch() {
	if rc.touch != nil {
		rc.touch()
	}
}
