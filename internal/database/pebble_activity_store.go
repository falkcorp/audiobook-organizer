// file: internal/database/pebble_activity_store.go
// version: 1.9.0
// guid: d4e5f6a7-b8c9-0004-def0-000000000004
// last-edited: 2026-08-30

// Package database — PebbleDB-backed activity log store.
//
// WHY a Pebble backend:
//   - The NutsDB activity store (nuts_activity_store.go) works but carries an
//     entire extra storage engine dependency (nutsdb/nutsdb). Pebble is already
//     the primary database engine. Removing NutsDB requires a Pebble backend
//     that satisfies the same ActivityStorer interface.
//   - Key layout mirrors NutsDB verbatim so lexicographic ordering and range
//     scans behave identically (no behavioral change to callers).
//
// Key layout (all keys are []byte; prefixes end with ':'):
//
//	act:<tier>:<20d-unix-nano>:<ulid>        = JSON(ActivityEntry)   primary
//	act:op:<op_id>:<20d-unix-nano>:<ulid>    = []byte("<tier>:<20d-unix-nano>:<ulid>")  op index
//	act:bk:<book_id>:<20d-unix-nano>:<ulid>  = []byte("<tier>:<20d-unix-nano>:<ulid>")  book index
//
// Compared with NutsDB, Pebble is a single shared key-space so every key is
// scoped with "act:" to avoid collisions with other prefixes. The "tier" component
// in the primary key separates tiers without needing separate buckets; Pebble range
// scans over ["act:<tier>:", "act:<tier>;") return exactly that tier's entries in
// timestamp order because ';' (0x3B) is one above ':' (0x3A) in ASCII.
package database

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"runtime"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/cockroachdb/pebble/v2"
	"github.com/oklog/ulid/v2"
	"golang.org/x/sync/errgroup"
)

// Tunables for the bounded query path. These are vars (not consts) so tests can
// shrink them and exercise the bound without seeding a huge store.
var (
	// activityQueryScanBudget caps how many stored entries a single Query or
	// GetDistinctSources call will decode. Before this bound existed, a default
	// Query decoded EVERY entry in the log (8.86 GB of heap on production,
	// 71.9% of the process, and a 120s non-returning GET /api/v1/activity?limit=5).
	// Hitting the bound is logged, never silent.
	activityQueryScanBudget = 20000

	// activitySourcesCacheTTL is how long a GetDistinctSources result is reused.
	// The source list feeds a filter picker, so slightly stale counts are fine;
	// re-scanning on every page load was half the OOM.
	activitySourcesCacheTTL = 45 * time.Second

	// activityCtxCheckInterval is how many rows a scan processes between
	// context checks. Checking every row would cost a select per decode;
	// checking never is what let 30 ABANDONED requests keep scanning and
	// allocating on production (zero connected clients, 30 goroutines still
	// inside scanTierKVs, 30.8 GB against a 30 GB cap). Tests set this to 1.
	activityCtxCheckInterval = 64
)

// PebbleActivityStore persists activity log entries in a shared PebbleDB database.
// It satisfies the ActivityStorer interface and is a drop-in replacement for both
// ActivityStore (SQLite) and NutsActivityStore.
//
// The caller retains ownership of the *pebble.DB — Close() on this store is a no-op.
type PebbleActivityStore struct {
	db      *pebble.DB
	counter atomic.Int64

	// entriesDecoded counts every attempt to json.Unmarshal a stored
	// ActivityEntry made by ANY scan path on this store (bounded query,
	// scanTierKVs, ...). It is the instrument that proves a paged query is
	// bounded: it must be incremented at every decode site, otherwise a
	// regression test asserting "few decodes" would pass against an
	// unbounded implementation that simply decodes somewhere else.
	entriesDecoded atomic.Int64

	// decodeFailures counts entries dropped because their stored JSON could
	// not be decoded. Dropped rows are also logged per-scan in aggregate.
	decodeFailures atomic.Int64

	// recordBatchCommits counts the durable commits RecordBatch has made on
	// THIS store.
	//
	// It exists because a commit leaves NO trace in the keyspace: 11 entries
	// look identical on disk whether they went down in one commit or three, so
	// a test for activityRecordBatchCap that only counted stored keys would
	// pass against a cap that had stopped working — the whole point of the cap
	// is the number of commits, and that number is otherwise unobservable.
	// Pebble exposes no per-commit counter (LogWriterMetrics carries throughput
	// and queue-length gauges only), so the instrument has to live here.
	//
	// It is per-store rather than a package var so that a test can read it as
	// an absolute count of its own store's commits. A package-level counter is
	// shared by every test in the package that records anything, which makes
	// the reading correct only while no two of them run concurrently — an
	// invariant nothing enforces and that t.Parallel() would quietly break.
	// One atomic add per commit, i.e. once per activityRecordBatchCap rows.
	recordBatchCommits atomic.Int64

	sourcesMu    sync.Mutex
	sourcesCache map[string]pactSourcesCacheEntry
}

// pactSourcesCacheEntry is one memoized GetDistinctSources result.
type pactSourcesCacheEntry struct {
	at  time.Time
	out []SourceCount
}

// NewPebbleActivityStore creates a PebbleActivityStore backed by the provided DB.
// The caller retains ownership of db; Close() on this store does NOT close db.
func NewPebbleActivityStore(db *pebble.DB) *PebbleActivityStore {
	s := &PebbleActivityStore{db: db, sourcesCache: make(map[string]pactSourcesCacheEntry)}
	// Seed counter from current time so IDs don't collide across restarts.
	s.counter.Store(time.Now().UnixNano())
	return s
}

// EntriesDecoded returns the cumulative number of stored-entry JSON decode
// attempts made by this store's scan paths, including failures. Exported for
// tests and for operational assertions about query cost.
func (s *PebbleActivityStore) EntriesDecoded() int64 { return s.entriesDecoded.Load() }

// DecodeFailures returns the cumulative number of stored entries dropped
// because their JSON could not be decoded.
func (s *PebbleActivityStore) DecodeFailures() int64 { return s.decodeFailures.Load() }

// Close is a no-op: the caller owns the PebbleDB instance.
func (s *PebbleActivityStore) Close() error { return nil }

// DB returns the underlying *pebble.DB. Used by callers (e.g., backfill, registry wiring)
// that need to check the backfill sentinel key directly.
func (s *PebbleActivityStore) DB() *pebble.DB { return s.db }

// ── Key construction ──────────────────────────────────────────────────────────

// pactPrimaryKey builds the primary key for a tier entry:
//
//	act:<tier>:<20d-unix-nano>:<ulid>
func pactPrimaryKey(tier string, t time.Time, id string) []byte {
	return []byte(fmt.Sprintf("act:%s:%020d:%s", tier, t.UnixNano(), id))
}

// pactPrimaryPrefix returns the inclusive lower-bound prefix for a tier range scan:
//
//	act:<tier>:
func pactPrimaryPrefix(tier string) []byte {
	return []byte("act:" + tier + ":")
}

// pactPrimaryUpperBound returns the exclusive upper-bound for a tier range scan.
// ';' is ASCII 0x3B — one above ':' (0x3A) — so the range covers exactly all
// entries for the tier.
func pactPrimaryUpperBound(tier string) []byte {
	return []byte("act:" + tier + ";")
}

// pactTierBounds computes the Pebble iterator bounds for one tier's primary key
// range, narrowed by the optional since/until instants.
//
// Extracted so the bounded query path and scanTierKVs share one definition of
// the time-range semantics rather than each keeping its own copy that can drift.
func pactTierBounds(tier string, since, until *time.Time) (lower, upper []byte) {
	lower = pactPrimaryPrefix(tier)
	upper = pactPrimaryUpperBound(tier)
	if since != nil {
		lower = pactPrimaryKey(tier, *since, "")
	}
	if until != nil {
		upper = pactPrimaryKey(tier, *until, "\xff\xff\xff\xff\xff\xff\xff\xff\xff\xff\xff\xff\xff")
	}
	return lower, upper
}

// pactKeyTimeField returns the zero-padded 20-digit unix-nano component of a
// primary key ("act:<tier>:<nanos>:<ulid>"), or "" if the key is malformed.
//
// The field is fixed-width and zero-padded, so comparing two of these as
// strings is equivalent to comparing the instants numerically. That is what
// lets the query path merge several tiers newest-first WITHOUT decoding each
// row's JSON just to read its timestamp — only the rows actually taken get
// decoded.
func pactKeyTimeField(key []byte) string {
	s := string(key)
	if !strings.HasPrefix(s, "act:") {
		return ""
	}
	rest := s[len("act:"):]
	i := strings.IndexByte(rest, ':') // end of <tier>
	if i < 0 {
		return ""
	}
	rest = rest[i+1:]
	j := strings.IndexByte(rest, ':') // end of <nanos>
	if j < 0 {
		return rest
	}
	return rest[:j]
}

// pactIndexRef encodes the cross-reference value stored in secondary indexes:
// "<tier>:<20d-unix-nano>:<ulid>" — enough to reconstruct the primary key.
func pactIndexRef(tier string, t time.Time, id string) []byte {
	return []byte(fmt.Sprintf("%s:%020d:%s", tier, t.UnixNano(), id))
}

// pactPrimaryKeyFromRef reconstructs the primary key from an index reference value.
// ref = "<tier>:<20d-unix-nano>:<ulid>"
func pactPrimaryKeyFromRef(ref []byte) ([]byte, bool) {
	s := string(ref)
	if !strings.Contains(s, ":") {
		return nil, false
	}
	return []byte("act:" + s), true
}

// pactIndexFamilyPrefixes are the two secondary-index key families Record
// writes alongside every primary row. They are NOT tiers — actTiers contains
// none of them — so a tier range scan never sees them, which is exactly how
// they went undeleted for so long.
var pactIndexFamilyPrefixes = []string{"act:op:", "act:bk:"}

// pactPrimaryKeySuffix splits a primary key "act:<tier>:<20d-unix-nano>:<ulid>"
// and returns everything after the tier — "<20d-unix-nano>:<ulid>" — which is
// byte-for-byte the suffix Record appended to both secondary index keys.
func pactPrimaryKeySuffix(key []byte) (string, bool) {
	s := string(key)
	if !strings.HasPrefix(s, "act:") {
		return "", false
	}
	rest := s[len("act:"):]
	i := strings.IndexByte(rest, ':') // end of <tier>
	if i < 0 {
		return "", false
	}
	rest = rest[i+1:]
	if strings.IndexByte(rest, ':') < 0 { // must still hold <nanos>:<ulid>
		return "", false
	}
	return rest, true
}

