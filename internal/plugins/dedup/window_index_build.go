// file: internal/plugins/dedup/window_index_build.go
// version: 1.2.1
// guid: 7e2b9d51-0c46-4f83-a1d7-5b8e3c9f6a20
// last-edited: 2026-11-19

package dedup

import (
	"context"
	"encoding/json"
	"fmt"
	"runtime"
	"sync"
	"sync/atomic"
	"time"

	"github.com/falkcorp/mediacore/tasks"

	"github.com/falkcorp/audiobook-organizer/internal/auth"
	database "github.com/falkcorp/audiobook-organizer/internal/books/store"
	"github.com/falkcorp/audiobook-organizer/internal/fingerprint"
	"github.com/falkcorp/audiobook-organizer/internal/logger"
	"github.com/falkcorp/audiobook-organizer/internal/operations/opmode"
	"github.com/falkcorp/audiobook-organizer/internal/operations/opswriter"
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
		Preview:         sdk.PreviewSpec{Params: windowIndexBuildParams{}},
		Liveness:        sdk.LivenessRunItems,
		Plugin:          "dedup",
		DisplayName:     "Build window-print index",
		Description:     windowIndexBuildHelp,
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

// windowIndexBuildKind is rule R3's native kind for dedup.window-index-build.
const windowIndexBuildKind = tasks.Kind("books." + windowIndexBuildOpID)

// windowIndexBuildHelp is HEAD's Description and the native Help (rule R5).
const windowIndexBuildHelp = "Builds the fpwinidx: band index over stored window fingerprints so the window_acoustid dedup signal and the fragment fixer's containment evidence can find matching files without a full scan. Preview by default; reads stored prints only."

// windowIndexBuildStore is the reads the core takes.
type windowIndexBuildStore interface {
	ListWindowIndexFiles() ([]string, error)
	WindowIndexMembers(fileID string) ([]fingerprint.WindowBand, bool, error)
}

// windowIndexBuildWriter is the writes the core makes: one file's rows and the
// completion flag.
type windowIndexBuildWriter interface {
	PutWindowIndex(fileID string) (int, error)
	SetWindowIndexBuilt() error
}

// windowIndexBuildEnv is what the core takes from its shell. Live is
// dry_run=false (HEAD) or a Live run (native); a preview never calls Writer,
// which may then be nil.
type windowIndexBuildEnv struct {
	kindEnv
	Store  windowIndexBuildStore
	Writer windowIndexBuildWriter
	// Fence is HEAD's 05-PR3 write fence, checked right before each file's
	// write: a stopped run's permit is revoked before its context is canceled.
	// Nil in a native shell, whose writer is the kernel's gated tasks.Writer
	// and refuses a stopped run's write itself.
	Fence func(ctx context.Context) error
}

// windowIndexBuildResult is the native run's result. HEAD stored none.
type windowIndexBuildResult struct {
	Total    int `json:"total"`
	Indexed  int `json:"indexed"`
	Already  int `json:"already_indexed"`
	Pending  int `json:"pending"`
	BandKeys int `json:"band_keys"`
	Errors   int `json:"errors"`
}

// windowIndexBuildCore is the body of runWindowIndexBuild, shared by both
// shells. The per-item Label reads atomic counters, which the native pool cannot
// carry (the kernel's label is pure), so it is passed as Label: HEAD's pool
// shows it, the native run drops it.
func windowIndexBuildCore(ctx context.Context, env windowIndexBuildEnv) (windowIndexBuildResult, error) {
	dryRun := !env.Live
	store := env.Store
	loadProg := newProgress(env.kindEnv, 0)
	loadProg.Start("Listing files with stored windows…")
	ids, err := store.ListWindowIndexFiles()
	if err != nil {
		return windowIndexBuildResult{}, fmt.Errorf("%s: list files: %w", windowIndexBuildOpID, err)
	}
	total := len(ids)
	windowIndexLog.Info("starting: %d file(s) with stored windows, dry_run=%v", total, dryRun)

	var indexed, already, pending, bandKeys, errs atomic.Int64
	var processed atomic.Int64
	var errMu sync.Mutex
	var firstErr error

	runErr := env.Items(ctx, windowIndexBuildParallelism(), total, kindItemsOptions{
		// HEAD's own count: Fixed's ceiling must not narrow HEAD's pool.
		HeadWorkers: windowIndexBuildConcurrency(),
		// Label runs in worker goroutines: every counter it reads is atomic.
		Label: func(i, t int) string {
			return fmt.Sprintf("Window index: %d/%d files (indexed=%d already=%d errors=%d)",
				processed.Load(), total, indexed.Load(), already.Load(), errs.Load())
		},
	}, func(ctx context.Context, i int) error {
		fileID := ids[i]
		if env.Canceled() {
			return context.Canceled
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		defer processed.Add(1)
		_, has, err := store.WindowIndexMembers(fileID)
		if err != nil {
			env.Logger.Error("window-index-build: read member list", "file_id", fileID, "error", err)
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
		if env.Fence != nil {
			if err := env.Fence(ctx); err != nil {
				return err
			}
		}
		n, err := env.Writer.PutWindowIndex(fileID)
		if err != nil {
			errs.Add(1)
			errMu.Lock()
			if firstErr == nil {
				firstErr = err
			}
			errMu.Unlock()
			env.Logger.Error("window-index-build: index file", "file_id", fileID, "error", err)
			return nil
		}
		indexed.Add(1)
		bandKeys.Add(int64(n))
		return nil
	})
	res := func() windowIndexBuildResult {
		return windowIndexBuildResult{Total: total, Indexed: int(indexed.Load()), Already: int(already.Load()),
			Pending: int(pending.Load()), BandKeys: int(bandKeys.Load()), Errors: int(errs.Load())}
	}
	if runErr != nil {
		windowIndexLog.Info("stopped after %d of %d file(s), %d indexed: %v", processed.Load(), total, indexed.Load(), runErr)
		return res(), runErr
	}

	prog := newProgress(env.kindEnv, total)
	if dryRun {
		summary := fmt.Sprintf("Preview: %d file(s) with stored windows, %d already indexed, %d to index. %s Run with dry_run=false to build.",
			total, already.Load(), pending.Load(), windowIndexSizing(total))
		prog.Done(summary)
		windowIndexLog.Info("preview: %d file(s), %d already indexed, %d to index", total, already.Load(), pending.Load())
		return res(), nil
	}
	if n := errs.Load(); n > 0 {
		return res(), fmt.Errorf("%s: %d of %d file(s) failed (first: %v); index NOT marked built, rerun to finish", windowIndexBuildOpID, n, total, firstErr)
	}
	if err := env.Writer.SetWindowIndexBuilt(); err != nil {
		return res(), fmt.Errorf("%s: index built but the completion flag failed to write: %w", windowIndexBuildOpID, err)
	}
	summary := fmt.Sprintf("Window index built: %d file(s) indexed now (%d band keys), %d already indexed, of %d.",
		indexed.Load(), bandKeys.Load(), already.Load(), total)
	prog.Done(summary)
	windowIndexLog.Info("complete: %d file(s) indexed now (%d band keys), %d already indexed, of %d", indexed.Load(), bandKeys.Load(), already.Load(), total)
	return res(), nil
}

// runWindowIndexBuild is the v2 shell: HEAD's Run (params, the dry_run
// resolution, the capability lookup) over windowIndexBuildCore.
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
	_, err = windowIndexBuildCore(ctx, windowIndexBuildEnv{
		kindEnv: headEnv(reporter, !dryRun),
		Store:   store,
		Writer:  store,
		Fence:   func(ctx context.Context) error { return opswriter.FenceFromContext(ctx).Check() },
	})
	return err
}

// windowIndexBuildConcurrency is the build op's worker count: one per CPU,
// and never the sequential default RunItems falls back to.
func windowIndexBuildConcurrency() int {
	return max(runtime.NumCPU(), 2)
}

// windowIndexBuildParallelism is the build's pool (rule R4): HEAD's own count,
// max(NumCPU, 2), which is not tasks.CPU()'s max(NumCPU, 1) (a single-CPU host
// ran two workers), so it is a Fixed count, not CPU.
func windowIndexBuildParallelism() tasks.Parallelism {
	return tasks.Fixed(windowIndexBuildConcurrency(), "one worker per CPU and never fewer than two, never the sequential default RunItems falls back to")
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
