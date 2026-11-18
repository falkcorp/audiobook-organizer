// file: internal/server/itunes_ops.go
// version: 1.5.0
// guid: 4b7e9f2a-1c3d-4e5f-8a9b-0c1d2e3f4a5b
// last-edited: 2026-11-18

// itunes_ops registers the v2 OperationDef for iTunes import, the one iTunes
// action. The incremental "itunes.sync" op was removed on 2026-10-08 (owner
// decision: import is manual only, and re-running it links rather than
// duplicates).
//
// The op is v2-NATIVE: no v1 operations row is created, and no legacy op id
// is threaded through the params. Everything that needs an operation id — the
// importer's checkpoint keyspace, the activity log — takes it from the reporter
// via registry.ReporterOpID, so there is exactly one id per run.
//
// It previously used the hybrid pattern (handler mints a v1 row, passes its id
// as params.LegacyOpID). That id was load-bearing in four separate ways, and the
// v1 row it named had to be updated by hand at the end of each Run — a write
// nobody checked, which is the shape that stranded 1,737 v1 rows at "pending".

package server

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/falkcorp/mediacore/tasks"

	"github.com/falkcorp/audiobook-organizer/internal/activity"
	"github.com/falkcorp/audiobook-organizer/internal/auth"
	itunesservice "github.com/falkcorp/audiobook-organizer/internal/itunes/service"
	"github.com/falkcorp/audiobook-organizer/internal/logger"
	"github.com/falkcorp/audiobook-organizer/internal/operations"
	opsregistry "github.com/falkcorp/audiobook-organizer/internal/operations/registry"
	"github.com/falkcorp/audiobook-organizer/pkg/ops"
)

// itunesImportOpID is HEAD's def ID for the iTunes import.
const itunesImportOpID = "itunes.import"

type itunesImportOpParams struct {
	Request itunesservice.ImportRequest `json:"request"`
}

// itunesImportWriter is every write the iTunes import makes: the importer's own
// library writes (books, files, authors, series, playlists, external ids), and
// the activity-log rows it closes the run with. The importer holds its store
// itself, so the import is one method here rather than a store call.
type itunesImportWriter interface {
	// ImportITunesLibrary runs the importer for the run keyed by opID.
	ImportITunesLibrary(ctx context.Context, opID string, req itunesservice.ImportRequest, log logger.Logger) error
	t7ActivityWriter
}

// itunesImportEnv is what the shared core takes from its shell (rules R2, R10).
type itunesImportEnv struct {
	t7Env
	Writer itunesImportWriter
}

// itunesImportCore is the body of the op, shared by HEAD's shell and the native
// kind. env.OpID is the run's own id: it keys the importer's checkpoint state
// and the activity log. No v1 row is written here.
func itunesImportCore(ctx context.Context, p itunesImportOpParams, env itunesImportEnv) error {
	if env.Writer == nil {
		return fmt.Errorf("itunes-import: iTunes importer not initialized")
	}
	runErr := env.Writer.ImportITunesLibrary(ctx, env.OpID, p.Request, operations.LoggerFromReporter(env.reporter()))
	env.Writer.FlushActivityOp(env.OpID)
	summary := "iTunes import completed"
	if runErr != nil {
		summary = fmt.Sprintf("iTunes import failed: %v", runErr)
	}
	env.Writer.EmitActivityInfo(env.OpID, "itunes.import", "itunes", summary, activity.AlwaysShow)
	return runErr
}

// headITunesImportWriter is HEAD's write side: the service's importer and the
// server's activity writer, each called as HEAD's body called it.
type headITunesImportWriter struct {
	headT7Activity
	importer *itunesservice.Importer
}

func (w headITunesImportWriter) ImportITunesLibrary(ctx context.Context, opID string, req itunesservice.ImportRequest, log logger.Logger) error {
	return w.importer.Execute(ctx, opID, req, log)
}

// itunesImportDef is HEAD's definition. Its Run is the v2 shell.
func (s *Server) itunesImportDef() opsregistry.OperationDef {
	return opsregistry.OperationDef{
		ID:              itunesImportOpID,
		Liveness:        opsregistry.LivenessManual,
		Plugin:          "itunes",
		DisplayName:     "iTunes Import",
		Description:     "Import audiobooks from an iTunes XML library file into the database.",
		DefaultPriority: opsregistry.PriorityNormal,
		Cancellable:     true,
		Isolate:         false,
		Timeout:         4 * time.Hour,
		ResumePolicy:    opsregistry.ResumeDrop,
		ConcurrencyKey:  itunesImportOpID,
		Permissions:     []auth.Permission{auth.PermIntegrationsManage},
		Capabilities:    []opsregistry.Capability{opsregistry.CapLibraryRead, opsregistry.CapLibraryWrite, opsregistry.CapNetworkITunes},
		Run: func(ctx context.Context, rawParams json.RawMessage, reporter opsregistry.Reporter) error {
			var p itunesImportOpParams
			if len(rawParams) > 0 {
				if err := json.Unmarshal(rawParams, &p); err != nil {
					return fmt.Errorf("itunes-import: decode params: %w", err)
				}
			}
			env := itunesImportEnv{t7Env: t7HeadEnv(reporter, true)}
			if s.itunesSvc != nil {
				env.Writer = headITunesImportWriter{importer: s.itunesSvc.Importer, headT7Activity: headT7Activity{w: s.activityWriter}}
			}
			return itunesImportCore(ctx, p, env)
		},
	}
}

// RegisterITunesImportOp registers the "itunes.import" v2 OperationDef.
func (s *Server) RegisterITunesImportOp(reg *opsregistry.Registry) error {
	return reg.RegisterOp(s.itunesImportDef())
}

// itunesImportNative is the native books.itunes.import kind. HEAD runs it only
// live (no preview, a library-write capability), so a Preview run is refused.
// It writes the library the importer fills, and the activity log.
func (s *Server) itunesImportNative(deps t7Deps, sched func(tasks.Kind) *tasks.Schedule) tasks.Definition {
	kind := t7KindFor(itunesImportOpID)
	c := t7Common(s.itunesImportDef(), sched)
	c.Effects = t7Writes(ops.ResBooks, ops.ResBookFiles, ops.ResAuthors, ops.ResSeries, ops.ResExternalIDs, ops.ResUserState, ops.ResSettings, ops.ResActivity)
	return tasks.Task(kind, tasks.TaskSpec[json.RawMessage, struct{}]{
		Common: c,
		Run: func(rc *tasks.RC, raw json.RawMessage) (struct{}, error) {
			if err := t7LiveOnly(rc, kind); err != nil {
				return struct{}{}, err
			}
			p, err := t7DecodeNative[itunesImportOpParams](kind, raw)
			if err != nil {
				return struct{}{}, err
			}
			w, err := t7NewWriter(kind, deps, rc)
			if err != nil {
				return struct{}{}, err
			}
			return struct{}{}, itunesImportCore(rc.Context(), p, itunesImportEnv{t7Env: t7NativeEnv(rc), Writer: w})
		},
	})
}

func init() {
	addOpRegistrar(func(s *Server, reg *opsregistry.Registry) error { return s.RegisterITunesImportOp(reg) })
}
