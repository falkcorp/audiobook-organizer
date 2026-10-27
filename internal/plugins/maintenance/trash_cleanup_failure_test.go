// file: internal/plugins/maintenance/trash_cleanup_failure_test.go
// version: 1.0.0
// guid: 9e2c4a17-5b83-4d60-a1f9-0c7d3e8b5a42
// last-edited: 2026-10-27

package maintenance

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"
)

// trashCleanupDeps answers CleanupTrashedVersions with a fixed outcome.
type trashCleanupDeps struct {
	fakeDeps
	purged, failed int
	err            error
}

func (d trashCleanupDeps) CleanupTrashedVersions() (int, int, error) {
	return d.purged, d.failed, d.err
}

// levelReporter records each log line with its level.
type levelReporter struct {
	fakeReporter
	levels []slog.Level
}

func (r *levelReporter) Log(l slog.Level, msg string, attrs ...slog.Attr) error {
	r.levels = append(r.levels, l)
	return r.fakeReporter.Log(l, msg, attrs...)
}

// A night on which due purges failed must not end "completed": the op returns
// the error, and the log names the purged and failed counts at Warn.
func TestRunTrashCleanup_FailedPurgesFailTheOp(t *testing.T) {
	cause := errors.New("book_file lookup incomplete")
	p := &Plugin{deps: trashCleanupDeps{purged: 2, failed: 3, err: cause}}
	rep := &levelReporter{}

	err := p.runTrashCleanup(context.Background(), nil, rep)
	if !errors.Is(err, cause) {
		t.Fatalf("runTrashCleanup = %v, want the cleanup's error", err)
	}
	if len(rep.logs) != 1 || rep.levels[0] != slog.LevelWarn {
		t.Fatalf("logs=%v levels=%v, want one Warn line", rep.logs, rep.levels)
	}
	for _, want := range []string{"purged 2", "3 failed", "book_file lookup incomplete"} {
		if !strings.Contains(rep.logs[0], want) {
			t.Errorf("log %q does not contain %q", rep.logs[0], want)
		}
	}
}

// A clean run is a success and logs the count at Info (the control: the op does
// succeed when nothing failed, including when nothing was due).
func TestRunTrashCleanup_CleanRunSucceeds(t *testing.T) {
	for _, purged := range []int{0, 4} {
		p := &Plugin{deps: trashCleanupDeps{purged: purged}}
		rep := &levelReporter{}
		if err := p.runTrashCleanup(context.Background(), nil, rep); err != nil {
			t.Fatalf("purged=%d: runTrashCleanup = %v, want nil", purged, err)
		}
		if len(rep.logs) != 1 || rep.levels[0] != slog.LevelInfo {
			t.Fatalf("purged=%d: logs=%v levels=%v, want one Info line", purged, rep.logs, rep.levels)
		}
	}
}
