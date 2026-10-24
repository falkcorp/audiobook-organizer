// file: internal/server/purge_soft_deleted_op.go
// version: 1.2.0
// guid: 2e7a9c41-5d08-4b63-8f1a-6c3d0b9e4a72
// last-edited: 2026-10-23

package server

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/activity"
	"github.com/falkcorp/audiobook-organizer/internal/audiobooks"
	"github.com/falkcorp/audiobook-organizer/internal/auth"
	"github.com/falkcorp/audiobook-organizer/internal/logger"
	"github.com/falkcorp/audiobook-organizer/internal/operations/opmode"
	opsregistry "github.com/falkcorp/audiobook-organizer/internal/operations/registry"
)

// purgeSoftDeletedOpID is the manual "Purge soft-deleted" operation behind
// DELETE /audiobooks/purge-soft-deleted and its preview.
//
// It is not maintenance.purge-deleted: that is the nightly job, which returns
// at once when purge_soft_deleted_after_days is 0 (prod's setting) and takes no
// parameters. The manual purge used to run synchronously inside the HTTP
// request: on 2026-10-21 it ran 72 s with no progress, logged nothing, and its
// result breakdown (what it kept and why) never reached the screen.
const purgeSoftDeletedOpID = "library.purge-soft-deleted"

// PurgeSoftDeletedOpParams are the op's params. dry_run defaults to TRUE (owner
// rule 2026-09-25): an enqueue that does not say otherwise is a preview, which
// classifies every trashed book exactly as a real pass would and changes
// nothing. The HTTP purge sends dry_run=false explicitly.
type PurgeSoftDeletedOpParams struct {
	DeleteFiles   bool  `json:"delete_files,omitempty"`
	OlderThanDays *int  `json:"older_than_days,omitempty"`
	DryRun        *bool `json:"dry_run,omitempty"`
	DryRunCamel   *bool `json:"dryRun,omitempty"`
}

// purgeResultCap bounds the id lists and error lines kept in the stored op
// result; the counts in it are exact.
const purgeResultCap = 200

var purgeOpLog = logger.New("purge-soft-deleted")

func init() {
	addOpRegistrar(func(s *Server, reg *opsregistry.Registry) error {
		return s.RegisterPurgeSoftDeletedOp(reg)
	})
}

// RegisterPurgeSoftDeletedOp registers library.purge-soft-deleted.
func (s *Server) RegisterPurgeSoftDeletedOp(reg *opsregistry.Registry) error {
	return reg.RegisterOp(opsregistry.OperationDef{
		ID:              purgeSoftDeletedOpID,
		Preview:         opsregistry.PreviewSpec{Params: PurgeSoftDeletedOpParams{}},
		Liveness:        opsregistry.LivenessManual,
		Plugin:          "library",
		DisplayName:     "Purge soft-deleted books",
		Description:     "Permanently removes soft-deleted books from the trash. A book is purged only when it owns no book_file rows, is not the retired shell of a combine that can still be undone, and has no listening state it cannot carry to a live version. Dry run by default: reports how many would be purged and how many are kept, by reason, and changes nothing. The result breaks the trash down by reason.",
		DefaultPriority: opsregistry.PriorityNormal,
		Cancellable:     true,
		Timeout:         2 * time.Hour,
		// Nothing to resume from: the next run reclassifies the trash.
		ResumePolicy:   opsregistry.ResumeRestart,
		ConcurrencyKey: purgeSoftDeletedOpID,
		Writes:         []opsregistry.Resource{opsregistry.Resource("books")},
		Permissions:    []auth.Permission{auth.PermLibraryDelete},
		Capabilities:   []opsregistry.Capability{opsregistry.CapLibraryRead, opsregistry.CapLibraryWrite, opsregistry.CapFilesWrite},
		Run:            s.runPurgeSoftDeletedOp,
	})
}

