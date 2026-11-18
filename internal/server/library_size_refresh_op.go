// file: internal/server/library_size_refresh_op.go
// version: 1.2.0
// guid: 9f1c2d3e-4b5a-6c7d-8e9f-0a1b2c3d4e5f
// last-edited: 2026-11-18

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

	"github.com/falkcorp/mediacore/tasks"

	"github.com/falkcorp/audiobook-organizer/internal/auth"
	database "github.com/falkcorp/audiobook-organizer/internal/books/store"
	"github.com/falkcorp/audiobook-organizer/internal/config"
	opsregistry "github.com/falkcorp/audiobook-organizer/internal/operations/registry"
)

// librarySizeRefreshOpID is the HEAD def ID; the native kind is "books." plus it.
const librarySizeRefreshOpID = "library.size-refresh"

// librarySizeRefreshStore is the one store read the size refresh makes.
type librarySizeRefreshStore interface {
	GetAllImportPaths() ([]database.ImportPath, error)
}

// librarySizeRefreshEnv is what the shared core takes from its shell (rule R2,
// R10): the run's hooks and the store it reads.
type librarySizeRefreshEnv struct {
	t7Env
	Store librarySizeRefreshStore
}

// librarySizeRefreshCore is the body of the op, shared by HEAD's shell and the
// native kind. It invalidates the package-level size cache and then calls
// calculateLibrarySizes to repopulate it from a fresh filesystem walk.
func librarySizeRefreshCore(_ context.Context, env librarySizeRefreshEnv) error {
	if env.Store == nil {
		return fmt.Errorf("%s: database not initialized", librarySizeRefreshOpID)
	}
	folders, err := env.Store.GetAllImportPaths()
	if err != nil {
		return err
	}
	rootDir := strings.TrimSpace(config.AppConfig.RootDir)

	progress := env.reporter()
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
}

// librarySizeRefreshDef is HEAD's definition. Its Run is the v2 shell: it fills
// the core's env from the Reporter and the server's store.
func (s *Server) librarySizeRefreshDef() opsregistry.OperationDef {
	return opsregistry.OperationDef{
		ID:              librarySizeRefreshOpID,
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
			env := librarySizeRefreshEnv{t7Env: t7HeadEnv(reporter, true)}
			if store := s.Ops(); store != nil {
				env.Store = store
			}
			return librarySizeRefreshCore(ctx, env)
		},
	}
}

// RegisterLibrarySizeRefreshOp registers the "library.size-refresh"
// OperationDef. The op invalidates the package-level size cache and then
// calls calculateLibrarySizes to repopulate it from a fresh filesystem
// walk. Used by both the manual /scheduler trigger and the nightly
// maintenance.window run.
func (s *Server) RegisterLibrarySizeRefreshOp(reg *opsregistry.Registry) error {
	return reg.RegisterOp(s.librarySizeRefreshDef())
}

// librarySizeRefreshNative is the native books.library.size-refresh kind. It
// writes no store (the size cache is in memory), so it is ReadOnly.
func (s *Server) librarySizeRefreshNative(deps t7Deps, sched func(tasks.Kind) *tasks.Schedule) tasks.Definition {
	c := t7Common(s.librarySizeRefreshDef(), sched)
	c.Effects = tasks.ReadOnly()
	return tasks.Task(t7KindFor(librarySizeRefreshOpID), tasks.TaskSpec[json.RawMessage, struct{}]{
		Common: c,
		Run: func(rc *tasks.RC, _ json.RawMessage) (struct{}, error) {
			env := librarySizeRefreshEnv{t7Env: t7NativeEnv(rc)}
			if deps.Store != nil {
				env.Store = deps.Store
			}
			return struct{}{}, librarySizeRefreshCore(rc.Context(), env)
		},
	})
}

func init() {
	addOpRegistrar(func(s *Server, reg *opsregistry.Registry) error { return s.RegisterLibrarySizeRefreshOp(reg) })
}
