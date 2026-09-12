// file: internal/organizer/backup_standdown_test.go
// version: 1.0.0
// guid: 0d6b2f8a-7e41-4c93-b5a0-3f9e8c1d2b74
// last-edited: 2026-09-12

package organizer

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/backup"
	"github.com/falkcorp/audiobook-organizer/internal/logger"
)

// TestBackupProgressReporter_StandDownCheckpoint pins the auto-backup's scan
// stand-down checkpoint. autoBackup runs inside library.scan; once the scan's
// ctx is canceled (a stand-down quiesce) the very next per-file callback must
// return an error so the archive walk stops between files and the scan parks.
// It must fire even inside the throttle window, which suppresses progress
// stamps but must never suppress the checkpoint.
func TestBackupProgressReporter_StandDownCheckpoint(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	report := backupProgressReporter(ctx, logger.New("test"), time.Hour)

	if err := report(backup.PhaseArchive, 1, 1024); err != nil {
		t.Fatalf("live ctx: want nil, got %v", err)
	}
	cancel()
	for _, phase := range []string{backup.PhaseArchive, backup.PhaseChecksum} {
		err := report(phase, 2, 2048)
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("%s after cancel: want context.Canceled, got %v", phase, err)
		}
	}
}
