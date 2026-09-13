// file: internal/database/pebble_store_atpath_index.go
// version: 1.1.0
// guid: 3f6c1b8e-9a42-4d7e-b5c1-0e8a7d2f4c93
// last-edited: 2026-09-12

// The book_atpath: multi-valued path index.
//
//	book_atpath:<path bytes>\x00<book id>  ->  (empty value)
//
// book:path:<path> holds exactly one id and the last writer wins, so two books
// at one path share a key that names whichever wrote last, and the key is
// deleted when either one moves away. book_atpath: holds one key per (path,
// book) pair instead, so every book at a path is findable.
//
// Invariant (completeness): for every book:<id> row with FilePath == P, the key
// book_atpath:P\x00<id> exists. Extras are allowed: a key whose row is gone,
// has moved, or is soft-deleted is dropped by the reader's point-verify. A
// MISSING key can report a taken path as free; an extra costs one point read.
//
// Maintained by CreateBook (Set), UpdateBook (Delete the old pair only when the
// path changed; Set the new pair UNCONDITIONALLY, so any write self-heals and a
// lost-update revert cannot strip a book's own entry) and DeleteBook (Delete),
// each in the same batch as the row. Every book is indexed regardless of
// MarkedForDeletion; liveness is checked on read, so soft-delete and restore
// toggles need no index work.
//
// The family sits outside the "book:" scan range on purpose ('_' sorts after
// ';'), so full book scans and memdb warmup do not walk ~60k more keys.
//
// Design: .claude/notes/book-pathset-index-design-2026-09-12.md.

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
	"sync/atomic"
	"time"

	"github.com/cockroachdb/pebble/v2"
	"golang.org/x/sync/errgroup"
)

const bookAtPathPrefix = "book_atpath:"

// bookAtPathBackfillKey marks the index complete. The reader trusts the index
// only once it exists. Bump the suffix if the key format or the set of indexed
// books ever changes.
//
// ROLLBACK HAZARD: a binary that predates this index does not maintain it. If
// one runs against the database after this sentinel is set, books it moves have
// no entry at their new path and the index silently under-reports. After any
// rollback-then-roll-forward, run maintenance.book-atpath-index-backfill (which
// rebuilds unconditionally) and then maintenance.book-atpath-index-verify.
const bookAtPathBackfillKey = "system:backfill:book_atpath_index_v1_done"

// bookAtPathBackfillChunk is how many index keys one worker buffers per commit.
// A var only so tests can exercise the multi-chunk path; never reassign in prod
// code.
var bookAtPathBackfillChunk = 10_000

// bookAtPathBackfillWorkers is how many goroutines decode rows and stage index
// keys during a backfill. A var only so tests can pin the pool size regardless
// of the machine's core count; never reassign in prod code.
var bookAtPathBackfillWorkers = runtime.NumCPU()

// bookAtPathBackfillLogEvery is how many rows are scanned between progress logs.
const bookAtPathBackfillLogEvery = 10_000

// bookAtPathBackfillAfterChunk, when non-nil, runs after each durable chunk
// commit, with the run's commit count so far. Test-only hook for the resume and
// race cases; nil in production. It is called from the backfill's worker
// goroutines, concurrently, so it must be goroutine-safe.
var bookAtPathBackfillAfterChunk func(commits int)

// updateBookAfterOldReadHook, when non-nil, runs inside UpdateBook right after
// it reads oldBook (unlocked) and before it commits. Test-only: it lets a test
// land a competing write in exactly the window UpdateBook's self-race lives
// in. nil in production.
var updateBookAfterOldReadHook func(id string)

// bookAtPathKey is the index key for one (path, book) pair.
func bookAtPathKey(path, id string) []byte {
	k := make([]byte, 0, len(bookAtPathPrefix)+len(path)+1+len(id))
	k = append(k, bookAtPathPrefix...)
	k = append(k, path...)
	k = append(k, 0)
	k = append(k, id...)
	return k
}

// bookAtPathBounds is the scan range holding exactly path's keys:
// [book_atpath:P\x00, book_atpath:P\x01). POSIX paths cannot contain NUL, so no
// other real path's key falls inside it; a corrupt NUL-bearing path can only
// add a spurious candidate, which the reader recognises and skips.
func bookAtPathBounds(path string) (lower, upper []byte) {
	lower = make([]byte, 0, len(bookAtPathPrefix)+len(path)+1)
	lower = append(lower, bookAtPathPrefix...)
	lower = append(lower, path...)
	upper = append(append([]byte{}, lower...), 1)
	lower = append(lower, 0)
	return lower, upper
}

