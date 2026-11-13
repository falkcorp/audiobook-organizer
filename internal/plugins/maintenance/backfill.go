// file: internal/plugins/maintenance/backfill.go
// version: 1.10.0
// guid: f2a3b4c5-d6e7-8901-5678-123456789012
// last-edited: 2026-11-13

package maintenance

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/walkresume"
	"github.com/falkcorp/audiobook-organizer/pkg/plugin/sdk"
	"github.com/falkcorp/mediacore/tasks"
)

// One-shot startup backfills / file repairs. Schedule is nil — they are
// enqueued once per boot by Server.startBackfills (enqueueBootOps) and not
// repeated, and an operator can run any of them by hand.
//
// RESUMABLE (04-P6, docs/decisions/2026-10-30-boot-ops-resumable-checkpoint.md).
// Each is a full-library walk under a fixed Timeout that a large first run can
// exceed, and a timeout is terminal: the done flag stays unset and the next
// boot starts again from zero. So the three ResumeRestart ops (external-id,
// movement-atom, remux) checkpoint a cursor as they go (bootBackfillParams
// .Resume, merged back into params on a restart) AND yield before their
// deadline (opsregistry.ErrContinueInPlace), which re-queues the SAME operation
// with a fresh Timeout and the checkpoint merged in. The Timeouts are
// unchanged. The transcode stays ResumeAsk (it re-encodes audio in place; the
// owner confirms an interrupted run): it checkpoints too, so the operator's
// Retry continues from the cursor, but it never yields by itself.
//
// Each waits for the store's startup warmup before it runs: the registry's
// dispatcher holds every def without NoWarmupWait (D44, warmup_gate.go).
//
// A run that found nothing to do logs its summary with opsregistry.NoOpAttr,
// and every op here logs its "Starting" line at Debug (the activity mirror
// files it in the debug tier), so the boot runs that are usually no-ops add
// no line to the default activity feed. A cancelled run returns ctx's error
// instead of logging "complete".

// bootBackfillParams is the params shape of the boot-enqueued ops in this
// file. Absent params (an operator's run) decode to the zero value, which is
// each op's behaviour from before boot enqueued them.
type bootBackfillParams struct {
	// IfNeeded (external-id only) takes the sentinel-gated path boot used to
	// run: skip when an earlier pass recorded completion.
	IfNeeded bool `json:"if_needed,omitempty"`
	// QuarantineAfter (transcode only) runs the known-bad-file quarantine
	// after the transcode, the second step of the old boot goroutine.
	QuarantineAfter bool `json:"quarantine_after,omitempty"`
	// Resume is the checkpoint (it is not a param an operator sets): the
	// registry merges the saved checkpoint over params on a restart, and a
	// checkpoint is these same params with Resume updated.
	Resume walkresume.State `json:"resume,omitempty"`
}

// bootYieldMargin is the least time left before the run's deadline at which a
// resumable boot op stops at its next checkpoint and hands itself back to the
// registry. The margin used is the larger of this and twice the longest gap
// between checkpoints the attempt has seen, so the items in flight when the
// line is crossed finish well inside the deadline. A variable so a test can
// move it.
var bootYieldMargin = 5 * time.Minute

// Pool sizes for the per-file passes (CLAUDE.md concurrency rule). The work is
// a taglib read per file, and for a malformed one an ffmpeg process, so a fixed
// small pool rather than NumCPU: ffmpeg is itself multi-threaded and these ops
// run at PriorityLow beside user work. The transcode is a full AAC re-encode,
// so it gets the smallest.
const (
	bootExternalIDWorkers = 1
	bootWalkWorkers       = 4
	bootTranscodeWorkers  = 2
)

