// file: internal/database/pebble_store_versiongroup_backfill.go
// version: 1.1.0
// guid: 9f3b7c21-6d84-4a5e-b0c9-2e7fa1d85b36
// last-edited: 2026-08-10
// PERF-VERSIONS: one-time backfill that writes the
// book:versiongroup:<gid>:<id> secondary index for every existing book
// that has a VersionGroupID. Without this, /audiobooks/:id/versions
// falls back to a 10K-row full scan (~15s in prod). After the backfill
// runs once, the fast path in GetBooksByVersionGroup serves all reads.

package database

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"

	"github.com/cockroachdb/pebble/v2"
)

// versionGroupBackfillKey gates the one-time backfill.
//
// BUMPED v1 -> v2 (2026-08-10): existing deployments already have the v1
// sentinel set, so they would never rebuild. Their index can be incomplete
// because UpdateBook used to write a book's index row only when its
// VersionGroupID *changed* — a row missing after the v1 run could never heal,
// and GetBooksByVersionGroup then under-reported that group without ever
// erroring. Bumping the key makes every deployment rebuild the index once on
// next start; UpdateBook's now-unconditional write keeps it complete after
// that. This IS the production repair — no manual step. Bump again if the key
// format or the set of indexed books ever changes.
const versionGroupBackfillKey = "system:backfill:versiongroup_index_v2_done"

// BackfillVersionGroupIndex writes the secondary index for every book with
// a non-empty VersionGroupID. Idempotent — gated by a sentinel key so
// repeated calls after the first successful run are cheap no-ops.
func (p *PebbleStore) BackfillVersionGroupIndex() error {
	if _, closer, err := p.db.Get([]byte(versionGroupBackfillKey)); err == nil {
		closer.Close()
		return nil
	}

	iter, err := p.db.NewIter(&pebble.IterOptions{
		LowerBound: []byte("book:0"),
		UpperBound: []byte("book:;"),
	})
	if err != nil {
		return err
	}

	batch := p.db.NewBatch()
	indexed := 0
	scanned := 0
	for iter.First(); iter.Valid(); iter.Next() {
		key := string(iter.Key())
		// Only the primary `book:<id>` rows carry full JSON payloads;
		// skip every secondary index prefix.
		if strings.Contains(key, ":path:") || strings.Contains(key, ":series:") ||
			strings.Contains(key, ":author:") || strings.Contains(key, ":version:") ||
			strings.Contains(key, ":versiongroup:") || strings.Contains(key, ":hash:") ||
			strings.Contains(key, ":originalhash:") || strings.Contains(key, ":organizedhash:") ||
			strings.Contains(key, ":organizedhash:") {
			continue
		}

		var book Book
		if err := json.Unmarshal(iter.Value(), &book); err != nil {
			continue
		}
		scanned++
		if book.VersionGroupID == nil || *book.VersionGroupID == "" {
			continue
		}
		vgKey := []byte(fmt.Sprintf("book:versiongroup:%s:%s", *book.VersionGroupID, book.ID))
		if err := batch.Set(vgKey, []byte(book.ID), nil); err != nil {
			iter.Close()
			batch.Close()
			return err
		}
		indexed++
	}
	iter.Close()

	if err := batch.Set([]byte(versionGroupBackfillKey), []byte("1"), nil); err != nil {
		batch.Close()
		return err
	}
	if err := batch.Commit(pebble.Sync); err != nil {
		return err
	}
	slog.Info("versiongroup-backfill scanned indexed", "scanned", scanned, "indexed", indexed)
	return nil
}
