// file: internal/organizer/backup_progress_test.go
// version: 1.0.0
// guid: 2f80a3d5-71c6-4e0b-9a48-c5d2b6e91473
// last-edited: 2026-08-11

package organizer

import (
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/backup"
	"github.com/falkcorp/audiobook-organizer/internal/logger"
)

// ---------------------------------------------------------------------------
// The pre-organize auto-backup takes 20-25 minutes on production. The ops
// registry watchdog CANCELS an operation that goes ProgressTimeout (default 5
// minutes, internal/operations/registry/watchdog.go) without an UpdateProgress
// stamp — which is precisely what killed the 2026-08-11 06:31 run:
//
//	06:36:36 registry: strike recorded ... kind=stuck  no progress for 5m8s
//
// The obvious fix — a goroutine stamping on a timer — would ALSO satisfy the
// watchdog for a backup that had genuinely wedged, turning a hang detector
// into a hang concealer. backupProgressReporter is therefore driven only by
// completed work. TestBackupProgressReporter_IsNotATicker is the test that
// keeps it that way; if someone later "simplifies" this into a ticker, that
// one fails and the others do not.
// ---------------------------------------------------------------------------

// recordingLogger captures UpdateProgress calls. Safe for concurrent use so a
// future caller that reports from a worker pool does not race the assertions.
type recordingLogger struct {
	mu       sync.Mutex
	progress []string
}

func (l *recordingLogger) Trace(string, ...any) {}
func (l *recordingLogger) Debug(string, ...any) {}
func (l *recordingLogger) Info(string, ...any)  {}
func (l *recordingLogger) Warn(string, ...any)  {}
func (l *recordingLogger) Error(string, ...any) {}
func (l *recordingLogger) UpdateProgress(_, _ int, message string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.progress = append(l.progress, message)
}
func (l *recordingLogger) RecordChange(logger.Change)     {}
func (l *recordingLogger) ChangeCounters() map[string]int { return nil }
func (l *recordingLogger) IsCanceled() bool               { return false }
func (l *recordingLogger) With(string) logger.Logger      { return l }

func (l *recordingLogger) messages() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]string(nil), l.progress...)
}

// TestBackupProgressReporter_IsNotATicker is the anti-regression guard on the
// whole design. A reporter that stamps on elapsed time would emit here; one
// driven by work cannot, because no work was reported.
func TestBackupProgressReporter_IsNotATicker(t *testing.T) {
	log := &recordingLogger{}
	// Interval far shorter than the wait, so a timer-based implementation
	// would have fired many times over by the time we assert.
	_ = backupProgressReporter(log, 1*time.Millisecond)

	time.Sleep(50 * time.Millisecond)

	if got := log.messages(); len(got) != 0 {
		t.Fatalf("reporter stamped %d progress update(s) without any work being reported: %v\n"+
			"backupProgressReporter must be driven by completed work, not by elapsed time — "+
			"a timer would satisfy the registry watchdog for a wedged backup too", len(got), got)
	}
}

// A single call must get through immediately: the first stamp after entering
// the backup phase is the one that resets the watchdog's clock.
func TestBackupProgressReporter_ForwardsFirstReportImmediately(t *testing.T) {
	log := &recordingLogger{}
	report := backupProgressReporter(log, 1*time.Hour)

	report(backup.PhaseArchive, 1, 2048)

	msgs := log.messages()
	if len(msgs) != 1 {
		t.Fatalf("expected exactly 1 progress update, got %d: %v", len(msgs), msgs)
	}
	if !strings.Contains(msgs[0], "archived 1 files") {
		t.Errorf("progress message %q should name what was archived", msgs[0])
	}
	if !strings.Contains(msgs[0], "2.0 KiB") {
		t.Errorf("progress message %q should carry the byte count", msgs[0])
	}
}

// The archive walk fires once per file — thousands of times on a 14 GB Pebble
// database — and UpdateProgress writes through to the DB. Throttling keeps the
// reporting from becoming a measurable share of the backup's own cost.
func TestBackupProgressReporter_ThrottlesBurstsWithinInterval(t *testing.T) {
	log := &recordingLogger{}
	report := backupProgressReporter(log, 1*time.Hour)

	for i := 1; i <= 500; i++ {
		report(backup.PhaseArchive, i, int64(i)*1024)
	}

	if got := len(log.messages()); got != 1 {
		t.Fatalf("500 reports inside one interval produced %d progress updates, want 1", got)
	}
}

// ...but the throttle must not swallow everything: once the interval elapses,
// the next completed unit of work gets through. A throttle that never reopened
// would reintroduce the silence this whole change exists to remove.
func TestBackupProgressReporter_ForwardsAgainAfterInterval(t *testing.T) {
	log := &recordingLogger{}
	report := backupProgressReporter(log, 10*time.Millisecond)

	report(backup.PhaseArchive, 1, 1024)
	report(backup.PhaseArchive, 2, 2048) // suppressed
	time.Sleep(20 * time.Millisecond)
	report(backup.PhaseArchive, 3, 3072)

	msgs := log.messages()
	if len(msgs) != 2 {
		t.Fatalf("expected 2 progress updates (one per interval), got %d: %v", len(msgs), msgs)
	}
	if !strings.Contains(msgs[1], "archived 3 files") {
		t.Errorf("second update %q should carry the latest counters", msgs[1])
	}
}

func TestBackupProgressReporter_NamesEachPhase(t *testing.T) {
	cases := []struct {
		phase string
		want  string
	}{
		{backup.PhaseCheckpoint, "snapshotting database"},
		{backup.PhaseArchive, "archived"},
		{backup.PhaseChecksum, "verifying archive"},
	}
	for _, tc := range cases {
		t.Run(tc.phase, func(t *testing.T) {
			log := &recordingLogger{}
			backupProgressReporter(log, 1*time.Hour)(tc.phase, 1, 1024)
			msgs := log.messages()
			if len(msgs) != 1 {
				t.Fatalf("expected 1 update, got %v", msgs)
			}
			if !strings.Contains(msgs[0], tc.want) {
				t.Errorf("phase %q produced %q, expected it to mention %q — an operator "+
					"needs to know WHICH phase went quiet, not just that something did",
					tc.phase, msgs[0], tc.want)
			}
		})
	}
}

func TestHumanBytes(t *testing.T) {
	cases := []struct {
		in   int64
		want string
	}{
		{0, "0 B"},
		{512, "512 B"},
		{2048, "2.0 KiB"},
		{5 << 20, "5.0 MiB"},
		{14 << 30, "14.0 GiB"}, // the production database
	}
	for _, tc := range cases {
		if got := humanBytes(tc.in); got != tc.want {
			t.Errorf("humanBytes(%d) = %q, want %q", tc.in, got, tc.want)
		}
	}
}