// DisplayName and Description of the four defs, shared by HEAD's def and the
// native kind so the two cannot drift (TestT6G2Kinds_MatchHEADDefs also checks).
const (
	t6g2ExternalIDBackfillTitle = "External ID backfill"
	t6g2ExternalIDBackfillHelp  = "Backfill of external IDs (iTunes PIDs, etc.) from the existing database. Runs in full whenever triggered, even after an earlier pass has recorded completion, unless enqueued with {\"if_needed\": true} (as boot does)."

	t6g2MovementAtomCleanupTitle = "Strip movement atoms"
	t6g2MovementAtomCleanupHelp  = "Strips unwanted movement atoms from M4B files that cause chapter parsing issues."

	t6g2MalformedM4BRemuxTitle = "Remux malformed M4B files"
	t6g2MalformedM4BRemuxHelp  = "Remuxes M4B files with broken container structure without re-encoding audio."

	t6g2MalformedM4BTranscodeTitle = "Transcode malformed M4B files"
	t6g2MalformedM4BTranscodeHelp  = "Full re-encode of M4B files that cannot be remuxed. Interrupted runs surface in UI for operator confirmation."
)

// The four native kinds of this file (F5-07, rule R3: "books." + the def's ID).
const (
	t6g2KindExternalIDBackfill    tasks.Kind = "books.maintenance.external-id-backfill"
	t6g2KindMovementAtomCleanup   tasks.Kind = "books.maintenance.movement-atom-cleanup"
	t6g2KindMalformedM4BRemux     tasks.Kind = "books.maintenance.malformed-m4b-remux"
	t6g2KindMalformedM4BTranscode tasks.Kind = "books.maintenance.malformed-m4b-transcode"
)

// quietReporter drops RunItems' own progress and current-item calls. The walk
// reports its own absolute (processed, total, message) through the progress
// callback; letting RunItems report the position within the pool's index list
// as well would make the bar jump between two numberings.
type quietReporter struct{ sdk.Reporter }

func (quietReporter) UpdateProgress(int, int, string) error { return nil }
func (quietReporter) SetCurrentItem(string)                 {}

// bootWalkRun builds the resume plumbing of one attempt for HEAD's shells: the
// saved position from params, a Checkpoint that persists params with an updated
// position (reporter.Checkpoint, the registry's one checkpoint format), the
// worker pool (opsregistry.RunItems; Concurrency is set, it defaults to one),
// and, when canYield, a YieldDue that fires near the deadline. It is
// t6g2BootWalkRun over HEAD's env.
func bootWalkRun(ctx context.Context, reporter sdk.Reporter, params bootBackfillParams, workers int, canYield bool) walkresume.Run {
	env := headT6G2Env(nil, reporter)
	env.Items = headT6G2Items(quietReporter{reporter}, workers)
	return t6g2BootWalkRun(ctx, env, params, canYield)
}

// t6g2BootWalkRun is the resume plumbing over an env, shared by HEAD's shells
// and the native ones: Checkpoint persists params with the position through
// env.Checkpoint, Exec is env.Items, and YieldDue (when canYield) is the env's.
func t6g2BootWalkRun(ctx context.Context, env t6g2Env, params bootBackfillParams, canYield bool) walkresume.Run {
	run := walkresume.Run{
		Resume: params.Resume,
		Checkpoint: func(st walkresume.State) {
			next := params
			next.Resume = st
			if err := env.Checkpoint(next); err != nil {
				env.Log(slog.LevelWarn, "Could not save the resume point; a restart would redo more", slog.String("error", err.Error()))
			}
		},
		Exec: func(ctx context.Context, n, resumeFrom int, fn func(context.Context, int) error, onWatermark func(context.Context, int)) error {
			return env.Items(ctx, n, t6g2Loop{
				// Collect, not Fail: a yield makes every not-yet-started item
				// return at once, and the first of those must not cancel the
				// items already in flight (an ffmpeg killed mid-file).
				Collect:         true,
				ResumeFrom:      resumeFrom,
				CheckpointEvery: walkresume.CheckpointEvery,
				CheckpointState: func(ctx context.Context, watermark int) error {
					onWatermark(ctx, watermark)
					return nil
				},
			}, fn)
		},
		Progress: func(processed, total int, msg string) { env.Progress(processed, total, msg) },
	}
	if canYield {
		run.YieldDue = env.NewYieldDue(ctx)
	}
	return run
}

