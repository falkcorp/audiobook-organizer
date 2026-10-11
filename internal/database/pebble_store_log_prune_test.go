// file: internal/database/pebble_store_log_prune_test.go
// version: 1.0.0
// guid: 0e8b41d7-6c2a-4f93-a5d0-7b3c9e14f2a8
// last-edited: 2026-10-11

package database

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/cockroachdb/pebble/v2"
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
	prune  func(p *PebbleStore, ctx context.Context, cut time.Time, onChunk PruneChunkFunc) (int, error)
}

var logPruneKinds = []logPruneKind{
	{
		name: "operationlog", prefix: "operationlog:",
		seed: func(t *testing.T, p *PebbleStore, n int, at time.Time, tag string) {
			t.Helper()
			b := p.db.NewBatch()
			defer b.Close()
			for i := 0; i < n; i++ {
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
			for i := 0; i < n; i++ {
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
			n, err := k.prune(p, context.Background(), cut, func(d int) { chunks = append(chunks, d) })
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
			calls := 0
			n, err = k.prune(p, context.Background(), cut, func(int) { calls++ })
			if err != nil || n != 0 || calls != 0 {
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
			n, err := k.prune(p, context.Background(), time.Now().Add(-time.Hour), func(d int) { chunks = append(chunks, d) })
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
			n, err := k.prune(p, ctx, cut, func(int) {
				chunks++
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
	for i := 0; i < rows; i++ {
		rawPutOpChange(t, p, &OperationChange{
			ID: fmt.Sprintf("c%02d", i), OperationID: "op1", BookID: fmt.Sprintf("b%d", i%3), CreatedAt: old,
		})
	}
	mustTrustOpChange(t, p)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var chunks []int
	n, err := p.PruneOperationChanges(ctx, time.Now().Add(-time.Hour), func(d int) {
		chunks = append(chunks, d)
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