// pactIndexKeysFor returns the act:op: / act:bk: keys Record wrote alongside
// primaryKey, so a delete of the primary can remove them in the SAME batch.
//
// WHY the ids are read from the DECODED ENTRY: op_id and book_id are not
// recoverable from the primary key, which carries only tier, timestamp and
// ULID. The alternatives were (a) a third key family mapping primary → index
// keys, which costs an extra write on EVERY Record plus a new invariant to keep
// consistent, and (b) re-scanning act:op:/act:bk: per delete, which is
// O(whole index) per row. Decoding wins here for a reason specific to these
// call sites and not generally true: every path that deletes a primary row
// (Summarize, Prune, WipeAllActivity, CompactByDay) already goes through
// scanTierKVs, which json.Unmarshals EVERY row it returns — Summarize and
// CompactByDay cannot group by day/op/type without it. So reading
// e.OperationID and e.BookID here adds ZERO additional decodes; the marginal
// cost of this fix is one string concat per non-empty id. A prune does not get
// slower because of it.
//
// WHY the nanos+ulid come from the PRIMARY KEY and never from e.Timestamp:
// Record derived both keys from one time.Time, but the entry has since
// round-tripped through JSON. Pebble's Delete of a key that does not exist
// succeeds silently, so one nanosecond of drift would ship a "fix" that
// deletes nothing and reports no error. Slicing the suffix off the key that is
// definitely being deleted cannot drift.
func pactIndexKeysFor(primaryKey []byte, e ActivityEntry) ([][]byte, bool) {
	suffix, ok := pactPrimaryKeySuffix(primaryKey)
	if !ok {
		return nil, false
	}
	var keys [][]byte
	if e.OperationID != "" {
		keys = append(keys, []byte("act:op:"+e.OperationID+":"+suffix))
	}
	if e.BookID != "" {
		keys = append(keys, []byte("act:bk:"+e.BookID+":"+suffix))
	}
	return keys, true
}

// pactDeleteEntry stages the full deletion of one activity row: its primary key
// AND the secondary index entries Record wrote with it, in the same batch.
//
// EVERY deletion path must go through this. Before it existed, Prune,
// Summarize, CompactByDay and WipeAllActivity each deleted kv.key alone, so
// nothing in the codebase ever deleted an act:op: or act:bk: key:
// WipeAllActivity did not wipe all activity, and on production act:op: alone
// reached ~0.783 GiB of a ~1.342 GiB activity keyspace — roughly 60% — largely
// index rows whose primary row had been pruned months earlier.
//
// UNDECODABLE ROWS: scanTierKVs drops a row whose stored JSON will not decode,
// so such a row never reaches here and its PRIMARY key is not deleted either.
// That is the deliberate choice, not an oversight: the primary/index pair stays
// intact together, no new orphan is manufactured from a row whose ids we cannot
// read, and the drop is counted and reported in aggregate by pactDecodeTally —
// it is never silent. The one contract that cannot live with that is
// WipeAllActivity, which therefore range-deletes the entire "act:" prefix after
// its row-by-row pass rather than relying on it; see its doc comment.
func pactDeleteEntry(batch *pebble.Batch, kv pactKV) error {
	if err := batch.Delete(kv.key, nil); err != nil {
		return err
	}
	idxKeys, ok := pactIndexKeysFor(kv.key, kv.entry)
	if !ok {
		// A primary key this malformed cannot have had index keys derived from
		// it by Record, so there is nothing to delete — but it is not silent.
		slog.Warn("[activity] malformed primary key: deleted the row, derived no index keys",
			"key", string(kv.key))
		return nil
	}
	for _, k := range idxKeys {
		if err := batch.Delete(k, nil); err != nil {
			return err
		}
	}
	return nil
}

// ── ActivityStorer implementation ─────────────────────────────────────────────

// pactPreparedEntry is one activity row normalized, marshalled and keyed, ready
// to be staged into a pebble.Batch.
//
// Preparing is split from staging because preparation is the ONLY step that can
// fail for a reason attributable to a single entry (a row whose Details map will
// not marshal to JSON). Once an entry is prepared, staging it is an in-memory
// append into the batch, and the failures left — a commit error — doom every row
// in the batch equally. RecordBatch's error semantics rest entirely on that
// split; see its doc comment.
type pactPreparedEntry struct {
	id      int64
	primary []byte
	value   []byte
	idxKeys [][]byte
	idxRef  []byte
}

// prepareEntry applies the field defaults and the synthetic ID that Record has
// always applied, marshals the row, and derives its primary and secondary index
// keys.
//
// This is the ONE place the activity write path builds keys or marshals a row:
// Record and RecordBatch both go through it, so a second writer cannot drift
// from the first. That matters more than ordinary de-duplication here, because
// Pebble's Delete of a key that does not exist succeeds SILENTLY — an index key
// written in even a slightly different format would be undeletable forever and
// nothing would report an error. That is the exact defect pactDeleteEntry was
// added to repair (~0.783 GiB of orphaned act:op: rows on production), so a new
// write path must not be able to recreate it.
//
// The index keys deliberately come from pactIndexKeysFor — the SAME derivation
// pactDeleteEntry uses to remove them — rather than from a private fmt.Sprintf.
// Writer and deleter now share one definition of the key format, so "what this
// wrote is what Prune deletes" is true by construction, not by inspection.
func (s *PebbleActivityStore) prepareEntry(e ActivityEntry) (pactPreparedEntry, error) {
	if e.Timestamp.IsZero() {
		e.Timestamp = time.Now().UTC()
	}
	if e.Level == "" {
		e.Level = "info"
	}
	if e.Tier == "" {
		e.Tier = "change"
	}

	id := s.counter.Add(1)
	e.ID = id

	entryID := ulid.Make().String()
	primaryKey := pactPrimaryKey(e.Tier, e.Timestamp, entryID)

	b, err := json.Marshal(e)
	if err != nil {
		return pactPreparedEntry{}, fmt.Errorf("pebble_activity_store: marshal: %w", err)
	}

	idxKeys, ok := pactIndexKeysFor(primaryKey, e)
	if !ok {
		// Unreachable for a key pactPrimaryKey just built, but a silent skip
		// here would write a primary row with no indexes, so it is an error.
		return pactPreparedEntry{}, fmt.Errorf("pebble_activity_store: derive index keys for %q", primaryKey)
	}

	return pactPreparedEntry{
		id:      id,
		primary: primaryKey,
		value:   b,
		idxKeys: idxKeys,
		idxRef:  pactIndexRef(e.Tier, e.Timestamp, entryID),
	}, nil
}

// pactStagePrepared stages a prepared row's primary key and every secondary
// index key into batch. Mirror image of pactDeleteEntry.
func pactStagePrepared(batch *pebble.Batch, p pactPreparedEntry) error {
	if err := batch.Set(p.primary, p.value, nil); err != nil {
		return fmt.Errorf("pebble_activity_store: set primary: %w", err)
	}
	for _, k := range p.idxKeys {
		if err := batch.Set(k, p.idxRef, nil); err != nil {
			return fmt.Errorf("pebble_activity_store: set index %q: %w", k, err)
		}
	}
	return nil
}

// Record inserts an ActivityEntry and returns a synthetic int64 ID.
//
// One entry, one fsync. Callers with many entries to write should use
// RecordBatch instead — see its doc comment for the measured cost of not doing
// so.
func (s *PebbleActivityStore) Record(e ActivityEntry) (int64, error) {
	p, err := s.prepareEntry(e)
	if err != nil {
		return 0, err
	}

	batch := s.db.NewBatch()
	defer batch.Close()

	if err := pactStagePrepared(batch, p); err != nil {
		return 0, err
	}
	if err := batch.Commit(pebble.Sync); err != nil {
		return 0, fmt.Errorf("pebble_activity_store: commit: %w", err)
	}

	return p.id, nil
}

// activityRecordBatchCap caps how many entries RecordBatch stages into ONE
// pebble.Batch before committing it and starting a fresh one.
//
// WHY a cap at all: a Pebble batch holds every staged key and value in memory
// until it commits, so one unbounded commit over an unbounded flush is a heap
// risk — and this service has already OOMed once on an activity path (a single
// unbounded activity Query held 8.86 GB, 71.9% of the process). A cap converts
// "as much memory as the caller happened to hand us" into a fixed ceiling.
//
// WHY 500: it matches ActivityBatcher's own early-flush threshold, so the
// normal flush is exactly one commit and the split path is reserved for the
// unusual oversized drain. At a generous 2 KB per serialized activity row that
// is ~1 MB of staged keys and values in flight — three orders of magnitude
// under Pebble's 64 MB memtable, and small enough that the amortization is
// already saturated: the per-fsync cost is paid once per 500 rows instead of
// once per row, and raising the cap further buys nothing measurable.
//
// A var, not a const, following activityQueryScanBudget above, so tests can
// shrink it and exercise the split without seeding hundreds of entries. It is
// package-scoped state: a test that overrides it must restore it and must not
// run in parallel with another test in this package that records entries.
var activityRecordBatchCap = 500

// RecordBatch writes every entry in entries, committing at most
// activityRecordBatchCap of them per durable commit. It returns the number of
// entries actually committed.
//
// WHY: ActivityBatcher and Writer.drain both exist to amortize activity writes,
// and before this method neither did. Writer.writeBatch received a batch and
// then called Record once per entry, and Record commits with pebble.Sync — so a
// "batch" of 100 rows was 100 separate fsyncs. Measured on this repo at the same
// durability level, 5,000 rows an iteration over two runs (eight samples a
// side): 76-199 rows/sec per-row against 27,440-54,531 rows/sec batched
// (BenchmarkActivityRecordPerEntry and BenchmarkActivityRecordBatch). Ranges,
// not medians, because the per-row path climbs monotonically within every run
// as it warms; even the most favourable pairing leaves a 138x gap. The fsync,
// not the row, was the unit of cost, and the multiplier tracks how many entries
// share one commit.
//
// ERROR SEMANTICS — deliberate, and different from the per-entry loop it
// replaces:
//
//   - A PREPARE failure is attributable to one entry (its JSON will not
//     marshal). That entry is dropped, the rest of the batch still commits, and
//     the returned error names how many were dropped. This preserves the old
//     loop's isolation for the only failure the old loop could actually isolate.
//
//   - A COMMIT failure is NOT attributable to any entry — it is the engine or
//     the disk — and it loses every row staged in that commit. The batch fails.
//     It deliberately does NOT fall back to writing those rows one at a time:
//     the realistic causes (a full or failing disk, a closed DB) fail the
//     retries too, and retrying per-entry would issue up to 500 further fsyncs
//     against a disk that just failed one — turning a logged loss into an I/O
//     storm. Losing activity rows is acceptable; losing them silently is not.
//
// Both early returns abandon the rest of the call, not just the failed commit,
// so the loss they name is len(entries)-written — every entry in the failed
// chunk plus every chunk after it, none of which is attempted. That is also the
// figure a caller should report: the count returned is always the number of
// rows actually made durable, so len(entries)-written is authoritative without
// trusting the error text.
func (s *PebbleActivityStore) RecordBatch(entries []ActivityEntry) (int, error) {
	if len(entries) == 0 {
		return 0, nil
	}

	// A cap of zero would never advance start, and the staged == 0 continue
	// below would spin the loop forever rather than failing. It is a test-only
	// var, but a suite that hangs is worse than one that fails, so clamp it.
	chunk := max(activityRecordBatchCap, 1)

	written := 0
	var prepareErrs []error

	for start := 0; start < len(entries); start += chunk {
		end := min(start+chunk, len(entries))

		batch := s.db.NewBatch()
		staged := 0
		for _, e := range entries[start:end] {
			p, err := s.prepareEntry(e)
			if err != nil {
				prepareErrs = append(prepareErrs, err)
				continue
			}
			if err := pactStagePrepared(batch, p); err != nil {
				batch.Close()
				// Join whatever was already dropped by prepare: abandoning the
				// batch must not also abandon the report of the earlier losses.
				return written, errors.Join(
					fmt.Errorf("pebble_activity_store: record batch staging lost %d of %d entries: %w",
						len(entries)-written, len(entries), err),
					errors.Join(prepareErrs...))
			}
			staged++
		}

		if staged == 0 {
			batch.Close()
			continue
		}
		if err := batch.Commit(pebble.Sync); err != nil {
			batch.Close()
			return written, errors.Join(
				fmt.Errorf("pebble_activity_store: record batch commit lost %d of %d entries: %w",
					len(entries)-written, len(entries), err),
				errors.Join(prepareErrs...))
		}
		batch.Close()
		s.recordBatchCommits.Add(1)
		written += staged
	}

	if len(prepareErrs) > 0 {
		return written, fmt.Errorf("pebble_activity_store: record batch dropped %d of %d entries: %w",
			len(prepareErrs), len(entries), errors.Join(prepareErrs...))
	}
	return written, nil
}

