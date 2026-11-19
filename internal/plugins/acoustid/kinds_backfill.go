// file: internal/plugins/acoustid/kinds_backfill.go
// version: 1.0.0
// guid: 9e1c5b73-3d82-4a6f-a7c0-4b8e6d2f1a94
// last-edited: 2026-11-19

package acoustid

import (
	"encoding/json"
	"time"

	"github.com/falkcorp/mediacore/tasks"

	"github.com/falkcorp/audiobook-organizer/pkg/ops"
)

// backfillNative is the native acoustid.backfill kind. HEAD has no preview and
// the op writes fingerprints, so a native Preview is refused (kindLiveOnly).
func (p *Plugin) backfillNative(d KindDeps, sched func(tasks.Kind) *tasks.Schedule) tasks.Definition {
	c := kindCommon(backfillKind, sched, false)
	c.Title = "AcoustID backfill"
	c.Help = backfillHelp
	// HEAD-declared Writes (verified at HEAD 2026-08-07: UpdateBookFile for the
	// fingerprint columns and ModifyBook for the signature): books and book_files.
	c.Effects = tasks.Writes(res(ops.ResBooks, ops.ResBookFiles)...)
	// HEAD ResumeRestart: the body reads its checkpoint on restart (the last
	// checkpoint is re-dispatched as its params and decoded into BackfillParams),
	// so the native kind continues from it (Restore decodes HEAD's bytes).
	c.Resume = tasks.ResumeContinue
	c.Priority = tasks.PriorityLow
	c.ConcurrencyKey = fingerprintKey
	c.Timeout = 24 * time.Hour
	return tasks.Task(backfillKind, tasks.TaskSpec[json.RawMessage, backfillResult]{
		Common: c,
		Run: func(rc *tasks.RC, raw json.RawMessage) (backfillResult, error) {
			// HEAD's params were its checkpoint; a native start has none of its own.
			if err := refuseKindModeKeys(backfillKind, raw); err != nil {
				return backfillResult{}, err
			}
			if err := kindLiveOnly(rc, backfillKind); err != nil {
				return backfillResult{}, err
			}
			if err := errNeed(backfillKind, "Store", d.Store != nil); err != nil {
				return backfillResult{}, err
			}
			w, err := writerFor(backfillKind, d, rc)
			if err != nil {
				return backfillResult{}, err
			}
			workers := backfillWorkers
			if d.Fingerprint.Workers != nil {
				workers = d.Fingerprint.Workers
			}
			return backfillCore(rc.Context(), nil, backfillEnv{
				kindEnv: nativeEnv(rc), Store: d.Store, Writer: w, Workers: workers,
				HasEngine: d.NewEngine != nil, Host: d.Fingerprint.hooks(),
			})
		},
	})
}
