// file: internal/plugins/dedup/window_index_build.go
// version: 1.1.2
// guid: 7e2b9d51-0c46-4f83-a1d7-5b8e3c9f6a20
// last-edited: 2026-10-18

package dedup

import (
	"context"
	"encoding/json"
	"fmt"
	"runtime"
	"sync"
	"sync/atomic"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/auth"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/fingerprint"
	"github.com/falkcorp/audiobook-organizer/internal/logger"
	"github.com/falkcorp/audiobook-organizer/internal/operations/opmode"
	"github.com/falkcorp/audiobook-organizer/internal/operations/opswriter"
	"github.com/falkcorp/audiobook-organizer/internal/operations/registry"
	"github.com/falkcorp/audiobook-organizer/pkg/plugin/sdk"
)

// windowIndexBuildOpID is the op that backfills the window-print band index.
const windowIndexBuildOpID = "dedup.window-index-build"

// windowIndexLog is the build op's process log; per-file failures also go to
// the run's own log (reporter.Logger) so they show on the operation.
var windowIndexLog = logger.New(windowIndexBuildOpID)

// WindowIndexStore is the narrow store the window-index build op needs. The
// *database.PebbleStore satisfies it; it is resolved through the decorator
// chain with database.AsCapability.
type WindowIndexStore interface {
	// ListWindowIndexFiles lists every tracked file with stored windows,
	// from a keys-only scan.
	ListWindowIndexFiles() ([]string, error)
	// WindowIndexMembers reports whether a file is already indexed under the
	// current band version.
	WindowIndexMembers(fileID string) ([]fingerprint.WindowBand, bool, error)
	// PutWindowIndex (re)builds one file's rows from its stored windows,
	// under the file's window stripe.
	PutWindowIndex(fileID string) (int, error)
	IsWindowIndexBuilt() bool
	SetWindowIndexBuilt() error
}

var _ WindowIndexStore = (*database.PebbleStore)(nil)

// windowIndexBuildParams: only the mode. Omitted = preview (owner rule
// 2026-09-25): the preview counts the work and prints the size estimate.
type windowIndexBuildParams struct {
	DryRun      *bool `json:"dry_run,omitempty"`
	DryRunCamel *bool `json:"dryRun,omitempty"`
}

// Sizing figures the preview prints (02-PR8 step 3): each indexed file gets at
// most windowsPerFileMax ws1 windows (slots 1000/5000/9000 for a file of ten
// minutes or more), each WindowBandCount band keys, at about
// windowIndexBytesPerKey on disk, plus one member list of 1 + 9 bytes per key.
const (
	windowsPerFileMax      = 3
	windowIndexBytesPerKey = 40
)

// windowIndexBuildDef is dedup.window-index-build: a resumable backfill of the
// fpwinidx: index for windows written before the write-through existed.
//
//   - Not scheduled. The owner runs it (live) once; after that every window
//     write keeps the index current on its own.
//   - Resumable by construction: a file with a member list is skipped, so a
//     rerun after a cancel, crash or restart (ResumeRestart) continues where
//     the last one stopped and ends with the same rows a fresh build makes.
//   - Whole-library CPU loop (banding is MinHash over every shingle of every
//     window): a RunItems pool sized to runtime.NumCPU(), set explicitly.
//   - Every item checks the run's write fence and context before writing
//     (05-PR3), so a stopped run makes no further writes.
//   - fpwinidx_v1_done is set only after a pass in which every listed file
//     is indexed, and only by a live run. Until then the window collector
//     stays off.
//   - Reads stored prints only; never decodes audio.
func (p *Plugin) windowIndexBuildDef() sdk.OperationDef {
	return sdk.OperationDef{
		ID:              windowIndexBuildOpID,
		Preview:         sdk.PreviewSpec{Key: sdk.PreviewDryRun, Params: windowIndexBuildParams{}},
		Liveness:        sdk.LivenessRunItems,
		Plugin:          "dedup",
		DisplayName:     "Build window-print index",
		Description:     "Builds the fpwinidx: band index over stored window fingerprints so the window_acoustid dedup signal and the fragment fixer's containment evidence can find matching files without a full scan. Preview by default; reads stored prints only.",
		ResumePolicy:    sdk.ResumeRestart,
		DefaultPriority: sdk.PriorityLow,
		ConcurrencyKey:  "dedup.window-index",
		Cancellable:     true,
		Timeout:         180 * time.Minute,
		Permissions:     []auth.Permission{auth.PermSettingsManage},
		Capabilities:    []sdk.Capability{sdk.CapLibraryRead, sdk.CapLibraryWrite},
		Writes:          []sdk.Resource{sdk.ResFingerprintIndex},
		Run:             p.runWindowIndexBuild,
	}
}

