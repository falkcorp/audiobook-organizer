// file: internal/server/diagnostics_ops.go
// version: 1.5.0
// guid: 7d8e9f0a-1b2c-3d4e-5f6a-7b8c9d0e1f2a
// last-edited: 2026-08-22

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
)

type diagnosticsExportOpParams struct {
	Category    string `json:"category"`
	Description string `json:"description"`
}

// RegisterDiagnosticsExportOp registers the "diagnostics.export" v2 OperationDef.
func (s *Server) RegisterDiagnosticsExportOp(reg *opsregistry.Registry) error {
	return reg.RegisterOp(opsregistry.OperationDef{
		ID:              "diagnostics.export",
		Liveness:        opsregistry.LivenessManual,
		Plugin:          "diagnostics",
		DisplayName:     "Export Diagnostics",
		Description:     "Generate a diagnostics ZIP export for analysis.",
		DefaultPriority: opsregistry.PriorityNormal,
		Cancellable:     false,
		Isolate:         false,
		Timeout:         30 * time.Minute,
		// Without this the watchdog applies its 5-minute default, so the
		// 30-minute Timeout above was never the real budget: any export quieter
		// than 5 minutes got cancelled. GenerateExport now reports once per
		// phase, which alone would fix it — this makes the declared budget match
		// the one actually enforced, rather than leaving the op one refactor
		// away from silent death again.
		ProgressTimeout: 30 * time.Minute,
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
			store := s.storeForWiring()
			ds := s.diagnosticsService
			if ds == nil {
				ds = diagnostics.NewService(store, nil, config.AppConfig.ITunes.LibraryReadPath)
			}
			// Forwards straight to the reporter rather than through sdk.Progress:
			// GenerateExport counts its own phases (which vary by category) and
			// reports real (current, total) pairs, so wrapping it in a second
			// counter would only let the two disagree.
			zipPath, genErr := ds.GenerateExport(ctx, p.Category, p.Description,
				func(current, total int, message string) {
					_ = reporter.UpdateProgress(current, total, message)
				})
			if genErr != nil {
				return fmt.Errorf("generate export: %w", genErr)
			}
			// The zip path is this op's result payload, stored on its own v2 row
			// and read back by DownloadExport. Persisting it is not bookkeeping we
			// can lose: a completed export whose path never landed is a download
			// that 500s with "no result data", so a failure here fails the op.
			//
			// Status and error are NOT written here. The v2 worker derives both
			// from this function's return value, which is why the three legacy
			// row updates this replaced are gone rather than translated.
			if err := opsregistry.ReporterSetResult(reporter, map[string]string{"zip_path": zipPath}); err != nil {
				return fmt.Errorf("persist export result: %w", err)
			}
			return nil
		},
	})
}

func init() {
	addOpRegistrar(func(s *Server, reg *opsregistry.Registry) error { return s.RegisterDiagnosticsExportOp(reg) })
}
