// file: internal/database/pebble_store_log_prune_test.go
// version: 1.1.1
// guid: 0e8b41d7-6c2a-4f93-a5d0-7b3c9e14f2a8
// last-edited: 2026-10-19

package database

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/cockroachdb/pebble/v2"

	"github.com/falkcorp/audiobook-organizer/internal/logger"
)

// logPruneKinds drives the same behavioural tests against both log prunes, which
// share pruneExpiredChunked: operationlog: rows (PruneOperationLogs) and
// syslog: rows (PruneSystemActivityLogs). seed writes n rows stamped at the
// given time in ONE batch so the fixture itself stays cheap; count reads the
// live rows back through the key prefix.
type logPruneKind struct {
	name   string
	prefix string
	seed   func(t *testing.T, p *PebbleStore, n int, at time.Time, tag string)
	prune  func(p *PebbleStore, ctx context.Context, cut time.Time, onProgress PruneProgressFunc) (int, error)
}

var logPruneKinds = []logPruneKind{
	{
		name: "operationlog", prefix: "operationlog:",
		seed: func(t *testing.T, p *PebbleStore, n int, at time.Time, tag string) {
			t.Helper()
			b := p.db.NewBatch()
			defer b.Close()
			for i := range n {
				// Distinct op ids so key order is NOT time order, the way real
				// operationlog keys (operationlog:<opID>:<nanos>:<seq>) are.
				key := fmt.Sprintf("operationlog:op%03d:%d:%s%d", (i*7)%13, at.UnixNano(), tag, i)
				val := fmt.Sprintf(`{"id":%d,"operation_id":"op","level":"info","message":"m","created_at":%q}`,
					i, at.Format(time.RFC3339Nano))
				if err := b.Set([]byte(key), []byte(val), nil); err != nil {
					t.Fatal(err)
				}
			}
			if err := b.Commit(pebble.Sync); err != nil {
				t.Fatal(err)
			}
		},
		prune: (*PebbleStore).PruneOperationLogs,
	},
	{
		name: "syslog", prefix: "syslog:",
		seed: func(t *testing.T, p *PebbleStore, n int, at time.Time, tag string) {
			t.Helper()
			b := p.db.NewBatch()
			defer b.Close()
			for i := range n {
				ts := at.Add(time.Duration(i) * time.Nanosecond)
				key := fmt.Sprintf("syslog:%s:%s%d", ts.Format(time.RFC3339Nano), tag, i)
				val := fmt.Sprintf(`{"source":"s","level":"info","message":"m","created_at":%q}`, ts.Format(time.RFC3339Nano))
				if err := b.Set([]byte(key), []byte(val), nil); err != nil {
					t.Fatal(err)
				}
			}
			if err := b.Commit(pebble.Sync); err != nil {
				t.Fatal(err)
			}
		},
		prune: (*PebbleStore).PruneSystemActivityLogs,
	},
}

func countPrefix(t *testing.T, p *PebbleStore, prefix, tagPrefix string) int {
	t.Helper()
	iter, err := p.db.NewIter(&pebble.IterOptions{LowerBound: []byte(prefix), UpperBound: prefixEnd([]byte(prefix))})
	if err != nil {
		t.Fatal(err)
	}
	defer iter.Close()
	n := 0
	for iter.First(); iter.Valid(); iter.Next() {
		if tagPrefix == "" || containsTag(string(iter.Key()), tagPrefix) {
			n++
		}
	}
	return n
}

func containsTag(key, tag string) bool {
	// seeded keys end in ":<tag><i>" (syslog) or ":<tag><i>" after the nanos (operationlog).
	for i := len(key) - 1; i >= 0; i-- {
		if key[i] == ':' {
			rest := key[i+1:]
			return len(rest) >= len(tag) && rest[:len(tag)] == tag
		}
	}
	return false
}

// chunkSizes returns a PruneProgressFunc that appends the size of each
// committed chunk (the growth of the running deleted total) to *chunks. A
// report that deletes nothing (a scan heartbeat) appends nothing.
func chunkSizes(chunks *[]int) PruneProgressFunc {
	last := 0
	return func(_, deleted int) {
		if deleted > last {
			*chunks = append(*chunks, deleted-last)
			last = deleted
		}
	}
}

