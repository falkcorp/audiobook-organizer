// file: internal/plugins/acoustid/kinds_window.go
// version: 1.0.0
// guid: ac083cf5-2a72-43e3-8e1a-fc47d5578d06
// last-edited: 2026-11-19

package acoustid

import (
	"encoding/json"
	"errors"
	"fmt"

	"github.com/falkcorp/mediacore/tasks"

	"github.com/falkcorp/audiobook-organizer/pkg/ops"
)

// WindowBackfillNativeParams are the native kind's parameters: HEAD's
// WindowBackfillParams without Live (the run's mode comes from the start
// request, rule: a native shell refuses a "live" key) and without Resume (the
// op's own cursor, restored from the checkpoint, never a caller's).
type WindowBackfillNativeParams struct {
	Concurrency          int  `json:"concurrency,omitempty"`
	RemoteOnly           bool `json:"remote_only,omitempty"`
	AllowServerDecode    bool `json:"allow_server_decode,omitempty"`
	NoWorkerGraceSec     int  `json:"no_worker_grace_sec,omitempty"`
	PendingGraceSec      int  `json:"pending_grace_sec,omitempty"`
	RebootstrapReference bool `json:"rebootstrap_reference,omitempty"`
}

// coreParams is the native params as the core's own params struct: every
// setting a caller can pass, the mode decided by the run (Resume is the op's
// own cursor and comes from the checkpoint, never from here).
func (np WindowBackfillNativeParams) coreParams(live bool) WindowBackfillParams {
	return WindowBackfillParams{
		Live: live, Concurrency: np.Concurrency, RemoteOnly: np.RemoteOnly,
		AllowServerDecode: np.AllowServerDecode, NoWorkerGraceSec: np.NoWorkerGraceSec,
		PendingGraceSec: np.PendingGraceSec, RebootstrapReference: np.RebootstrapReference,
	}
}

// windowBackfillResult is the native run's result: HEAD returned nothing, so
// this is the run's own tally (the plan summary and the counters the progress
// line already carried).
type windowBackfillResult struct {
	Plan        string         `json:"plan"`
	Written     int64          `json:"written"`
	Failed      int64          `json:"failed"`
	Transient   int64          `json:"transient"`
	Stale       int64          `json:"stale"`
	RowGone     int64          `json:"row_gone"`
	WriteErrors int64          `json:"write_errors"`
	Skipped     int            `json:"skipped"`
	Deferred    map[string]int `json:"deferred,omitempty"`
}

// windowBackfillNative is the native acoustid.window-backfill kind. HEAD is a
// dry run by default, so a native Preview is that dry run (not refused): it
// builds the plan and writes nothing, and no writer is built for it.
func (p *Plugin) windowBackfillNative(d KindDeps, sched func(tasks.Kind) *tasks.Schedule) tasks.Definition {
	c := kindCommon(windowBackfillKind, sched, false)
	c.Title = "AcoustID window backfill"
	c.Help = windowBackfillHelp
	// HEAD declares no Writes (its comment: fpwin: sidecar rows are not a
	// Book/BookFile resource). The body writes two method families, both the
	// fpwin: sidecar: ReplaceFingerprintWindows and
	// RecordFingerprintWindowFailure. Neither has a row in
	// internal/maintenance/resources.Table(); the table maps the same key family
	// (fpwin:, through CarryOverFingerprintWindows) to fingerprint_index, the one
	// pkg/ops resource for fingerprint-derived state, so that is declared. It is
	// not books or book_files: HEAD says the op writes no row of either, and the
	// concurrency key already keeps it from the other fingerprint writers
	// (seams.d/F5-05.md).
	c.Effects = tasks.Writes(res(ops.ResFingerprintIndex)...)
	// HEAD ResumeRestart: the body reads its checkpoint on restart (the last
	// checkpoint, the caller's params plus the cursor, is re-dispatched as the
	// params), so the native kind continues from it.
	c.Resume = tasks.ResumeContinue
	c.Priority = tasks.PriorityLow
	c.ConcurrencyKey = fingerprintKey
	// HEAD's timeout is 72h; the kernel refuses a Timeout over tasks.MaxTimeout
	// (24h). The run keeps HEAD's 72h as windowTotalBudget across attempts: it
	// stops admitting work windowYieldMargin before each attempt's deadline,
	// settles its leases, saves the cursor and yields (tasks.ErrContinueInPlace),
	// and the resumed attempt continues from it (window_yield.go).
	c.Timeout = tasks.MaxTimeout
	return tasks.Task(windowBackfillKind, tasks.TaskSpec[json.RawMessage, windowBackfillResult]{
		Common: c,
		Run: func(rc *tasks.RC, raw json.RawMessage) (windowBackfillResult, error) {
			if err := errNeed(windowBackfillKind, "Store", d.Store != nil); err != nil {
				return windowBackfillResult{}, err
			}
			np, err := decodeNativeParams[WindowBackfillNativeParams](windowBackfillKind, raw)
			if err != nil {
				return windowBackfillResult{}, err
			}
			live := rc.Mode() == tasks.ModeLive
			params := np.coreParams(live)
			// HEAD's checkpoint struct, so Restore decodes the bytes HEAD wrote.
			// Only the cursor is taken: the settings are this start's. A garbled
			// checkpoint fails closed, as HEAD's params decode does.
			var cp WindowBackfillParams
			if ok, rerr := rc.Restore(&cp); rerr != nil {
				return windowBackfillResult{}, fmt.Errorf("window-backfill params: %w", rerr)
			} else if ok {
				params.Resume = cp.Resume
			}
			env := windowEnv{kindEnv: nativeEnv(rc), Store: d.Store, Host: p.windowHooks(d.Window),
				Deadline: rc.Deadline, YieldMargin: windowYieldMargin, TotalBudget: windowTotalBudget,
				ContinueInPlace: tasks.ErrContinueInPlace}
			if live {
				w, werr := writerFor(windowBackfillKind, d, rc)
				if werr != nil {
					return windowBackfillResult{}, werr
				}
				env.Writer = w
			}
			res, err := windowBackfillCore(rc.Context(), params, env)
			if err != nil && !(errors.Is(err, tasks.ErrContinueInPlace) && res != nil) {
				return windowBackfillResult{}, err
			}
			yielded := err
			out := windowBackfillResult{
				Written: res.written, Failed: res.failed, Transient: res.transient, Stale: res.stale,
				RowGone: res.rowGone, WriteErrors: res.writeErrors, Skipped: res.skipped, Deferred: res.deferred,
			}
			if res.plan != nil {
				out.Plan = windowPlanSummary(res.plan)
			}
			if yielded != nil {
				// Continue in place: the tally so far, and the sentinel the executor
				// re-queues the run on.
				return out, yielded
			}
			if res.writeErrors > 0 {
				return out, fmt.Errorf("window-backfill: %d file(s) could not be written; see log", res.writeErrors)
			}
			return out, nil
		},
	})
}
