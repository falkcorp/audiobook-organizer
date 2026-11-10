// file: core/internal/storagemigrate/run.go
// version: 1.0.1
// guid: 98f094d6-2e4a-420d-b36c-e26822f5cc4d
// last-edited: 2026-11-10

package storagemigrate

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/falkcorp/mediacore/internal/bootseam"
	"github.com/falkcorp/mediacore/storage"
	"github.com/falkcorp/mediacore/storage/datamigrate"
	"github.com/falkcorp/mediacore/tasks"
)

// The crash-matrix labels of the runner's three boot writers (06 §6.5.1
// "Boot writers"; coretest/crashmatrix registers the same names): the plan
// with the running state, each chunk's batch with its shard cursor, and the
// verifying, done and failed states with Verify's report.
const (
	LabelPlan  = "storage.dm.plan"
	LabelChunk = "storage.dm.chunk"
	LabelState = "storage.dm.state"
)

// ErrUnknownMigration: Run was asked for an ID the runner did not register.
// Name coined by F2-06.
var ErrUnknownMigration = errors.New("storagemigrate: unknown data migration")

// ErrVerifyFailed: the migration's Verify failed. Its state is failed, its
// report is kept, and the boot fails with the checkpoint open (§5S.3.13);
// the owner's Barrier versions stay blocked. Name coined by F2-06.
var ErrVerifyFailed = errors.New("storagemigrate: Verify failed")

// Run runs migration id to completion on db, the store the boot opened: the
// state machine of §5S.3.6 (runner rule 2). An absent record is pending: the
// level stamp, then the plan and the running state in one batch. Running
// runs the shards from their cursors; when every shard is at its end the
// state is verifying, and Verify decides done or failed. A failed record is
// verified again (a fixed build may pass). done is never re-entered.
//
// A failure of the migration itself (Step, Verify, a write the rules
// refuse) is a *storage.StepError; bookkeeping I/O and a cancelled boot are
// returned as they are. Cancellation stops at a chunk boundary: the chunk in
// progress is abandoned, so it commits whole or not at all.
func (r *Runner) Run(ctx context.Context, db *storage.DB, id string) (err error) {
	mg := r.byID[id]
	if mg == nil {
		return fmt.Errorf("%w: %s", ErrUnknownMigration, id)
	}
	start := r.opts.Clock()
	mg.stats.mu.Lock()
	mg.stats.ran = true
	mg.stats.mu.Unlock()
	defer func() {
		mg.stats.mu.Lock()
		mg.stats.took = r.opts.Clock().Sub(start)
		mg.stats.mu.Unlock()
	}()
	r.extend("storage.data_migration " + id)
	sks, ok := db.Keyspace(storageOwner)
	if !ok {
		return fmt.Errorf("storagemigrate: the store has no %s keyspace", storageOwner)
	}
	src, err := mg.handle(db)
	if err != nil {
		return err
	}
	rec, found, err := read(ctx, sks, stateKey(id), storage.DataMigrationCodec())
	if err != nil {
		return err
	}
	state := datamigrate.StatePending
	if found {
		state = rec.State
	}
	mg.setState(state)
	var plan planRecord
	switch state {
	case datamigrate.StateDone:
		return nil // never re-entered (DATA2-15); storage does not call a done step
	case datamigrate.StatePending:
		if err := r.raiseLevel(ctx, db, mg); err != nil {
			return err
		}
		if plan, err = r.newPlan(ctx, src, mg); err != nil {
			return err
		}
		b := sks.NewBatch()
		if err := stage(b, sks, planKey(id), planCodec(), plan); err != nil {
			return errors.Join(err, b.Close())
		}
		if err := r.stageState(b, sks, id, datamigrate.StateRunning); err != nil {
			return errors.Join(err, b.Close())
		}
		if err := commit(ctx, b, LabelPlan); err != nil {
			return err
		}
		state = datamigrate.StateRunning
		mg.setState(state)
		r.opts.Logger.Info("storagemigrate: data migration planned", "migration", id, "shards", plan.Shards, "workers", plan.Workers)
	case datamigrate.StateRunning, datamigrate.StateVerifying, datamigrate.StateFailed:
		p, ok, err := read(ctx, sks, planKey(id), planCodec())
		if err != nil {
			return err
		}
		if !ok || p.Shards != len(p.Boundaries)+1 {
			return fmt.Errorf("%w: %s is %s with no usable plan", ErrBookkeepingUnreadable, id, state)
		}
		plan = p
		mg.stats.mu.Lock()
		mg.stats.resumed = true
		mg.stats.mu.Unlock()
	default:
		return fmt.Errorf("%w: %s has unknown state %q", ErrBookkeepingUnreadable, id, state)
	}
	mg.stats.mu.Lock()
	mg.stats.shards = plan.Shards
	mg.stats.mu.Unlock()
	if state == datamigrate.StateRunning {
		if err := r.runShards(ctx, sks, src, mg, plan); err != nil {
			return err
		}
	}
	return r.verify(ctx, sks, src, mg)
}

