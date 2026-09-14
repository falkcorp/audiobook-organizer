// file: internal/metafetch/rename_path_retry.go
// version: 1.0.0
// guid: 8c2d6a41-0f93-4e7b-a5c8-1b9e3d7f6204
// last-edited: 2026-09-14

package metafetch

import (
	"context"
	"fmt"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/logger"
	"github.com/falkcorp/audiobook-organizer/internal/organizer"
)

// renamePathWriteAttempts and renamePathWriteBaseDelay bound the retry of a
// path write that follows a disk move: 4 attempts, waiting 100ms, 200ms and
// 400ms between them (0.7s worst case). Variables so tests can shorten them.
var (
	renamePathWriteAttempts  = 4
	renamePathWriteBaseDelay = 100 * time.Millisecond
)

// retryPathWrite runs write up to renamePathWriteAttempts times with
// exponential backoff, returning nil on the first success. It stops waiting
// as soon as ctx is done and returns the last write error joined with the
// context error.
func retryPathWrite(ctx context.Context, write func() error) error {
	var err error
	delay := renamePathWriteBaseDelay
	for attempt := 1; ; attempt++ {
		if err = write(); err == nil {
			return nil
		}
		if attempt >= renamePathWriteAttempts {
			return err
		}
		t := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			t.Stop()
			return fmt.Errorf("%w (retry stopped: %w)", err, ctx.Err())
		case <-t.C:
		}
		delay *= 2
	}
}

// writeMovedPath is the path write that follows a file's disk move. It
// retries write; if every attempt fails it durably records the {old, new}
// pair for maintenance.repoint-unrecorded-renames and returns the write
// error. The record is for repair: the rename still fails.
func (mfs *Service) writeMovedPath(ctx context.Context, rec organizer.RenamePathWriteFailure, write func() error) error {
	err := retryPathWrite(ctx, write)
	if err == nil {
		return nil
	}
	rec.Error = err.Error()
	if recErr := organizer.RecordRenamePathWriteFailure(mfs.db, rec); recErr != nil {
		renameSyncLog.Error("book %s file %s moved on disk from %s to %s; the path write failed AND the repair record could not be saved: %v",
			logger.SanitizeLogValue(rec.BookID), logger.SanitizeLogValue(rec.BookFileID),
			logger.SanitizeLogValue(rec.OldPath), logger.SanitizeLogValue(rec.NewPath),
			logger.SanitizeLogValue(recErr.Error()))
	} else {
		renameSyncLog.Error("book %s file %s moved on disk from %s to %s but the path write failed; recorded for maintenance.repoint-unrecorded-renames: %v",
			logger.SanitizeLogValue(rec.BookID), logger.SanitizeLogValue(rec.BookFileID),
			logger.SanitizeLogValue(rec.OldPath), logger.SanitizeLogValue(rec.NewPath),
			logger.SanitizeLogValue(err.Error()))
	}
	return err
}