// splitBookAtPathKey recovers (path, id) from a full index key. Book ids never
// contain NUL, so the LAST NUL is the separator. ok is false for a key with no
// separator or an empty id.
func splitBookAtPathKey(key []byte) (path, id string, ok bool) {
	rest := key[len(bookAtPathPrefix):]
	i := bytes.LastIndexByte(rest, 0)
	if i < 0 || i == len(rest)-1 {
		return "", "", false
	}
	return string(rest[:i]), string(rest[i+1:]), true
}

// bookAtPathIndexBuilt reports whether the backfill sentinel exists. Only true
// is cached, so a long-lived process picks up a completion without a restart.
// A read error other than ErrNotFound is returned, never read as "not built"
// (which would be harmless but slow) nor as "built" (which could be a lie).
func (p *PebbleStore) bookAtPathIndexBuilt() (bool, error) {
	if p.bookAtPathBuilt.Load() {
		return true, nil
	}
	_, closer, err := p.db.Get([]byte(bookAtPathBackfillKey))
	switch {
	case err == nil:
		closer.Close()
		p.bookAtPathBuilt.Store(true)
		return true, nil
	case errors.Is(err, pebble.ErrNotFound):
		return false, nil
	default:
		return false, fmt.Errorf("read %s: %w", bookAtPathBackfillKey, err)
	}
}

// bookAtPathBackfillRow is one book row handed from the backfill's iterator to
// a worker. v is a copy the worker owns.
type bookAtPathBackfillRow struct {
	id string
	v  []byte
}

// BookAtPathBackfillResult summarises one backfill run.
type BookAtPathBackfillResult struct {
	// Skipped is true when the sentinel was already set and nothing was done.
	Skipped bool
	Scanned int
	Commits int
}

// BackfillBookAtPathIndex builds the book_atpath: index once, gated by its
// sentinel. It is the startup repair: after the first successful run it is a
// logged no-op.
func (p *PebbleStore) BackfillBookAtPathIndex(ctx context.Context) (BookAtPathBackfillResult, error) {
	return p.backfillBookAtPath(ctx, false)
}

// RebuildBookAtPathIndex rewrites every book's index key regardless of the
// sentinel, then sets it. For the rollback runbook: a pre-index binary may have
// moved books without maintaining the index. It only ever Sets keys, so running
// it on a live store is safe; stale keys it cannot see stay as harmless extras.
func (p *PebbleStore) RebuildBookAtPathIndex(ctx context.Context) (BookAtPathBackfillResult, error) {
	return p.backfillBookAtPath(ctx, true)
}