// source is a migration's source handle: the owner's Keyspace or its
// LegacyRange, which both read and bind to a batch.
type source interface {
	storage.Handle
	storage.Reader
}

// handle is the migration's source handle on db.
func (mg *migration) handle(db *storage.DB) (source, error) {
	if mg.legacy {
		h, ok := db.Legacy(mg.m.Owner)
		if !ok {
			return nil, fmt.Errorf("storagemigrate: %s: owner %s has no legacy range on this store", mg.m.ID, mg.m.Owner)
		}
		return h, nil
	}
	h, ok := db.Keyspace(mg.m.Owner)
	if !ok {
		return nil, fmt.Errorf("storagemigrate: %s: owner %s has no keyspace on this store", mg.m.ID, mg.m.Owner)
	}
	return h, nil
}

func (mg *migration) setState(s string) {
	mg.stats.mu.Lock()
	mg.stats.state = s
	mg.stats.mu.Unlock()
}

// raiseLevel is the level stamp before the first chunk (§5S.3.7): nothing
// when the store's stamp is already at RaisesLevel (a cutover step, whose
// level M7 raised), else the kernel's level step, and without one the boot
// fails before it writes.
func (r *Runner) raiseLevel(ctx context.Context, db *storage.DB, mg *migration) error {
	if mg.m.RaisesLevel == 0 {
		return nil
	}
	lo := mg.m.Owner
	if strings.HasPrefix(string(lo), "core/") {
		lo = "core" // core subsystems share one level (§5S.3.7)
	}
	stored, _ := db.Levels()
	if stored[lo] >= mg.m.RaisesLevel {
		return nil
	}
	if r.opts.LevelStep == nil {
		return fmt.Errorf("%w: %s raises %s to level %d and the store is at %d (F3-04's level step)",
			datamigrate.ErrLevelStepNotWired, mg.m.ID, lo, mg.m.RaisesLevel, stored[lo])
	}
	return r.opts.LevelStep(ctx, db, mg.m.Owner, mg.m.RaisesLevel)
}

// stageState stages the state record s into b.
func (r *Runner) stageState(b *storage.Batch, sks storage.Keyspace, id, s string) error {
	return stage(b, sks, stateKey(id), storage.DataMigrationCodec(),
		storage.DataMigrationRecord{State: s, At: r.opts.Clock().UTC(), Build: r.opts.Build})
}

// commit commits b with storage.Sync inside label's crash-matrix region.
func commit(ctx context.Context, b *storage.Batch, label string) error {
	exit := bootseam.Region(ctx, label)
	defer exit()
	return b.Commit(ctx, storage.Sync)
}

// runShards runs every shard from its committed cursor through
// tasks.ForEach with the migration's Parallelism (runner rule 6), then
// records the verifying state. The first shard to fail cancels the others,
// which stop at their next chunk boundary; its error is returned.
func (r *Runner) runShards(ctx context.Context, sks storage.Keyspace, src source, mg *migration, plan planRecord) error {
	shards := plan.shards()
	for _, s := range shards {
		c, ok, err := read(ctx, sks, shardKey(mg.m.ID, s.n), cursorCodec())
		if err != nil {
			return err
		}
		if ok {
			s.cursor, s.pos = c.Last, c.Last
		}
	}
	run := &shardRun{r: r, mg: mg, sks: sks, src: src, bounds: plan.Boundaries, shards: shards}
	sctx, cancel := context.WithCancelCause(ctx)
	defer cancel(nil)
	_, err := tasks.ForEach(sctx, mg.m.Parallelism, shards, func(c context.Context, s *shardState) error {
		err := run.shard(c, s)
		if err != nil {
			cancel(err)
		}
		return err
	}, tasks.ItemsOptions[*shardState]{Label: func(s *shardState) string { return fmt.Sprintf("%s shard %d", mg.m.ID, s.n) }})
	if err != nil {
		return run.firstError(ctx, err)
	}
	b := sks.NewBatch()
	if err := r.stageState(b, sks, mg.m.ID, datamigrate.StateVerifying); err != nil {
		return errors.Join(err, b.Close())
	}
	if err := commit(ctx, b, LabelState); err != nil {
		return err
	}
	mg.setState(datamigrate.StateVerifying)
	return nil
}