// Query returns entries matching f, newest-first, plus the total matching
// count, and aborts as soon as ctx is cancelled.
//
// There is deliberately no context-free variant: an abandoned request that
// could not be cancelled is what held 30.8 GB across 30 goroutines with no
// connected clients on production. Callers on a request path must pass the
// request's context, never context.Background().
//
// total is EXACT when the walk exhausts the matching rows. Otherwise it is a
// LOWER BOUND: either offset+limit+1 (the probe row, which tells the caller
// another page exists) or however many matches were found before the scan
// budget was reached. An exact count would require decoding every stored
// entry, which is the behaviour this method exists to remove.
func (s *PebbleActivityStore) Query(ctx context.Context, f ActivityFilter) ([]ActivityEntry, int, error) {
	if f.Limit == 0 {
		f.Limit = 50
	}

	// Fast path: op_id or book_id filter → use secondary index.
	if f.OperationID != "" {
		return s.queryByIndexPrefix(ctx, fmt.Sprintf("act:op:%s:", f.OperationID), f)
	}
	if f.BookID != "" {
		return s.queryByIndexPrefix(ctx, fmt.Sprintf("act:bk:%s:", f.BookID), f)
	}

	// General path: bounded newest-first merge over the tier key ranges.
	//
	// Activity keys embed the timestamp, so the store can be read in result
	// order and abandoned as soon as the requested page is filled. The previous
	// implementation instead decoded EVERY entry in EVERY tier into one slice,
	// filtered it, sorted it, and then sliced out the page — 8.86 GB of heap on
	// production for a limit=5 request, and the cause of the OOM kills.
	nonDigest, digestTiers := pactSelectTiers(f)

	// Collect one match past the requested page. That extra row is what tells
	// the caller another page exists without counting the rest of the log.
	probe := f.Offset + f.Limit + 1

	pageCap := f.Limit
	if pageCap < 0 {
		pageCap = 0
	}
	page := make([]ActivityEntry, 0, pageCap)
	matched := 0
	collect := func(e ActivityEntry) bool {
		matched++
		if matched > f.Offset && len(page) < f.Limit {
			page = append(page, e)
		}
		return matched < probe
	}

	examined := 0
	exhausted := true
	for _, phase := range [][]string{nonDigest, digestTiers} {
		if len(phase) == 0 {
			continue
		}
		remaining := activityQueryScanBudget - examined
		if matched >= probe || remaining <= 0 {
			exhausted = false
			break
		}
		ex, phaseExhausted, err := s.scanNewestFirst(ctx, phase, f, remaining, "query", collect)
		examined += ex
		if err != nil {
			// Explicitly nil: drop the partial page rather than returning it.
			return nil, 0, err
		}
		if !phaseExhausted {
			exhausted = false
			break
		}
	}

	// total is exact when the walk drained the input. Otherwise it is a lower
	// bound: either it equals probe (one row past this page, so pagination
	// still advances) or the scan budget cut the walk short.
	total := matched
	if !exhausted && matched < probe {
		slog.Warn("[activity] query hit the scan budget; total is a lower bound and older matches were not examined",
			"budget", activityQueryScanBudget,
			"examined", examined,
			"matched", matched,
			"limit", f.Limit,
			"offset", f.Offset,
			"tier", f.Tier,
			"type", f.Type,
			"level", f.Level,
			"source", f.Source,
			"search", f.Search)
	}
	return page, total, nil
}

// Summarize groups old entries by (day, operation_id, type), writes one
// summary row per day, and deletes the originals. Returns count of deleted
// rows.
//
// The day is part of the group key — not just an artifact of scan order —
// because entries sharing an operation_id and type (most commonly both
// empty, e.g. routine "scan_progress" noise) can otherwise span the entire
// olderThan window. Without the day boundary, a single summary row silently
// swallowed weeks or months of entries into one "N entries (day1 to dayN)"
// row, the opposite of the per-day boundary CompactByDay enforces.
func (s *PebbleActivityStore) Summarize(ctx context.Context, olderThan time.Time, tier string) (int, error) {
	kvs, err := s.scanTierKVs(ctx, tier, nil, &olderThan)
	if err != nil {
		return 0, err
	}

	type groupKey struct{ day, opID, typ string }
	type group struct{ kvs []pactKV }
	groups := make(map[groupKey]*group)

	for _, kv := range kvs {
		if kv.entry.PrunedAt != nil {
			continue
		}
		k := groupKey{
			day:  kv.entry.Timestamp.UTC().Format("2006-01-02"),
			opID: kv.entry.OperationID,
			typ:  kv.entry.Type,
		}
		if groups[k] == nil {
			groups[k] = &group{}
		}
		groups[k].kvs = append(groups[k].kvs, kv)
	}
	if len(groups) == 0 {
		return 0, nil
	}

	totalDeleted := 0
	now := time.Now().UTC()

	for gk, g := range groups {
		select {
		case <-ctx.Done():
			return totalDeleted, ctx.Err()
		default:
		}

		entries := make([]ActivityEntry, len(g.kvs))
		for i, kv := range g.kvs {
			entries[i] = kv.entry
		}

		first := entries[0].Timestamp
		last := entries[len(entries)-1].Timestamp
		summaryText := fmt.Sprintf("Summary: %d %s entries (%s to %s)",
			len(entries), gk.typ,
			first.Format(time.RFC3339), last.Format(time.RFC3339),
		)

		prunedAt := now
		summary := ActivityEntry{
			ID:          s.counter.Add(1),
			Timestamp:   now,
			Tier:        tier,
			Type:        gk.typ,
			Level:       "info",
			Source:      "summarize",
			OperationID: gk.opID,
			Summary:     summaryText,
			PrunedAt:    &prunedAt,
		}

		summaryID := ulid.Make().String()
		summaryKey := pactPrimaryKey(tier, now, summaryID)
		summaryBytes, err := json.Marshal(summary)
		if err != nil {
			return totalDeleted, err
		}

		batch := s.db.NewBatch()
		if setErr := batch.Set(summaryKey, summaryBytes, nil); setErr != nil {
			batch.Close()
			return totalDeleted, setErr
		}
		for _, kv := range g.kvs {
			if delErr := pactDeleteEntry(batch, kv); delErr != nil {
				batch.Close()
				return totalDeleted, delErr
			}
		}
		if commitErr := batch.Commit(pebble.Sync); commitErr != nil {
			batch.Close()
			return totalDeleted, fmt.Errorf("pebble_activity_store: summarize commit: %w", commitErr)
		}
		batch.Close()
		totalDeleted += len(g.kvs)
	}
	return totalDeleted, nil
}

// Prune hard-deletes all entries of the given tier older than olderThan.
func (s *PebbleActivityStore) Prune(olderThan time.Time, tier string) (int, error) {
	// Prune has no caller context; it is a scheduled maintenance op.
	kvs, err := s.scanTierKVs(context.Background(), tier, nil, &olderThan)
	if err != nil {
		return 0, err
	}
	if len(kvs) == 0 {
		return 0, nil
	}

	deleted := 0
	// Delete in batches of 500 to keep batch size reasonable.
	for i := 0; i < len(kvs); i += 500 {
		end := i + 500
		if end > len(kvs) {
			end = len(kvs)
		}
		batch := s.db.NewBatch()
		for _, kv := range kvs[i:end] {
			if err := pactDeleteEntry(batch, kv); err != nil {
				batch.Close()
				return deleted, fmt.Errorf("pebble_activity_store: prune batch delete: %w", err)
			}
		}
		if err := batch.Commit(pebble.Sync); err != nil {
			batch.Close()
			return deleted, fmt.Errorf("pebble_activity_store: prune batch commit: %w", err)
		}
		batch.Close()
		deleted += end - i
	}
	return deleted, nil
}

// pactSourcesCacheKey derives a cache key covering every filter field that can
// change the result, so one filter's counts are never served for another.
func pactSourcesCacheKey(f ActivityFilter) string {
	since, until := "", ""
	if f.Since != nil {
		since = fmt.Sprintf("%d", f.Since.UnixNano())
	}
	if f.Until != nil {
		until = fmt.Sprintf("%d", f.Until.UnixNano())
	}
	return strings.Join([]string{
		f.Tier, f.Type, f.Level, f.Source, f.OperationID, f.BookID, f.Search,
		since, until,
		strings.Join(f.Tags, ","),
		strings.Join(f.ExcludeSources, ","),
		strings.Join(f.ExcludeTiers, ","),
		strings.Join(f.ExcludeTags, ","),
	}, "\x00")
}

// GetDistinctSources returns unique sources with entry counts, ordered by count desc.
//
// Bounded + memoized. This used to full-scan and JSON-decode every entry in
// every tier on EVERY page load, concurrently with Query — 3.21 GB of the
// production heap profile. It now scans at most activityQueryScanBudget of the
// newest entries and reuses the result for activitySourcesCacheTTL.
//
// Consequence, deliberate: when the log is larger than the budget, a source
// that only ever appears in older entries will be missing, and counts are of
// the newest window rather than of all time. This feeds a filter picker, where
// a bounded, recent view is the useful one; hitting the bound is logged.
//
// Cancellable, with no context-free variant: this ran concurrently with Query
// on every page load and contributed 3.21 GB to the OOM heap profile, so an
// abandoned request must be able to stop it. On a cache hit it returns without
// touching the store at all.
func (s *PebbleActivityStore) GetDistinctSources(ctx context.Context, f ActivityFilter) ([]SourceCount, error) {
	key := pactSourcesCacheKey(f)
	if cached, ok := s.lookupSourcesCache(key); ok {
		return cached, nil
	}

	nonDigest, digestTiers := pactSelectTiers(f)
	tiers := append(append([]string{}, nonDigest...), digestTiers...)

	counts := make(map[string]int)
	examined, exhausted, err := s.scanNewestFirst(ctx, tiers, f, activityQueryScanBudget, "sources",
		func(e ActivityEntry) bool {
			counts[e.Source]++
			return true
		})
	if err != nil {
		// Explicitly nil, and nothing is cached: a cancelled scan's partial
		// counts must not become the memoized answer for the next 45s.
		return nil, err
	}
	if !exhausted {
		slog.Warn("[activity] distinct-sources scan hit the budget; counts cover only the newest entries",
			"budget", activityQueryScanBudget,
			"examined", examined,
			"sources", len(counts),
			"tier", f.Tier,
			"level", f.Level)
	}

	out := make([]SourceCount, 0, len(counts))
	for src, cnt := range counts {
		out = append(out, SourceCount{Source: src, Count: cnt})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Count > out[j].Count })

	s.storeSourcesCache(key, out)
	return out, nil
}