// newYieldDue returns the YieldDue of one attempt. It is called once per
// checkpoint, serialized. The first call only starts the clock: the time to
// collect the candidates is not a checkpoint gap.
func newYieldDue(ctx context.Context, margin time.Duration) func() bool {
	var last time.Time
	var slowest time.Duration
	return func() bool {
		now := time.Now()
		if !last.IsZero() {
			slowest = max(slowest, now.Sub(last))
		}
		last = now
		dl, ok := ctx.Deadline()
		return ok && time.Until(dl) < max(margin, 2*slowest)
	}
}

// yielded turns a walk's ErrYield into the registry's continue-in-place
// sentinel; any other error is returned as is.
func yielded(reporter sdk.Reporter, what string, err error) error {
	return t6g2Yielded(headT6G2Env(nil, reporter), what, err)
}

// t6g2Yielded is yielded over an env: the sentinel is env.Continue.
func t6g2Yielded(env t6g2Env, what string, err error) error {
	if !errors.Is(err, walkresume.ErrYield) {
		return err
	}
	env.Log(slog.LevelInfo, what+": pausing near this run's time limit; the operation restarts under the same id and continues from its saved position")
	return fmt.Errorf("%s: %w", what, env.Continue)
}

func decodeBootBackfillParams(opID string, raw json.RawMessage) (bootBackfillParams, error) {
	var params bootBackfillParams
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &params); err != nil {
			return params, fmt.Errorf("%s: params: %w", opID, err)
		}
	}
	return params, nil
}

func (p *Plugin) externalIDBackfillDef() sdk.OperationDef {
	return sdk.OperationDef{
		ID:              "maintenance.external-id-backfill",
		Liveness:        sdk.LivenessManual,
		Plugin:          "maintenance",
		DisplayName:     t6g2ExternalIDBackfillTitle,
		Description:     t6g2ExternalIDBackfillHelp,
		ResumePolicy:    sdk.ResumeRestart, // 04-P6: checkpoints a book-ID cursor / XML track index and yields near its deadline
		DefaultPriority: sdk.PriorityLow,
		ConcurrencyKey:  "maintenance.external-id-backfill",
		Cancellable:     true, // honours ctx at every book/file; a cancelled run leaves its done flag unset
		Isolate:         false,
		Timeout:         30 * time.Minute,
		Schedule:        nil,
		Capabilities:    []sdk.Capability{sdk.CapLibraryRead, sdk.CapLibraryWrite},
		Run:             p.runExternalIDBackfill,
	}
}

func (p *Plugin) runExternalIDBackfill(ctx context.Context, raw json.RawMessage, reporter sdk.Reporter) error {
	params, err := decodeBootBackfillParams("external-id-backfill", raw)
	if err != nil {
		return err
	}
	_, err = runExternalIDBackfillCore(ctx, params, p.headBootEnv(reporter, bootExternalIDWorkers))
	return err
}

// headBootEnv is the env of a boot op under HEAD's shell: the walk's pool is
// quiet (the walk reports its own absolute progress) with the op's worker
// count, and the writer is the plugin's ServerDeps.
func (p *Plugin) headBootEnv(reporter sdk.Reporter, workers int) t6g2Env {
	env := headT6G2Env(p, reporter)
	env.Items = headT6G2Items(quietReporter{reporter}, workers)
	env.Writer = headT6G2Writer{deps: p.deps}
	return env
}

// runExternalIDBackfillCore is the body shared by HEAD's shell and the native
// kind. env.Writer and env.Items are its only reach outside params.
func runExternalIDBackfillCore(ctx context.Context, params bootBackfillParams, env t6g2Env) (t6g2NoResult, error) {
	env.Log(slog.LevelDebug, "Starting external ID backfill", slog.Bool("if_needed", params.IfNeeded))
	skipped, err := env.Writer.BackfillExternalIDs(ctx, params.IfNeeded, env.Progress, t6g2BootWalkRun(ctx, env, params, true))
	if errors.Is(err, walkresume.ErrYield) {
		return t6g2NoResult{}, t6g2Yielded(env, "External ID backfill", err)
	}
	if err != nil {
		env.Log(slog.LevelError, "External ID backfill failed", slog.String("error", err.Error()))
		return t6g2NoResult{}, err
	}
	if skipped {
		env.Log(slog.LevelInfo, "External ID backfill already complete; nothing to do", env.NoOp)
		return t6g2NoResult{}, nil
	}
	if err := ctx.Err(); err != nil {
		return t6g2NoResult{}, err
	}
	env.Log(slog.LevelInfo, "External ID backfill complete")
	return t6g2NoResult{}, nil
}

