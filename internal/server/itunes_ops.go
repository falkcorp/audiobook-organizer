// file: internal/server/itunes_ops.go
// version: 1.2.0
// guid: 4b7e9f2a-1c3d-4e5f-8a9b-0c1d2e3f4a5b
// last-edited: 2026-08-19

// itunes_ops registers v2 OperationDefs for iTunes import and sync.
// Both ops use the hybrid migration pattern: a v1 op record is created
// in the handler so clients can still poll /api/v1/operations/{id}/status,
// and the v1 op ID is passed into the params struct for use by the Run func.

package server

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/activity"
	"github.com/falkcorp/audiobook-organizer/internal/auth"
	"github.com/falkcorp/audiobook-organizer/internal/itunes"
	itunesservice "github.com/falkcorp/audiobook-organizer/internal/itunes/service"
	"github.com/falkcorp/audiobook-organizer/internal/operations"
	opsregistry "github.com/falkcorp/audiobook-organizer/internal/operations/registry"
)

type itunesImportOpParams struct {
	LegacyOpID string                      `json:"legacy_op_id"`
	Request    itunesservice.ImportRequest `json:"request"`
}

type itunesSyncOpParams struct {
	LegacyOpID   string               `json:"legacy_op_id"`
	LibraryPath  string               `json:"library_path"`
	PathMappings []itunes.PathMapping `json:"path_mappings"`
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
			progress := registryProgressAdapter{r: reporter}
			runErr := s.itunesSvc.Importer.Execute(ctx, p.LegacyOpID, p.Request, operations.LoggerFromReporter(progress))
			// Bridge v2 run completion back to the legacy v1 row so HTTP
			// callers that received legacy_op_id can poll completion.
			if p.LegacyOpID != "" && s.Ops() != nil {
				if runErr != nil {
					_ = s.Ops().UpdateOperationStatus(p.LegacyOpID, "failed", 0, 0, runErr.Error())
				} else {
					_ = s.Ops().UpdateOperationStatus(p.LegacyOpID, "completed", 0, 0, "iTunes import completed")
				}
				if s.activityWriter != nil {
					activity.FlushOperation(s.activityWriter, p.LegacyOpID)
					summary := "iTunes import completed"
					if runErr != nil {
						summary = fmt.Sprintf("iTunes import failed: %v", runErr)
					}
					activity.EmitInfo(s.activityWriter, p.LegacyOpID, "itunes.import", "itunes", summary, activity.AlwaysShow)
				}
			}
			return runErr
		},
	})
}

// RegisterITunesSyncOp registers the "itunes.sync" v2 OperationDef.
func (s *Server) RegisterITunesSyncOp(reg *opsregistry.Registry) error {
	return reg.RegisterOp(opsregistry.OperationDef{
		ID:              "itunes.sync",
		Liveness:        opsregistry.LivenessManual,
		Plugin:          "itunes",
		DisplayName:     "iTunes Sync",
		Description:     "Sync the iTunes library XML into the database (incremental, fingerprint-gated).",
		DefaultPriority: opsregistry.PriorityNormal,
		Cancellable:     true,
		Isolate:         false,
		Timeout:         2 * time.Hour,
		ResumePolicy:    opsregistry.ResumeDrop,
		ConcurrencyKey:  "itunes.sync",
		Permissions:     []auth.Permission{auth.PermIntegrationsManage},
		Capabilities:    []opsregistry.Capability{opsregistry.CapLibraryRead, opsregistry.CapLibraryWrite, opsregistry.CapNetworkITunes},
		Run: func(ctx context.Context, rawParams json.RawMessage, reporter opsregistry.Reporter) error {
			var p itunesSyncOpParams
			if len(rawParams) > 0 {
				if err := json.Unmarshal(rawParams, &p); err != nil {
					return fmt.Errorf("itunes-sync: decode params: %w", err)
				}
			}
			progress := registryProgressAdapter{r: reporter}
			syncErr := s.itunesSvc.Importer.Sync(ctx, p.LibraryPath, p.PathMappings, s.itunesActivityFn, operations.LoggerFromReporter(progress))
			if s.activityWriter != nil && p.LegacyOpID != "" {
				activity.FlushOperation(s.activityWriter, p.LegacyOpID)
				summary := "iTunes sync completed"
				if syncErr != nil {
					summary = fmt.Sprintf("iTunes sync failed: %v", syncErr)
				}
				activity.EmitInfo(s.activityWriter, p.LegacyOpID, "itunes.sync", "itunes", summary, activity.AlwaysShow)
			}
			return syncErr
		},
	})
}

func init() {
	addOpRegistrar(func(s *Server, reg *opsregistry.Registry) error { return s.RegisterITunesImportOp(reg) })
	addOpRegistrar(func(s *Server, reg *opsregistry.Registry) error { return s.RegisterITunesSyncOp(reg) })
}
