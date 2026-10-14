// file: internal/operations/registry/v3_adapter.go
// version: 1.4.0
// guid: 28d66eb5-7481-4feb-aaa5-ca3f49cc9838
// last-edited: 2026-10-14

package registry

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"sort"
	"sync"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/auth"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/operations"
	"github.com/falkcorp/audiobook-organizer/internal/operations/childop"
	"github.com/falkcorp/audiobook-organizer/internal/operations/opmode"
	"github.com/falkcorp/audiobook-organizer/internal/operations/opswriter"
	"github.com/falkcorp/audiobook-organizer/pkg/ops"
)

// The v3 adapter (05-PR5): an ops.Definition registers as an ordinary
// OperationDef and runs on this executor, so there is one queue, one worker
// pool and one ConcurrencyKey map for v2 and v3 defs alike (library.scan's
// key is never split). FromV2 goes the other way: it wraps an unchanged v2
// def so the catalog can hold it next to native ones.

// v2Origin is an adapted definition's v2 def.
type v2Origin struct{ def OperationDef }

func (o v2Origin) AdaptedDefID() string { return o.def.ID }

// FromV2 wraps an unchanged v2 def as an ops.Definition for the catalog. Its
// behavior is the v2 def's; Register of the result registers it as is. An
// empty v2 Permissions list keeps meaning settings.manage on the trigger
// route (08 X2), so the adapted def carries no permission of its own.
func FromV2(def OperationDef) ops.Definition {
	c := ops.Common{Title: def.DisplayName, Help: def.Description, Exclusive: def.ConcurrencyKey,
		FormerIDs: slices.Clone(def.FormerIDs)}
	if c.Title == "" {
		c.Title = def.ID
	}
	if len(def.Permissions) > 0 {
		c.Permission = def.Permissions[0]
	}
	if len(def.Writes) > 0 {
		ws := make([]ops.Resource, len(def.Writes))
		for i, w := range def.Writes {
			ws[i] = ops.Resource(w)
		}
		c.Effects = ops.Writes(ws...)
	}
	return ops.Adapted(def.ID, c, v2Origin{def: def})
}

// RegisterBundle registers every definition of a bundle under its plugin.
func RegisterBundle(r *Registry, b ops.Bundle) error {
	var errs []error
	for _, d := range b.Defs {
		if err := Register(r, b.Plugin, d); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// Register turns a definition into a registry OperationDef and registers it.
// An adapted v2 def registers unchanged. A native def is validated first
// (ValidateCatalog over it and its pipeline stages).
func Register(r *Registry, plugin string, def ops.Definition) error {
	c := ops.Compile(def)
	if c.Kind == ops.KindAdapted {
		o, ok := c.Origin.(v2Origin)
		if !ok {
			return fmt.Errorf("registry: adapted def %s carries no v2 def", c.ID)
		}
		if err := r.RegisterOp(o.def); err != nil {
			return err
		}
		// A v2 def under this id is not a native one: no stale native entry
		// may make Catalog report it as such.
		r.mu.Lock()
		delete(r.v3defs, c.ID)
		r.mu.Unlock()
		return nil
	}
	check := []ops.Definition{def}
	for _, st := range c.Stages {
		if st.Def != nil {
			check = append(check, st.Def)
		}
	}
	if err := ops.ValidateCatalog(check, nil); err != nil {
		return fmt.Errorf("registry: v3 def %s: %w", c.ID, err)
	}
	od := buildV3Def(r, plugin, c)
	// The native entry goes in BEFORE the def is registered, so there is no
	// moment at which Catalog sees the def and wraps it as a v2 one. Catalog
	// lists r.defs, so an entry whose registration then fails is never seen,
	// and it is taken out again below.
	r.mu.Lock()
	if _, taken := r.defs[c.ID]; taken {
		r.mu.Unlock()
		return fmt.Errorf("registry: OperationDef already registered (id=%s)", c.ID)
	}
	if r.v3defs == nil {
		r.v3defs = map[string]ops.Definition{}
	}
	r.v3defs[c.ID] = def
	r.mu.Unlock()
	if err := r.RegisterOp(od); err != nil {
		r.mu.Lock()
		if r.v3defs[c.ID] == def {
			delete(r.v3defs, c.ID)
		}
		r.mu.Unlock()
		return err
	}
	return nil
}

// Catalog returns every registered def as an ops.Definition: native ones as
// registered, v2 ones wrapped by FromV2, sorted by id.
func (r *Registry) Catalog() []ops.Definition {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]ops.Definition, 0, len(r.defs))
	for id, d := range r.defs {
		if v3, ok := r.v3defs[id]; ok {
			out = append(out, v3)
			continue
		}
		out = append(out, FromV2(d))
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID() < out[j].ID() })
	return out
}

