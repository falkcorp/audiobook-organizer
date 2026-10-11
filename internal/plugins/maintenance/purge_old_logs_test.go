// file: internal/plugins/maintenance/purge_old_logs_test.go
// version: 1.1.0
// guid: 8a4f2c61-3d95-4b07-b6e8-1c9d7e05a3f4
// last-edited: 2026-10-11

package maintenance

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"
)

// purgeProgressReporter records UpdateProgress calls; Log goes to the embedded
// fakeReporter. Read the slices only after the op returns.
type purgeProgressReporter struct {
	fakeReporter
	pmu      sync.Mutex
	progress []int
	messages []string
}

func (r *purgeProgressReporter) UpdateProgress(current, _ int, msg string) error {
	r.pmu.Lock()
	r.progress = append(r.progress, current)
	r.messages = append(r.messages, msg)
	r.pmu.Unlock()
	return nil
}

// purgeDeps stubs LogRetentionDays and PruneOldLogs on top of fakeDeps.
type purgeDeps struct {
	fakeDeps
	days  int
	prune func(ctx context.Context, days int, onProgress func(kind string, scanned, total int)) (int, error)
}

func (d purgeDeps) LogRetentionDays() int { return d.days }
func (d purgeDeps) PruneOldLogs(ctx context.Context, days int, onProgress func(string, int, int)) (int, error) {
	return d.prune(ctx, days, onProgress)
}

func TestPurgeOldLogsOp_IsCancellableAndReportsManually(t *testing.T) {
	def := (&Plugin{}).purgeOldLogsDef()
	if !def.Cancellable {
		t.Error("purge-old-logs must be Cancellable: the prune checks ctx between chunks")
	}
	if def.Liveness.String() != "manual" {
		t.Errorf("Liveness = %s, want manual: the op calls UpdateProgress after every chunk", def.Liveness)
	}
	// The scan heartbeat cannot fire while one iterator step skips a long
	// tombstone run, so the silence budget stays at the old 30 minutes.
	if def.ProgressTimeout < 30*time.Minute {
		t.Errorf("ProgressTimeout = %s, want at least 30m", def.ProgressTimeout)
	}
	if strings.Contains(strings.ToLower(def.Description), "operation changes") ||
		!strings.Contains(def.Description, "Never touches undo history") {
		t.Errorf("Description = %q: it must say the purge never touches undo history", def.Description)
	}
}

func TestPurgeOldLogsOp_ReportsEachChunk(t *testing.T) {
	p := &Plugin{deps: purgeDeps{days: 90, prune: func(_ context.Context, days int, onProgress func(string, int, int)) (int, error) {
		if days != 90 {
			t.Errorf("days = %d, want 90", days)
		}
		onProgress("operation logs", 6000, 5000)
		onProgress("operation logs", 10000, 10000)
		onProgress("system logs", 10000, 12345)
		return 12345, nil
	}}}
	rep := &purgeProgressReporter{}
	if err := p.runPurgeOldLogs(context.Background(), nil, rep); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if fmt.Sprint(rep.progress) != "[0 5000 10000 12345]" {
		t.Errorf("progress = %v, want the initial 0 then the running total per chunk", rep.progress)
	}
}

// A scan heartbeat (rows scanned, nothing more deleted) must still reach
// UpdateProgress, which is what stamps the watchdog's liveness clock: a weekly
// run that scans months of unexpired rows deletes nothing for a long stretch.
func TestPurgeOldLogsOp_ScanHeartbeatReachesUpdateProgress(t *testing.T) {
	p := &Plugin{deps: purgeDeps{days: 90, prune: func(_ context.Context, _ int, onProgress func(string, int, int)) (int, error) {
		onProgress("system logs", 10000, 0)
		onProgress("system logs", 20000, 0)
		onProgress("system logs", 30000, 0)
		return 0, nil
	}}}
	rep := &purgeProgressReporter{}
	if err := p.runPurgeOldLogs(context.Background(), nil, rep); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(rep.messages) != 4 {
		t.Fatalf("UpdateProgress calls = %d (%q), want the initial one plus one per heartbeat", len(rep.messages), rep.messages)
	}
	if !strings.Contains(rep.messages[3], "30000 rows checked") {
		t.Errorf("last progress message = %q, want it to name the rows scanned", rep.messages[3])
	}
}

// A cancel mid-purge must surface as an error (the run is recorded as
// canceled, not as a clean prune) and the log must say how much was kept.
func TestPurgeOldLogsOp_CancelReturnsTheErrorAndLogsPartialCount(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	p := &Plugin{deps: purgeDeps{days: 90, prune: func(ctx context.Context, _ int, onProgress func(string, int, int)) (int, error) {
		onProgress("operation logs", 5000, 5000)
		cancel()
		return 5000, fmt.Errorf("prune operation logs: %w", ctx.Err())
	}}}
	rep := &purgeProgressReporter{}
	err := p.runPurgeOldLogs(ctx, nil, rep)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want it to wrap context.Canceled", err)
	}
	if len(rep.logs) != 1 || rep.logs[0] != fmt.Sprintf("Log purge stopped after pruning 5000 records: %v", err) {
		t.Errorf("logs = %q, want one 'stopped after pruning 5000 records' line and no success line", rep.logs)
	}
}

func TestPurgeOldLogsOp_KeepForeverSkipsThePrune(t *testing.T) {
	p := &Plugin{deps: purgeDeps{days: -1, prune: func(context.Context, int, func(string, int, int)) (int, error) {
		t.Error("PruneOldLogs must not run when log_retention_days is -1")
		return 0, nil
	}}}
	if err := p.runPurgeOldLogs(context.Background(), nil, &purgeProgressReporter{}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}
