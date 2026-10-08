// file: internal/server/itunes_ops.go
// version: 1.4.0
// guid: 4b7e9f2a-1c3d-4e5f-8a9b-0c1d2e3f4a5b
// last-edited: 2026-10-08

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

	"github.com/falkcorp/audiobook-organizer/internal/activity"
	"github.com/falkcorp/audiobook-organizer/internal/auth"
	itunesservice "github.com/falkcorp/audiobook-organizer/internal/itunes/service"
	"github.com/falkcorp/audiobook-organizer/internal/operations"
	opsregistry "github.com/falkcorp/audiobook-organizer/internal/operations/registry"
)

type itunesImportOpParams struct {
	Request itunesservice.ImportRequest `json:"request"`
}

// RegisterITunesImportOp registers the "itunes.import" v2 OperationDef.
func (s *Server) RegisterITunesImportOp(reg *opsregistry.Registry) error {
	return reg.RegisterOp(opsregistry.OperationDef{
		ID:              "itunes.import",
		Liveness:        opsregistry.LivenessManual,
		Plugin:          "itunes",
		DisplayName:     "iTunes Import",
		Description:     "Import audiobooks from an iTunes XML library file into the database.",
		DefaultPriority: opsregistry.PriorityNormal,
		Cancellable:     true,
		Isolate:         false,
		Timeout:         4 * time.Hour,
		ResumePolicy:    opsregistry.ResumeDrop,
		ConcurrencyKey:  "itunes.import",
		Permissions:     []auth.Permission{auth.PermIntegrationsManage},
		Capabilities:    []opsregistry.Capability{opsregistry.CapLibraryRead, opsregistry.CapLibraryWrite, opsregistry.CapNetworkITunes},
		Run: func(ctx context.Context, rawParams json.RawMessage, reporter opsregistry.Reporter) error {
			var p itunesImportOpParams
			if len(rawParams) > 0 {
				if err := json.Unmarshal(rawParams, &p); err != nil {
					return fmt.Errorf("itunes-import: decode params: %w", err)
				}
			}
			// opID is the v2 run's own id. It keys the importer's checkpoint
			// state and the activity log. No v1 row is written here: the worker
			// records this run's terminal status on the v2 row itself.
			opID := opsregistry.ReporterOpID(reporter)
			progress := registryProgressAdapter{r: reporter}
			runErr := s.itunesSvc.Importer.Execute(ctx, opID, p.Request, operations.LoggerFromReporter(progress))
			if s.activityWriter != nil {
				activity.FlushOperation(s.activityWriter, opID)
				summary := "iTunes import completed"
				if runErr != nil {
					summary = fmt.Sprintf("iTunes import failed: %v", runErr)
				}
				activity.EmitInfo(s.activityWriter, opID, "itunes.import", "itunes", summary, activity.AlwaysShow)
			}
			return runErr
		},
	})
}

func init() {
	addOpRegistrar(func(s *Server, reg *opsregistry.Registry) error { return s.RegisterITunesImportOp(reg) })
}