// ValidateCatalog runs ops.ValidateCatalog over the registered catalog. The
// startup call, and the embedded op-id ledger it is given, are 05-PR7's.
func (r *Registry) ValidateCatalog(ledgerIDs []string) error {
	return ops.ValidateCatalog(r.Catalog(), ledgerIDs)
}

// buildV3Def maps Common onto the v2 fields and builds the Run closure.
func buildV3Def(r *Registry, plugin string, c ops.Compiled) OperationDef {
	cm := c.Common
	od := OperationDef{
		ID: c.ID, Plugin: plugin, DisplayName: cm.Title, Description: cm.Help,
		FormerIDs:       slices.Clone(cm.FormerIDs),
		DefaultPriority: Priority(cm.Priority - ops.PriorityLow), // Low/Normal/High by name
		Cancellable:     true,
		Timeout:         cm.Timeout,
		ConcurrencyKey:  cm.Exclusive,
		// ReadyNone runs during the startup warm-up; ReadyStore and
		// ReadyIndexes wait in the dispatcher's warm-up hold (D44), where a
		// run takes no worker, no key and no timeout or watchdog clock.
		NoWarmupWait: cm.Effects.Ready() == ops.ReadyNone,
		Permissions:  []auth.Permission{cm.Permission},
		NotifyLevel:  NotifyAlert,
	}
	if cm.Notify == ops.NotifyActivity {
		od.NotifyLevel = NotifyActivity
	}
	for _, w := range cm.Effects.WriteSet() {
		od.Writes = append(od.Writes, Resource(w))
	}
	for _, rd := range cm.Effects.ReadSet() {
		od.Reads = append(od.Reads, Resource(rd))
	}
	switch c.Resume {
	case ops.ResumeContinue, ops.ResumeFromZero:
		// Same op id, run again; the runner reads or ignores the ledger.
		od.ResumePolicy = ResumeRestart
	case ops.ResumeAsk:
		od.ResumePolicy = ResumeAsk
	default:
		od.ResumePolicy = ResumeDrop
	}
	cancel := cancelContract(c)
	switch {
	case cancel.IsUninterruptible():
		od.Cancellable = false
		od.Liveness = LivenessNone
		od.ProgressTimeout = max(cancel.Budget(), time.Minute)
	case c.Kind == ops.KindBatch || c.Kind == ops.KindFixer:
		// The runner stamps liveness after every item.
		od.Liveness = LivenessRunItems
	default:
		// A Task reports through rc.Progress() at least every Timeout/6; a
		// Pipeline relays its children's progress (rc.Await).
		od.Liveness = LivenessManual
		od.ProgressTimeout = max(cm.Timeout/6, defaultProgressTimeout)
	}
	od.Run = func(ctx context.Context, params json.RawMessage, rep Reporter) error {
		return r.runV3(ctx, c, od, params, rep)
	}
	return od
}

func cancelContract(c ops.Compiled) ops.CancelContract {
	if c.Common.Cancel != nil {
		return *c.Common.Cancel
	}
	if c.Kind == ops.KindTask {
		return ops.Cooperative(time.Minute)
	}
	return ops.AtItemBoundaries()
}

// checkpointField is where a Task's rc.Checkpoint state lives in its params:
// v2 ResumeRestart merges the last checkpoint into the params of the resumed
// run, so rc.Restore reads it back from there.
const checkpointField = "_ops_checkpoint"

