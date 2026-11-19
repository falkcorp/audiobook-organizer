// file: internal/server/purge_soft_deleted_op.go
// version: 1.2.0
// guid: 2e7a9c41-5d08-4b63-8f1a-6c3d0b9e4a72
// last-edited: 2026-11-19

package server

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"github.com/falkcorp/mediacore/tasks"

	"github.com/falkcorp/audiobook-organizer/internal/activity"
	"github.com/falkcorp/audiobook-organizer/internal/audiobooks"
	"github.com/falkcorp/audiobook-organizer/internal/auth"
	"github.com/falkcorp/audiobook-organizer/internal/logger"
	"github.com/falkcorp/audiobook-organizer/internal/operations/opmode"
	opsregistry "github.com/falkcorp/audiobook-organizer/internal/operations/registry"
	"github.com/falkcorp/audiobook-organizer/pkg/ops"
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
	// TrashedBefore (RFC 3339) is the instant the preview returned. A real
	// pass needs it and takes no book trashed after it; without it the pass is
	// refused (audiobooks.ErrPurgePreviewRequired). Ignored by a preview.
	TrashedBefore *time.Time `json:"trashed_before,omitempty"`
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

// purgeSoftDeletedNativeParams are the native kind's params: HEAD's without the
// two dry-run spellings, which the run's mode carries.
type purgeSoftDeletedNativeParams struct {
	DeleteFiles   bool `json:"delete_files,omitempty"`
	OlderThanDays *int `json:"older_than_days,omitempty"`
	// TrashedBefore (RFC 3339) is the instant the preview returned. A live pass
	// needs it and takes no book trashed after it.
	TrashedBefore *time.Time `json:"trashed_before,omitempty"`
}

// purgeSoftDeletedStore is the read the preview makes: the purge's classification
// pass with DryRun set, which changes nothing.
type purgeSoftDeletedStore interface {
	RunPurgePreview(ctx context.Context, opts audiobooks.PurgeOptions) (*audiobooks.PurgeResult, error)
}

// purgeSoftDeletedWriter is the live purge: it deletes the trashed books that
// own nothing, and their files when DeleteFiles is set.
type purgeSoftDeletedWriter interface {
	RunPurge(ctx context.Context, opts audiobooks.PurgeOptions) (*audiobooks.PurgeResult, error)
	t7ActivityWriter
}

// purgeSoftDeletedEnv is what the shared core takes from its shell (rules R2,
// R10).
type purgeSoftDeletedEnv struct {
	t7Env
	Store  purgeSoftDeletedStore
	Writer purgeSoftDeletedWriter
}

// purgeSoftDeletedCore is the body of the op, shared by HEAD's shell and the
// native kind. It returns the capped result (non-nil whenever the pass reported
// one, including a refused or stopped pass) and also hands it to env.SetResult
// where the shell has one, at the point HEAD stored it.
func purgeSoftDeletedCore(ctx context.Context, p purgeSoftDeletedNativeParams, env purgeSoftDeletedEnv) (*audiobooks.PurgeResult, error) {
	dryRun := !env.Live
	if (dryRun && env.Store == nil) || (!dryRun && env.Writer == nil) {
		return nil, fmt.Errorf("%s: audiobook service not initialized", purgeSoftDeletedOpID)
	}
	opID := env.OpID

	verb := "Purging"
	if dryRun {
		verb = "Previewing the purge of"
	}
	env.progress(0, 0, verb+" soft-deleted books: listing the trash")
	// The callback runs inside the purge's worker goroutines; the progress hook
	// is safe for that and the callback keeps no state of its own.
	opts := audiobooks.PurgeOptions{
		DeleteFiles:   p.DeleteFiles,
		OlderThanDays: p.OlderThanDays,
		TrashedBefore: p.TrashedBefore,
		DryRun:        dryRun,
		Progress: func(done, total int) {
			env.progress(done, total, fmt.Sprintf("%s soft-deleted books: %d of %d decided", verb, done, total))
		},
	}
	var (
		res *audiobooks.PurgeResult
		err error
	)
	if dryRun {
		res, err = env.Store.RunPurgePreview(ctx, opts)
	} else {
		res, err = env.Writer.RunPurge(ctx, opts)
	}
	if err != nil {
		// A pass that was refused before it began still returns its result
		// (StoppedEarly, StopReason). Store it so GET /operations/:id/result
		// says why; the op fails with the error either way.
		var capped *audiobooks.PurgeResult
		if res != nil {
			capped = res.Capped(purgeResultCap)
			if env.SetResult != nil {
				if serr := env.SetResult(capped); serr != nil {
					purgeOpLog.Warn("%s opID=%s: result of the refused pass not stored: %s", purgeSoftDeletedOpID, logger.SanitizeLogValue(opID), logger.SanitizeLogValue(serr.Error()))
				}
			}
		}
		return capped, fmt.Errorf("%s: %w", purgeSoftDeletedOpID, err)
	}

	// (c) one summary line, in the server log, the op's own log and the
	// activity log, with every count the pass kept.
	msg := res.Summary()
	purgeOpLog.Info("%s opID=%s deleteFiles=%t", msg, logger.SanitizeLogValue(opID), p.DeleteFiles)
	env.Log(slog.LevelInfo, msg)
	// HEAD wrote the row on a dry run too (its shell sets Writer in both modes),
	// so the gate is the writer, not the run mode: the native Preview builds no
	// writer and so writes nothing.
	if env.Writer != nil && opID != "" {
		env.Writer.EmitActivityInfo(opID, purgeSoftDeletedOpID, "purge-deleted", msg, activity.AlwaysShow)
		for _, e := range res.Capped(purgeResultCap).Errors {
			env.Writer.LogActivityBatch(opID, purgeSoftDeletedOpID, "purge-deleted",
				activity.BatchItem{Name: e, Detail: "error"})
		}
		env.Writer.FlushActivityOp(opID)
	}
	if res.StoppedEarly {
		// RunPurge's last callback reported how many it decided; do not
		// overwrite that with "all of them".
		env.progress(res.Decided(), res.Attempted, msg)
	} else {
		env.progress(res.Attempted, res.Attempted, msg)
	}

	// Stored even when the pass stopped early: the counts of the books it did
	// decide, and stopped_early/stop_reason, are what the operator reads.
	capped := res.Capped(purgeResultCap)
	if env.SetResult != nil {
		if err := env.SetResult(capped); err != nil {
			return capped, fmt.Errorf("%s: store the result: %w", purgeSoftDeletedOpID, err)
		}
	}
	return capped, purgeStoppedError(ctx, purgeSoftDeletedOpID, res)
}