func setPruneScanTick(t *testing.T, n int) {
	t.Helper()
	old := pruneScanTick
	pruneScanTick = n
	t.Cleanup(func() { pruneScanTick = old })
}

func setLogPruneChunk(t *testing.T, n int) {
	t.Helper()
	old := logPruneChunk
	logPruneChunk = n
	t.Cleanup(func() { logPruneChunk = old })
}

// A backlog several chunks deep is deleted completely, one commit per chunk,
// with every row of the cutoff's right side untouched; a second run finds
// nothing and reports no chunk.
func TestLogPrune_MultiChunkDeletesAllExpiredAndOnlyExpired(t *testing.T) {
	for _, k := range logPruneKinds {
		t.Run(k.name, func(t *testing.T) {
			p := newOpChangeTestStore(t)
			setLogPruneChunk(t, 7)
			old := time.Now().Add(-100 * 24 * time.Hour)
			fresh := time.Now().Add(-1 * time.Hour)
			cut := time.Now().Add(-90 * 24 * time.Hour)
			k.seed(t, p, 50, old, "old") // 7 full chunks of 7 + a partial of 1
			k.seed(t, p, 10, fresh, "new")

			var chunks []int
			n, err := k.prune(p, context.Background(), cut, chunkSizes(&chunks))
			if err != nil || n != 50 {
				t.Fatalf("prune = %d, %v; want 50, nil", n, err)
			}
			wantChunks := []int{7, 7, 7, 7, 7, 7, 7, 1}
			if fmt.Sprint(chunks) != fmt.Sprint(wantChunks) {
				t.Fatalf("chunks = %v, want %v (one commit per logPruneChunk rows)", chunks, wantChunks)
			}
			if got := countPrefix(t, p, k.prefix, ""); got != 10 {
				t.Fatalf("rows left = %d, want the 10 non-expired ones", got)
			}
			if got := countPrefix(t, p, k.prefix, "new"); got != 10 {
				t.Fatalf("non-expired rows left = %d, want all 10", got)
			}

			// Idempotent: nothing expired is left, so no chunk and no change.
			var calls []int
			n, err = k.prune(p, context.Background(), cut, chunkSizes(&calls))
			if err != nil || n != 0 || len(calls) != 0 {
				t.Fatalf("re-run = %d, %v with %d chunks; want 0, nil, 0", n, err, calls)
			}
			if got := countPrefix(t, p, k.prefix, ""); got != 10 {
				t.Fatalf("rows after re-run = %d, want 10", got)
			}
		})
	}
}

// When the expired count is an exact multiple of the chunk size the last chunk
// is full and there is no extra empty commit.
func TestLogPrune_ExactMultipleOfChunk(t *testing.T) {
	for _, k := range logPruneKinds {
		t.Run(k.name, func(t *testing.T) {
			p := newOpChangeTestStore(t)
			setLogPruneChunk(t, 10)
			k.seed(t, p, 30, time.Now().Add(-48*time.Hour), "old")
			var chunks []int
			n, err := k.prune(p, context.Background(), time.Now().Add(-time.Hour), chunkSizes(&chunks))
			if err != nil || n != 30 || fmt.Sprint(chunks) != "[10 10 10]" {
				t.Fatalf("prune = %d, %v, chunks %v; want 30, nil, [10 10 10]", n, err, chunks)
			}
		})
	}
}

