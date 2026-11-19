// file: internal/plugins/acoustid/kinds_duration.go
// version: 1.0.0
// guid: 5b0a0f4e-2f0e-4a54-9f5b-8f0c0c2f6a11
// last-edited: 2026-11-19

package acoustid

import (
	"encoding/json"
	"time"

	"github.com/falkcorp/mediacore/tasks"

	"github.com/falkcorp/audiobook-organizer/pkg/ops"
)

// durationRepairNative is the native acoustid.fingerprint-duration-repair kind.
// HEAD's inverted `live` param (default false = dry run) is dropped: the mode is
// rc.Mode(), and a Preview run is the dry run (it reads and reports, writes
// nothing), so this kind is not live-only.
func (p *Plugin) durationRepairNative(d KindDeps, sched func(tasks.Kind) *tasks.Schedule) tasks.Definition {
	c := kindCommon(durationRepairKind, sched, false)
	c.Title = "Repair fingerprint duration (re-runs fpcalc)"
	c.Help = durationRepairHelp
	// Writes found by reading: the live pass reaches fingerprintBookFile, whose
	// store writes are UpdateBookFile (the re-saved fingerprint and duration, or
	// the failure stamp) and, for the failure path, the same row again. HEAD
	// declares no Writes. UpdateBookFile's table row is the book_files row plus
	// its book aggregate; the fpidx rows the index hook writes inside the call are
	// fingerprint_index, declared on top of the row (seams.d/F5-05.md).
	c.Effects = tasks.Writes(res(ops.ResAuthors, ops.ResBookFiles, ops.ResBooks, ops.ResFingerprintIndex, ops.ResIndexes, ops.ResMetadataCache)...)
	// HEAD Drop: no checkpoint is kept or read.
	c.Resume = tasks.ResumeDrop
	c.Priority = tasks.PriorityLow
	c.ConcurrencyKey = fingerprintKey
	c.Timeout = 6 * time.Hour
	return tasks.Task(durationRepairKind, tasks.TaskSpec[json.RawMessage, durationRepairResult]{
		Common: c,
		Run: func(rc *tasks.RC, raw json.RawMessage) (durationRepairResult, error) {
			if err := refuseKindModeKeys(durationRepairKind, raw); err != nil {
				return durationRepairResult{}, err
			}
			if err := errNeed(durationRepairKind, "Store", d.Store != nil); err != nil {
				return durationRepairResult{}, err
			}
			env := durationRepairEnv{kindEnv: nativeEnv(rc), Store: d.Store, Host: d.Fingerprint.hooks()}
			if env.Live {
				w, err := writerFor(durationRepairKind, d, rc)
				if err != nil {
					return durationRepairResult{}, err
				}
				env.Writer = w
			}
			return durationRepairCore(rc.Context(), env)
		},
	})
}