func (p *Plugin) movementAtomCleanupDef() sdk.OperationDef {
	return sdk.OperationDef{
		ID:              "maintenance.movement-atom-cleanup",
		Liveness:        sdk.LivenessManual,
		Plugin:          "maintenance",
		DisplayName:     t6g2MovementAtomCleanupTitle,
		Description:     t6g2MovementAtomCleanupHelp,
		ResumePolicy:    sdk.ResumeRestart, // 04-P6: checkpoints a file-path cursor and yields near its deadline
		DefaultPriority: sdk.PriorityLow,
		ConcurrencyKey:  "maintenance.movement-atom-cleanup",
		Cancellable:     true,  // honours ctx at every book/file; a cancelled run leaves its done flag unset
		Isolate:         false, // DISABLED 2026-05-29: PR #1172 child-mode wire-up cannot work because Pebble is single-writer; child re-open fails. See MAYDEPLOY-A revisit.
		Timeout:         60 * time.Minute,
		Schedule:        nil,
		Capabilities:    []sdk.Capability{sdk.CapLibraryRead, sdk.CapFilesRead, sdk.CapFilesWrite, sdk.CapSubprocessSpawn},
		Run:             p.runMovementAtomCleanup,
	}
}

func (p *Plugin) runMovementAtomCleanup(ctx context.Context, raw json.RawMessage, reporter sdk.Reporter) error {
	params, err := decodeBootBackfillParams("movement-atom-cleanup", raw)
	if err != nil {
		return err
	}
	_, err = runMovementAtomCleanupCore(ctx, params, p.headBootEnv(reporter, bootWalkWorkers))
	return err
}

func runMovementAtomCleanupCore(ctx context.Context, params bootBackfillParams, env t6g2Env) (t6g2NoResult, error) {
	env.Log(slog.LevelDebug, "Starting movement atom cleanup")
	alreadyDone, err := env.Writer.StripMovementAtoms(ctx, t6g2BootWalkRun(ctx, env, params, true))
	if err != nil {
		return t6g2NoResult{}, t6g2Yielded(env, "Movement atom cleanup", err)
	}
	if alreadyDone {
		env.Log(slog.LevelInfo, "Movement atom cleanup already complete; nothing to do", env.NoOp)
		return t6g2NoResult{}, nil
	}
	if err := ctx.Err(); err != nil {
		return t6g2NoResult{}, err
	}
	env.Log(slog.LevelInfo, "Movement atom cleanup complete")
	return t6g2NoResult{}, nil
}

func (p *Plugin) malformedM4BRemuxDef() sdk.OperationDef {
	return sdk.OperationDef{
		ID:              "maintenance.malformed-m4b-remux",
		Liveness:        sdk.LivenessManual,
		Plugin:          "maintenance",
		DisplayName:     t6g2MalformedM4BRemuxTitle,
		Description:     t6g2MalformedM4BRemuxHelp,
		ResumePolicy:    sdk.ResumeRestart, // 04-P6: checkpoints a file-path cursor and yields near its deadline
		DefaultPriority: sdk.PriorityLow,
		ConcurrencyKey:  "maintenance.malformed-m4b-remux",
		Cancellable:     true,  // honours ctx at every book/file; a cancelled run leaves its done flag unset
		Isolate:         false, // DISABLED 2026-05-29: PR #1172 child-mode wire-up cannot work because Pebble is single-writer; child re-open fails. See MAYDEPLOY-A revisit.
		Timeout:         120 * time.Minute,
		Schedule:        nil,
		Capabilities:    []sdk.Capability{sdk.CapLibraryRead, sdk.CapFilesRead, sdk.CapFilesWrite, sdk.CapSubprocessSpawn},
		Run:             p.runMalformedM4BRemux,
	}
}

func (p *Plugin) runMalformedM4BRemux(ctx context.Context, raw json.RawMessage, reporter sdk.Reporter) error {
	params, err := decodeBootBackfillParams("malformed-m4b-remux", raw)
	if err != nil {
		return err
	}
	_, err = runMalformedM4BRemuxCore(ctx, params, p.headBootEnv(reporter, bootWalkWorkers))
	return err
}