// A cancel between chunks stops the prune: the chunks already committed stay
// deleted and the returned count is exactly them, the cancel is returned (not
// swallowed), the staged remainder is not committed, and a later run with a
// live context finishes the job.
func TestLogPrune_CancelStopsBetweenChunksKeepingCommittedWork(t *testing.T) {
	for _, k := range logPruneKinds {
		t.Run(k.name, func(t *testing.T) {
			p := newOpChangeTestStore(t)
			setLogPruneChunk(t, 10)
			cut := time.Now().Add(-time.Hour)
			k.seed(t, p, 55, time.Now().Add(-48*time.Hour), "old")
			k.seed(t, p, 5, time.Now(), "new")

			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			chunks := 0
			n, err := k.prune(p, ctx, cut, func(_, deleted int) {
				chunks = deleted / 10
				if chunks == 2 {
					cancel()
				}
			})
			if !errors.Is(err, context.Canceled) {
				t.Fatalf("err = %v, want context.Canceled (a cancel must not be reported as success)", err)
			}
			if n != 20 || chunks != 2 {
				t.Fatalf("n = %d after %d chunks; want exactly the 2 committed chunks (20 rows)", n, chunks)
			}
			if got := countPrefix(t, p, k.prefix, "old"); got != 35 {
				t.Fatalf("expired rows left = %d, want 35 (55 - the 20 committed)", got)
			}
			if got := countPrefix(t, p, k.prefix, "new"); got != 5 {
				t.Fatalf("non-expired rows left = %d, want 5", got)
			}

			n, err = k.prune(p, context.Background(), cut, nil)
			if err != nil || n != 35 {
				t.Fatalf("resume = %d, %v; want the remaining 35, nil", n, err)
			}
			if got := countPrefix(t, p, k.prefix, ""); got != 5 {
				t.Fatalf("rows after resume = %d, want 5", got)
			}
		})
	}
}

// An already-cancelled context deletes nothing and says so.
func TestLogPrune_CancelledBeforeStartDeletesNothing(t *testing.T) {
	for _, k := range logPruneKinds {
		t.Run(k.name, func(t *testing.T) {
			p := newOpChangeTestStore(t)
			k.seed(t, p, 12, time.Now().Add(-48*time.Hour), "old")
			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			n, err := k.prune(p, ctx, time.Now().Add(-time.Hour), nil)
			if !errors.Is(err, context.Canceled) || n != 0 {
				t.Fatalf("prune = %d, %v; want 0, context.Canceled", n, err)
			}
			if got := countPrefix(t, p, k.prefix, ""); got != 12 {
				t.Fatalf("rows left = %d, want all 12", got)
			}
		})
	}
}

// A row whose value does not decode is skipped, never deleted, and does not
// stop the prune of the rows around it.
func TestLogPrune_UndecodableRowIsKept(t *testing.T) {
	for _, k := range logPruneKinds {
		t.Run(k.name, func(t *testing.T) {
			p := newOpChangeTestStore(t)
			setLogPruneChunk(t, 3)
			k.seed(t, p, 8, time.Now().Add(-48*time.Hour), "old")
			if err := p.db.Set([]byte(k.prefix+"garbage"), []byte("{not json"), pebble.Sync); err != nil {
				t.Fatal(err)
			}
			n, err := k.prune(p, context.Background(), time.Now().Add(-time.Hour), nil)
			if err != nil || n != 8 {
				t.Fatalf("prune = %d, %v; want 8, nil", n, err)
			}
			if got := countPrefix(t, p, k.prefix, ""); got != 1 {
				t.Fatalf("rows left = %d, want only the undecodable one", got)
			}
		})
	}
}