// firstError is the error a failed ForEach stands for: the boot's own end
// when ctx is done, else the first shard error that is not a sibling
// stopping because the first failure cancelled it.
func (run *shardRun) firstError(ctx context.Context, err error) error {
	if ctx.Err() != nil {
		return context.Cause(ctx)
	}
	var ie *tasks.ItemsError
	if !errors.As(err, &ie) {
		return err
	}
	for _, e := range append([]error{ie.First}, ie.Errs...) {
		if e == nil || errors.Is(e, context.Canceled) {
			continue
		}
		var se *storage.StepError
		var pe *tasks.PanicError
		switch {
		case errors.As(e, &se):
			return se
		case errors.As(e, &pe):
			return &storage.StepError{Step: run.mg.ref(), Err: e} // a panic in Step is the step's failure
		default:
			return e
		}
	}
	return err
}

// verify runs Verify after the last shard (runner rule 7) and records done
// or failed with the report, in one batch. A failed Verify is the step's
// failure: the boot fails with the checkpoint open, and storage keeps the
// owner's Barrier versions blocked because the record is not done.
func (r *Runner) verify(ctx context.Context, sks storage.Keyspace, src source, mg *migration) error {
	id := mg.m.ID
	var rep datamigrate.VerifyReport
	var verr error
	if mg.m.Verify != nil {
		rep, verr = mg.m.Verify(ctx, src)
		if verr != nil && ctx.Err() != nil {
			return context.Cause(ctx) // a stopping boot failed nothing
		}
	}
	state := datamigrate.StateDone
	if verr != nil || len(rep.Failures) > 0 {
		state = datamigrate.StateFailed
	}
	rr := reportRecord{State: state, Report: rep, At: r.opts.Clock().UTC()}
	if verr != nil {
		rr.Error = verr.Error()
	}
	b := sks.NewBatch()
	if err := stage(b, sks, reportKey(id), reportCodec(), rr); err != nil {
		return errors.Join(err, b.Close())
	}
	if err := r.stageState(b, sks, id, state); err != nil {
		return errors.Join(err, b.Close())
	}
	if err := commit(ctx, b, LabelState); err != nil {
		return err
	}
	mg.setState(state)
	if state == datamigrate.StateDone {
		r.opts.Logger.Info("storagemigrate: data migration done", "migration", id)
		return nil
	}
	cause := verr
	if cause == nil {
		cause = fmt.Errorf("%d failure(s): %s", len(rep.Failures), strings.Join(rep.Failures, "; "))
	}
	return &storage.StepError{Step: mg.ref(), Err: fmt.Errorf("%w: %s: %w", ErrVerifyFailed, id, cause)}
}

// shardRun is one migration's shard phase.
type shardRun struct {
	r      *Runner
	mg     *migration
	sks    storage.Keyspace
	src    source
	bounds [][]byte
	shards []*shardState
}

// item is one materialised source pair.
type item struct{ kv storage.KV }

// shard runs one shard from its cursor to its end, chunk by chunk.
func (run *shardRun) shard(ctx context.Context, s *shardState) error {
	for {
		if err := ctx.Err(); err != nil {
			return context.Cause(ctx)
		}
		items, err := run.materialize(ctx, s)
		if err != nil {
			return err
		}
		if len(items) == 0 {
			return nil
		}
		if err := run.chunk(ctx, s, items); err != nil {
			s.rewind()
			return err
		}
	}
}

// materialize reads the shard's next chunk of source pairs after its cursor
// (runner rule 5: the key list is fixed before any item is stepped, so the
// chunk's own commits can never appear as input): at most ChunkItems pairs
// and, past the first, at most ChunkBytes of keys and values. The iterator
// is closed before the first Step.
func (run *shardRun) materialize(ctx context.Context, s *shardState) ([]item, error) {
	o := run.r.opts
	var out []item
	var size int64
	for _, rg := range run.mg.source {
		start, end := rg.Start, rg.End
		if s.lo != nil && bytes.Compare(s.lo, start) > 0 {
			start = s.lo
		}
		if s.cursor != nil {
			if succ := append(bytes.Clone(s.cursor), 0); bytes.Compare(succ, start) > 0 {
				start = succ
			}
		}
		if s.hi != nil && (end == nil || bytes.Compare(s.hi, end) < 0) {
			end = s.hi
		}
		if end != nil && bytes.Compare(start, end) >= 0 {
			continue
		}
		for kv, err := range run.src.Scan(ctx, storage.ScanOptions{Start: start, End: end}) {
			if err != nil {
				return nil, err
			}
			kv = kv.Clone()
			out = append(out, item{kv: kv})
			size += int64(len(kv.Key) + len(kv.Value))
			if len(out) >= o.ChunkItems || size >= o.ChunkBytes {
				return out, nil
			}
		}
	}
	return out, nil
}