func runMalformedM4BRemuxCore(ctx context.Context, params bootBackfillParams, env t6g2Env) (t6g2NoResult, error) {
	env.Log(slog.LevelDebug, "Starting malformed M4B remux")
	alreadyDone, err := env.Writer.RemuxMalformedM4BFiles(ctx, env.Progress, t6g2BootWalkRun(ctx, env, params, true))
	if errors.Is(err, walkresume.ErrYield) {
		return t6g2NoResult{}, t6g2Yielded(env, "Malformed M4B remux", err)
	}
	if err != nil {
		env.Log(slog.LevelError, "Malformed M4B remux failed", slog.String("error", err.Error()))
		return t6g2NoResult{}, err
	}
	if alreadyDone {
		env.Log(slog.LevelInfo, "Malformed M4B remux already complete; nothing to do", env.NoOp)
		return t6g2NoResult{}, nil
	}
	if err := ctx.Err(); err != nil {
		return t6g2NoResult{}, err
	}
	env.Log(slog.LevelInfo, "Malformed M4B remux complete")
	return t6g2NoResult{}, nil
}

// Hard rule: transcode = ResumeAsk (destructive; operator must confirm).

func (p *Plugin) malformedM4BTranscodeDef() sdk.OperationDef {
	return sdk.OperationDef{
		ID:              "maintenance.malformed-m4b-transcode",
		Liveness:        sdk.LivenessManual,
		Plugin:          "maintenance",
		DisplayName:     t6g2MalformedM4BTranscodeTitle,
		Description:     t6g2MalformedM4BTranscodeHelp,
		ResumePolicy:    sdk.ResumeAsk,
		DefaultPriority: sdk.PriorityLow,
		ConcurrencyKey:  "maintenance.malformed-m4b-transcode",
		Cancellable:     true,
		Isolate:         false, // DISABLED 2026-05-29: PR #1172 child-mode wire-up cannot work because Pebble is single-writer; child re-open fails. See MAYDEPLOY-A revisit.
		Timeout:         6 * time.Hour,
		// C2: a full AAC re-encode can take minutes per file, so the every-25
		// -files progress cadence can leave a long gap between UpdateProgress
		// stamps. The registry watchdog's default ProgressTimeout is 5 minutes
		// (see internal/operations/registry/watchdog.go) — without an explicit
		// override here a slow but healthy transcode run risks being killed as
		// "stuck". 30m matches the precedent in intro_transcribe.go for a
		// similarly slow per-item op.
		ProgressTimeout: 30 * time.Minute,
		Schedule:        nil,
		Capabilities:    []sdk.Capability{sdk.CapLibraryRead, sdk.CapFilesRead, sdk.CapFilesWrite, sdk.CapSubprocessSpawn},
		Run:             p.runMalformedM4BTranscode,
	}
}

// runMalformedM4BTranscode transcodes, then, with {"quarantine_after": true}
// (boot passes it), quarantines the files the transcode could not fix: the two
// steps the old boot goroutine ran in sequence. As there, the quarantine runs
// even when the transcode failed, since it acts on files earlier transcode
// runs marked; both errors are returned.
func (p *Plugin) runMalformedM4BTranscode(ctx context.Context, raw json.RawMessage, reporter sdk.Reporter) error {
	params, err := decodeBootBackfillParams("malformed-m4b-transcode", raw)
	if err != nil {
		return err
	}
	_, err = runMalformedM4BTranscodeCore(ctx, params, p.headBootEnv(reporter, bootTranscodeWorkers))
	return err
}