// headPurgeSoftDeleted is HEAD's side of both interfaces: the audiobook
// service's RunPurge, and the server's activity writer.
type headPurgeSoftDeleted struct {
	headT7Activity
	svc *audiobooks.AudiobookService
}

func (h headPurgeSoftDeleted) RunPurgePreview(ctx context.Context, opts audiobooks.PurgeOptions) (*audiobooks.PurgeResult, error) {
	return h.svc.RunPurge(ctx, opts)
}

func (h headPurgeSoftDeleted) RunPurge(ctx context.Context, opts audiobooks.PurgeOptions) (*audiobooks.PurgeResult, error) {
	return h.svc.RunPurge(ctx, opts)
}

// purgeSoftDeletedDef is HEAD's definition. Its Run is the v2 shell.
func (s *Server) purgeSoftDeletedDef() opsregistry.OperationDef {
	return opsregistry.OperationDef{
		ID:              purgeSoftDeletedOpID,
		Preview:         opsregistry.PreviewSpec{Params: PurgeSoftDeletedOpParams{}},
		Liveness:        opsregistry.LivenessManual,
		Plugin:          "library",
		DisplayName:     "Purge soft-deleted books",
		Description:     "Permanently removes soft-deleted books from the trash. A book is purged only when it owns no book_file rows, is not the retired shell of a combine that can still be undone, and has no listening state it cannot carry to a live version. Dry run by default: reports how many would be purged and how many are kept, by reason, and changes nothing. A real run must be given the trashed_before the preview returned and takes no book trashed after it. The result breaks the trash down by reason.",
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
	}
}

// RegisterPurgeSoftDeletedOp registers library.purge-soft-deleted.
func (s *Server) RegisterPurgeSoftDeletedOp(reg *opsregistry.Registry) error {
	return reg.RegisterOp(s.purgeSoftDeletedDef())
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
	env := purgeSoftDeletedEnv{t7Env: t7HeadEnv(reporter, !dryRun)}
	if s.audiobookService != nil {
		h := headPurgeSoftDeleted{headT7Activity: headT7Activity{w: s.activityWriter}, svc: s.audiobookService}
		env.Store, env.Writer = h, h
	}
	_, err = purgeSoftDeletedCore(ctx, purgeSoftDeletedNativeParams{
		DeleteFiles: p.DeleteFiles, OlderThanDays: p.OlderThanDays, TrashedBefore: p.TrashedBefore,
	}, env)
	return err
}

// purgeSoftDeletedNative is the native books.library.purge-soft-deleted kind.
// Preview classifies the trash through the read side; Live deletes through the
// writer. It returns the capped result HEAD stored with ReporterSetResult.
func (s *Server) purgeSoftDeletedNative(deps t7Deps, sched func(tasks.Kind) *tasks.Schedule) tasks.Definition {
	kind := t7KindFor(purgeSoftDeletedOpID)
	c := t7Common(s.purgeSoftDeletedDef(), sched)
	c.Effects = t7Writes(ops.ResBooks)
	return tasks.Task(kind, tasks.TaskSpec[json.RawMessage, *audiobooks.PurgeResult]{
		Common: c,
		Run: func(rc *tasks.RC, raw json.RawMessage) (*audiobooks.PurgeResult, error) {
			p, err := t7DecodeNative[purgeSoftDeletedNativeParams](kind, raw)
			if err != nil {
				return nil, err
			}
			if err := t7NeedStore(kind, deps); err != nil {
				return nil, err
			}
			env := purgeSoftDeletedEnv{t7Env: t7NativeEnv(rc), Store: deps.Store}
			if env.Live {
				w, err := t7NewWriter(kind, deps, rc)
				if err != nil {
					return nil, err
				}
				env.Writer = w
			}
			return purgeSoftDeletedCore(rc.Context(), p, env)
		},
	})
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