// lookupSourcesCache returns a copy of a fresh cached result, if one exists.
// A copy is returned so a caller mutating the slice cannot corrupt the cache.
func (s *PebbleActivityStore) lookupSourcesCache(key string) ([]SourceCount, bool) {
	s.sourcesMu.Lock()
	defer s.sourcesMu.Unlock()
	entry, ok := s.sourcesCache[key]
	if !ok || time.Since(entry.at) > activitySourcesCacheTTL {
		return nil, false
	}
	out := make([]SourceCount, len(entry.out))
	copy(out, entry.out)
	return out, true
}

// storeSourcesCache memoizes a result and drops expired keys. Expiry is by TTL
// only — writes do not invalidate, so counts trail new activity by up to
// activitySourcesCacheTTL.
func (s *PebbleActivityStore) storeSourcesCache(key string, out []SourceCount) {
	stored := make([]SourceCount, len(out))
	copy(stored, out)

	s.sourcesMu.Lock()
	defer s.sourcesMu.Unlock()
	if s.sourcesCache == nil {
		s.sourcesCache = make(map[string]pactSourcesCacheEntry)
	}
	now := time.Now()
	for k, v := range s.sourcesCache {
		if now.Sub(v.at) > activitySourcesCacheTTL {
			delete(s.sourcesCache, k)
		}
	}
	s.sourcesCache[key] = pactSourcesCacheEntry{at: now, out: stored}
}

// WipeAllActivity deletes every entry from all tier buckets. Returns the
// count of rows ACTUALLY deleted, whether it finished or was cancelled.
//
// Cancellation is checked at two points per tier: once before the tier's scan
// starts (so an already-cancelled ctx stops before the next tier is even
// read), and once before each 500-row delete batch (so a large tier's delete
// loop itself stops promptly rather than draining a fully-scanned kvs slice).
// scanTierKVs also carries ctx down into its own per-row check, so a
// cancellation mid-scan of one tier aborts that tier's scan too — see its
// doc comment. On cancellation, rows already committed in prior batches and
// prior tiers stay deleted; rows not yet reached are left untouched. There is
// no partial-tier bookkeeping to resume: a retry just calls WipeAllActivity
// again, which rescans every tier and deletes whatever remains.
//
// After the tier scan, the WHOLE "act:" prefix is removed with one DeleteRange.
// This method's contract is its name, and the row-by-row path cannot honour it
// on its own — in two directions, both of which were verified by test rather
// than assumed:
//
//   - A row whose stored JSON will not decode is dropped by scanTierKVs, so it
//     is never staged for deletion at all. Sweeping only the two index families
//     would leave that PRIMARY row sitting in the log after a "wipe all"; the
//     test seeds exactly such a row and asserts nothing under "act:" survives.
//   - An index row orphaned before this leak was closed has no primary left to
//     drive its deletion, and its ids are only recoverable from the key itself.
//
// A range delete needs neither a decode nor the ids, and one range tombstone is
// cheaper than millions of point tombstones. It is safe because "act:" is the
// activity subsystem's entire keyspace: the only other key it owns is the
// backfill sentinel "system:backfill:activity_pebble_v1_done", which is outside
// "act:" — and ';' is one above ':' in ASCII, so ["act:", "act;") is exactly
// that prefix and nothing beyond it.
//
// The blast radius was ENUMERATED, not reasoned about: every "act"-prefixed key
// literal in internal/ is either act:<tier>: for a member of actTiers (all seven,
// "digest" included — so the digest rollup is inside the sweep, and the test
// seeds a digest row so the assertion is not vacuously true of one tier) or one
// of the two index families. The unrelated "action:..." literals are activity
// field values rather than keys, and sort after "act;" regardless.
//
// The returned count stays PRIMARY ROWS ONLY and is therefore a LOWER BOUND: it
// counts the rows deleted individually above, so a row the scan could not decode
// is swept but not counted (pactDecodeTally counts and logs those separately),
// and index keys are never counted at all. That is deliberate — the
// ActivityRetention doc promises "rows actually deleted", and counting index
// keys would silently inflate the number the wipe endpoint shows a user.
func (s *PebbleActivityStore) WipeAllActivity(ctx context.Context) (int64, error) {
	var total int64
	for _, tier := range actTiers {
		if err := ctx.Err(); err != nil {
			return total, err
		}
		kvs, err := s.scanTierKVs(ctx, tier, nil, nil)
		if err != nil {
			return total, err
		}
		for i := 0; i < len(kvs); i += 500 {
			if err := ctx.Err(); err != nil {
				return total, err
			}
			end := i + 500
			if end > len(kvs) {
				end = len(kvs)
			}
			batch := s.db.NewBatch()
			for _, kv := range kvs[i:end] {
				if err := pactDeleteEntry(batch, kv); err != nil {
					batch.Close()
					return total, fmt.Errorf("pebble_activity_store: wipe batch delete: %w", err)
				}
			}
			if err := batch.Commit(pebble.Sync); err != nil {
				batch.Close()
				return total, fmt.Errorf("pebble_activity_store: wipe batch commit: %w", err)
			}
			batch.Close()
			total += int64(end - i)
		}
	}

	// Sweep the whole act: keyspace. See the doc comment: this is what makes
	// "wipe all" true for the rows the per-row path above cannot reach — a
	// primary row whose JSON will not decode, and an index row whose primary
	// was already gone. The ctx check keeps the cancellation contract: a
	// cancelled wipe leaves the rows it has not reached alone rather than
	// destroying the whole log on its way out.
	if err := ctx.Err(); err != nil {
		return total, err
	}
	sweep := s.db.NewBatch()
	defer sweep.Close()
	// ';' is one above ':' in ASCII, so ["act:", "act;") is every activity key:
	// all tier rows and both index families, and nothing else.
	if err := sweep.DeleteRange([]byte("act:"), []byte("act;"), nil); err != nil {
		return total, fmt.Errorf("pebble_activity_store: wipe activity range: %w", err)
	}
	if err := sweep.Commit(pebble.Sync); err != nil {
		return total, fmt.Errorf("pebble_activity_store: wipe activity range commit: %w", err)
	}
	slog.Info("[activity] wipe swept the entire act: keyspace",
		"range", "act: .. act;", "primary_rows_counted", total)

	return total, nil
}

// activityRepairChunkSize is how many index rows one repair worker checks per
// task. Large enough that the errgroup dispatch is noise next to the work,
// small enough that at most NumCPU*chunk index keys are resident at once.
var activityRepairChunkSize = 1000

// pactIndexRow is one secondary-index key/value pair read by the repair scan.
type pactIndexRow struct {
	key []byte // act:op:<id>:<nanos>:<ulid> or act:bk:<id>:<nanos>:<ulid>
	ref []byte // "<tier>:<nanos>:<ulid>" — the primary key minus the "act:" prefix
}

// pactRepairCounters accumulates a RepairActivityIndexes run across workers.
type pactRepairCounters struct {
	scanned   atomic.Int64
	orphaned  atomic.Int64
	malformed atomic.Int64
	deleted   atomic.Int64

	mu           sync.Mutex
	firstBadKey  string
	firstBadSeen bool
}

func (c *pactRepairCounters) noteMalformed(key []byte) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.firstBadSeen {
		c.firstBadSeen = true
		c.firstBadKey = string(key)
	}
}

func (c *pactRepairCounters) result() ActivityIndexRepairResult {
	return ActivityIndexRepairResult{
		Scanned:   c.scanned.Load(),
		Orphaned:  c.orphaned.Load(),
		Malformed: c.malformed.Load(),
		Deleted:   c.deleted.Load(),
	}
}

// RepairActivityIndexes deletes act:op: / act:bk: index entries whose primary
// row no longer exists.
//
// WHY THIS EXISTS: closing the leak (see pactDeleteEntry) stops NEW orphans; it
// gives the ones already stored no route to deletion. Preventing corruption is
// not repairing it. On production the activity keyspace measured ~1.342 GiB
// with act:op: alone at ~0.783 GiB, the bulk of it index rows pointing at
// primaries pruned long ago; nothing in the codebase could remove them.
//
// CONCURRENCY (per the repo's whole-collection rule): the meaningful per-item
// work is the existence check — one point lookup per index row — not the
// iteration, so the iteration stays a single sequential scan per family and the
// LOOKUPS fan out. Rows are cut into fixed chunks and handed to an errgroup
// bounded by runtime.NumCPU(). Every index key is produced exactly once by one
// sequential iterator and lands in exactly one chunk, so the chunks are DISJOINT
// BY CONSTRUCTION and two workers can never delete the same key; each worker
// commits its own batch and no shared mutable state is touched except atomic
// counters. The refs are deliberately NOT all materialized for a merge-join
// against the primary keyspace: holding a 0.78 GiB key set in memory is the
// exact shape of the OOM this file's history is about.
//
// A malformed ref (one pactPrimaryKeyFromRef rejects) is DELETED and counted
// separately, not skipped: queryByIndexPrefix cannot follow it either, so it is
// dead weight by definition. The first such key is logged with the total.
func (s *PebbleActivityStore) RepairActivityIndexes(ctx context.Context) (ActivityIndexRepairResult, error) {
	c := &pactRepairCounters{}
	for _, prefix := range pactIndexFamilyPrefixes {
		if err := ctx.Err(); err != nil {
			return c.result(), err
		}
		if err := s.repairIndexFamily(ctx, prefix, c); err != nil {
			return c.result(), err
		}
	}

	res := c.result()
	if res.Malformed > 0 {
		slog.Warn("[activity] repair deleted index entries with an unusable reference",
			"malformed", res.Malformed, "first_key", c.firstBadKey)
	}
	slog.Info("[activity] secondary index repair complete",
		"scanned", res.Scanned, "orphaned", res.Orphaned,
		"malformed", res.Malformed, "deleted", res.Deleted)
	return res, nil
}