// backfillBookAtPath is one pass over the book rows, fanned out over a bounded
// worker pool.
//
// CONCURRENCY: one producer goroutine owns the single Pebble iterator
// (forEachBookRow) and hands each row, copied, to exactly one of
// bookAtPathBackfillWorkers workers over a bounded channel. Each worker decodes
// its rows (the JSON decode is the per-row cost) and stages Sets into its OWN
// *pebble.Batch, which is not goroutine-safe and so is never shared, committing
// every bookAtPathBackfillChunk keys. The rows a worker sees are disjoint from
// every other worker's, and each key is derived from one row's id, so no two
// workers ever write the same key. The counters are atomics.
//
// Race with live writes: the pass only Sets index keys and never writes a row
// or deletes a key. Any row changed after the iterator opened was written by
// this binary, which maintains its own entry in the row's batch; any row not
// changed is visited. So completeness holds at the end. The one artefact is an
// extra (a book seen at A, moved to B, then re-added at A by this pass), which
// the reader drops. The pool widens that window but cannot turn it into a gap.
//
// Restart-from-zero: every chunk is committed with pebble.Sync, and the
// sentinel is committed only after every worker's last commit has returned, so
// the sentinel can never outlive a key it vouches for, an interrupted run loses
// no correctness, and the next run simply rescans. A checkpoint cursor would
// miss rows a pre-index binary moved between attempts.
func (p *PebbleStore) backfillBookAtPath(ctx context.Context, force bool) (BookAtPathBackfillResult, error) {
	var res BookAtPathBackfillResult
	start := time.Now()
	mode := "backfill"
	if force {
		mode = "rebuild"
	}

	if !force {
		switch _, closer, err := p.db.Get([]byte(bookAtPathBackfillKey)); {
		case err == nil:
			closer.Close()
			p.bookAtPathBuilt.Store(true)
			slog.Info("book-atpath-backfill: already complete, skipping", "sentinel", bookAtPathBackfillKey)
			res.Skipped = true
			return res, nil
		case errors.Is(err, pebble.ErrNotFound):
		default:
			slog.Error("book-atpath-backfill: cannot read sentinel, aborting",
				"sentinel", bookAtPathBackfillKey, "err", err)
			return res, fmt.Errorf("read backfill sentinel: %w", err)
		}
	}

	workers := bookAtPathBackfillWorkers
	if workers < 1 {
		workers = 1
	}
	slog.Info("book-atpath-backfill: starting", "mode", mode,
		"chunk", bookAtPathBackfillChunk, "workers", workers)

	var scanned, commits atomic.Int64
	g, gctx := errgroup.WithContext(ctx)
	rows := make(chan bookAtPathBackfillRow, workers*64)

	// Producer: the single iterator. forEachBookRow's value slice is only valid
	// until the iterator advances, so each value is copied before it is handed
	// to a worker.
	g.Go(func() error {
		defer close(rows)
		return forEachBookRow(p.db, func(id string, v []byte) error {
			select {
			case rows <- bookAtPathBackfillRow{id: id, v: bytes.Clone(v)}:
				return nil
			case <-gctx.Done():
				return gctx.Err()
			}
		})
	})

	for w := 0; w < workers; w++ {
		g.Go(func() error {
			batch := p.db.NewBatch()
			defer func() { batch.Close() }()
			buffered := 0
			flush := func() error {
				if buffered == 0 {
					return nil
				}
				if err := batch.Commit(pebble.Sync); err != nil {
					return fmt.Errorf("chunk commit: %w", err)
				}
				batch.Close()
				batch = p.db.NewBatch()
				buffered = 0
				n := commits.Add(1)
				if bookAtPathBackfillAfterChunk != nil {
					bookAtPathBackfillAfterChunk(int(n))
				}
				// The caller's ctx, not gctx, so a cancel surfaces as
				// context.Canceled rather than as another worker's error.
				return ctx.Err()
			}
			for r := range rows {
				if err := gctx.Err(); err != nil {
					return err
				}
				var row bookAtPathRow
				if err := json.Unmarshal(r.v, &row); err != nil {
					// A row that cannot be decoded cannot be indexed, and its
					// absence would break completeness. Fail the run; no sentinel.
					return fmt.Errorf("decode book row %s: %w", r.id, err)
				}
				if err := batch.Set(bookAtPathKey(row.FilePath, r.id), []byte{}, nil); err != nil {
					return fmt.Errorf("stage index key for %s: %w", r.id, err)
				}
				buffered++
				if n := scanned.Add(1); n%bookAtPathBackfillLogEvery == 0 {
					slog.Info("book-atpath-backfill: progress", "scanned", n,
						"elapsed", time.Since(start).Round(time.Second).String())
				}
				if buffered >= bookAtPathBackfillChunk {
					if err := flush(); err != nil {
						return err
					}
				}
			}
			return flush()
		})
	}

	err := g.Wait()
	res.Scanned = int(scanned.Load())
	res.Commits = int(commits.Load())
	if err == nil {
		// A cancel that landed after the last row was handed out is still a
		// cancel: do not vouch for the index on a run the caller abandoned.
		err = ctx.Err()
	}
	if err != nil {
		slog.Error("book-atpath-backfill: failed, sentinel NOT set", "mode", mode,
			"scanned", res.Scanned, "commits", res.Commits, "err", err)
		return res, err
	}

	// Every worker's commits were Sync and have returned, so every key is
	// durable before the sentinel that vouches for them is written.
	if err := p.db.Set([]byte(bookAtPathBackfillKey), []byte("1"), pebble.Sync); err != nil {
		slog.Error("book-atpath-backfill: sentinel commit failed", "scanned", res.Scanned, "err", err)
		return res, err
	}
	res.Commits++
	p.bookAtPathBuilt.Store(true)
	slog.Info("book-atpath-backfill: complete", "mode", mode, "scanned", res.Scanned,
		"commits", res.Commits, "duration", time.Since(start).Round(time.Millisecond).String())
	return res, nil
}

// bookAtPathSampleCap bounds each sample list in the verify report.
const bookAtPathSampleCap = 50