func runMalformedM4BTranscodeCore(ctx context.Context, params bootBackfillParams, env t6g2Env) (t6g2NoResult, error) {
	env.Log(slog.LevelDebug, "Starting malformed M4B transcode", slog.Bool("quarantine_after", params.QuarantineAfter))
	// canYield is false: the transcode is ResumeAsk, and the registry honours a
	// continue-in-place yield only for ResumeRestart, so it would be a failure.
	// The checkpoint is what an operator's Retry of an interrupted run uses.
	alreadyDone, transcodeErr := env.Writer.TranscodeMalformedM4BFiles(ctx, env.Progress, t6g2BootWalkRun(ctx, env, params, false))
	if transcodeErr != nil {
		env.Log(slog.LevelError, "Malformed M4B transcode failed", slog.String("error", transcodeErr.Error()))
	}
	quarantined := 0
	var quarantineErr error
	if params.QuarantineAfter {
		var qDone bool
		quarantined, qDone, quarantineErr = env.Writer.QuarantineKnownBadFiles(ctx)
		switch {
		case quarantineErr != nil:
			env.Log(slog.LevelError, "Quarantine of known-bad files failed",
				slog.Int("quarantined", quarantined), slog.String("error", quarantineErr.Error()))
		case !qDone:
			env.Log(slog.LevelInfo, fmt.Sprintf("Quarantined %d known-bad file(s)", quarantined))
		}
	}
	if err := errors.Join(transcodeErr, quarantineErr); err != nil {
		return t6g2NoResult{}, err
	}
	if alreadyDone && quarantined == 0 {
		env.Log(slog.LevelInfo, "Malformed M4B transcode already complete; nothing to do", env.NoOp)
		return t6g2NoResult{}, nil
	}
	if err := ctx.Err(); err != nil {
		return t6g2NoResult{}, err
	}
	env.Log(slog.LevelInfo, "Malformed M4B transcode complete")
	return t6g2NoResult{}, nil
}

// ---------------------------------------------------------------------------
// Native kinds (F5-07). One shell serves the four: they differ only in the
// core, the meta and the pool size.
// ---------------------------------------------------------------------------

// t6g2NoResult is the result of a core that has none.
type t6g2NoResult = struct{}

func t6g2ExternalIDBackfillParallelism() tasks.Parallelism {
	return tasks.Fixed(bootExternalIDWorkers, "one walker: the book pass and the iTunes XML pass checkpoint a single cursor")
}

func t6g2MovementAtomCleanupParallelism() tasks.Parallelism {
	return tasks.Fixed(bootWalkWorkers, "a taglib read per file and an ffmpeg process for a malformed one; run at low priority beside user work")
}

func t6g2MalformedM4BRemuxParallelism() tasks.Parallelism {
	return tasks.Fixed(bootWalkWorkers, "a taglib read per file and an ffmpeg process for a malformed one; run at low priority beside user work")
}

func t6g2MalformedM4BTranscodeParallelism() tasks.Parallelism {
	return tasks.Fixed(bootTranscodeWorkers, "a full AAC re-encode per file; ffmpeg is itself multi-threaded")
}

// Effects found by reading what each server function writes (the PR lists them):
// the ids pass writes external_ids and its done flag (settings); the atom and
// remux passes rewrite files on disk, the book_file rows that describe them and
// their done flag; the transcode also quarantines books. HEAD declares no
// Writes for any of the four.
func t6g2ExternalIDBackfillMeta() t6g2Meta {
	return t6g2Meta{
		Kind:     t6g2KindExternalIDBackfill,
		Title:    t6g2ExternalIDBackfillTitle,
		Help:     t6g2ExternalIDBackfillHelp,
		Effects:  tasks.Writes(t6g2Res("external_ids", "settings")...),
		Resume:   tasks.ResumeContinue,
		Priority: tasks.PriorityLow,
		Timeout:  30 * time.Minute,
		Key:      "maintenance.external-id-backfill",
	}
}

func t6g2MovementAtomCleanupMeta() t6g2Meta {
	return t6g2Meta{
		Kind:     t6g2KindMovementAtomCleanup,
		Title:    t6g2MovementAtomCleanupTitle,
		Help:     t6g2MovementAtomCleanupHelp,
		Effects:  tasks.Writes(t6g2Res("files", "settings")...),
		Resume:   tasks.ResumeContinue,
		Priority: tasks.PriorityLow,
		Timeout:  60 * time.Minute,
		Key:      "maintenance.movement-atom-cleanup",
	}
}