// repairIndexFamily scans one index prefix sequentially and fans the existence
// checks out over a bounded worker pool.
func (s *PebbleActivityStore) repairIndexFamily(ctx context.Context, prefix string, c *pactRepairCounters) error {
	iter, err := s.db.NewIter(&pebble.IterOptions{
		LowerBound: []byte(prefix),
		UpperBound: []byte(prefix[:len(prefix)-1] + ";"), // ';' is one above ':'
	})
	if err != nil {
		return fmt.Errorf("pebble_activity_store: repair new iter (%s): %w", prefix, err)
	}

	g, gctx := errgroup.WithContext(ctx)
	g.SetLimit(runtime.NumCPU())

	chunk := make([]pactIndexRow, 0, activityRepairChunkSize)
	dispatch := func() {
		if len(chunk) == 0 {
			return
		}
		batchRows := chunk
		chunk = make([]pactIndexRow, 0, activityRepairChunkSize)
		// SetLimit makes this block once NumCPU chunks are in flight, which is
		// what bounds resident memory to NumCPU*activityRepairChunkSize rows.
		g.Go(func() error { return s.repairIndexChunk(gctx, batchRows, c) })
	}

	seen := 0
	var scanErr error
	for iter.First(); iter.Valid(); iter.Next() {
		if seen%activityCtxCheckInterval == 0 {
			if ctxErr := gctx.Err(); ctxErr != nil {
				scanErr = ctxErr
				break
			}
		}
		seen++
		row := pactIndexRow{
			key: append([]byte(nil), iter.Key()...),
			ref: append([]byte(nil), iter.Value()...),
		}
		chunk = append(chunk, row)
		if len(chunk) >= activityRepairChunkSize {
			dispatch()
		}
	}
	dispatch()

	if closeErr := iter.Close(); closeErr != nil && scanErr == nil {
		scanErr = closeErr
	}
	if waitErr := g.Wait(); waitErr != nil {
		return waitErr
	}
	if scanErr != nil {
		return fmt.Errorf("pebble_activity_store: repair scan (%s): %w", prefix, scanErr)
	}
	return nil
}

// repairIndexChunk checks one disjoint chunk of index rows and deletes the ones
// whose primary row is gone or whose reference is unusable.
func (s *PebbleActivityStore) repairIndexChunk(ctx context.Context, rows []pactIndexRow, c *pactRepairCounters) error {
	if err := ctx.Err(); err != nil {
		return err
	}

	var dead [][]byte
	for _, r := range rows {
		c.scanned.Add(1)
		primary, ok := pactPrimaryKeyFromRef(r.ref)
		if !ok {
			c.malformed.Add(1)
			c.noteMalformed(r.key)
			dead = append(dead, r.key)
			continue
		}
		_, closer, getErr := s.db.Get(primary)
		if getErr != nil {
			if errors.Is(getErr, pebble.ErrNotFound) {
				c.orphaned.Add(1)
				dead = append(dead, r.key)
				continue
			}
			return fmt.Errorf("pebble_activity_store: repair get primary %q: %w", string(primary), getErr)
		}
		// Pebble hands back a closer on every HIT; not closing it leaks once
		// per live index row, which on production is millions of rows.
		if closeErr := closer.Close(); closeErr != nil {
			return fmt.Errorf("pebble_activity_store: repair close primary: %w", closeErr)
		}
	}
	if len(dead) == 0 {
		return nil
	}

	batch := s.db.NewBatch()
	defer batch.Close()
	for _, k := range dead {
		if err := batch.Delete(k, nil); err != nil {
			return fmt.Errorf("pebble_activity_store: repair delete index: %w", err)
		}
	}
	if err := batch.Commit(pebble.Sync); err != nil {
		return fmt.Errorf("pebble_activity_store: repair commit: %w", err)
	}
	c.deleted.Add(int64(len(dead)))
	return nil
}

// CompactByDay collapses all compactable tier entries into daily digest rows.
// Every tier except "digest" is eligible — newly-introduced tiers are automatically
// compacted without an allowlist update (denylist approach).
// Each day is processed atomically.
func (s *PebbleActivityStore) CompactByDay(ctx context.Context, olderThan time.Time) (CompactResult, error) {
	var result CompactResult

	// Load all compactable entries (all tiers except "digest").
	var all []pactKV
	for _, tier := range actCompactableTiers() {
		kvs, err := s.scanTierKVs(ctx, tier, nil, &olderThan)
		if err != nil {
			return result, err
		}
		all = append(all, kvs...)
	}
	if len(all) == 0 {
		return result, nil
	}

	// Group by date.
	type dayGroup struct{ kvs []pactKV }
	days := make(map[string]*dayGroup)
	var dayOrder []string
	for _, kv := range all {
		dk := kv.entry.Timestamp.UTC().Format("2006-01-02")
		if _, ok := days[dk]; !ok {
			days[dk] = &dayGroup{}
			dayOrder = append(dayOrder, dk)
		}
		days[dk].kvs = append(days[dk].kvs, kv)
	}
	sort.Strings(dayOrder)

	for _, dateKey := range dayOrder {
		select {
		case <-ctx.Done():
			return result, ctx.Err()
		default:
		}

		dg := days[dateKey]
		entries := make([]ActivityEntry, len(dg.kvs))
		for i, kv := range dg.kvs {
			entries[i] = kv.entry
		}

		counts := make(map[string]int)
		for _, e := range entries {
			counts[e.Type]++
		}

		var auditItems, errItems, normalItems []DigestItem
		for _, e := range entries {
			item := DigestItem{
				Type:        e.Type,
				Tier:        e.Tier,
				Book:        extractBookName(e),
				BookID:      e.BookID,
				OperationID: e.OperationID,
				Summary:     extractItemSummary(e),
				Timestamp:   e.Timestamp,
				Tags:        e.Tags,
			}
			switch {
			case e.Tier == "audit":
				auditItems = append(auditItems, item)
			case e.Level == "error" || e.Level == "warn":
				item.Details = extractErrorDetails(e)
				errItems = append(errItems, item)
			default:
				normalItems = append(normalItems, item)
			}
		}
		items := append(auditItems, errItems...)
		items = append(items, normalItems...)

		truncated := false
		truncatedCount := 0
		if len(items) > maxDigestItems {
			truncatedCount = len(items) - maxDigestItems
			items = items[:maxDigestItems]
			truncated = true
		}

		dd := DigestDetails{
			Date:           dateKey,
			OriginalCount:  len(entries),
			Counts:         counts,
			Items:          items,
			Truncated:      truncated,
			TruncatedCount: truncatedCount,
		}

		// Check for existing digest for this date to merge into.
		existing, existingKey, err := s.findExistingDigest(dateKey)
		if err != nil {
			return result, err
		}
		if existingKey != nil {
			// Merge existing digest into dd.
			for k, v := range existing.Counts {
				dd.Counts[k] += v
			}
			dd.OriginalCount += existing.OriginalCount
			combined := append(existing.Items, dd.Items...)
			if existing.Truncated {
				dd.Truncated = true
				dd.TruncatedCount += existing.TruncatedCount
			}
			if len(combined) > maxDigestItems {
				dd.TruncatedCount += len(combined) - maxDigestItems
				combined = combined[:maxDigestItems]
				dd.Truncated = true
			}
			dd.Items = combined
		}

		detailsBytes, err := json.Marshal(dd)
		if err != nil {
			return result, fmt.Errorf("pebble_activity_store: compact marshal: %w", err)
		}

		startOfDay, err := time.Parse("2006-01-02", dateKey)
		if err != nil {
			return result, fmt.Errorf("pebble_activity_store: compact parse date: %w", err)
		}

		// Populate Details from the DigestDetails map so it survives the ActivityEntry round-trip.
		var ddMap map[string]any
		if mapErr := json.Unmarshal(detailsBytes, &ddMap); mapErr != nil {
			return result, fmt.Errorf("pebble_activity_store: compact unmarshal dd map: %w", mapErr)
		}
		digestID := ulid.Make().String()
		digest := ActivityEntry{
			ID:        s.counter.Add(1),
			Timestamp: startOfDay,
			Tier:      "digest",
			Type:      "daily_digest",
			Level:     "info",
			Source:    "compaction",
			Summary:   fmt.Sprintf("Daily digest for %s (%d entries)", dateKey, dd.OriginalCount),
			Details:   ddMap,
		}
		digestKey := pactPrimaryKey("digest", startOfDay, digestID)
		digestBytes, err := json.Marshal(digest)
		if err != nil {
			return result, fmt.Errorf("pebble_activity_store: compact marshal digest: %w", err)
		}

		batch := s.db.NewBatch()

		// Delete old digest if present. This is a plain Delete, not
		// pactDeleteEntry: digest rows are written just below with neither an
		// OperationID nor a BookID, so Record never indexed them and there is
		// nothing for the helper to find. findExistingDigest also returns only
		// the key, not a decoded entry, so routing it through the helper would
		// mean re-reading a row to derive index keys that cannot exist.
		if existingKey != nil {
			if err := batch.Delete(existingKey, nil); err != nil {
				batch.Close()
				return result, fmt.Errorf("pebble_activity_store: compact delete old digest: %w", err)
			}
		}

		// Write new digest.
		if err := batch.Set(digestKey, digestBytes, nil); err != nil {
			batch.Close()
			return result, fmt.Errorf("pebble_activity_store: compact set digest: %w", err)
		}

		// Delete originals, plus each one's act:op:/act:bk: index entries.
		for _, kv := range dg.kvs {
			if err := pactDeleteEntry(batch, kv); err != nil {
				batch.Close()
				return result, fmt.Errorf("pebble_activity_store: compact delete original: %w", err)
			}
		}

		if err := batch.Commit(pebble.Sync); err != nil {
			batch.Close()
			return result, fmt.Errorf("pebble_activity_store: compact commit day %s: %w", dateKey, err)
		}
		batch.Close()

		result.DaysCompacted++
		result.EntriesDeleted += len(dg.kvs)
	}
	return result, nil
}

// MigrateSystemActivityLogs is a no-op for PebbleActivityStore since it's not backed by SQLite.
func (s *PebbleActivityStore) MigrateSystemActivityLogs() (int, error) {
	return 0, nil
}

