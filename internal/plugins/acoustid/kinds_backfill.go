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
	// HEAD declares Writes [books, book_files] (verified 2026-08-07). The body's
	// store writes are UpdateBookFile (backfill.go:681 doFingerprintFile,
	// backfill.go:710 markFingerprintFailure), ClearBookSignature (backfill.go:789)
	// and ModifyBook (backfill.go:802), the last two in synthesizeBookSignatureForBook. The shared table's row for UpdateBookFile is
	// book_files plus the books row, its metadata_cache stamp and its ident rows
	// (indexes), because the store recomputes the book's aggregates after the file
	// row commits; ModifyBook's row is the same books, metadata_cache and indexes.
	// The kind declares the union: the fence refuses a write whose row is not
	// inside the declaration, so HEAD's narrower set would break the backfill once
	// the executor fences it. HEAD has no fence and already performs these writes;
	// this declares them.
	c.Effects = tasks.Writes(res(ops.ResBooks, ops.ResBookFiles, ops.ResIndexes, ops.ResMetadataCache)...)
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
