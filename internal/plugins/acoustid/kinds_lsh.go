// file: internal/plugins/acoustid/kinds_lsh.go
// version: 1.0.0
// guid: 1424c98c-df69-4ed6-b47b-b008743b5cb1
// last-edited: 2026-11-19

package acoustid

import (
	"encoding/json"
	"time"

	"github.com/falkcorp/mediacore/tasks"

	"github.com/falkcorp/audiobook-organizer/pkg/ops"
)

// lshBackfillNative is the native acoustid.lsh-backfill kind. HEAD has no
// preview (the op re-saves rows to fire the index hook), so a native Preview is
// refused.
func (p *Plugin) lshBackfillNative(d KindDeps, sched func(tasks.Kind) *tasks.Schedule) tasks.Definition {
	c := kindCommon(lshBackfillKind, sched, false)
	c.Title = "Backfill LSH fingerprint index"
	c.Help = lshBackfillHelp
	// Writes found by reading: the only write is Writer.UpdateBookFile, whose
	// table row (internal/maintenance/resources, "UpdateBookFile") is the book_files
	// row plus the book aggregate it recomputes (books, authors, metadata_cache,
	// indexes). The fpidx/fpidx_meta rows the index hook writes inside that call
	// are fingerprint_index, which the table row does not name; it is declared
	// here on top of the row (seams.d/F5-05.md). HEAD declares no Writes.
	c.Effects = tasks.Writes(res(ops.ResAuthors, ops.ResBookFiles, ops.ResBooks, ops.ResFingerprintIndex, ops.ResIndexes, ops.ResMetadataCache)...)
	// HEAD Drop: the body keeps no checkpoint and reads none; the pass is idempotent.
	c.Resume = tasks.ResumeDrop
	c.Priority = tasks.PriorityLow
	c.ConcurrencyKey = fingerprintKey
	c.Timeout = 2 * time.Hour
	return tasks.Task(lshBackfillKind, tasks.TaskSpec[json.RawMessage, lshBackfillResult]{
		Common: c,
		Run: func(rc *tasks.RC, raw json.RawMessage) (lshBackfillResult, error) {
			if err := refuseKindModeKeys(lshBackfillKind, raw); err != nil {
				return lshBackfillResult{}, err
			}
			if err := kindLiveOnly(rc, lshBackfillKind); err != nil {
				return lshBackfillResult{}, err
			}
			if err := errNeed(lshBackfillKind, "Store", d.Store != nil); err != nil {
				return lshBackfillResult{}, err
			}
			w, err := writerFor(lshBackfillKind, d, rc)
			if err != nil {
				return lshBackfillResult{}, err
			}
			return lshBackfillCore(rc.Context(), lshBackfillEnv{kindEnv: nativeEnv(rc), Store: d.Store, Writer: w})
		},
	})
}
