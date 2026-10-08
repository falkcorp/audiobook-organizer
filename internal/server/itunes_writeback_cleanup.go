// file: internal/server/itunes_writeback_cleanup.go
// version: 1.0.0
// guid: 6431384b-3b6a-4482-873b-5d295bf639e7
// last-edited: 2026-10-07
//
// One-shot startup cleanup of the state the removed iTunes write-back left in
// the store (owner decision 2026-10-07: iTunes is import-only).

package server

import (
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/logger"
)

// legacyITunesWriteBackPrefixes are the raw-KV prefixes the removed iTunes
// write-back wrote: the batcher's durable queue, held removes and status
// (itunes_writeback:q:*, itunes_writeback:held:remove:*,
// itunes_writeback:status), and the never-wired outbox, which stored
// pref:_system:outbox:writeback:{bookID} rows through SetUserPreferenceForUser.
var legacyITunesWriteBackPrefixes = []string{
	"itunes_writeback:",
	"pref:_system:outbox:writeback:",
}

var legacyITunesWriteBackLog = logger.New("server.itunes-writeback-cleanup")

// legacyITunesWriteBackPageSize bounds each scan page and delete batch.
const legacyITunesWriteBackPageSize = 500

// legacyKVCleaner is the slice of the raw KV store the cleanup needs.
type legacyKVCleaner interface {
	ScanPrefixPage(prefix, after string, limit int) ([]database.KVPair, string, error)
	DeleteRawBatch(keys []string) error
}

// purgeLegacyITunesWriteBackKeys deletes every key under
// legacyITunesWriteBackPrefixes and returns how many it deleted. Idempotent:
// once the keys are gone each run is one empty scan per prefix. A failure is
// logged and stops that prefix; the next startup retries it. Nothing reads
// these keys any more, so a key left behind is dead weight, never a hazard.
func purgeLegacyITunesWriteBackKeys(store legacyKVCleaner) int {
	if store == nil {
		return 0
	}
	deleted := 0
	for _, prefix := range legacyITunesWriteBackPrefixes {
		for {
			// Always page from the start: the previous page was deleted,
			// so the next matching key is the first one left.
			pairs, _, err := store.ScanPrefixPage(prefix, "", legacyITunesWriteBackPageSize)
			if err != nil {
				legacyITunesWriteBackLog.Warn("iTunes write-back cleanup: scan of %s failed; retried next startup: %v", prefix, err)
				break
			}
			if len(pairs) == 0 {
				break
			}
			keys := make([]string, len(pairs))
			for i, kv := range pairs {
				keys[i] = kv.Key
			}
			if err := store.DeleteRawBatch(keys); err != nil {
				legacyITunesWriteBackLog.Warn("iTunes write-back cleanup: delete under %s failed; retried next startup: %v", prefix, err)
				break
			}
			deleted += len(keys)
			if len(pairs) < legacyITunesWriteBackPageSize {
				break
			}
		}
	}
	if deleted > 0 {
		legacyITunesWriteBackLog.Info("iTunes write-back cleanup: deleted %d leftover write-back keys", deleted)
	}
	return deleted
}
