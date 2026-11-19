// file: internal/plugins/acoustid/kinds_online.go
// version: 1.0.0
// guid: 2d9b7e44-6a13-4f8c-b0a5-9c1e3d5f7a82
// last-edited: 2026-11-19

package acoustid

import (
	"encoding/json"
	"time"

	"github.com/falkcorp/mediacore/tasks"

	"github.com/falkcorp/audiobook-organizer/pkg/ops"
)

// onlineLookupNative is the native acoustid.lookup-online kind. HEAD has no
// preview and the op stamps every looked-up row, so a native Preview is
// refused (kindLiveOnly).
func (p *Plugin) onlineLookupNative(d KindDeps, sched func(tasks.Kind) *tasks.Schedule) tasks.Definition {
	c := kindCommon(onlineLookupKind, sched, false)
	c.Title = "Look up fingerprints on AcoustID.org"
	c.Help = onlineLookupHelp
	// Writes found by reading: the only write is Writer.UpdateBookFile (the
	// looked-up stamp and the recording match on the hydrated row). Its table row
	// is the book_files row and its book aggregate, plus fingerprint_index, which
	// the row does not name but the save rewrites (seams.d/F5-05.md). HEAD declares
	// no Writes.
	c.Effects = tasks.Writes(res(ops.ResAuthors, ops.ResBookFiles, ops.ResBooks, ops.ResFingerprintIndex, ops.ResIndexes, ops.ResMetadataCache)...)
	// HEAD ResumeRequeue maps to FromZero: the pass skips files already stamped, so
	// a re-run is cheap, but HEAD never read a checkpoint.
	c.Resume = tasks.ResumeFromZero
	c.Priority = tasks.PriorityLow
	c.ConcurrencyKey = onlineKey
	c.Timeout = 24 * time.Hour
	return tasks.Task(onlineLookupKind, tasks.TaskSpec[json.RawMessage, onlineLookupResult]{
		Common: c,
		Run: func(rc *tasks.RC, raw json.RawMessage) (onlineLookupResult, error) {
			if _, err := decodeNativeParams[OnlineLookupParams](onlineLookupKind, raw); err != nil {
				return onlineLookupResult{}, err
			}
			if err := kindLiveOnly(rc, onlineLookupKind); err != nil {
				return onlineLookupResult{}, err
			}
			if err := errNeed(onlineLookupKind, "Store", d.Store != nil); err != nil {
				return onlineLookupResult{}, err
			}
			w, err := writerFor(onlineLookupKind, d, rc)
			if err != nil {
				return onlineLookupResult{}, err
			}
			return onlineLookupCore(rc.Context(), raw, onlineLookupEnv{
				kindEnv: nativeEnv(rc), Store: d.Store, Writer: w, Lookup: d.Lookup.hooks(),
			})
		},
	})
}