// PruneOperationChanges honours the same ctx and progress contract: a cancel
// between chunks keeps the committed chunks (rows and index entries together)
// and returns the cancel.
func TestOpChangePrune_CancelStopsBetweenChunks(t *testing.T) {
	p := newOpChangeTestStore(t)
	oldChunk := opChangePruneChunk
	opChangePruneChunk = 4
	t.Cleanup(func() { opChangePruneChunk = oldChunk })
	mustTrustOpChange(t, p)
	old := time.Now().Add(-48 * time.Hour)
	const rows = 15
	for i := range rows {
		rawPutOpChange(t, p, &OperationChange{
			ID: fmt.Sprintf("c%02d", i), OperationID: "op1", BookID: fmt.Sprintf("b%d", i%3), CreatedAt: old,
		})
	}
	mustTrustOpChange(t, p)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var chunks []int
	record := chunkSizes(&chunks)
	n, err := p.PruneOperationChanges(ctx, time.Now().Add(-time.Hour), func(scanned, deleted int) {
		record(scanned, deleted)
		if len(chunks) == 2 {
			cancel()
		}
	})
	if !errors.Is(err, context.Canceled) || n != 8 || fmt.Sprint(chunks) != "[4 4]" {
		t.Fatalf("prune = %d, %v, chunks %v; want 8, context.Canceled, [4 4]", n, err, chunks)
	}
	if k := countOpChangeIndexKeys(t, p); k != rows-8 {
		t.Fatalf("index keys = %d, want %d (entries leave with their rows, chunk by chunk)", k, rows-8)
	}

	n, err = p.PruneOperationChanges(context.Background(), time.Now().Add(-time.Hour), nil)
	if err != nil || n != rows-8 {
		t.Fatalf("resume = %d, %v; want %d, nil", n, err, rows-8)
	}
	if k := countOpChangeIndexKeys(t, p); k != 0 {
		t.Fatalf("index keys after resume = %d, want 0", k)
	}
}

// A scan with nothing to delete still reports every pruneScanTick rows, with
// running scanned totals and deleted still 0: after the backlog is gone every
// weekly purge is mostly such a scan, and without the heartbeat it was silent
// for its whole length (the purge op's watchdog read that as stuck).
func TestPrune_ScanHeartbeatReportsWithNothingToDelete(t *testing.T) {
	for _, k := range logPruneKinds {
		t.Run(k.name, func(t *testing.T) {
			p := newOpChangeTestStore(t)
			setPruneScanTick(t, 5)
			k.seed(t, p, 23, time.Now(), "new")
			var seen [][2]int
			n, err := k.prune(p, context.Background(), time.Now().Add(-time.Hour), func(scanned, deleted int) {
				seen = append(seen, [2]int{scanned, deleted})
			})
			if err != nil || n != 0 {
				t.Fatalf("prune = %d, %v; want 0, nil", n, err)
			}
			if fmt.Sprint(seen) != "[[5 0] [10 0] [15 0] [20 0]]" {
				t.Fatalf("progress = %v, want a heartbeat every 5 scanned rows with nothing deleted", seen)
			}
		})
	}
	t.Run("opchange", func(t *testing.T) {
		p := newOpChangeTestStore(t)
		setPruneScanTick(t, 5)
		mustTrustOpChange(t, p)
		for i := range 12 {
			rawPutOpChange(t, p, &OperationChange{ID: fmt.Sprintf("c%02d", i), OperationID: "op1", BookID: "b", CreatedAt: time.Now()})
		}
		var seen [][2]int
		n, err := p.PruneOperationChanges(context.Background(), time.Now().Add(-time.Hour), func(scanned, deleted int) {
			seen = append(seen, [2]int{scanned, deleted})
		})
		if err != nil || n != 0 || fmt.Sprint(seen) != "[[5 0] [10 0]]" {
			t.Fatalf("prune = %d, %v, progress %v; want 0, nil, [[5 0] [10 0]]", n, err, seen)
		}
	})
}