// runV3 is a v3 def's Run: it builds the run context and drives the kind.
func (r *Registry) runV3(ctx context.Context, c ops.Compiled, od OperationDef, params json.RawMessage, rep Reporter) error {
	opID := ReporterOpID(rep)
	mode := ops.Live
	if !c.Common.Effects.IsReadOnly() {
		// Omitted mode is Preview for every def that writes.
		dry, err := opmode.ParseDryRun(c.ID, params)
		if err != nil {
			return err
		}
		if dry {
			mode = ops.Preview
		}
	}
	// The run's refusal latch, installed before the Writer is built so the
	// Writer and the batch runner share it (ops.RefusalLatch).
	ctx = ops.WithRefusalLatch(ctx, &ops.RefusalLatch{})
	env := ops.RunEnv{Context: ctx, OpID: opID, DefID: c.ID, Mode: mode, Trigger: ops.Trigger(operations.TriggerSourceFromContext(ctx))}
	if rep != nil {
		env.Log = rep.Logger()
	}
	if row, err := r.store.GetOperationV2(opID); err == nil && row != nil {
		if row.ActorUserID != nil {
			env.Actor.UserID = *row.ActorUserID
		}
		env.Actor.AuthMethod = row.ActorAuthMethod
		if row.ParentID != nil && *row.ParentID != "" {
			env.Trigger = ops.TriggerChild
		}
	}
	// Attempt counts every move into running; above 1, an earlier attempt of
	// this op id ran (a resume or a retry), so writes under it may exist.
	if rec, err := r.getRunV3(opID); err == nil && rec != nil {
		env.Resumed = rec.Attempt > 1
	}
	hooks := r.runnerHooksFor(opID) // nil in production
	var w *opswriter.Writer
	if !c.Common.Effects.IsReadOnly() {
		var err error
		if w, err = r.newV3Writer(ctx, opID, c.ID, mode); err != nil {
			return err
		}
		env.Writer = hooks.writerForTest(w)
	}
	host := &v3Host{r: r, ctx: ctx, opID: opID, defID: c.ID, rep: rep, params: params, writer: w}
	defer host.releaseHolds()
	env.Host = host
	rc := ops.NewRC(env)

	var result json.RawMessage
	var err error
	switch {
	case c.OpenBatch != nil:
		var job *ops.BatchJob
		job, err = c.OpenBatch(rc, params)
		if err != nil {
			return err
		}
		hooks.jobForTest(job)
		bs, _ := database.AsCapability[batchStore](r.store)
		ds, _ := database.AsCapability[dirtySetStore](r.store)
		var sum ops.Summary
		sum, result, err = runBatch(ctx, batchConfig{opID: opID, job: job, rc: rc, store: bs, dirty: ds, rep: rep,
			log: env.Log, stuckAfter: effectiveProgressTimeout(od),
			resumes: func() bool { return r.stopResumes(ctx, opID, od.ResumePolicy) },
			now:     hooks.clockForTest(), hooks: hooks})
		hooks.summaryForTest(sum)
	case c.RunTask != nil:
		result, err = c.RunTask(rc, params)
	default:
		return fmt.Errorf("registry: v3 def %s has nothing to run", c.ID)
	}
	if len(result) > 0 && rep != nil {
		if serr := ReporterSetResult(rep, result); serr != nil {
			err = errors.Join(err, serr)
		}
	}
	return err
}

// v3WriterStores is the store surface a v3 Writer is built from, resolved as
// capabilities so the registry never needs the wide database.Store.
type v3WriterStores struct {
	books   opswriter.BookStore
	files   opswriter.BookFileStore
	authors opswriter.AuthorStore
	history opswriter.HistoryRecorder
	journal opswriter.ChangeJournal
	intents opswriter.IntentStore
}

// historyChangeTypeV3 is the metadata-history change type of v3 Writer rows.
const historyChangeTypeV3 = "ops_v3"

// ErrWriterUnavailable refuses a Live run of a writing def whose store lacks
// the write-ahead intents, the operation journal or the metadata history
// recorder: without them a write could commit with no intent and no
// revertable history.
var ErrWriterUnavailable = errors.New("registry: the store cannot back a v3 Writer (no intents, operation journal or metadata history)")

func (r *Registry) newV3Writer(ctx context.Context, opID, defID string, mode ops.Mode) (*opswriter.Writer, error) {
	var s v3WriterStores
	s.books, _ = database.AsCapability[opswriter.BookStore](r.store)
	s.files, _ = database.AsCapability[opswriter.BookFileStore](r.store)
	s.authors, _ = database.AsCapability[opswriter.AuthorStore](r.store)
	s.history, _ = database.AsCapability[opswriter.HistoryRecorder](r.store)
	s.journal, _ = database.AsCapability[opswriter.ChangeJournal](r.store)
	s.intents, _ = database.AsCapability[opswriter.IntentStore](r.store)
	if mode == ops.Live && (s.intents == nil || s.journal == nil || s.history == nil) {
		return nil, fmt.Errorf("%s: %w", defID, ErrWriterUnavailable)
	}
	return opswriter.New(opswriter.Config{
		OpID: opID, Mode: mode, Fence: opswriter.FenceFromContext(ctx).Check, RunLatch: ops.RefusalLatchFrom(ctx),
		Intents: s.intents, Books: s.books, Files: s.files, Authors: s.authors, Journal: s.journal,
		History: opswriter.NewBookHistory(s.history, defID, historyChangeTypeV3, "op-"+opID+"-", nil),
	}), nil
}

// awaitPollInterval is how often rc.Await reads a child's row.
var awaitPollInterval = childop.DefaultInterval

// v3Host is the executor's half of a v3 run context (ops.Host).
type v3Host struct {
	r      *Registry
	ctx    context.Context
	opID   string
	defID  string
	rep    Reporter
	params json.RawMessage
	writer *opswriter.Writer

	mu        sync.Mutex
	lastDone  int
	lastTotal int
	holds     []*ScanStandDownHold
}