// RecompactDigests re-derives type, tier, and tags on every stored daily-digest
// entry whose items were compacted before enrichment was added.
//
// Algorithm:
//  1. Range-scan the entire "act:digest:" prefix (all digest keys).
//  2. Decode each entry's Details map as DigestDetails.
//  3. Skip entries where no items are legacy (idempotent guard).
//  4. For each legacy item: call deriveTypeFromMessage + enrichLegacyLogTags.
//  5. Rebuild Counts and TagCounts, marshal back, and overwrite with the same key.
func (s *PebbleActivityStore) RecompactDigests(ctx context.Context) (RecompactResult, error) {
	var result RecompactResult

	type digestKV struct {
		key   []byte
		entry ActivityEntry
		dd    DigestDetails
	}

	var candidates []digestKV

	// Scan all digest entries.
	iter, err := s.db.NewIter(&pebble.IterOptions{
		LowerBound: pactPrimaryPrefix("digest"),
		UpperBound: pactPrimaryUpperBound("digest"),
	})
	if err != nil {
		return result, fmt.Errorf("pebble_activity_store: recompact new iter: %w", err)
	}
	for iter.First(); iter.Valid(); iter.Next() {
		var e ActivityEntry
		if jsonErr := json.Unmarshal(iter.Value(), &e); jsonErr != nil {
			continue
		}
		if e.Type != "daily_digest" {
			continue
		}
		var dd DigestDetails
		if e.Details != nil {
			if b, merr := json.Marshal(e.Details); merr == nil {
				_ = json.Unmarshal(b, &dd)
			}
		}
		keyCopy := make([]byte, len(iter.Key()))
		copy(keyCopy, iter.Key())
		candidates = append(candidates, digestKV{key: keyCopy, entry: e, dd: dd})
	}
	if err := iter.Close(); err != nil {
		return result, fmt.Errorf("pebble_activity_store: recompact iter close: %w", err)
	}

	slog.Info("[activity] recompact: starting digest re-derivation (pebble)",
		"digest_count", len(candidates))

	for _, c := range candidates {
		select {
		case <-ctx.Done():
			return result, ctx.Err()
		default:
		}

		// Check if any items need updating.
		needsUpdate := false
		for _, item := range c.dd.Items {
			if isLegacyItem(item) {
				needsUpdate = true
				break
			}
		}
		if !needsUpdate {
			result.Skipped++
			continue
		}

		// Re-derive type, tier, and tags on each legacy item.
		for i, item := range c.dd.Items {
			if !isLegacyItem(item) {
				continue
			}
			derivedType, derivedTier := deriveTypeFromMessage(item.Summary, "")
			derivedTags := enrichLegacyLogTags(item.Summary, "", "info")
			c.dd.Items[i].Type = derivedType
			c.dd.Items[i].Tier = derivedTier
			c.dd.Items[i].Tags = derivedTags
		}

		// Rebuild Counts from updated items.
		newCounts := make(map[string]int)
		for _, item := range c.dd.Items {
			newCounts[item.Type]++
		}
		c.dd.Counts = newCounts

		// Rebuild TagCounts from updated items (action: and source: namespaces only).
		newTagCounts := make(map[string]map[string]int)
		for _, item := range c.dd.Items {
			for _, tag := range item.Tags {
				colonIdx := strings.Index(tag, ":")
				if colonIdx < 1 {
					continue
				}
				ns := tag[:colonIdx]
				val := tag[colonIdx+1:]
				if ns != "action" && ns != "source" {
					continue
				}
				if newTagCounts[ns] == nil {
					newTagCounts[ns] = make(map[string]int)
				}
				newTagCounts[ns][val]++
			}
		}
		if len(newTagCounts) > 0 {
			c.dd.TagCounts = newTagCounts
		}

		// Merge updated DigestDetails back into the entry's Details map.
		ddBytes, merr := json.Marshal(c.dd)
		if merr != nil {
			return result, fmt.Errorf("pebble_activity_store: recompact marshal digest key=%s: %w", c.key, merr)
		}
		var detailsMap map[string]any
		if err := json.Unmarshal(ddBytes, &detailsMap); err != nil {
			return result, fmt.Errorf("pebble_activity_store: recompact unmarshal detailsMap key=%s: %w", c.key, err)
		}
		c.entry.Details = detailsMap
		c.entry.Summary = fmt.Sprintf("Daily digest for %s (%d entries)", c.dd.Date, c.dd.OriginalCount)

		entryBytes, merr := json.Marshal(c.entry)
		if merr != nil {
			return result, fmt.Errorf("pebble_activity_store: recompact marshal entry key=%s: %w", c.key, merr)
		}

		if err := s.db.Set(c.key, entryBytes, pebble.Sync); err != nil {
			return result, fmt.Errorf("pebble_activity_store: recompact write key=%s: %w", c.key, err)
		}

		slog.Info("[activity] recompact: updated digest (pebble)",
			"key", string(c.key), "date", c.dd.Date, "items", len(c.dd.Items))
		result.Touched++
	}

	slog.Info("[activity] recompact: complete (pebble)",
		"touched", result.Touched, "skipped", result.Skipped)
	return result, nil
}

// ── internal helpers ──────────────────────────────────────────────────────────

// pactKV holds a Pebble key and its decoded ActivityEntry.
type pactKV struct {
	key   []byte
	entry ActivityEntry
}

// pactDecodeTally accumulates JSON decode failures across a single scan so the
// scan can emit one aggregate warning instead of flooding the log when a whole
// key range is corrupt. Requirement: dropped rows are counted and reported,
// never silently skipped.
type pactDecodeTally struct {
	dropped  int
	firstErr error
	firstKey string
}

func (t *pactDecodeTally) record(key []byte, err error) {
	t.dropped++
	if t.firstErr == nil {
		t.firstErr = err
		t.firstKey = string(key)
	}
}

func (t *pactDecodeTally) log(op string) {
	if t.dropped == 0 {
		return
	}
	slog.Warn("[activity] pebble store dropped undecodable entries",
		"op", op,
		"dropped", t.dropped,
		"first_key", t.firstKey,
		"error", t.firstErr)
}

// pactCursor is one tier's reverse (newest-first) iterator plus the cached time
// field of the key it currently sits on.
type pactCursor struct {
	tier string
	iter *pebble.Iterator
	tf   string
}

// pactSelectTiers splits the tiers a filter is allowed to touch into the
// non-digest group and the digest group.
//
// The two groups exist because the historical result order is a TWO-GROUP
// order (matching the old SQL "ORDER BY compacted ASC, timestamp DESC"):
// every non-digest entry sorts before every digest entry, and within a group
// entries are newest-first. Walking non-digest tiers to exhaustion before
// touching digest reproduces that order without a global sort.
//
// f.Tier and f.ExcludeTiers are applied here rather than left to matchesFilter
// so excluded tiers never open an iterator or consume scan budget.
func pactSelectTiers(f ActivityFilter) (nonDigest, digest []string) {
	base := actTiers
	if f.Tier != "" {
		base = []string{f.Tier}
	}
	for _, tier := range base {
		excluded := false
		for _, ex := range f.ExcludeTiers {
			if tier == ex {
				excluded = true
				break
			}
		}
		if excluded {
			continue
		}
		if tier == "digest" {
			digest = append(digest, tier)
		} else {
			nonDigest = append(nonDigest, tier)
		}
	}
	return nonDigest, digest
}

// scanNewestFirst walks the given tiers newest-first as a k-way merge over
// per-tier reverse iterators, decoding at most `budget` entries, and invokes
// visit for every decoded entry that passes matchesFilter. visit returns false
// to stop the walk early.
//
// Returns the number of entries decoded and whether the input was exhausted.
// exhausted == false means the walk stopped early — either visit asked to stop
// or the budget ran out — so any count derived from it is a LOWER BOUND.
//
// Ordering note: the merge orders on the key's embedded timestamp, not on the
// decoded Timestamp field. Those are written together by Record, so they agree;
// a row whose stored Tier/Timestamp disagrees with its key (already-inconsistent
// data) now groups by its key.
//
// Deliberately single-goroutine: this is a stop-early walk, and fanning it out
// over workers would mean over-reading, which is the very cost being removed.
// Cancellation: ctx is checked every activityCtxCheckInterval rows. The budget
// bounds a request that RUNS TO COMPLETION; ctx is what bounds an ABANDONED
// one. They solve different halves and both are required.
func (s *PebbleActivityStore) scanNewestFirst(
	ctx context.Context,
	tiers []string,
	f ActivityFilter,
	budget int,
	op string,
	visit func(ActivityEntry) bool,
) (examined int, exhausted bool, err error) {
	if len(tiers) == 0 {
		return 0, true, nil
	}
	if budget <= 0 {
		return 0, false, nil
	}
	if ctxErr := ctx.Err(); ctxErr != nil {
		return 0, false, ctxErr
	}

	cursors := make([]*pactCursor, 0, len(tiers))
	defer func() {
		for _, c := range cursors {
			if cerr := c.iter.Close(); cerr != nil && err == nil {
				err = fmt.Errorf("pebble_activity_store: %s iter close (tier=%s): %w", op, c.tier, cerr)
			}
		}
	}()

	for _, tier := range tiers {
		lower, upper := pactTierBounds(tier, f.Since, f.Until)
		it, iterErr := s.db.NewIter(&pebble.IterOptions{LowerBound: lower, UpperBound: upper})
		if iterErr != nil {
			return examined, false, fmt.Errorf("pebble_activity_store: %s new iter (tier=%s): %w", op, tier, iterErr)
		}
		if !it.Last() {
			// Empty range for this tier — close now rather than carrying it.
			if cerr := it.Close(); cerr != nil {
				return examined, false, fmt.Errorf("pebble_activity_store: %s iter close (tier=%s): %w", op, tier, cerr)
			}
			continue
		}
		cursors = append(cursors, &pactCursor{tier: tier, iter: it, tf: pactKeyTimeField(it.Key())})
	}

	tally := &pactDecodeTally{}
	defer tally.log(op)

	for {
		// Bail out promptly when the caller has gone away. Checked before the
		// cursor work so an abandoned request stops allocating immediately.
		if examined%activityCtxCheckInterval == 0 {
			if ctxErr := ctx.Err(); ctxErr != nil {
				return examined, false, ctxErr
			}
		}

		// Pick the cursor sitting on the newest remaining key. Ties break on
		// the full key bytes so the order is deterministic.
		best := -1
		for i, c := range cursors {
			if !c.iter.Valid() {
				continue
			}
			if best < 0 {
				best = i
				continue
			}
			b := cursors[best]
			if c.tf > b.tf || (c.tf == b.tf && bytes.Compare(c.iter.Key(), b.iter.Key()) > 0) {
				best = i
			}
		}
		if best < 0 {
			return examined, true, nil // every cursor drained
		}
		if examined >= budget {
			return examined, false, nil // caller logs the truncation
		}

		c := cursors[best]
		examined++
		s.entriesDecoded.Add(1)
		var e ActivityEntry
		if jsonErr := json.Unmarshal(c.iter.Value(), &e); jsonErr != nil {
			s.decodeFailures.Add(1)
			tally.record(c.iter.Key(), jsonErr)
		} else if matchesFilter(e, f) {
			if !visit(e) {
				return examined, false, nil
			}
		}

		if c.iter.Prev() {
			c.tf = pactKeyTimeField(c.iter.Key())
		}
	}
}

// NOTE: scanTier (a materializing []ActivityEntry wrapper over scanTierKVs)
// was removed here. Its only callers were Query and GetDistinctSources, both of
// which now use the bounded scanNewestFirst walk. The maintenance ops that
// legitimately need every row call scanTierKVs directly.

