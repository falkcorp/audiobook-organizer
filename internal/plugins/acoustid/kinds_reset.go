// file: internal/plugins/acoustid/kinds_reset.go
// version: 1.0.0
// guid: 8a3f6d1e-7c52-4b0e-9d41-2e6f5a7b9c30
// last-edited: 2026-11-19

package acoustid

import (
	"encoding/json"
	"time"

	"github.com/falkcorp/mediacore/tasks"

	"github.com/falkcorp/audiobook-organizer/pkg/ops"
)

// resetAllNative is the native acoustid.reset-all kind. HEAD has no preview and
// the op wipes state, so a native Preview is refused (kindLiveOnly).
func (p *Plugin) resetAllNative(d KindDeps, sched func(tasks.Kind) *tasks.Schedule) tasks.Definition {
	c := kindCommon(resetAllKind, sched, false)
	c.Title = "Reset all AcoustID fingerprints"
	c.Help = resetAllHelp
	// Writes found by reading: the batched wipe (ClearAllAcoustIDFingerprints)
	// and the per-row fallback (UpdateBookFile) clear the AcoustIDSeg columns of
	// book_file rows and their fpidx/fpidx_meta index entries; DeleteCandidates
	// drops dedup candidate rows. UpdateBookFile's table row supplies the file row
	// and its book aggregate; the wipe and the index entries are fingerprint_index,
	// and the candidates are embeddings (the family F5-04 declares for every dedup
	// candidate write). ClearAllAcoustIDFingerprints and DeleteCandidates have no
	// table row (seams.d/F5-05.md). HEAD declares no Writes.
	c.Effects = tasks.Writes(res(ops.ResAuthors, ops.ResBookFiles, ops.ResBooks, ops.ResEmbeddings,
		ops.ResFingerprintIndex, ops.ResIndexes, ops.ResMetadataCache)...)
	// HEAD Drop: no checkpoint is kept or read; the wipe is idempotent.
	c.Resume = tasks.ResumeDrop
	c.Priority = tasks.PriorityHigh
	c.ConcurrencyKey = fingerprintKey
	c.Timeout = 2 * time.Hour
	return tasks.Task(resetAllKind, tasks.TaskSpec[json.RawMessage, resetAllResult]{
		Common: c,
		Run: func(rc *tasks.RC, raw json.RawMessage) (resetAllResult, error) {
			if err := refuseKindModeKeys(resetAllKind, raw); err != nil {
				return resetAllResult{}, err
			}
			if err := kindLiveOnly(rc, resetAllKind); err != nil {
				return resetAllResult{}, err
			}
			if err := errNeed(resetAllKind, "Store", d.Store != nil); err != nil {
				return resetAllResult{}, err
			}
			w, err := writerFor(resetAllKind, d, rc)
			if err != nil {
				return resetAllResult{}, err
			}
			return resetAllCore(rc.Context(), resetAllEnv{
				kindEnv: nativeEnv(rc), Store: d.Store, Writer: w, Candidates: d.Candidates,
			})
		},
	})
}