// chunk steps items into one batch and commits it with the shard's cursor
// (runner rules 3 and 4). It stops early, before the next item, once the
// batch reaches ChunkBytes. On any failure nothing of the chunk commits.
func (run *shardRun) chunk(ctx context.Context, s *shardState, items []item) error {
	mg, sks := run.mg, run.sks
	b := run.newBatch()
	c := &chunk{run: run, shard: s, b: b, counts: map[string]int64{}, staged: map[string]struct{}{}, ctx: ctx}
	w := datamigrate.NewStepWriter(c)
	var last []byte
	for _, it := range items {
		if int64(b.Len()) >= run.r.opts.ChunkBytes && last != nil {
			break
		}
		last = it.kv.Key
		if s.step(it.kv.Key) {
			continue // the migration wrote this key itself (RewritesInPlace)
		}
		c.current = it.kv.Key
		if err := mg.m.Step(ctx, it.kv, w); err != nil {
			return errors.Join(stepFailed(ctx, mg, err), b.Close())
		}
		if c.err != nil {
			return errors.Join(stepFailed(ctx, mg, c.err), b.Close())
		}
	}
	if err := stage(b, sks, shardKey(mg.m.ID, s.n), cursorCodec(), cursorRecord{Last: last}); err != nil {
		if errors.Is(err, storage.ErrBatchDone) {
			// Only a step that committed or closed StepWriter.Batch() ends
			// the chunk's batch before here.
			return stepFailed(ctx, mg, fmt.Errorf("a step committed or closed its chunk's batch: %w", err))
		}
		return errors.Join(err, b.Close())
	}
	// A cancelled boot commits nothing of the chunk: Batch.Commit checks
	// ctx.Err() before anything else (core/storage/batch.go). The mutation
	// sweep's M13 (a cancel that breaks the item loop and commits a cursor
	// past unstepped items) survives only because of that check; this loop
	// has none of its own.
	if err := commit(ctx, b, LabelChunk); err != nil {
		if ctx.Err() == nil && stepCaused(err) {
			return stepFailed(ctx, mg, err)
		}
		return err
	}
	s.commit(last)
	mg.stats.add(c.counts)
	mg.stats.mu.Lock()
	n := mg.stats.chunks
	mg.stats.mu.Unlock()
	if n%int64(run.r.opts.ExtendEvery) == 0 {
		run.r.extend(fmt.Sprintf("storage.data_migration %s chunk %d", mg.m.ID, n))
	}
	return nil
}

// newBatch is a chunk's batch: from the source handle, with c/storage/
// bound for the cursor and the held-item records.
func (run *shardRun) newBatch() *storage.Batch {
	var b *storage.Batch
	switch h := run.src.(type) {
	case storage.LegacyRange:
		b = h.NewBatch()
	case storage.Keyspace:
		b = h.NewBatch()
	}
	return b.Bind(run.sks)
}

// stepFailed marks err, the migration's own failure, as a StepError, unless
// the boot is stopping: a Step that returns because ctx ended failed
// nothing.
func stepFailed(ctx context.Context, mg *migration, err error) error {
	if ctx.Err() != nil {
		return context.Cause(ctx)
	}
	if _, ok := errors.AsType[*storage.StepError](err); ok {
		return err
	}
	return &storage.StepError{Step: mg.ref(), Err: err}
}

// stepCaused reports a commit refusal that the chunk's own writes caused (a
// precondition, a claim's mode, a write outside the handles, a blind write
// in a Serialized range, an oversized chunk, a batch a step committed or
// closed), as opposed to I/O.
func stepCaused(err error) bool {
	for _, e := range []error{storage.ErrConflict, storage.ErrClaimMode, storage.ErrOutsideKeyspace,
		storage.ErrNotSerialized, storage.ErrBlindWrite, storage.ErrBatchTooLarge, storage.ErrBatchDone} {
		if errors.Is(err, e) {
			return true
		}
	}
	return false
}
