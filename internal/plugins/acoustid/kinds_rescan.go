// file: internal/plugins/acoustid/kinds_rescan.go
// version: 1.0.0
// guid: 6c4e8a20-1b7d-4f93-8e56-3a9d2c0b7f15
// last-edited: 2026-11-19

package acoustid

import (
	"encoding/json"
	"time"

	"github.com/falkcorp/mediacore/tasks"

	"github.com/falkcorp/audiobook-organizer/pkg/ops"
)

// fingerprintRescanNative is the native acoustid.fingerprint-rescan kind. HEAD
// has no preview and the op rewrites fingerprints, so a native Preview is
// refused (kindLiveOnly). Its params are HEAD's {scope, book_ids, force}: none
// of them carries the mode.
func (p *Plugin) fingerprintRescanNative(d KindDeps, sched func(tasks.Kind) *tasks.Schedule) tasks.Definition {
	c := kindCommon(fingerprintRescanKind, sched, false)
	c.Title = "Fingerprint rescan"
	c.Help = fingerprintRescanHelp
	// Writes found by reading: doFingerprintFile and markFingerprintFailure write
	// UpdateBookFile, and synthesizeBookSignatureForBook writes ModifyBook and
	// ClearBookSignature (a book-signature key under books); the table's
	// UpdateBookFile row covers all three (ModifyBook's row is a subset of it) plus
	// fingerprint_index on top, as for the other fingerprint writers. The JSONL
	// report of ineligible files is bytes on disk (files). HEAD declares no Writes.
	c.Effects = tasks.Writes(res(ops.ResAuthors, ops.ResBookFiles, ops.ResBooks, ops.ResFiles,
		ops.ResFingerprintIndex, ops.ResIndexes, ops.ResMetadataCache)...)
	// HEAD Drop: no checkpoint is kept or read.
	c.Resume = tasks.ResumeDrop
	c.Priority = tasks.PriorityLow
	c.ConcurrencyKey = fingerprintKey
	c.Timeout = 12 * time.Hour
	return tasks.Task(fingerprintRescanKind, tasks.TaskSpec[json.RawMessage, fingerprintRescanResult]{
		Common: c,
		Run: func(rc *tasks.RC, raw json.RawMessage) (fingerprintRescanResult, error) {
			// HEAD decodes these params leniently (a bad body means the defaults,
			// which is a library-wide rescan); a native start refuses one instead.
			if _, err := decodeNativeParams[FingerprintRescanParams](fingerprintRescanKind, raw); err != nil {
				return fingerprintRescanResult{}, err
			}
			if err := kindLiveOnly(rc, fingerprintRescanKind); err != nil {
				return fingerprintRescanResult{}, err
			}
			if err := errNeed(fingerprintRescanKind, "Store", d.Store != nil); err != nil {
				return fingerprintRescanResult{}, err
			}
			w, err := writerFor(fingerprintRescanKind, d, rc)
			if err != nil {
				return fingerprintRescanResult{}, err
			}
			return fingerprintRescanCore(rc.Context(), raw, fingerprintRescanEnv{
				kindEnv: nativeEnv(rc), Store: d.Store, Writer: w, Host: d.Fingerprint.hooks(),
			})
		},
	})
}
