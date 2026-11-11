// file: core/internal/restoreswap/hooks.go
// version: 1.0.0
// guid: 3a9e6d52-1f84-4c07-b2d3-8c5e7a1f0b94
// last-edited: 2026-11-11

package restoreswap

import (
	"context"
	"fmt"
	"strconv"

	"github.com/falkcorp/mediacore/storage/restorejournal"
)

// The journal steps of a restore boot that runs P4d steps 2 and 3 (05-storage
// §5S.3.16 machine 1): opened, restore_line, hooks_done, marker_written,
// applied. A build without those steps (R1) takes the one edge opened -->
// marker_written (RestoreApplied); a journaling build takes the long way, and
// these methods are its edges. Each is idempotent (a crash loop re-runs the
// boot, which calls it again) and refuses a journal that is not at the step
// before it, so no boot can skip the hooks.
const (
	StepRestoreLine = "restore_line" // P4d step 2: the restore line is in the revocation journal
	StepHooksDone   = "hooks_done"   // P4d step 3: every hook ran, their last batch recorded c/backup/restore_hooked/<seq>
)

// afterOpened are the steps a restore boot's journal may read once the boot
// opened it: every later step of the long way. Opened is a no-op at each, so a
// boot that crashed after restore_line and re-ran P2 does not append a second
// "opened" after it.
func afterOpened(step string) bool {
	switch step {
	case StepOpened, StepRestoreLine, StepHooksDone:
		return true
	}
	return false
}

// RestoreLine journals that P4d step 2 appended the restore line (seq) for
// restore id: opened --> restore_line. The record's Note is the line's seq, for
// the reader of the journal; the line itself is found in the revocation
// journal by the restore's ID (it is the kernel's, not this package's).
func (s *Swapper) RestoreLine(ctx context.Context, id string, seq uint64) error {
	last, err := s.last(id)
	if err != nil {
		return err
	}
	switch last {
	case StepRestoreLine, StepHooksDone, StepMarkerWritten, StepApplied:
		return nil
	case StepOpened:
		return s.appendRec(ctx, id, restorejournal.Record{Step: StepRestoreLine, Note: strconv.FormatUint(seq, 10)})
	}
	return fmt.Errorf("restoreswap: restore journal %s is at %q, not opened: no restore line may be journaled", id, last)
}

// HooksDone journals that P4d step 3's hooks all ran and committed:
// restore_line --> hooks_done.
func (s *Swapper) HooksDone(ctx context.Context, id string) error {
	last, err := s.last(id)
	if err != nil {
		return err
	}
	switch last {
	case StepHooksDone, StepMarkerWritten, StepApplied:
		return nil
	case StepRestoreLine:
		return s.appendRec(ctx, id, restorejournal.Record{Step: StepHooksDone})
	}
	return fmt.Errorf("restoreswap: restore journal %s is at %q, not restore_line: the hooks cannot be journaled done", id, last)
}

// MarkerWritten journals that c/backup/served_boot and backup.location.json
// were written for the restored store after the hooks: hooks_done -->
// marker_written. Unlike RestoreApplied's record it carries no HooksOwed: the
// hooks ran.
func (s *Swapper) MarkerWritten(ctx context.Context, id string) error {
	last, err := s.last(id)
	if err != nil {
		return err
	}
	switch last {
	case StepMarkerWritten, StepApplied:
		return nil
	case StepHooksDone:
		return s.appendRec(ctx, id, restorejournal.Record{Step: StepMarkerWritten})
	}
	return fmt.Errorf("restoreswap: restore journal %s is at %q, not hooks_done: the marker cannot be journaled written", id, last)
}

// HooksApplied completes a restore boot that ran its hooks: marker_written -->
// applied, then the in-flight file is deleted (05-backup step h). A journal that
// already reads applied only has its in-flight file removed again.
func (s *Swapper) HooksApplied(ctx context.Context, id string) error {
	last, err := s.last(id)
	if err != nil {
		return err
	}
	switch last {
	case StepMarkerWritten:
		if err := s.appendRec(ctx, id, restorejournal.Record{Step: StepApplied}); err != nil {
			return err
		}
	case StepApplied:
	default:
		return fmt.Errorf("restoreswap: restore journal %s is at %q, not marker_written: it cannot be applied", id, last)
	}
	return s.removeInflight(ctx, id)
}