func (s *Server) runPurgeSoftDeletedOp(ctx context.Context, raw json.RawMessage, reporter opsregistry.Reporter) error {
	var p PurgeSoftDeletedOpParams
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &p); err != nil {
			return fmt.Errorf("%s: decode params: %w", purgeSoftDeletedOpID, err)
		}
	}
	dryRun, err := opmode.ResolveDryRun(purgeSoftDeletedOpID, p.DryRun, p.DryRunCamel)
	if err != nil {
		return err
	}
	if s.audiobookService == nil {
		return fmt.Errorf("%s: audiobook service not initialized", purgeSoftDeletedOpID)
	}
	opID := opsregistry.ReporterOpID(reporter)

	verb := "Purging"
	if dryRun {
		verb = "Previewing the purge of"
	}
	_ = reporter.UpdateProgress(0, 0, verb+" soft-deleted books: listing the trash")
	// The callback runs inside the purge's worker goroutines; the reporter is
	// safe for that and the callback keeps no state of its own.
	res, err := s.audiobookService.RunPurge(ctx, audiobooks.PurgeOptions{
		DeleteFiles:   p.DeleteFiles,
		OlderThanDays: p.OlderThanDays,
		DryRun:        dryRun,
		Progress: func(done, total int) {
			_ = reporter.UpdateProgress(done, total, fmt.Sprintf("%s soft-deleted books: %d of %d decided", verb, done, total))
		},
	})
	if err != nil {
		// A pass that was refused before it began still returns its result
		// (StoppedEarly, StopReason). Store it so GET /operations/:id/result
		// says why; the op fails with the error either way.
		if res != nil {
			if serr := opsregistry.ReporterSetResult(reporter, res.Capped(purgeResultCap)); serr != nil {
				purgeOpLog.Warn("%s opID=%s: result of the refused pass not stored: %s", purgeSoftDeletedOpID, logger.SanitizeLogValue(opID), logger.SanitizeLogValue(serr.Error()))
			}
		}
		return fmt.Errorf("%s: %w", purgeSoftDeletedOpID, err)
	}

	// (c) one summary line, in the server log, the op's own log and the
	// activity log, with every count the pass kept.
	msg := res.Summary()
	purgeOpLog.Info("%s opID=%s deleteFiles=%t", msg, logger.SanitizeLogValue(opID), p.DeleteFiles)
	_ = reporter.Log(slog.LevelInfo, msg)
	if s.activityWriter != nil && opID != "" {
		activity.EmitInfo(s.activityWriter, opID, purgeSoftDeletedOpID, "purge-deleted", msg,
			activity.AlwaysShow)
		for _, e := range res.Capped(purgeResultCap).Errors {
			activity.LogBatch(s.activityWriter, opID, purgeSoftDeletedOpID, "purge-deleted",
				activity.BatchItem{Name: e, Detail: "error"})
		}
		activity.FlushOperation(s.activityWriter, opID)
	}
	if res.StoppedEarly {
		// RunPurge's last callback reported how many it decided; do not
		// overwrite that with "all of them".
		_ = reporter.UpdateProgress(res.Decided(), res.Attempted, msg)
	} else {
		_ = reporter.UpdateProgress(res.Attempted, res.Attempted, msg)
	}

	// Stored even when the pass stopped early: the counts of the books it did
	// decide, and stopped_early/stop_reason, are what the operator reads.
	if err := opsregistry.ReporterSetResult(reporter, res.Capped(purgeResultCap)); err != nil {
		return fmt.Errorf("%s: store the result: %w", purgeSoftDeletedOpID, err)
	}
	return purgeStoppedError(ctx, purgeSoftDeletedOpID, res)
}

// purgeStoppedError is the error an operation ends with when its purge pass
// stopped before it decided every book, nil for a pass that finished. Not a
// completed run: a cancel or a timeout must not end "completed". The context's
// own error is wrapped so the registry records a cancel as a cancel and a
// timeout as a failure. The manual op and the nightly one share it, so the two
// cannot disagree about what a stopped pass is.
func purgeStoppedError(ctx context.Context, opID string, res *audiobooks.PurgeResult) error {
	if res == nil || !res.StoppedEarly {
		return nil
	}
	if cerr := ctx.Err(); cerr != nil {
		return fmt.Errorf("%s: %s: %w", opID, res.StopReason, cerr)
	}
	return fmt.Errorf("%s: %s", opID, res.StopReason)
}