// scanTierKVs returns key+entry pairs for a tier within the time range.
//
// This is a FULL materializing scan and is used only by the maintenance
// operations (Summarize, Prune, WipeAllActivity, CompactByDay), which
// legitimately need every row. Query and GetDistinctSources deliberately do NOT
// use it — see scanNewestFirst.
// This scan is unbounded by design, so ctx is its ONLY brake: it is checked
// every activityCtxCheckInterval rows, and on cancellation returns a nil slice
// with ctx.Err() so the entries accumulated so far become garbage immediately
// rather than being handed back to a caller that has gone away.
func (s *PebbleActivityStore) scanTierKVs(ctx context.Context, tier string, since, until *time.Time) ([]pactKV, error) {
	lower, upper := pactTierBounds(tier, since, until)

	iter, err := s.db.NewIter(&pebble.IterOptions{
		LowerBound: lower,
		UpperBound: upper,
	})
	if err != nil {
		return nil, fmt.Errorf("pebble_activity_store: scanTierKVs new iter (tier=%s): %w", tier, err)
	}
	defer iter.Close()

	tally := &pactDecodeTally{}
	defer tally.log("scanTierKVs tier=" + tier)

	var out []pactKV
	seen := 0
	for iter.First(); iter.Valid(); iter.Next() {
		if seen%activityCtxCheckInterval == 0 {
			if ctxErr := ctx.Err(); ctxErr != nil {
				slog.Warn("[activity] scanTierKVs aborted: caller went away",
					"tier", tier, "rows_scanned", seen, "error", ctxErr)
				// Explicitly nil: release the accumulated slice.
				return nil, ctxErr
			}
		}
		seen++

		var e ActivityEntry
		s.entriesDecoded.Add(1)
		if jsonErr := json.Unmarshal(iter.Value(), &e); jsonErr != nil {
			// Counted and reported in aggregate by the deferred tally.log —
			// previously a bare `continue` that dropped rows silently.
			s.decodeFailures.Add(1)
			tally.record(iter.Key(), jsonErr)
			continue
		}
		keyCopy := make([]byte, len(iter.Key()))
		copy(keyCopy, iter.Key())
		out = append(out, pactKV{key: keyCopy, entry: e})
	}
	return out, nil
}

// queryByIndexPrefix reads ref values from an index prefix, then fetches primary entries.
// Handles both op and book secondary indexes.
//
// This is the fast path Query takes whenever OperationID or BookID is set, so
// GET /api/v1/operations/:id/activity never reaches scanNewestFirst.
//
// It dispatches between two implementations that MUST return identical
// (entries, total) for every input they both accept:
//
//   - queryByIndexPrefixPaged — the limit pushdown. Reads the index newest-first
//     and decodes only the requested page. Taken when pactIndexPushdownEligible
//     says every predicate in f is either trivially true or decidable without
//     the stored entry.
//   - queryByIndexPrefixFull — the historical full materialization. Taken for
//     every other filter shape, and kept byte-for-byte as the reference
//     implementation the differential tests compare the pushdown against.
func (s *PebbleActivityStore) queryByIndexPrefix(ctx context.Context, prefix string, f ActivityFilter) ([]ActivityEntry, int, error) {
	if pactIndexPushdownEligible(prefix, f) {
		return s.queryByIndexPrefixPaged(ctx, prefix, f)
	}
	return s.queryByIndexPrefixFull(ctx, prefix, f)
}

// pactIndexPushdownEligible reports whether f can be served by the pushdown
// without changing EITHER the returned page or the returned total.
//
// The rule is one question asked of every field matchesFilter reads: can this
// predicate be decided without the stored ActivityEntry? Anything that needs a
// decoded field the secondary index does not carry forces the full path,
// because the pushdown decodes only the page and would otherwise count rows
// into `total` that matchesFilter would have rejected. `total` drives the UI's
// pager, and a wrong page count raises no error anywhere — it is exactly the
// kind of silent wrong answer that must not be traded for speed.
//
// Decidable, therefore allowed:
//
//   - The id the index family is keyed by. Every ref under "act:op:<X>:" was
//     written by prepareEntry/backfill from an entry whose OperationID was X, so
//     f.OperationID == X is true by construction on the op path (and the same
//     for BookID on the book path). It is re-asserted per page row anyway — see
//     queryByIndexPrefixPaged — so a corrupt index cannot leak a wrong row.
//   - f.Since / f.Until. matchesFilter does not read them and this path applies
//     no time bounds, so BOTH implementations ignore them identically. That is a
//     pre-existing defect of the index path (GET /api/v1/activity?operation_id=
//     X&since=... silently ignores `since`), deliberately NOT fixed here: fixing
//     it belongs in its own change with its own test, and "fixing" it inside a
//     performance PR would silently change results for that endpoint.
//
// NOT decidable, therefore refused:
//
//   - Type, Level, Source, Search, Tags, ExcludeSources, ExcludeTags — none of
//     these fields exist anywhere in the index key or its ref value.
//   - The OTHER id: f.BookID on the op path (and f.OperationID on the book
//     path). Query never routes there today, but queryByIndexPrefix is reachable
//     directly and a book filter cannot be decided from the op index.
//   - Tier and ExcludeTiers, even though the ref value's first component IS a
//     tier. That tier is the one baked into the PRIMARY KEY, while matchesFilter
//     compares f.Tier against the decoded e.Tier — and those two can disagree:
//     pebble_activity_backfill.go builds both keys from the NutsDB BUCKET's tier
//     while marshalling the entry body untouched, so a backfilled row whose body
//     Tier differs from its bucket would be filtered differently by the two
//     paths. Pushing tier down would be right for every row Record wrote and
//     wrong for that population, so it is refused outright.
//
// Negative Limit or Offset also refuse: queryByIndexPrefixFull ends in
// all[start:end], which PANICS when end < start, and preserving a panic exactly
// is safer than quietly inventing a clamp the callers have never seen. The HTTP
// layer cannot produce either (ParsePaginationParams forces limit into [1,1000]
// and offset >= 0), so this only guards direct in-process callers.
func pactIndexPushdownEligible(prefix string, f ActivityFilter) bool {
	if f.Limit < 0 || f.Offset < 0 {
		return false
	}
	if f.Type != "" || f.Level != "" || f.Source != "" || f.Search != "" {
		return false
	}
	if f.Tier != "" || len(f.ExcludeTiers) > 0 {
		return false
	}
	if len(f.Tags) > 0 || len(f.ExcludeSources) > 0 || len(f.ExcludeTags) > 0 {
		return false
	}
	switch {
	case strings.HasPrefix(prefix, "act:op:"):
		return f.BookID == ""
	case strings.HasPrefix(prefix, "act:bk:"):
		return f.OperationID == ""
	default:
		// An unknown index family: neither id predicate can be justified by
		// construction, so refuse rather than guess.
		return false
	}
}

// pactIndexScan is one newest-first pass over a secondary-index range, holding
// the primary keys the refs point at without having touched a single primary row.
//
// The primary keys live in ONE arena []byte with an end-offset per key rather
// than in a [][]byte: at 50k refs the per-ref slice header plus allocation cost
// more than the whole scan (measured: 52k allocations for the [][]byte shape
// against 614 for the arena, 16.3ms against 6.2ms).
//
// byTier groups positions by the ref's tier component so the existence pass can
// walk one primary iterator per tier IN KEY ORDER. That grouping is safe even
// though tier as a FILTER is not (see pactIndexPushdownEligible): the ref's tier
// is by definition the tier in the primary key it reconstructs, whatever the
// entry body says, so it always locates the right range.
type pactIndexScan struct {
	arena  []byte
	ends   []int32
	byTier []pactTierPositions
}

// pactTierPositions is one tier's positions within a pactIndexScan, in the
// newest-first order the scan produced them (so DESCENDING by primary key).
type pactTierPositions struct {
	tier string
	pos  []int32
}

// key returns the primary key at position i. It aliases the arena and must not
// be retained past the scan.
func (sc *pactIndexScan) key(i int32) []byte {
	start := int32(0)
	if i > 0 {
		start = sc.ends[i-1]
	}
	return sc.arena[start:sc.ends[i]]
}

func (sc *pactIndexScan) len() int { return len(sc.ends) }

// scanIndexRefs walks [prefix, prefix-with-';') in REVERSE and reconstructs the
// primary key of every ref, newest-first.
//
// Reverse is what makes the pushdown possible at all: the index key is
// act:op:<id>:<20d-unix-nano>:<ulid>, and because the nanos are zero-padded to a
// fixed 20 digits, lexicographic key order IS chronological order. Iterating
// backwards therefore yields exactly the newest-first order the full path
// produced with a sort.Slice over every decoded entry.
func (s *PebbleActivityStore) scanIndexRefs(ctx context.Context, prefix string) (*pactIndexScan, error) {
	// Upper bound: replace trailing ':' with ';' to cover the entire id sub-namespace.
	upperPrefix := prefix[:len(prefix)-1] + ";"

	iter, err := s.db.NewIter(&pebble.IterOptions{
		LowerBound: []byte(prefix),
		UpperBound: []byte(upperPrefix),
	})
	if err != nil {
		return nil, fmt.Errorf("pebble_activity_store: queryByIndex new iter: %w", err)
	}
	defer iter.Close()

	sc := &pactIndexScan{}
	seen := 0
	for iter.Last(); iter.Valid(); iter.Prev() {
		if seen%activityCtxCheckInterval == 0 {
			if ctxErr := ctx.Err(); ctxErr != nil {
				return nil, ctxErr
			}
		}
		seen++

		ref := iter.Value()
		// Same rejection as pactPrimaryKeyFromRef: a ref with no ':' cannot be
		// turned into a primary key, so the full path skips it and it is absent
		// from that path's total. Skipping it here keeps the two totals equal.
		ti := bytes.IndexByte(ref, ':')
		if ti < 0 {
			continue
		}

		pos := int32(len(sc.ends))
		sc.arena = append(sc.arena, "act:"...)
		sc.arena = append(sc.arena, ref...)
		sc.ends = append(sc.ends, int32(len(sc.arena)))

		tier := ref[:ti]
		bucket := -1
		for i := range sc.byTier {
			// string(tier) in a comparison does not allocate; assigning it does,
			// which is why the string is only built when a NEW tier appears.
			if sc.byTier[i].tier == string(tier) {
				bucket = i
				break
			}
		}
		if bucket < 0 {
			sc.byTier = append(sc.byTier, pactTierPositions{tier: string(tier)})
			bucket = len(sc.byTier) - 1
		}
		sc.byTier[bucket].pos = append(sc.byTier[bucket].pos, pos)
	}
	if err := iter.Error(); err != nil {
		// The full path returns whatever it had scanned so far, which turns a
		// read error into a silently short total. A truncated pager is a wrong
		// answer nothing surfaces, so the pushdown reports it instead.
		return nil, fmt.Errorf("pebble_activity_store: queryByIndex scan: %w", err)
	}
	return sc, nil
}