// A cancel during the scan stops it at the next heartbeat even when no chunk
// is due: the cancel is returned, the staged rows are not committed, and the
// scan does not run on to the end of the prefix.
func TestPrune_CancelDuringScanStopsAtTheNextHeartbeat(t *testing.T) {
	for _, k := range logPruneKinds {
		t.Run(k.name, func(t *testing.T) {
			p := newOpChangeTestStore(t)
			setPruneScanTick(t, 5)
			setLogPruneChunk(t, 1000) // no chunk commits during this scan
			k.seed(t, p, 30, time.Now().Add(-48*time.Hour), "old")
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			var seen []int
			n, err := k.prune(p, ctx, time.Now().Add(-time.Hour), func(scanned, _ int) {
				seen = append(seen, scanned)
				cancel()
			})
			if !errors.Is(err, context.Canceled) || n != 0 {
				t.Fatalf("prune = %d, %v; want 0, context.Canceled", n, err)
			}
			if fmt.Sprint(seen) != "[5]" {
				t.Fatalf("heartbeats = %v, want the scan to stop at the first one after the cancel ([5])", seen)
			}
			if got := countPrefix(t, p, k.prefix, ""); got != 30 {
				t.Fatalf("rows left = %d, want all 30 (staged rows are never committed on a cancel)", got)
			}
		})
	}
	t.Run("opchange", func(t *testing.T) {
		p := newOpChangeTestStore(t)
		setPruneScanTick(t, 5)
		old := opChangePruneChunk
		opChangePruneChunk = 1000
		t.Cleanup(func() { opChangePruneChunk = old })
		mustTrustOpChange(t, p)
		for i := range 12 {
			rawPutOpChange(t, p, &OperationChange{ID: fmt.Sprintf("c%02d", i), OperationID: "op1", BookID: "b", CreatedAt: time.Now().Add(-48 * time.Hour)})
		}
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		calls := 0
		n, err := p.PruneOperationChanges(ctx, time.Now().Add(-time.Hour), func(int, int) { calls++; cancel() })
		if !errors.Is(err, context.Canceled) || n != 0 || calls != 1 {
			t.Fatalf("prune = %d, %v after %d heartbeats; want 0, context.Canceled, 1", n, err, calls)
		}
		if got := countPrefix(t, p, opChangeKeyPrefix, ""); got != 12 {
			t.Fatalf("opchange rows = %d, want all 12", got)
		}
	})
}

// The log purge (logger.PruneOldLogs, what maintenance.purge-old-logs runs)
// must never touch the undo journal. opchange: rows are undo history: the
// approved storage design (docs/design/2026-10-03-storage-efficiency-design.md
// section 7) gives them their own undo window so no log setting can shorten
// undo, and keeps book_file_delete / book_file_repoint rows until the release C
// ledger scan. Old journal rows of every type, and their opchange_by_book:
// entries, survive a purge that does delete the old log rows around them.
func TestPruneOldLogs_LeavesTheUndoJournalUntouched(t *testing.T) {
	p := newOpChangeTestStore(t)
	mustTrustOpChange(t, p)
	old := time.Now().Add(-400 * 24 * time.Hour)
	types := []string{"book_file_delete", "book_file_repoint", "file_move", "metadata_update", "repair_plan_record"}
	for i, ct := range types {
		rawPutOpChange(t, p, &OperationChange{
			ID: fmt.Sprintf("c%02d", i), OperationID: "op-old", BookID: fmt.Sprintf("b%d", i), ChangeType: ct, CreatedAt: old,
		})
	}
	mustTrustOpChange(t, p)
	rowsBefore := countPrefix(t, p, opChangeKeyPrefix, "")
	indexBefore := countOpChangeIndexKeys(t, p)
	if rowsBefore != len(types) || indexBefore != len(types) {
		t.Fatalf("fixture: %d rows, %d index entries; want %d of each", rowsBefore, indexBefore, len(types))
	}
	for _, k := range logPruneKinds {
		k.seed(t, p, 20, old, "old")
		k.seed(t, p, 3, time.Now(), "new")
	}

	n, err := logger.PruneOldLogs(context.Background(), p, 90, logger.New("test"), nil)
	if err != nil || n != 40 {
		t.Fatalf("PruneOldLogs = %d, %v; want the 40 old log rows, nil", n, err)
	}
	for _, k := range logPruneKinds {
		if got := countPrefix(t, p, k.prefix, ""); got != 3 {
			t.Errorf("%s rows left = %d, want the 3 recent ones (the purge must still purge logs)", k.name, got)
		}
	}
	if got := countPrefix(t, p, opChangeKeyPrefix, ""); got != rowsBefore {
		t.Errorf("opchange rows = %d after the log purge, want all %d: the log purge deleted undo history", got, rowsBefore)
	}
	if got := countOpChangeIndexKeys(t, p); got != indexBefore {
		t.Errorf("opchange_by_book entries = %d after the log purge, want all %d", got, indexBefore)
	}
}
