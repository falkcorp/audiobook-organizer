// file: internal/plugins/maintenance/reconcile.go
// version: 1.7.1
// guid: b8c9d0e1-f2a3-4567-1234-789012345678
// last-edited: 2026-11-15

package maintenance

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/falkcorp/mediacore/tasks"

	"github.com/falkcorp/audiobook-organizer/internal/reconcile"
	"github.com/falkcorp/audiobook-organizer/pkg/plugin/sdk"

	"github.com/falkcorp/audiobook-organizer/pkg/ops"
)

// itunesHealHelp is the def's Description, which the native kind carries as its Help.
const itunesHealHelp = "Heals iTunes tracks moved by the organize operation: parses iTunes XML, finds each missing file in the library by filename/author/track, and reflinks it back to the expected path."

// itunesHealDef registers the iTunes XML-driven path healing operation.
// It parses the iTunes Library.xml, finds tracks whose expected path is
// missing on disk, locates the file via filename+author+track-number
// matching, and refllinks it back — 16 workers, completes in minutes.
func (p *Plugin) itunesHealDef() sdk.OperationDef {
	return sdk.OperationDef{
		ID: "itunes.heal",
		// Renamed 2026-09-25 (naming audit class 8): every iTunes op lives in itunes.*, whichever Go package implements it.
		FormerIDs:       []string{"maintenance.itunes-heal"},
		Liveness:        sdk.LivenessRunItems,
		Plugin:          "maintenance",
		DisplayName:     "iTunes path heal",
		Description:     itunesHealHelp,
		ResumePolicy:    sdk.ResumeRestart, // reflink is idempotent
		DefaultPriority: sdk.PriorityNormal,
		ConcurrencyKey:  "itunes.heal",
		Cancellable:     true,
		Isolate:         false,
		Timeout:         60 * time.Minute,
		Capabilities:    []sdk.Capability{sdk.CapLibraryRead, sdk.CapFilesRead, sdk.CapFilesWrite},
		Run:             p.runITunesHeal,
	}
}

func (p *Plugin) runITunesHeal(ctx context.Context, params json.RawMessage, reporter sdk.Reporter) error {
	// HEAD's entry point, unchanged: the heal over the reporter's pool, log and
	// progress with the plugin's reconcile store as both halves.
	return reconcile.RunITunesHeal(ctx, p.deps.ReconcileStore(), reporter, params)
}

// itunesHealKind is rule R3's kind for the def above.
const itunesHealKind = tasks.Kind("books.itunes.heal")

// itunesHealEnv is what the core takes from its shell: the reads and the writes
// of internal/reconcile's heal, split so the writes can be gated (the collapse
// of duplicate books, the track reflink and the whisper-result journal).
type itunesHealEnv struct {
	t6g6Env
	Reader reconcile.HealReader
	Writer reconcile.HealWriter
}

// itunesHealParallelism is the heal's pool: HEAD's RunItems Concurrency of 16.
func itunesHealParallelism() tasks.Parallelism {
	return tasks.Fixed(reconcile.HealConcurrency, "reads the iTunes XML once, then resolves and reflinks each missing track: file-index lookups, DB reads and an optional AcoustID/Whisper round trip, so I/O and network bound; HEAD's own pool")
}

// itunesHealNative is the native kind. It reflinks files and repoints and merges
// rows, so it has no preview and refuses a Preview run.
func (p *Plugin) itunesHealNative(deps t6g6Deps, sched func(tasks.Kind) *tasks.Schedule) tasks.Definition {
	c := t6g6Common(itunesHealKind, sched)
	c.Title = "iTunes path heal"
	c.Help = itunesHealHelp
	// HEAD declared no Writes. The heal repoints and merges book rows and reflinks
	// files back into the library. Its duplicate collapse is a full book merge
	// (merge.Service.MergeBooksWithOptions), traced in the test map
	// t6g6WriterResource: books, external_ids (the losers' ids move to the
	// survivor), operations (the undo pointer and sibling journal), user_state (the
	// losers' progress and read status follow), indexes (the identification dirty
	// mark), plus files for the reflink. It writes no book_file row (the merge's
	// file moves are combine-only). It also writes sync_identity (the merge follow
	// mints and records sync ids: MintOrGetSyncID and RecordSyncMerge,
	// internal/merge/service.go) and ai_journal (the ambiguous-track resolver
	// journals Whisper results through HealWhisperJournal,
	// internal/reconcile/itunes_heal.go).
	c.Effects = tasks.Writes(
		tasks.Res("books", string(ops.ResBooks)),
		tasks.Res("books", string(ops.ResExternalIDs)), tasks.Res("books", string(ops.ResFiles)),
		tasks.Res("books", string(ops.ResIndexes)), tasks.Res("books", string(ops.ResOperations)),
		tasks.Res("books", string(ops.ResUserState)),
		tasks.Res("books", string(ops.ResSyncIdentity)), tasks.Res("books", string(ops.ResAIJournal)))
	// HEAD's def says Restart because the reflink is idempotent. The heal never
	// writes a checkpoint of its own (LastProcessedPID only comes in through the
	// params), so a restart re-walks from the start: ResumeFromZero, not Continue.
	c.Resume = tasks.ResumeFromZero
	c.Priority = tasks.PriorityNormal
	c.ConcurrencyKey = tasks.Key("books.itunes.heal")
	c.Timeout = 60 * time.Minute
	return tasks.Task(itunesHealKind, tasks.TaskSpec[json.RawMessage, struct{}]{
		Common: c,
		Run: func(rc *tasks.RC, raw json.RawMessage) (struct{}, error) {
			params, err := t6g6DecodeNative[reconcile.ITunesHealParams](itunesHealKind, raw)
			if err != nil {
				return struct{}{}, err
			}
			if err := t6g6LiveOnly(rc, itunesHealKind); err != nil {
				return struct{}{}, err
			}
			if err := t6g6NeedDep(itunesHealKind, "Store", deps.Store != nil); err != nil {
				return struct{}{}, err
			}
			w, err := t6g6NewWriter(itunesHealKind, deps, rc)
			if err != nil {
				return struct{}{}, err
			}
			env := itunesHealEnv{t6g6Env: t6g6NativeEnv(rc), Reader: deps.Store, Writer: w}
			return struct{}{}, itunesHealCore(rc.Context(), params, env)
		},
	})
}

// itunesHealCore runs the heal over the env's reads, writes and pool.
func itunesHealCore(ctx context.Context, params reconcile.ITunesHealParams, env itunesHealEnv) error {
	if env.Reader == nil || env.Writer == nil {
		return fmt.Errorf("iTunes heal: no store")
	}
	return reconcile.RunITunesHealVia(ctx, reconcile.HealEnv{
		Logger:   env.Logger,
		Progress: env.Progress,
		OpID:     env.OpID,
		Reader:   env.Reader,
		Writer:   env.Writer,
		Items: func(ctx context.Context, n int, o reconcile.HealItemsOptions, fn func(ctx context.Context, i int) error) error {
			return env.Items(ctx, itunesHealParallelism(), n, t6g6ItemsOptions{
				Collect:       true,
				ProgressBase:  o.ProgressOffset,
				ProgressTotal: o.ProgressTotal,
				Label:         o.Label,
			}, fn)
		},
	}, params)
}
