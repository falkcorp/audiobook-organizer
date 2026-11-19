// file: internal/plugins/acoustid/kinds_scan.go
// version: 1.0.0
// guid: 04a8cd3a-045c-426b-9265-47a2fa73406c
// last-edited: 2026-11-19

package acoustid

import (
	"encoding/json"
	"time"

	"github.com/falkcorp/mediacore/tasks"

	"github.com/falkcorp/audiobook-organizer/pkg/ops"
)

// scanNative is the native acoustid.scan kind. HEAD has no preview and the
// engine's scan writes candidate rows as it goes, so a native Preview is
// refused (kindLiveOnly).
func (p *Plugin) scanNative(d KindDeps, sched func(tasks.Kind) *tasks.Schedule) tasks.Definition {
	c := kindCommon(scanKind, sched, false)
	c.Title = "AcoustID fingerprint scan"
	c.Help = scanHelp
	// Writes found by reading: Engine.AcoustIDScan upserts the dedup candidate
	// rows (EmbeddingStore.UpsertCandidateNew) and, for a new book pair, a labeled
	// example (UpsertLabeledExample); both live in the embedding keyspace, which
	// F5-04 declares as embeddings for every dedup candidate and label write. The
	// scan reads books, book_files and the LSH index and writes no other family.
	// HEAD declares no Writes.
	c.Effects = tasks.Writes(res(ops.ResEmbeddings)...)
	// HEAD Drop: the body keeps no checkpoint and reads none.
	c.Resume = tasks.ResumeDrop
	c.Priority = tasks.PriorityLow
	c.ConcurrencyKey = fingerprintKey
	c.Timeout = 6 * time.Hour
	return tasks.Task(scanKind, tasks.TaskSpec[json.RawMessage, scanResult]{
		Common: c,
		Run: func(rc *tasks.RC, raw json.RawMessage) (scanResult, error) {
			if err := refuseKindModeKeys(scanKind, raw); err != nil {
				return scanResult{}, err
			}
			if err := kindLiveOnly(rc, scanKind); err != nil {
				return scanResult{}, err
			}
			if err := errNeed(scanKind, "NewEngine", d.NewEngine != nil); err != nil {
				return scanResult{}, err
			}
			env := scanEnv{kindEnv: nativeEnv(rc), Candidates: d.Candidates}
			if eng := d.NewEngine(rc.Writer()); eng != nil {
				env.Engine = eng
			}
			return scanCore(rc.Context(), env)
		},
	})
}
