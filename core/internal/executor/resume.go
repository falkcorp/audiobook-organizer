// file: core/internal/executor/resume.go
// version: 1.1.0
// guid: 4cd3dc83-a564-4bfd-90d7-fa6113583f62
// last-edited: 2026-11-10

package executor

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/falkcorp/mediacore/storage"
	"github.com/falkcorp/mediacore/tasks"
)

// reasonUnknownKind is the interrupted_dropped reason of a stored run whose
// kind is neither registered nor held (5T.5; HEAD resume.go's unknown-def
// drop).
const reasonUnknownKind = "unknown_kind"

// consultStandDowns is 5T.4 step 2's boot consult (MOD6-4): a marker that
// survived the restart means its holder died mid-work, so the runs it
// quiesced stay interrupted_quiesced for this boot; the marker is cleared
// and a WARN names the holder.
func (e *executor) consultStandDowns(ctx context.Context) error {
	var markers []tasks.StandDown
	for m, err := range e.opts.Stores.StandDown.List(ctx) {
		if err != nil {
			e.ins.undecodable("standdown")
			continue
		}
		markers = append(markers, m)
	}
	for _, m := range markers {
		e.mu.Lock()
		for _, id := range m.Quiesced {
			e.bootQuiet[id] = true
		}
		e.mu.Unlock()
		if err := e.write(ctx, func(ctx context.Context, b *storage.Batch) error {
			return e.opts.Stores.StandDown.Set(ctx, b, m.Key, nil)
		}); err != nil {
			return fmt.Errorf("executor: clear stand-down marker %s: %w", m.Key, err)
		}
		e.log.Warn("a stand-down holder died mid-work; the runs it quiesced stay interrupted_quiesced until the next boot",
			"key", string(m.Key), "holder", string(m.Holder), "quiesced", len(m.Quiesced))
	}
	return nil
}

// resumeSweep decides every unfinished run at boot (5T.3.6, 5T.4 step 2):
// held runs are left untouched; a run of an unknown kind is dropped; a run
// that was executing when the process died (running or stopping) and an
// interrupted run go through the resume policy; a quiesced run is
// re-queued unless a surviving marker named it or a newer run supersedes it
// (supersededQuiesced).
func (e *executor) resumeSweep(ctx context.Context) error {
	var recs []tasks.RunRecord
	for rec, err := range e.runs.Unfinished(ctx) {
		if err != nil {
			e.ins.undecodable("run")
			e.log.Warn("undecodable run record hidden from the resume sweep", "err", err)
			continue
		}
		recs = append(recs, rec)
	}
	superseded := e.supersededQuiesced(recs)
	for _, rec := range recs {
		resume := e.resumeOne
		if superseded[rec.ID] {
			resume = func(ctx context.Context, rec tasks.RunRecord) error {
				return e.toState(ctx, rec, tasks.StateInterruptedDropped, reasonSuperseded)
			}
		}
		if err := resume(ctx, rec); err != nil && !errors.Is(err, tasks.ErrRunConflict) {
			return fmt.Errorf("executor: resume %s: %w", rec.ID, err)
		}
	}
	return nil
}

// reasonSuperseded is the reason of a quiesced run dropped at boot because a
// newer run of its kind exists (HEAD: "superseded: a newer run of this op
// exists").
const reasonSuperseded = "superseded"

// supersededQuiesced is HEAD's boot supersession of stale quiesced runs
// (supersedeStaleQuiesced, internal/operations/registry/resume.go:65-71),
// which 05-tasks does not restate; F2-03 keeps it (seams.d/F2-03.md). Per
// kind, among the quiesced runs this boot would re-queue (interrupted_quiesced
// or stopping{quiesce}, not those a surviving marker keeps): only the newest
// is kept, and none when a live run of the kind (queued, waiting or running)
// exists, unless the kind merges queued params (Dedupe.Merge): such a kind's
// runs differ by params, so a live run does not cover its quiesced one, but
// its own quiesced backlog still collapses to the newest. Held and unknown
// kinds take no part.
func (e *executor) supersededQuiesced(recs []tasks.RunRecord) map[tasks.RunID]bool {
	quiet := map[tasks.Kind][]tasks.RunRecord{}
	live := map[tasks.Kind]bool{}
	e.mu.Lock()
	for _, r := range recs {
		if e.defs[r.Kind] == nil || e.held(r.Kind) {
			continue
		}
		switch {
		case r.State == tasks.StateInterruptedQuiesced || r.State == tasks.StateStopping && r.Reason == reasonQuiesce:
			if !e.bootQuiet[r.ID] {
				quiet[r.Kind] = append(quiet[r.Kind], r)
			}
		case r.State == tasks.StateQueued || r.State == tasks.StateWaitingDeps || r.State == tasks.StateRunning:
			live[r.Kind] = true
		}
	}
	e.mu.Unlock()
	out := map[tasks.RunID]bool{}
	for kind, rs := range quiet {
		slices.SortFunc(rs, func(a, b tasks.RunRecord) int { // newest first
			if c := b.Created.Compare(a.Created); c != 0 {
				return c
			}
			return strings.Compare(string(b.ID), string(a.ID))
		})
		keep := 1
		if live[kind] && e.defs[kind].c.Dedupe.Merge == nil {
			keep = 0
		}
		for _, r := range rs[keep:] {
			out[r.ID] = true
		}
	}
	return out
}

