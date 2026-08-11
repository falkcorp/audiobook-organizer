// file: internal/database/pebble_store_versiongroup_backfill.go
// version: 1.2.0
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
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

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

// versionGroupBackfillChunk is how many index rows are buffered before a
// commit.
//
// This used to be unbounded: one pebble.Batch accumulated a row for EVERY
// book and was committed once at the end. On a 366,922-book production
// library that buffers the entire rebuild in memory and makes nothing durable
// until the final commit, so an interrupted run threw away all of its work and
// started over from zero on the next boot — and, because the only log line was
// emitted after that final commit, a long run was indistinguishable from a run
// that never started. Chunking bounds the memory, makes progress durable, and
// gives the progress log something to report.
// A var, not a const, only so tests can lower it and exercise the multi-chunk
// path without creating ten thousand books. Never reassign it in production
// code.
var versionGroupBackfillChunk = 10_000

// versionGroupBackfillLogEvery is how many books are scanned between progress
// logs. A backfill over a six-figure library must say something while it
// works; silence for minutes is indistinguishable from being wedged.
const versionGroupBackfillLogEvery = 50_000

// BackfillVersionGroupIndex writes the secondary index for every book with a
// non-empty VersionGroupID.
//
// Idempotent in two independent senses, both of which matter:
//
//  1. Gated by a sentinel key, so the common case after the first successful
//     run is a cheap no-op (and says so in the log).
//  2. The work itself is repeatable. Every write is a deterministic
//     key -> value derived only from the book row, so re-running re-writes
//     identical bytes. An interrupted run therefore costs time on the next
//     boot, never correctness, and the sentinel is committed only after every
//     chunk is durable — a partial rebuild never marks itself complete.
//
// Every exit path logs. A one-time repair whose execution cannot be observed
// should be treated as not having run.
func (p *PebbleStore) BackfillVersionGroupIndex() error {
	start := time.Now()

	// Only pebble.ErrNotFound means "not yet run". Any other error is a real
	// read failure, and must not be silently reinterpreted as "not done" —
	// that would trigger a full rebuild of a six-figure library on every boot.
	switch _, closer, err := p.db.Get([]byte(versionGroupBackfillKey)); {
	case err == nil:
		closer.Close()
		slog.Info("versiongroup-backfill: already complete, skipping",
			"sentinel", versionGroupBackfillKey)
		return nil
	case errors.Is(err, pebble.ErrNotFound):
		// Not yet run — fall through and do the work.
	default:
		slog.Error("versiongroup-backfill: cannot read sentinel, aborting",
			"sentinel", versionGroupBackfillKey, "err", err)
		return fmt.Errorf("read backfill sentinel: %w", err)
	}

	slog.Info("versiongroup-backfill: starting",
		"sentinel", versionGroupBackfillKey,
		"chunk", versionGroupBackfillChunk)

	iter, err := p.db.NewIter(&pebble.IterOptions{
		LowerBound: []byte("book:0"),
		UpperBound: []byte("book:;"),
	})
	if err != nil {
		slog.Error("versiongroup-backfill: cannot open iterator", "err", err)
		return err
	}
	defer iter.Close()

	var (
		batch     = p.db.NewBatch()
		buffered  int
		indexed   int
		scanned   int
		skipped   int
		unmarshal int
		commits   int
	)
	// Close whatever batch is current if we bail out early. Reassigned after
	// each commit, so this always refers to the live one. The previous version
	// leaked the batch on the success path entirely.
	defer func() {
		if batch != nil {
			batch.Close()
		}
	}()

	flush := func() error {
		if buffered == 0 {
			return nil
		}
		if err := batch.Commit(pebble.Sync); err != nil {
			return err
		}
		batch.Close()
		batch = p.db.NewBatch()
		commits++
		buffered = 0
		return nil
	}

	for iter.First(); iter.Valid(); iter.Next() {
		key := string(iter.Key())
		// Only the primary `book:<id>` rows carry full JSON payloads. Book IDs
		// are ULIDs and contain no colons, so a primary row is exactly
		// "book:<id>" — one colon. Every secondary index has more. Counting is
		// exact where the previous substring blacklist was not: it had to
		// enumerate every index prefix, listed ":organizedhash:" twice, and
		// would have silently started unmarshalling rows for any index prefix
		// added later and forgotten here.
		if strings.Count(key, ":") != 1 {
			continue
		}

		var book Book
		if err := json.Unmarshal(iter.Value(), &book); err != nil {
			unmarshal++
			continue
		}
		scanned++
		if scanned%versionGroupBackfillLogEvery == 0 {
			slog.Info("versiongroup-backfill: progress",
				"scanned", scanned, "indexed", indexed,
				"elapsed", time.Since(start).Round(time.Second).String())
		}
		if book.VersionGroupID == nil || *book.VersionGroupID == "" {
			skipped++
			continue
		}
		vgKey := []byte(fmt.Sprintf("book:versiongroup:%s:%s", *book.VersionGroupID, book.ID))
		if err := batch.Set(vgKey, []byte(book.ID), nil); err != nil {
			slog.Error("versiongroup-backfill: batch write failed",
				"book_id", book.ID, "scanned", scanned, "indexed", indexed, "err", err)
			return err
		}
		indexed++
		buffered++
		if buffered >= versionGroupBackfillChunk {
			if err := flush(); err != nil {
				slog.Error("versiongroup-backfill: chunk commit failed",
					"scanned", scanned, "indexed", indexed, "err", err)
				return err
			}
		}
	}
	if err := iter.Error(); err != nil {
		slog.Error("versiongroup-backfill: iteration failed",
			"scanned", scanned, "indexed", indexed, "err", err)
		return err
	}

	// The sentinel joins the CURRENT batch, so the tail rows and the
	// "complete" marker become durable in the same atomic commit. The sentinel
	// therefore can never outlive the rows it claims were written: if the
	// process dies before this commit, no sentinel exists, and the next boot
	// re-runs and rewrites identical rows over the chunks already committed.
	if err := batch.Set([]byte(versionGroupBackfillKey), []byte("1"), nil); err != nil {
		return err
	}
	if err := batch.Commit(pebble.Sync); err != nil {
		slog.Error("versiongroup-backfill: sentinel commit failed",
			"scanned", scanned, "indexed", indexed, "err", err)
		return err
	}

	slog.Info("versiongroup-backfill: complete",
		"scanned", scanned,
		"indexed", indexed,
		"no_version_group", skipped,
		"unmarshal_errors", unmarshal,
		"commits", commits+1,
		"duration", time.Since(start).Round(time.Millisecond).String())
	return nil
}
