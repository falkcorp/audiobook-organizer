// file: internal/logger/retention_test.go
// version: 1.0.0
// guid: 5d0c7a3e-91b4-4f6a-8e25-c3a1f7b94d60
// last-edited: 2026-10-11

package logger

import (
	"context"
	"errors"
	"testing"
	"time"
)

// chunkedFake is a RetentionStore whose prunes "commit" fixed chunk sizes, so
// PruneOldLogs's progress and cancellation handling can be checked without a
// database. The real chunking is tested against Pebble in internal/database.
type chunkedFake struct {
	logs, changes, activity []int // chunk sizes each prune commits, in order
	cancelAfterLogs         context.CancelFunc
}

func (f *chunkedFake) run(ctx context.Context, chunks []int, onChunk func(int)) (int, error) {
	total := 0
	for _, c := range chunks {
		if err := ctx.Err(); err != nil {
			return total, err
		}
		total += c
		if onChunk != nil {
			onChunk(c)
		}
	}
	return total, ctx.Err()
}

func (f *chunkedFake) PruneOperationLogs(ctx context.Context, _ time.Time, onChunk func(int)) (int, error) {
	n, err := f.run(ctx, f.logs, onChunk)
	if f.cancelAfterLogs != nil {
		f.cancelAfterLogs()
		return n, ctx.Err()
	}
	return n, err
}

func (f *chunkedFake) PruneOperationChanges(ctx context.Context, _ time.Time, onChunk func(int)) (int, error) {
	return f.run(ctx, f.changes, onChunk)
}

func (f *chunkedFake) PruneSystemActivityLogs(ctx context.Context, _ time.Time, onChunk func(int)) (int, error) {
	return f.run(ctx, f.activity, onChunk)
}

func TestPruneOldLogs_ReportsRunningTotalPerChunk(t *testing.T) {
	store := &chunkedFake{logs: []int{5, 5, 2}, changes: []int{4}, activity: []int{3, 1}}
	type step struct {
		kind  string
		total int
	}
	var got []step
	n, err := PruneOldLogs(context.Background(), store, 30, New("test"), func(kind string, total int) {
		got = append(got, step{kind, total})
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := []step{
		{"operation logs", 5}, {"operation logs", 10}, {"operation logs", 12},
		{"operation changes", 16},
		{"system activity logs", 19}, {"system activity logs", 20},
	}
	if n != 20 || len(got) != len(want) {
		t.Fatalf("n=%d progress=%v, want n=20 progress=%v", n, got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("progress[%d] = %v, want %v", i, got[i], want[i])
		}
	}
}

// A cancelled context returns the rows already committed with an error that
// wraps context.Canceled (so the op records a cancel, not a clean prune), and
// nothing from the prunes after the cancel is counted.
func TestPruneOldLogs_CancelKeepsPartialCountAndReportsTheCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	store := &chunkedFake{logs: []int{5, 5}, changes: []int{9}, activity: []int{9}, cancelAfterLogs: cancel}

	n, err := PruneOldLogs(ctx, store, 30, New("test"), nil)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want it to wrap context.Canceled", err)
	}
	if n != 10 {
		t.Errorf("n = %d, want the 10 rows the committed chunks deleted and none from after the cancel", n)
	}
}
