// file: internal/server/openlibrary_ops.go
// version: 1.1.0
// guid: 3c7e9a21-f4b5-4d68-8e2f-1a6c0b9d7f43
// last-edited: 2026-08-11

package server

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/auth"
	"github.com/falkcorp/audiobook-organizer/internal/openlibrary"
	opsregistry "github.com/falkcorp/audiobook-organizer/internal/operations/registry"
)

type olDownloadOpParams struct {
	LegacyOpID string   `json:"legacy_op_id"`
	Types      []string `json:"types"`
	TargetDir  string   `json:"target_dir"`
}

type olImportOpParams struct {
	LegacyOpID string   `json:"legacy_op_id"`
	Types      []string `json:"types"`
	TargetDir  string   `json:"target_dir"`
}

// RegisterOLDownloadOp registers the "openlibrary.download" v2 OperationDef.
func (s *Server) RegisterOLDownloadOp(reg *opsregistry.Registry) error {
	return reg.RegisterOp(opsregistry.OperationDef{
		ID:              "openlibrary.download",
		Plugin:          "openlibrary",
		DisplayName:     "OpenLibrary Dump Download",
		Description:     "Download OpenLibrary data dump files (editions, authors, works).",
		DefaultPriority: opsregistry.PriorityNormal,
		Cancellable:     true,
		Isolate:         false,
		Timeout:         8 * time.Hour,
		ResumePolicy:    opsregistry.ResumeDrop,
		ConcurrencyKey:  "openlibrary.download",
		Permissions:     []auth.Permission{auth.PermSettingsManage},
		Capabilities:    []opsregistry.Capability{opsregistry.CapNetworkOpenLibrary, opsregistry.CapFilesWrite},
		Run: func(ctx context.Context, rawParams json.RawMessage, reporter opsregistry.Reporter) error {
			var p olDownloadOpParams
			if len(rawParams) > 0 {
				if err := json.Unmarshal(rawParams, &p); err != nil {
					return fmt.Errorf("openlibrary.download: decode params: %w", err)
				}
			}
			tracker := s.olService.Tracker
			for i, dumpType := range p.Types {
				if reporter.IsCanceled() {
					return fmt.Errorf("download canceled")
				}
				_ = reporter.Log(slog.LevelInfo, fmt.Sprintf("Starting OL dump download: %s", dumpType))
				_ = reporter.UpdateProgress(i, len(p.Types), fmt.Sprintf("Downloading %s...", dumpType))
				if err := openlibrary.DownloadDump(dumpType, p.TargetDir, tracker); err != nil {
					_ = reporter.Log(slog.LevelError, fmt.Sprintf("OL dump download failed for %s: %v", dumpType, err))
					return fmt.Errorf("download failed for %s: %w", dumpType, err)
				}
				_ = reporter.Log(slog.LevelInfo, fmt.Sprintf("OL dump download complete: %s", dumpType))
			}
			_ = reporter.UpdateProgress(len(p.Types), len(p.Types), "All downloads complete")
			return nil
		},
	})
}

// RegisterOLImportOp registers the "openlibrary.import" v2 OperationDef.
func (s *Server) RegisterOLImportOp(reg *opsregistry.Registry) error {
	return reg.RegisterOp(opsregistry.OperationDef{
		ID:              "openlibrary.import",
		Plugin:          "openlibrary",
		DisplayName:     "OpenLibrary Dump Import",
		Description:     "Import OpenLibrary data dump files into the local search store.",
		DefaultPriority: opsregistry.PriorityNormal,
		Cancellable:     true,
		Isolate:         false,
		Timeout:         12 * time.Hour,
		ResumePolicy:    opsregistry.ResumeDrop,
		ConcurrencyKey:  "openlibrary.import",
		Permissions:     []auth.Permission{auth.PermSettingsManage},
		Capabilities:    []opsregistry.Capability{opsregistry.CapLibraryRead, opsregistry.CapLibraryWrite, opsregistry.CapFilesRead},
		Run: func(ctx context.Context, rawParams json.RawMessage, reporter opsregistry.Reporter) error {
			var p olImportOpParams
			if len(rawParams) > 0 {
				if err := json.Unmarshal(rawParams, &p); err != nil {
					return fmt.Errorf("openlibrary.import: decode params: %w", err)
				}
			}
			svc := s.olService
			if err := svc.EnsureStore(p.TargetDir); err != nil {
				return fmt.Errorf("failed to open store: %w", err)
			}
			progress := registryProgressAdapter{r: reporter}
			return svc.Import(ctx, progress, p.TargetDir, p.Types)
		},
	})
}

func init() {
	addOpRegistrar(func(s *Server, reg *opsregistry.Registry) error { return s.RegisterOLDownloadOp(reg) })
	addOpRegistrar(func(s *Server, reg *opsregistry.Registry) error { return s.RegisterOLImportOp(reg) })
}
