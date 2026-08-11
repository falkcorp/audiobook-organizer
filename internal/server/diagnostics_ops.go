// file: internal/server/diagnostics_ops.go
// version: 1.2.0
// guid: 7d8e9f0a-1b2c-3d4e-5f6a-7b8c9d0e1f2a
// last-edited: 2026-08-11

// diagnostics_ops registers the diagnostics export OperationDef (v2 UOS).

package server

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/auth"
	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/diagnostics"
	opsregistry "github.com/falkcorp/audiobook-organizer/internal/operations/registry"
	"github.com/falkcorp/audiobook-organizer/pkg/plugin/sdk"
)

type diagnosticsExportOpParams struct {
	LegacyOpID  string `json:"legacy_op_id"`
	Category    string `json:"category"`
	Description string `json:"description"`
}

// RegisterDiagnosticsExportOp registers the "diagnostics.export" v2 OperationDef.
func (s *Server) RegisterDiagnosticsExportOp(reg *opsregistry.Registry) error {
	return reg.RegisterOp(opsregistry.OperationDef{
		ID:              "diagnostics.export",
		Plugin:          "diagnostics",
		DisplayName:     "Export Diagnostics",
		Description:     "Generate a diagnostics ZIP export for analysis.",
		DefaultPriority: opsregistry.PriorityNormal,
		Cancellable:     false,
		Isolate:         false,
		Timeout:         30 * time.Minute,
		ResumePolicy:    opsregistry.ResumeDrop,
		ConcurrencyKey:  "diagnostics.export",
		Permissions:     []auth.Permission{auth.PermSettingsManage},
		Capabilities:    []opsregistry.Capability{opsregistry.CapLibraryRead},
		Run: func(ctx context.Context, rawParams json.RawMessage, reporter opsregistry.Reporter) error {
			var p diagnosticsExportOpParams
			if len(rawParams) > 0 {
				if err := json.Unmarshal(rawParams, &p); err != nil {
					return fmt.Errorf("diagnostics.export: decode params: %w", err)
				}
			}
			store := s.Store()
			ds := s.diagnosticsService
			if ds == nil {
				ds = diagnostics.NewService(store, nil, config.AppConfig.ITunes.LibraryReadPath)
			}
			prog := sdk.NewProgress(reporter, 0)
			prog.Start("Generating export data")
			zipPath, genErr := ds.GenerateExport(p.Category, p.Description)
			if genErr != nil {
				if store != nil {
					_ = store.UpdateOperationError(p.LegacyOpID, genErr.Error())
				}
				return fmt.Errorf("generate export: %w", genErr)
			}
			resultJSON, _ := json.Marshal(map[string]string{"zip_path": zipPath})
			if store != nil {
				_ = store.UpdateOperationResultData(p.LegacyOpID, string(resultJSON))
				_ = store.UpdateOperationStatus(p.LegacyOpID, "completed", 100, 100, "Export complete")
			}
			prog.Done("Export complete")
			return nil
		},
	})
}

func init() {
	addOpRegistrar(func(s *Server, reg *opsregistry.Registry) error { return s.RegisterDiagnosticsExportOp(reg) })
}
