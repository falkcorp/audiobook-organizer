// file: internal/scheduler/db_optimize_progress_test.go
// version: 1.0.1
// guid: 9d4c2b7e-1f6a-4e83-b0c5-6a8e3d2f7b14
// last-edited: 2026-10-02

package scheduler

import (
	"context"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	opsregistry "github.com/falkcorp/audiobook-organizer/internal/operations/registry"
)

// dbOptStore embeds ExtraOpsStore so only the two methods db-optimize calls
// need bodies; anything else panics on the nil embedded interface.
type dbOptStore struct {
	ExtraOpsStore
	mu      sync.Mutex
	stats   database.CompactionStats
	release chan struct{}
}

func (s *dbOptStore) Optimize(ctx context.Context) error {
	select {
	case <-s.release:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (s *dbOptStore) CompactionStats() database.CompactionStats {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.stats.InProgressBytes += 8 << 20 // the running compaction keeps writing
	s.stats.NumInProgress = 1
	return s.stats
}

type dbOptReporter struct {
	opsregistry.Reporter
	mu     sync.Mutex
	frames []string
	logs   []string
}

func (r *dbOptReporter) UpdateProgress(_, _ int, msg string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.frames = append(r.frames, msg)
	return nil
}

func (r *dbOptReporter) Log(_ slog.Level, msg string, _ ...slog.Attr) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.logs = append(r.logs, msg)
	return nil
}

func (r *dbOptReporter) SetCurrentItem(string) {}

func (r *dbOptReporter) has(list *[]string, sub string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, m := range *list {
		if strings.Contains(m, sub) {
			return true
		}
	}
	return false
}

// The op the owner looks at ("Database Optimize") must say what the main
// compaction is doing while it runs, not just "(0/3)" for half an hour.
func TestRunDBOptimize_ReportsMainCompactionWhileItRuns(t *testing.T) {
	store := &dbOptStore{release: make(chan struct{})}
	rep := &dbOptReporter{}
	r := &ExtraOpsRegistrar{Store: store}

	done := make(chan struct{})
	go func() {
		defer close(done)
		r.runDBOptimize(context.Background(), rep, 5*time.Millisecond)
	}()

	require.Eventually(t, func() bool {
		return rep.has(&rep.frames, "Compacting main database (1/3): ") &&
			rep.has(&rep.logs, "written by compactions (")
	}, 2*time.Second, 5*time.Millisecond)
	close(store.release)
	<-done

	assert.True(t, rep.has(&rep.logs, "Compacting main database (1/3): done in "), "completion line")
	assert.True(t, rep.has(&rep.logs, "live tables"), "before/after sizes on the completion line")
	assert.True(t, rep.has(&rep.logs, "AI scan store not initialized"))
	assert.True(t, rep.has(&rep.frames, "Database optimization complete: 1/3 stores"))
	rep.mu.Lock()
	for _, l := range rep.logs {
		if strings.Contains(l, "elapsed") {
			t.Logf("sample tick: %s", l)
			break
		}
	}
	rep.mu.Unlock()
}