// BookAtPathIndexReport is the result of VerifyBookAtPathIndex.
type BookAtPathIndexReport struct {
	SentinelSet      bool `json:"sentinel_set"`
	BooksScanned     int  `json:"books_scanned"`
	IndexKeysScanned int  `json:"index_keys_scanned"`
	// UndecodableRows are book rows whose JSON did not decode. They cannot be
	// checked, so a report with any is incomplete.
	UndecodableRows int `json:"undecodable_rows"`

	// MissingLive is the bug class: a live book with no key at its path, which
	// LiveBookIDsAtPath would report as free once the sentinel is set.
	MissingLive int `json:"missing_live"`
	// MissingTrashed is a soft-deleted book with no key. A defect, but it
	// cannot produce a false "free" (the reader excludes trashed books).
	MissingTrashed int `json:"missing_trashed"`
	// ExtraRowGone / ExtraRowMoved are keys with no matching row. Benign.
	ExtraRowGone  int `json:"extra_row_gone"`
	ExtraRowMoved int `json:"extra_row_moved"`
	// Malformed keys have no NUL separator or an empty id. Informational.
	Malformed int `json:"malformed"`

	// Samples are "<id> <path>" strings (keys, quoted, for malformed), at most
	// bookAtPathSampleCap per class, sorted.
	SampleMissingLive    []string `json:"sample_missing_live,omitempty"`
	SampleMissingTrashed []string `json:"sample_missing_trashed,omitempty"`
	SampleExtra          []string `json:"sample_extra,omitempty"`
	SampleMalformed      []string `json:"sample_malformed,omitempty"`
}

// Complete reports whether every book row was decodable and therefore checked.
func (r BookAtPathIndexReport) Complete() bool { return r.UndecodableRows == 0 }

// VerifyBookAtPathIndex diffs the book_atpath: index against the book rows
// from one snapshot. Read-only.
//
// Sequential for the same reason as the backfill: two single-cursor scans with
// a map lookup per key. A few hundred milliseconds at library scale.
func (p *PebbleStore) VerifyBookAtPathIndex(ctx context.Context) (BookAtPathIndexReport, error) {
	var rep BookAtPathIndexReport
	built, err := p.bookAtPathIndexBuilt()
	if err != nil {
		return rep, err
	}
	rep.SentinelSet = built

	snap := p.db.NewSnapshot()
	defer snap.Close()

	type pair struct{ path, id string }
	expected := make(map[pair]bool) // value: trashed
	rowPath := make(map[string]string)
	n := 0
	if err := forEachBookRow(snap, func(id string, v []byte) error {
		n++
		if n%10_000 == 0 {
			if err := ctx.Err(); err != nil {
				return err
			}
		}
		rep.BooksScanned++
		var row bookAtPathRow
		if err := json.Unmarshal(v, &row); err != nil {
			rep.UndecodableRows++
			return nil
		}
		expected[pair{row.FilePath, id}] = markedForDeletionFlag(row.MarkedForDeletion)
		rowPath[id] = row.FilePath
		return nil
	}); err != nil {
		return rep, fmt.Errorf("verify book_atpath: scan books: %w", err)
	}

	var extra, malformed []string
	lower := []byte(bookAtPathPrefix)
	upper := bareRowUpperBound(bookAtPathPrefix)
	if err := forEachKeyInRange(snap, lower, upper, func(key, _ []byte) error {
		rep.IndexKeysScanned++
		if rep.IndexKeysScanned%10_000 == 0 {
			if err := ctx.Err(); err != nil {
				return err
			}
		}
		path, id, ok := splitBookAtPathKey(key)
		if !ok {
			rep.Malformed++
			malformed = append(malformed, fmt.Sprintf("%q", key))
			return nil
		}
		k := pair{path, id}
		if _, found := expected[k]; found {
			delete(expected, k)
			return nil
		}
		if _, exists := rowPath[id]; exists {
			rep.ExtraRowMoved++
		} else {
			rep.ExtraRowGone++
		}
		extra = append(extra, id+" "+path)
		return nil
	}); err != nil {
		return rep, fmt.Errorf("verify book_atpath: scan index: %w", err)
	}

	var missLive, missTrashed []string
	for k, trashed := range expected {
		if trashed {
			rep.MissingTrashed++
			missTrashed = append(missTrashed, k.id+" "+k.path)
		} else {
			rep.MissingLive++
			missLive = append(missLive, k.id+" "+k.path)
		}
	}
	rep.SampleMissingLive = sampleSorted(missLive)
	rep.SampleMissingTrashed = sampleSorted(missTrashed)
	rep.SampleExtra = sampleSorted(extra)
	rep.SampleMalformed = sampleSorted(malformed)
	return rep, nil
}

func sampleSorted(s []string) []string {
	sort.Strings(s)
	if len(s) > bookAtPathSampleCap {
		s = s[:bookAtPathSampleCap]
	}
	return s
}