func (p *Plugin) runWindowIndexBuild(ctx context.Context, raw json.RawMessage, reporter sdk.Reporter) error {
	var params windowIndexBuildParams
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &params); err != nil {
			return fmt.Errorf("%s: invalid params: %w", windowIndexBuildOpID, err)
		}
	}
	dryRun, err := opmode.ResolveDryRun(windowIndexBuildOpID, params.DryRun, params.DryRunCamel)
	if err != nil {
		return err
	}
	store, ok := database.AsCapability[WindowIndexStore](p.store)
	if !ok {
		return fmt.Errorf("%s: store has no window index (PebbleDB required)", windowIndexBuildOpID)
	}

	loadProg := sdk.NewProgress(reporter, 0)
	loadProg.Start("Listing files with stored windows…")
	ids, err := store.ListWindowIndexFiles()
	if err != nil {
		return fmt.Errorf("%s: list files: %w", windowIndexBuildOpID, err)
	}
	total := len(ids)
	windowIndexLog.Info("starting: %d file(s) with stored windows, dry_run=%v", total, dryRun)

	var indexed, already, pending, bandKeys, errs atomic.Int64
	var processed atomic.Int64
	var errMu sync.Mutex
	var firstErr error

	runErr := registry.RunItems(ctx, reporter, ids, func(ctx context.Context, fileID string) error {
		if reporter.IsCanceled() {
			return context.Canceled
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		defer processed.Add(1)
		_, has, err := store.WindowIndexMembers(fileID)
		if err != nil {
			reporter.Logger().Error("window-index-build: read member list", "file_id", fileID, "error", err)
			errs.Add(1)
			errMu.Lock()
			if firstErr == nil {
				firstErr = err
			}
			errMu.Unlock()
			return nil
		}
		if has {
			already.Add(1)
			return nil
		}
		pending.Add(1)
		if dryRun {
			return nil
		}
		// 05-PR3: a stopped run's permit is revoked before its context is
		// canceled; check it right before the write.
		if err := opswriter.FenceFromContext(ctx).Check(); err != nil {
			return err
		}
		n, err := store.PutWindowIndex(fileID)
		if err != nil {
			errs.Add(1)
			errMu.Lock()
			if firstErr == nil {
				firstErr = err
			}
			errMu.Unlock()
			reporter.Logger().Error("window-index-build: index file", "file_id", fileID, "error", err)
			return nil
		}
		indexed.Add(1)
		bandKeys.Add(int64(n))
		return nil
	}, registry.RunItemsOptions{
		Concurrency: windowIndexBuildConcurrency(),
		// Label runs in worker goroutines: every counter it reads is atomic.
		Label: func(i, t int) string {
			return fmt.Sprintf("Window index: %d/%d files (indexed=%d already=%d errors=%d)",
				processed.Load(), total, indexed.Load(), already.Load(), errs.Load())
		},
	})
	if runErr != nil {
		windowIndexLog.Info("stopped after %d of %d file(s), %d indexed: %v", processed.Load(), total, indexed.Load(), runErr)
		return runErr
	}

	prog := sdk.NewProgress(reporter, total)
	if dryRun {
		summary := fmt.Sprintf("Preview: %d file(s) with stored windows, %d already indexed, %d to index. %s Run with dry_run=false to build.",
			total, already.Load(), pending.Load(), windowIndexSizing(total))
		prog.Done(summary)
		windowIndexLog.Info("preview: %d file(s), %d already indexed, %d to index", total, already.Load(), pending.Load())
		return nil
	}
	if n := errs.Load(); n > 0 {
		return fmt.Errorf("%s: %d of %d file(s) failed (first: %v); index NOT marked built, rerun to finish", windowIndexBuildOpID, n, total, firstErr)
	}
	if err := store.SetWindowIndexBuilt(); err != nil {
		return fmt.Errorf("%s: index built but the completion flag failed to write: %w", windowIndexBuildOpID, err)
	}
	summary := fmt.Sprintf("Window index built: %d file(s) indexed now (%d band keys), %d already indexed, of %d.",
		indexed.Load(), bandKeys.Load(), already.Load(), total)
	prog.Done(summary)
	windowIndexLog.Info("complete: %d file(s) indexed now (%d band keys), %d already indexed, of %d", indexed.Load(), bandKeys.Load(), already.Load(), total)
	return nil
}

// windowIndexBuildConcurrency is the build op's worker count: one per CPU,
// and never the sequential default RunItems falls back to.
func windowIndexBuildConcurrency() int {
	return max(runtime.NumCPU(), 2)
}

// windowIndexSizing is the step-3 arithmetic for files files, as an upper
// bound (every file a long one with three windows, every band key distinct).
func windowIndexSizing(files int) string {
	keys := int64(files) * windowsPerFileMax * fingerprint.WindowBandCount
	idxBytes := keys * windowIndexBytesPerKey
	metaBytes := int64(files) * (1 + 9*windowsPerFileMax*fingerprint.WindowBandCount)
	return fmt.Sprintf("Size estimate (upper bound): %d files x %d windows x %d bands = %d keys x %d B = %.2f GB, plus %d member lists = %.2f GB; total %.2f GB.",
		files, windowsPerFileMax, fingerprint.WindowBandCount, keys, windowIndexBytesPerKey, float64(idxBytes)/1e9,
		files, float64(metaBytes)/1e9, float64(idxBytes+metaBytes)/1e9)
}
