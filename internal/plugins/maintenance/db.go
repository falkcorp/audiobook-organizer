// file: internal/plugins/maintenance/db.go
// version: 1.1.0
// guid: d4e5f6a7-b8c9-0123-def0-345678901234
// last-edited: 2026-08-19

package maintenance

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"github.com/falkcorp/audiobook-organizer/pkg/plugin/sdk"
)

func (p *Plugin) dbOptimizeDef() sdk.OperationDef {
	sched := "0 2 * * 0" // 02:00 every Sunday
	return sdk.OperationDef{
		ID:              "maintenance.db-optimize",
		Liveness:        sdk.LivenessManual,
		Plugin:          "maintenance",
		DisplayName:     "Optimize database",
		Description:     "Runs VACUUM/ANALYZE/WAL-checkpoint on all database stores.",
		ResumePolicy:    sdk.ResumeDrop,
		DefaultPriority: sdk.PriorityLow,
		ConcurrencyKey:  "maintenance.db-optimize",
		Cancellable:     false,
		Isolate:         false,
		Timeout:         60 * time.Minute,
		Schedule:        &sched,
		Capabilities:    []sdk.Capability{sdk.CapLibraryRead, sdk.CapLibraryWrite},
		Run:             p.runDBOptimize,
	}
}

func (p *Plugin) runDBOptimize(_ context.Context, _ json.RawMessage, reporter sdk.Reporter) error {
	store := p.deps.OpsStore()
	if store == nil {
		return fmt.Errorf("database not initialized")
	}

	storesOptimized := 0
	storesTotal := 3
	startTotal := time.Now()

	// 1. Main store
	_ = reporter.Log(slog.LevelInfo, "Optimizing main database (VACUUM, ANALYZE, WAL checkpoint)...")
	_ = reporter.UpdateProgress(0, storesTotal, "Optimizing main database...")
	t1 := time.Now()
	if err := store.Optimize(); err != nil {
		_ = reporter.Log(slog.LevelError, fmt.Sprintf("Main DB optimization failed: %v", err))
	} else {
		storesOptimized++
		_ = reporter.Log(slog.LevelInfo, fmt.Sprintf("Main database optimized in %s", time.Since(t1).Round(time.Millisecond)))
	}

	// 2. AI scan store
	_ = reporter.UpdateProgress(1, storesTotal, "Optimizing AI scan database...")
	if err := p.deps.OptimizeAIScanStore(); err != nil {
		_ = reporter.Log(slog.LevelError, fmt.Sprintf("AI scan DB optimization failed: %v", err))
	} else {
		storesOptimized++
	}

	// 3. OpenLibrary store
	_ = reporter.UpdateProgress(2, storesTotal, "Optimizing OpenLibrary cache...")
	if err := p.deps.OptimizeOLStore(); err != nil {
		_ = reporter.Log(slog.LevelError, fmt.Sprintf("OL cache optimization failed: %v", err))
	} else {
		storesOptimized++
	}

	_ = reporter.UpdateProgress(storesTotal, storesTotal,
		fmt.Sprintf("Database optimization complete: %d/%d stores in %s",
			storesOptimized, storesTotal, time.Since(startTotal).Round(time.Millisecond)))
	return nil
}