func t6g2MalformedM4BRemuxMeta() t6g2Meta {
	return t6g2Meta{
		Kind:     t6g2KindMalformedM4BRemux,
		Title:    t6g2MalformedM4BRemuxTitle,
		Help:     t6g2MalformedM4BRemuxHelp,
		Effects:  tasks.Writes(t6g2Res("files", "book_files", "settings")...),
		Resume:   tasks.ResumeContinue,
		Priority: tasks.PriorityLow,
		Timeout:  120 * time.Minute,
		Key:      "maintenance.malformed-m4b-remux",
	}
}

func t6g2MalformedM4BTranscodeMeta() t6g2Meta {
	return t6g2Meta{
		Kind:            t6g2KindMalformedM4BTranscode,
		Title:           t6g2MalformedM4BTranscodeTitle,
		Help:            t6g2MalformedM4BTranscodeHelp,
		Effects:         tasks.Writes(t6g2Res("files", "book_files", "settings", "books")...),
		Resume:          tasks.ResumeAsk,
		Priority:        tasks.PriorityLow,
		Timeout:         6 * time.Hour,
		ProgressTimeout: 30 * time.Minute,
		Key:             "maintenance.malformed-m4b-transcode",
	}
}

// t6g2BootKind builds one boot kind: the shell refuses a preview (a live-only
// writer, rule R7), restores the checkpoint (the registry merged it over params
// under HEAD; the native run restores it), and hands the core a native env.
func t6g2BootKind(m t6g2Meta, par tasks.Parallelism, deps t6g2Deps, sched func(tasks.Kind) *tasks.Schedule,
	core func(context.Context, bootBackfillParams, t6g2Env) (t6g2NoResult, error)) tasks.Definition {
	run := func(rc *tasks.RC, raw json.RawMessage) (t6g2NoResult, error) {
		if err := t6g2RefuseNotLive(rc, m.Kind); err != nil {
			return t6g2NoResult{}, err
		}
		if err := t6g2RefuseModeKeys(m.Kind, raw, "dry_run", "dryRun"); err != nil {
			return t6g2NoResult{}, err
		}
		params, err := decodeBootBackfillParams(string(m.Kind), raw)
		if err != nil {
			return t6g2NoResult{}, err
		}
		// A checkpoint is these same params with Resume updated, so a restored
		// one replaces the start params whole (HEAD merged it over them).
		var saved bootBackfillParams
		ok, err := rc.Restore(&saved)
		if err != nil {
			return t6g2NoResult{}, fmt.Errorf("%s: restore checkpoint: %w", m.Kind, err)
		}
		if ok {
			params = saved
		}
		env, err := t6g2NativeEnv(rc, deps, par)
		if err != nil {
			return t6g2NoResult{}, err
		}
		return core(rc.Context(), params, env)
	}
	return tasks.Task(m.Kind, tasks.TaskSpec[json.RawMessage, t6g2NoResult]{Common: m.common(sched), Run: run})
}

func t6g2ExternalIDBackfillKind(deps t6g2Deps, sched func(tasks.Kind) *tasks.Schedule) tasks.Definition {
	return t6g2BootKind(t6g2ExternalIDBackfillMeta(), t6g2ExternalIDBackfillParallelism(), deps, sched, runExternalIDBackfillCore)
}

func t6g2MovementAtomCleanupKind(deps t6g2Deps, sched func(tasks.Kind) *tasks.Schedule) tasks.Definition {
	return t6g2BootKind(t6g2MovementAtomCleanupMeta(), t6g2MovementAtomCleanupParallelism(), deps, sched, runMovementAtomCleanupCore)
}

func t6g2MalformedM4BRemuxKind(deps t6g2Deps, sched func(tasks.Kind) *tasks.Schedule) tasks.Definition {
	return t6g2BootKind(t6g2MalformedM4BRemuxMeta(), t6g2MalformedM4BRemuxParallelism(), deps, sched, runMalformedM4BRemuxCore)
}

func t6g2MalformedM4BTranscodeKind(deps t6g2Deps, sched func(tasks.Kind) *tasks.Schedule) tasks.Definition {
	return t6g2BootKind(t6g2MalformedM4BTranscodeMeta(), t6g2MalformedM4BTranscodeParallelism(), deps, sched, runMalformedM4BTranscodeCore)
}