func (e *executor) resumeOne(ctx context.Context, rec tasks.RunRecord) error {
	if e.held(rec.Kind) {
		e.mu.Lock()
		e.heldCount[rec.Kind.Module()]++
		e.mu.Unlock()
		return nil // 5T.3.11: read-only while the module is disabled
	}
	d := e.defs[rec.Kind]
	if d == nil {
		_, err := e.move(ctx, rec.ID, func(cur tasks.RunRecord) (tasks.RunRecord, tasks.RunEvent, error) {
			if cur.State.Terminal() {
				return cur, tasks.RunEvent{}, errStale(cur, "drop unknown kind")
			}
			next := cur
			next.State, next.Reason, next.Finished = tasks.StateInterruptedDropped, reasonUnknownKind, e.now()
			return next, tasks.RunEvent{Event: "resume", Reason: reasonUnknownKind}, nil
		}, nil)
		if err == nil {
			e.log.Warn("run of an unknown kind dropped at resume", "run", string(rec.ID), "kind", string(rec.Kind))
		}
		return err
	}
	switch rec.State {
	case tasks.StateQueued, tasks.StateWaitingDeps, tasks.StateAwaitingDecision:
		return nil // the dispatcher (or a decision) takes it from here
	case tasks.StateInterruptedQuiesced:
		e.mu.Lock()
		keep := e.bootQuiet[rec.ID]
		e.mu.Unlock()
		if keep {
			return nil
		}
		return e.requeue(ctx, rec, tasks.StateInterruptedQuiesced, false)
	case tasks.StateStopping:
		if rec.Reason == reasonCancel || rec.Reason == reasonWatchdog {
			_, err := e.move(ctx, rec.ID, func(cur tasks.RunRecord) (tasks.RunRecord, tasks.RunEvent, error) {
				if cur.State != tasks.StateStopping {
					return cur, tasks.RunEvent{}, errStale(cur, "finish cancel")
				}
				next := cur
				next.State, next.Finished = tasks.StateCanceled, e.now()
				return next, tasks.RunEvent{Event: "resume", Reason: cur.Reason}, nil
			}, nil)
			return err
		}
		if rec.Reason == reasonQuiesce {
			e.mu.Lock()
			keep := e.bootQuiet[rec.ID]
			e.mu.Unlock()
			if keep {
				return e.toState(ctx, rec, tasks.StateInterruptedQuiesced, reasonQuiesce)
			}
			return e.requeue(ctx, rec, tasks.StateStopping, false)
		}
	}
	// running, stopping{shutdown} and interrupted: the policy decides.
	if rec.Attempt >= e.config().MaxAttempts+rec.Yields && rec.Attempt > 0 {
		e.log.Warn("run restarted too often; failed by the restart guard", "run", string(rec.ID), "attempts", rec.Attempt)
		return e.fail(ctx, rec, "restart_loop")
	}
	switch d.c.Resume {
	case tasks.ResumeContinue:
		return e.requeue(ctx, rec, rec.State, false)
	case tasks.ResumeFromZero:
		return e.requeue(ctx, rec, rec.State, true)
	case tasks.ResumeDrop:
		return e.toState(ctx, rec, tasks.StateInterruptedDropped, "resume_drop")
	default: // ResumeAsk
		return e.toState(ctx, rec, tasks.StateAwaitingDecision, "resume_ask")
	}
}

// requeue puts a run back in the queue under the same run ID; fromZero
// voids its earlier checkpoints and chunks (ResumeFromZero) in the same
// batch.
func (e *executor) requeue(ctx context.Context, rec tasks.RunRecord, from tasks.RunState, fromZero bool) error {
	var extra func(ctx context.Context, b *storage.Batch, next tasks.RunRecord) error
	if fromZero {
		extra = func(ctx context.Context, b *storage.Batch, next tasks.RunRecord) error {
			v, err := json.Marshal(next.Attempt + 1)
			if err != nil {
				return err
			}
			return e.runs.SaveCheckpoint(ctx, b, rec.ID, ckptZero, v)
		}
	}
	_, err := e.move(ctx, rec.ID, func(cur tasks.RunRecord) (tasks.RunRecord, tasks.RunEvent, error) {
		if cur.State != from || cur.Attempt != rec.Attempt || cur.FenceEpoch != rec.FenceEpoch {
			return cur, tasks.RunEvent{}, errStale(cur, "requeue")
		}
		next := cur
		next.State, next.Reason = tasks.StateQueued, ""
		next.Trigger = tasks.TriggerResume
		if fromZero {
			next.Done, next.Total, next.Current = 0, 0, ""
		}
		return next, tasks.RunEvent{Event: "resume", Reason: "requeue"}, nil
	}, extra)
	return err
}

func (e *executor) toState(ctx context.Context, rec tasks.RunRecord, state tasks.RunState, reason string) error {
	_, err := e.move(ctx, rec.ID, func(cur tasks.RunRecord) (tasks.RunRecord, tasks.RunEvent, error) {
		if cur.State != rec.State || cur.Attempt != rec.Attempt || cur.FenceEpoch != rec.FenceEpoch {
			return cur, tasks.RunEvent{}, errStale(cur, "resume")
		}
		next := cur
		next.State, next.Reason = state, reason
		if state.Terminal() {
			next.Finished = e.now()
		}
		return next, tasks.RunEvent{Event: "resume", Reason: reason}, nil
	}, nil)
	return err
}

func (e *executor) fail(ctx context.Context, rec tasks.RunRecord, reason string) error {
	return e.toState(ctx, rec, tasks.StateFailed, reason)
}
