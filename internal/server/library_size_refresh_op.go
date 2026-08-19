// file: internal/server/library_size_refresh_op.go
// version: 1.1.0
// guid: 9f1c2d3e-4b5a-6c7d-8e9f-0a1b2c3d4e5f
// last-edited: 2026-08-19

// library.size-refresh: walks the library root + import-path trees to
// recompute physical on-disk sizes. Runs nightly via the maintenance
// window and can be triggered manually from /scheduler.

package server

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/auth"
	"github.com/falkcorp/audiobook-organizer/internal/config"
	opsregistry "github.com/falkcorp/audiobook-organizer/internal/operations/registry"
)

// RegisterLibrarySizeRefreshOp registers the "library.size-refresh"
// OperationDef. The op invalidates the package-level size cache and then
// calls calculateLibrarySizes to repopulate it from a fresh filesystem
// walk. Used by both the manual /scheduler trigger and the nightly
// maintenance.window run.
func (s *Server) RegisterLibrarySizeRefreshOp(reg *opsregistry.Registry) error {
	return reg.RegisterOp(opsregistry.OperationDef{
		ID:              "library.size-refresh",
		Liveness:        opsregistry.LivenessManual,
		Plugin:          "library",
		DisplayName:     "Library Size Refresh",
		Description:     "Walk the library + import-path trees to refresh on-disk size cache.",
		DefaultPriority: opsregistry.PriorityLow,
		Cancellable:     true,
		Isolate:         false,
		Timeout:         2 * time.Hour,
		ResumePolicy:    opsregistry.ResumeDrop,
		ConcurrencyKey:  "library.size-refresh",
		Permissions:     []auth.Permission{auth.PermSettingsManage},
		Capabilities:    []opsregistry.Capability{opsregistry.CapLibraryRead},
		Run: func(ctx context.Context, _ json.RawMessage, reporter opsregistry.Reporter) error {
			store := s.Store()
			if store == nil {
				return fmt.Errorf("library.size-refresh: database not initialized")
			}
			folders, err := store.GetAllImportPaths()
			if err != nil {
				return err
			}
			rootDir := strings.TrimSpace(config.AppConfig.RootDir)

			progress := registryProgressAdapter{r: reporter}
			_ = progress.Log("info", fmt.Sprintf("library size refresh starting (root=%s, import_folders=%d)", rootDir, len(folders)), nil)
			_ = progress.UpdateProgress(0, 1, "walking filesystem")

			// Invalidate the cache so calculateLibrarySizes does a real walk
			// instead of returning the existing cached value.
			resetLibrarySizeCache()
			started := time.Now()
			lib, imp := calculateLibrarySizes(rootDir, folders)
			elapsed := time.Since(started)

			slog.Info("library size refresh complete",
				"library_bytes", lib,
				"import_bytes", imp,
				"duration_ms", elapsed.Milliseconds(),
			)
			_ = progress.UpdateProgress(1, 1, "done")
			_ = progress.Log("info",
				fmt.Sprintf("library size refresh complete: library=%d import=%d (%dms)", lib, imp, elapsed.Milliseconds()),
				nil)
			return nil
		},
	})
}

func init() {
	addOpRegistrar(func(s *Server, reg *opsregistry.Registry) error { return s.RegisterLibrarySizeRefreshOp(reg) })
}