func (h *v3Host) Progress(done int64, total ops.Total, note string) {
	if h.rep == nil {
		return
	}
	h.mu.Lock()
	if done >= 0 {
		h.lastDone = int(done)
		if n, ok := total.Value(); ok {
			h.lastTotal = int(n)
		} else {
			h.lastTotal = 0
		}
	}
	d, t := h.lastDone, h.lastTotal
	h.mu.Unlock()
	if err := h.rep.UpdateProgress(d, t, note); err != nil {
		h.rep.Logger().Debug("ops v3: progress update failed", "op_id", h.opID, "error", err)
	}
}

func (h *v3Host) Touch() {
	if h.rep != nil {
		TouchLiveness(h.rep)
	}
}

func (h *v3Host) Phase(name string) {
	var p *string
	if name != "" {
		p = &name
	}
	if err := h.r.store.UpdateOpPhaseV2(h.opID, p); err != nil && h.rep != nil {
		h.rep.Logger().Debug("ops v3: phase update failed", "op_id", h.opID, "error", err)
	}
}

// Checkpoint saves a Task's state where v2 ResumeRestart merges it back into
// the resumed run's params.
func (h *v3Host) Checkpoint(state json.RawMessage) error {
	if h.rep == nil {
		return errors.New("ops v3: no reporter to checkpoint through")
	}
	return h.rep.Checkpoint(map[string]json.RawMessage{checkpointField: state})
}

func (h *v3Host) Restore() (json.RawMessage, bool, error) {
	var m map[string]json.RawMessage
	if len(h.params) == 0 || json.Unmarshal(h.params, &m) != nil {
		return nil, false, nil
	}
	raw, ok := m[checkpointField]
	return raw, ok && len(raw) > 0, nil
}

func (h *v3Host) Enqueue(def ops.Definition, params json.RawMessage, cfg ops.EnqueueConfig) (ops.RunRef, error) {
	opts := []EnqueueOption{WithParent(h.opID)}
	if cfg.Priority != 0 {
		opts = append(opts, WithPriority(Priority(cfg.Priority-ops.PriorityLow)))
	}
	id, err := h.r.EnqueueOp(h.ctx, def.ID(), params, opts...)
	if err != nil {
		return ops.RunRef{}, err
	}
	if id == "" {
		return ops.RunRef{}, fmt.Errorf("ops v3: %s is batchable; a child run needs an id to await", def.ID())
	}
	return ops.RunRef{ID: id}, nil
}

// Await follows a child to its end and relays its observed progress to this
// run's liveness (childop.Follow: a wedged child makes the parent go quiet).
func (h *v3Host) Await(ctx context.Context, ref ops.RunRef) (ops.Outcome, error) {
	row, err := childop.Follow(ctx, h.r.store, ref.ID, childop.Options{
		Interval:  awaitPollInterval,
		Paused:    OperationsPaused,
		OnObserve: func(childop.Observation) { h.Touch() },
	})
	if err != nil {
		return ops.Outcome{}, err
	}
	out := ops.Outcome{State: row.Status}
	if row.ResultData != nil {
		out.Result = json.RawMessage(*row.ResultData)
	}
	if row.ErrorMessage != nil {
		out.Err = *row.ErrorMessage
	}
	return out, nil
}

// StandDown holds the scan stand-down for a write phase. The run's Writer
// renews the lease on every write and, on a fenced write, tells a lost lease
// from a cancel without renewing.
func (h *v3Host) StandDown(ctx context.Context) (ops.Hold, error) {
	hold, err := HoldScanStandDown(ctx, h.r, h.rep, "ops "+h.defID)
	if err != nil {
		return nil, err
	}
	if h.writer != nil && hold.Held() {
		h.writer.SetLease(hold.Beat)
		opID := h.opID
		h.writer.SetLeaseValid(func() bool { return h.r.ScanStandDownValid(opID) })
	}
	h.mu.Lock()
	h.holds = append(h.holds, hold)
	h.mu.Unlock()
	return &v3Hold{h: h, hold: hold}, nil
}

// releaseHolds releases every hold still held when the run ends.
func (h *v3Host) releaseHolds() {
	h.mu.Lock()
	holds := h.holds
	h.holds = nil
	h.mu.Unlock()
	for _, hd := range holds {
		hd.Release()
	}
	if h.writer != nil {
		h.writer.SetLease(nil)
		h.writer.SetLeaseValid(nil)
	}
}

type v3Hold struct {
	h    *v3Host
	hold *ScanStandDownHold
	once sync.Once
}

func (v *v3Hold) Release() {
	v.once.Do(func() {
		v.hold.Release()
		if v.h.writer != nil {
			v.h.writer.SetLease(nil)
			v.h.writer.SetLeaseValid(nil)
		}
	})
}
