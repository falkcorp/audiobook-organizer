// file: internal/audiobooks/purge_replay_boot.go
// version: 1.0.0
// guid: 8d4a6f13-2b95-4e07-a1c8-3f6e9b0d5c72
// last-edited: 2026-10-26

package audiobooks

import (
	"context"
	"fmt"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/config"
)

// ReplayPendingFileRemovalsOnBoot carries out the file removals an earlier
// purge pass journaled and never finished (purge_pending_journal.go), once, when
// the server starts, so records a crash or a deploy left do not sit on disk with
// nothing to remove them until somebody asks for a purge with delete_files: the
// next library scan would import those files back as new books.
//
// It is the same replay a pass runs first (replayPendingFileRemovals), with the
// same checks on every file, and it is safe to run at boot for the same reason:
// records are written only by a purge that was deleting files, and a purge op
// that was running when the server stopped is resumed at boot and replays too
// (ResumeRestart). The replay lock (replayMu) serializes the two removal runs.
//
// It does nothing while the library root is not ready (libraryRootReady): a
// library mounted late, as a remote filesystem can be, is retried every `every`,
// at most `attempts` times, and then given up on with an error and every record
// kept; the next purge with delete_files replays them.
//
// It reports how many things it removed and the error lines of the replay. The
// error is non-nil when the records could not be read or the root never became
// ready; the records are kept then.
func (svc *AudiobookService) ReplayPendingFileRemovalsOnBoot(ctx context.Context, every time.Duration, attempts int) (removed int, errs []string, err error) {
	if svc.store == nil {
		return 0, nil, fmt.Errorf("database not initialized")
	}
	attempts = max(attempts, 1)
	var lastRootErr error
	for attempt := 1; attempt <= attempts; attempt++ {
		// Cheap and read-only: nothing pending is the usual case, and then the
		// mount is nobody's business.
		counts, cerr := svc.countPendingFileRemovals()
		if cerr != nil {
			return 0, nil, fmt.Errorf("could not read the journal of pending file removals: %w", cerr)
		}
		// Records waiting for a folder are looked at too: the folder may be back.
		pending := counts.Total()
		if pending == 0 {
			return 0, nil, nil
		}
		lastRootErr = svc.libraryRootReady(config.Snapshot().RootDir)
		if lastRootErr == nil {
			removed, errs = svc.replayPendingFileRemovals(ctx)
			return removed, errs, nil
		}
		singleLog.Warn("purge journal replay at boot: %d file removal(s) pending, but the library root is not ready (attempt %d of %d): %v", pending, attempt, attempts, lastRootErr)
		if attempt == attempts {
			break
		}
		select {
		case <-ctx.Done():
			return 0, nil, fmt.Errorf("purge journal replay at boot stopped before the library root was ready: %w", ctx.Err())
		case <-time.After(every):
		}
	}
	return 0, nil, fmt.Errorf("purge journal replay at boot gave up after %d attempt(s), every record kept for the next purge with delete_files: %w", attempts, lastRootErr)
}