// markLiveRefs sets alive[i] for every scanned position whose PRIMARY ROW STILL
// EXISTS, and returns how many that is.
//
// WHY existence has to be checked for every ref, not just the page: an
// act:op:/act:bk: ref outlives its primary row. Deletion paths only started
// removing index keys recently, and on production act:op: had reached ~0.783 GiB
// of a ~1.342 GiB activity keyspace, largely refs whose row was pruned months
// earlier. The full path skips those refs, so they are absent from its total.
// A pushdown that counted index keys instead would report a total inflated by
// whatever fraction of the index is stale — on real data, a large one.
//
// WHY a merge, not a point Get per ref: db.Get costs ~7.9µs per ref here (50k
// refs = 393ms, barely faster than the 564ms full path — the Get, not the
// json.Unmarshal, is what the old implementation was actually paying for).
// Walking one KEY-ONLY iterator per tier in ascending key order instead costs
// ~330ns per ref, because an operation's rows are contiguous in time and the
// iterator advances rather than re-seeking from the root. The positions in each
// tier bucket are descending (the scan is newest-first), so they are walked in
// reverse to feed the iterator strictly ascending targets — which is what makes
// "the iterator is already past this key ⇒ this key does not exist" sound.
func (s *PebbleActivityStore) markLiveRefs(ctx context.Context, sc *pactIndexScan, alive []bool) (int, error) {
	live := 0
	for _, bucket := range sc.byTier {
		pit, err := s.db.NewIter(&pebble.IterOptions{
			LowerBound: []byte("act:" + bucket.tier + ":"),
			UpperBound: []byte("act:" + bucket.tier + ";"),
		})
		if err != nil {
			return 0, fmt.Errorf("pebble_activity_store: queryByIndex primary iter (tier=%s): %w", bucket.tier, err)
		}

		positioned := false
		checked := 0
		for i := len(bucket.pos) - 1; i >= 0; i-- {
			if checked%activityCtxCheckInterval == 0 {
				if ctxErr := ctx.Err(); ctxErr != nil {
					pit.Close()
					return 0, ctxErr
				}
			}
			checked++

			k := sc.key(bucket.pos[i])
			if !positioned || !pit.Valid() || bytes.Compare(pit.Key(), k) < 0 {
				pit.SeekGE(k)
				positioned = true
			}
			if pit.Valid() && bytes.Equal(pit.Key(), k) {
				alive[bucket.pos[i]] = true
				live++
				pit.Next()
			}
		}
		if err := pit.Error(); err != nil {
			pit.Close()
			return 0, fmt.Errorf("pebble_activity_store: queryByIndex primary scan (tier=%s): %w", bucket.tier, err)
		}
		pit.Close()
	}
	return live, nil
}

// queryByIndexPrefixPaged is the limit pushdown: it counts with key scans and
// decodes only the page.
//
// Cost, against the full path it replaces: the full path did one db.Get and one
// json.Unmarshal for EVERY entry of the operation before slicing out the page —
// 50,000 of each to return 1,000 rows. This does one index scan, one key-only
// existence merge, and exactly len(page) Gets and decodes.
//
// HOW total stays exactly what the full path returns: total is the number of
// refs whose primary row exists, which is the same set the full path accumulates
// into `all` — minus only rows whose stored JSON will not decode. Those are the
// one divergence and it is bounded to that case: a row that EXISTS but whose
// body cannot be unmarshalled into an ActivityEntry is counted here and was not
// counted by the full path. Pebble checksums its blocks, so disk corruption
// surfaces as a Get error (already handled as "gone"), not as garbage bytes; the
// reachable route is a schema change that makes historical rows undecodable, and
// that is loud in the page itself. Rows inside the page window ARE decode-checked
// and DO correct the total, so the divergence needs an undecodable row that the
// caller never pages to.
//
// ORPHANED REFS MUST NOT CONSUME A PAGE SLOT. The page is taken by rank over the
// SURVIVING refs, never by position in the index, so a pruned row at the newest
// position shifts the whole page up by one instead of silently returning
// limit-1 rows. Same for a row that vanishes between the existence pass and the
// fetch, and for one that fails matchesFilter.
func (s *PebbleActivityStore) queryByIndexPrefixPaged(ctx context.Context, prefix string, f ActivityFilter) ([]ActivityEntry, int, error) {
	if ctxErr := ctx.Err(); ctxErr != nil {
		return nil, 0, ctxErr
	}

	sc, err := s.scanIndexRefs(ctx, prefix)
	if err != nil {
		return nil, 0, err
	}

	alive := make([]bool, sc.len())
	total, err := s.markLiveRefs(ctx, sc, alive)
	if err != nil {
		return nil, 0, err
	}
	if total == 0 {
		// The full path's `all` is nil here, and all[0:0] on a nil slice is nil.
		// Returning an empty non-nil slice instead would be a visible difference
		// to any caller that distinguishes them.
		return nil, 0, nil
	}

	capacity := f.Limit
	if room := total - f.Offset; room < capacity {
		capacity = room
	}
	if capacity < 0 {
		capacity = 0
	}
	page := make([]ActivityEntry, 0, capacity)

	tally := &pactDecodeTally{}
	defer tally.log("queryByIndexPrefix pushdown prefix=" + prefix)

	rank := 0
	fetched := 0
	for i := 0; i < sc.len() && len(page) < f.Limit; i++ {
		if !alive[i] {
			continue
		}
		if rank < f.Offset {
			rank++
			continue
		}
		if fetched%activityCtxCheckInterval == 0 {
			if ctxErr := ctx.Err(); ctxErr != nil {
				return nil, 0, ctxErr
			}
		}
		fetched++

		primaryKey := sc.key(int32(i))
		val, closer, getErr := s.db.Get(primaryKey)
		if getErr != nil {
			// Deleted between markLiveRefs and now (a concurrent prune). The
			// full path would not have counted it either, so drop it from the
			// total AND do not let it consume a page slot.
			total--
			continue
		}
		var entry ActivityEntry
		s.entriesDecoded.Add(1)
		jsonErr := json.Unmarshal(val, &entry)
		closer.Close()
		if jsonErr != nil {
			s.decodeFailures.Add(1)
			tally.record(primaryKey, jsonErr)
			total--
			continue
		}
		// Belt and braces on the "the index decides the id" assumption in
		// pactIndexPushdownEligible: if a ref under act:op:<X>: ever points at a
		// row whose OperationID is not X, the full path would have rejected it
		// here and so does this.
		if !matchesFilter(entry, f) {
			total--
			continue
		}
		page = append(page, entry)
		rank++
	}

	if len(page) == 0 && total == 0 {
		return nil, 0, nil
	}
	return page, total, nil
}

// queryByIndexPrefixFull is the pre-pushdown implementation, unchanged.
//
// It stays because it is the only correct answer for a filter that reads entry
// fields the secondary index does not carry (see pactIndexPushdownEligible), and
// it doubles as the reference implementation the differential tests run
// side-by-side with queryByIndexPrefixPaged over the same fixture. It is
// deliberately NOT "improved" — every difference between it and the pushdown
// must be a difference the pushdown introduced, not one the reference drifted
// into.
//
// It needs its own cancellation checks: without them, threading a request
// context into Query would be cosmetic on that endpoint and an abandoned
// operation-transcript request would keep scanning to completion. ctx is checked
// every activityCtxCheckInterval rows in BOTH loops, and on cancellation the
// accumulated slices are dropped (explicit nil) so they become garbage
// immediately rather than being handed to a caller that has gone away.
//
// Known cost: this collects every ref for the id and decodes all of them before
// slicing by offset/limit, so it is bounded by the number of entries for one
// operation rather than by f.Limit.
func (s *PebbleActivityStore) queryByIndexPrefixFull(ctx context.Context, prefix string, f ActivityFilter) ([]ActivityEntry, int, error) {
	// Upper bound: replace trailing ':' with ';' to cover the entire id sub-namespace.
	upperPrefix := prefix[:len(prefix)-1] + ";"

	if ctxErr := ctx.Err(); ctxErr != nil {
		return nil, 0, ctxErr
	}

	iter, err := s.db.NewIter(&pebble.IterOptions{
		LowerBound: []byte(prefix),
		UpperBound: []byte(upperPrefix),
	})
	if err != nil {
		return nil, 0, fmt.Errorf("pebble_activity_store: queryByIndex new iter: %w", err)
	}
	defer iter.Close()

	var refs [][]byte
	seen := 0
	for iter.First(); iter.Valid(); iter.Next() {
		if seen%activityCtxCheckInterval == 0 {
			if ctxErr := ctx.Err(); ctxErr != nil {
				return nil, 0, ctxErr
			}
		}
		seen++
		valCopy := make([]byte, len(iter.Value()))
		copy(valCopy, iter.Value())
		refs = append(refs, valCopy)
	}

	tally := &pactDecodeTally{}
	defer tally.log("queryByIndexPrefix full prefix=" + prefix)

	var all []ActivityEntry
	for i, ref := range refs {
		if i%activityCtxCheckInterval == 0 {
			if ctxErr := ctx.Err(); ctxErr != nil {
				return nil, 0, ctxErr
			}
		}
		primaryKey, ok := pactPrimaryKeyFromRef(ref)
		if !ok {
			continue
		}
		val, closer, err := s.db.Get(primaryKey)
		if err != nil {
			// Entry may have been deleted (e.g., pruned); skip stale index refs.
			continue
		}
		var entry ActivityEntry
		// Counted like every other decode site in this store. Before this, the
		// index path was the ONE scan invisible to EntriesDecoded/DecodeFailures,
		// so "this query is bounded" could not be asserted about the very
		// endpoint the bound matters most for, and an undecodable row here was
		// dropped by a bare `continue` with no counter and no log.
		s.entriesDecoded.Add(1)
		jsonErr := json.Unmarshal(val, &entry)
		closer.Close()
		if jsonErr != nil {
			s.decodeFailures.Add(1)
			tally.record(primaryKey, jsonErr)
			continue
		}
		if matchesFilter(entry, f) {
			all = append(all, entry)
		}
	}

	sort.Slice(all, func(i, j int) bool {
		return all[i].Timestamp.After(all[j].Timestamp)
	})

	total := len(all)
	start := f.Offset
	if start > len(all) {
		start = len(all)
	}
	end := start + f.Limit
	if end > len(all) {
		end = len(all)
	}
	return all[start:end], total, nil
}

// findExistingDigest looks for a digest row for the given date string ("2006-01-02").
// Returns the DigestDetails, the Pebble key, and any error.
func (s *PebbleActivityStore) findExistingDigest(dateKey string) (DigestDetails, []byte, error) {
	day, err := time.Parse("2006-01-02", dateKey)
	if err != nil {
		return DigestDetails{}, nil, err
	}
	dayEnd := day.Add(24 * time.Hour)

	lower := pactPrimaryKey("digest", day, "")
	upper := pactPrimaryKey("digest", dayEnd, "")

	iter, err := s.db.NewIter(&pebble.IterOptions{
		LowerBound: lower,
		UpperBound: upper,
	})
	if err != nil {
		return DigestDetails{}, nil, fmt.Errorf("pebble_activity_store: findExistingDigest new iter: %w", err)
	}
	defer iter.Close()

	for iter.First(); iter.Valid(); iter.Next() {
		var row struct {
			ActivityEntry
			DigestDetails json.RawMessage `json:"digest_details,omitempty"`
		}
		if jsonErr := json.Unmarshal(iter.Value(), &row); jsonErr != nil {
			continue
		}
		if row.ActivityEntry.Type != "daily_digest" {
			continue
		}

		var dd DigestDetails
		if row.DigestDetails != nil {
			// Old format: digest_details at top level.
			_ = json.Unmarshal(row.DigestDetails, &dd)
		} else if row.ActivityEntry.Details != nil {
			// New format: stored in ActivityEntry.Details.
			if b, merr := json.Marshal(row.ActivityEntry.Details); merr == nil {
				_ = json.Unmarshal(b, &dd)
			}
		}
		keyCopy := make([]byte, len(iter.Key()))
		copy(keyCopy, iter.Key())
		return dd, keyCopy, nil
	}
	return DigestDetails{}, nil, nil
}
