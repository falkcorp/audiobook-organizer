// file: internal/plugins/maintenance/write_back.go
// version: 1.3.0
// guid: d0e1f2a3-b4c5-6789-3456-901234567890
// last-edited: 2026-11-19

package maintenance

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"github.com/falkcorp/mediacore/tasks"

	"github.com/falkcorp/audiobook-organizer/internal/operations"
	"github.com/falkcorp/audiobook-organizer/internal/tagger"

	"github.com/falkcorp/audiobook-organizer/pkg/plugin/sdk"
)

// Hard rule: bulk-write-back = ResumeAsk (file writes; operator must confirm).

// BulkWriteBackParams are the JSON parameters for bulk write-back.
type BulkWriteBackParams struct {
	BookIDs  []string `json:"book_ids"`
	Rename   bool     `json:"rename"`
	StartIdx int      `json:"start_idx"` // checkpoint resume index
}

// bulkWriteBackHelp is the def's Description, which the native kind carries as
// its Help.
const bulkWriteBackHelp = "Writes metadata tags back to files for a set of books. Interrupted runs surface in UI for operator confirmation before resuming."

// bulkWriteBackKind is rule R3's kind for the def below.
const bulkWriteBackKind = tasks.Kind("books.maintenance.bulk-write-back")

// bulkWriteBackWriter is the one write: the server's tag/rename pass over a set
// of books (delegated, so the whole of its call chain is one writer method; its
// row is in the shared table).
type bulkWriteBackWriter interface {
	RunBulkWriteBack(ctx context.Context, opID string, bookIDs []string, doRename bool, startIdx int, progress operations.ProgressReporter) error
}

func (p *Plugin) bulkWriteBackDef() sdk.OperationDef {
	return sdk.OperationDef{
		ID:              "maintenance.bulk-write-back",
		Liveness:        sdk.LivenessManual,
		Plugin:          "maintenance",
		DisplayName:     "Bulk write-back",
		Description:     bulkWriteBackHelp,
		ResumePolicy:    sdk.ResumeAsk,
		DefaultPriority: sdk.PriorityNormal,
		ConcurrencyKey:  "maintenance.bulk-write-back",
		Cancellable:     true,
		Isolate:         false,
		Timeout:         240 * time.Minute,
		Schedule:        nil,
		Capabilities: []sdk.Capability{
			sdk.CapLibraryRead, sdk.CapLibraryWrite, sdk.CapFilesRead, sdk.CapFilesWrite,
		},
		Run: p.runBulkWriteBack,
	}
}

func (p *Plugin) runBulkWriteBack(ctx context.Context, raw json.RawMessage, reporter sdk.Reporter) error {
	var params BulkWriteBackParams
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &params); err != nil {
			return fmt.Errorf("invalid params: %w", err)
		}
	}
	env := bulkWriteBackEnv{t6g7Env: t6g7HeadEnv(reporter, true), Writer: p.deps, Hold: p.t6g7HeadHold(reporter)}
	// HEAD hands the server the id the run's context carries, not the reporter's.
	env.OpID = ctxOpID(ctx)
	return bulkWriteBackCore(ctx, params, env)
}

// bulkWriteBackEnv is what the core takes from its shell.
type bulkWriteBackEnv struct {
	t6g7Env
	Writer bulkWriteBackWriter
	// Hold takes the scan stand-down (HEAD: registry.HoldScanStandDown; native:
	// rc.StandDown).
	Hold t6g7HoldFunc
}

// bulkWriteBackNative is the native kind. HEAD has no dry run for it, so a
// Preview run is refused. A write-back rewrites files, so an interrupted run is
// ResumeAsk, as HEAD's was: the operator confirms before it continues from
// start_idx.
func (p *Plugin) bulkWriteBackNative(deps t6g7Deps, sched func(tasks.Kind) *tasks.Schedule) tasks.Definition {
	c := t6g7Common(bulkWriteBackKind, sched)
	c.Title = "Bulk write-back"
	c.Help = bulkWriteBackHelp
	// The server's pass renames files, writes tags and the ledger, repoints the
	// book rows and logs the batch (the shared row, kinds_resources.go).
	c.Effects = tasks.Writes(t6g7Res(t6WriterResources["RunBulkWriteBack"]...)...)
	c.Resume = tasks.ResumeAsk
	c.Priority = tasks.PriorityNormal
	c.ConcurrencyKey = tasks.Key("books.maintenance.bulk-write-back")
	c.Timeout = 240 * time.Minute
	return tasks.Task(bulkWriteBackKind, tasks.TaskSpec[json.RawMessage, struct{}]{
		Common: c,
		Run: func(rc *tasks.RC, raw json.RawMessage) (struct{}, error) {
			if err := t6g7LiveOnly(rc, bulkWriteBackKind); err != nil {
				return struct{}{}, err
			}
			params, err := t6g7DecodeNative[BulkWriteBackParams](bulkWriteBackKind, raw)
			if err != nil {
				return struct{}{}, err
			}
			w, err := t6g7NewWriter(bulkWriteBackKind, deps, rc)
			if err != nil {
				return struct{}{}, err
			}
			env := bulkWriteBackEnv{t6g7Env: t6g7NativeEnv(rc), Writer: w}
			env.Hold = t6g7NativeHold(rc, deps.StandDownKey, env.t6g7Env)
			return struct{}{}, bulkWriteBackCore(rc.Context(), params, env)
		},
	})
}

// bulkWriteBackCore is the body of runBulkWriteBack.
func bulkWriteBackCore(ctx context.Context, params BulkWriteBackParams, env bulkWriteBackEnv) (retErr error) {
	// Bulk write-back keeps no .bak-* sibling per file (owner decision D69):
	// its safety net is the provenance ledger, and create_backups would double
	// the library's footprint until the backup-cleanup op runs.
	ctx = tagger.WithoutBackup(ctx)
	if len(params.BookIDs) == 0 {
		env.Log(slog.LevelInfo, "No book IDs specified, nothing to do")
		return nil
	}
	// Metadata is never applied during a library scan: hold the scan stand-down
	// before the first write (fails the op if the scan does not park).
	hold, sdErr := env.Hold(ctx, "maintenance.bulk-write-back apply")
	if sdErr != nil {
		return sdErr
	}
	defer func() { retErr = hold.Finish(retErr) }()
	return env.Writer.RunBulkWriteBack(hold.Ctx, env.OpID, params.BookIDs, params.Rename, params.StartIdx, hold.Progress)
}
