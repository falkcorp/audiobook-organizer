// file: internal/plugins/maintenance/reconcile.go
// version: 1.6.0
// guid: b8c9d0e1-f2a3-4567-1234-789012345678
// last-edited: 2026-11-13

package maintenance

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/falkcorp/mediacore/tasks"

	"github.com/falkcorp/audiobook-organizer/internal/reconcile"
	"github.com/falkcorp/audiobook-organizer/pkg/plugin/sdk"
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
	env := itunesHealEnv{
		t6g6Env: t6g6HeadEnv(reporter, true),
		Writer:  headITunesHealWriter{store: p.deps.ReconcileStore(), reporter: reporter},
	}
	return itunesHealCore(ctx, params, env)
}

// itunesHealKind is rule R3's kind for the def above.
const itunesHealKind = tasks.Kind("books.itunes.heal")

// itunesHealWriter is the one write the op makes. The heal is a single pass in
// internal/reconcile that reads the iTunes XML and the library, reflinks each
// missing file back to its expected path, repoints the book_file row and
// collapses duplicates through merge.Service. Its run is the gated write: the
// kernel's writer (F5-18) adapts the run's progress and log to the reporter the
// reconcile package takes.
type itunesHealWriter interface {
	// RunITunesHeal runs reconcile.RunITunesHeal; params is its checkpoint state
	// (reconcile.ITunesHealParams as JSON, empty for a fresh run).
	RunITunesHeal(ctx context.Context, params json.RawMessage) error
}

// headITunesHealWriter is HEAD's writer: reconcile.RunITunesHeal over the
// plugin's reconcile store and the run's reporter, as HEAD called it.
type headITunesHealWriter struct {
	store    ReconcileStore
	reporter sdk.Reporter
}

func (w headITunesHealWriter) RunITunesHeal(ctx context.Context, params json.RawMessage) error {
	return reconcile.RunITunesHeal(ctx, w.store, w.reporter, params)
}

// itunesHealEnv is what the core takes from its shell.
type itunesHealEnv struct {
	t6g6Env
	Writer itunesHealWriter
}

// itunesHealParallelism is the op's concurrency. HEAD declared none on the def:
// the heal runs its own 16-worker pool inside internal/reconcile.
func itunesHealParallelism() tasks.Parallelism {
	return tasks.Sequential("HEAD declared no concurrency on the def; reconcile.RunITunesHeal runs its own pool inside")
}

// itunesHealNative is the native kind. It reflinks files and repoints and merges
// rows, so it has no preview and refuses a Preview run.
func (p *Plugin) itunesHealNative(deps t6g6Deps, sched func(tasks.Kind) *tasks.Schedule) tasks.Definition {
	c := t6g6Common(itunesHealKind, sched)
	c.Title = "iTunes path heal"
	c.Help = itunesHealHelp
	c.Effects = tasks.Writes(tasks.Res("books", "books"), tasks.Res("books", "book_files"), tasks.Res("books", "operations"))
	c.Resume = tasks.ResumeContinue
	c.Priority = tasks.PriorityNormal
	c.ConcurrencyKey = tasks.Key("books.itunes.heal")
	c.Timeout = 60 * time.Minute
	return tasks.Task(itunesHealKind, tasks.TaskSpec[reconcile.ITunesHealParams, struct{}]{
		Common: c,
		Run: func(rc *tasks.RC, params reconcile.ITunesHealParams) (struct{}, error) {
			if err := t6g6LiveOnly(rc, itunesHealKind); err != nil {
				return struct{}{}, err
			}
			raw, err := json.Marshal(params)
			if err != nil {
				return struct{}{}, fmt.Errorf("encode params: %w", err)
			}
			env := itunesHealEnv{
				t6g6Env: t6g6NativeEnv(rc),
				Writer:  deps.NewWriter(rc.Writer()),
			}
			return struct{}{}, itunesHealCore(rc.Context(), raw, env)
		},
	})
}

// itunesHealCore is the body of runITunesHeal.
func itunesHealCore(ctx context.Context, params json.RawMessage, env itunesHealEnv) error {
	return env.Writer.RunITunesHeal(ctx, params)
}
