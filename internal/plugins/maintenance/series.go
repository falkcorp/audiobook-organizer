// file: internal/plugins/maintenance/series.go
// version: 1.1.0
// guid: f6a7b8c9-d0e1-2345-f012-567890123456
// last-edited: 2026-08-14

package maintenance

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"github.com/falkcorp/audiobook-organizer/pkg/plugin/sdk"
)

// --- series-normalize ---

func (p *Plugin) seriesNormalizeDef() sdk.OperationDef {
	return sdk.OperationDef{
		ID:              "maintenance.series-normalize",
		Plugin:          "maintenance",
		DisplayName:     "Normalize series names",
		Description:     "Strips title/position contamination from series names and enqueues affected books for write-back.",
		ResumePolicy:    sdk.ResumeRequeue,
		DefaultPriority: sdk.PriorityLow,
		ConcurrencyKey:  "maintenance.series-normalize",
		Cancellable:     false,
		Isolate:         false,
		Timeout:         30 * time.Minute,
		Schedule:        nil,
		Capabilities:    []sdk.Capability{sdk.CapLibraryRead, sdk.CapLibraryWrite},
		Run:             p.runSeriesNormalize,
	}
}

func (p *Plugin) runSeriesNormalize(ctx context.Context, _ json.RawMessage, reporter sdk.Reporter) error {
	store := p.deps.Store()
	if store == nil {
		return fmt.Errorf("database not initialized")
	}
	enqueueWB := func(bookID string) {
		p.deps.EnqueueWriteBack(bookID)
	}
	affected, err := p.deps.ExecuteSeriesNormalizeCore(ctx, store, enqueueWB)
	// Renaming a series changes the cached series list, which carries a 24-hour
	// TTL. Without this the normalize lands in the store while /api/v1/series
	// keeps serving the old names. Only invalidate when rows actually changed;
	// dropping a warm cache for a no-op run costs a full recount for nothing.
	if len(affected) > 0 {
		p.deps.InvalidateSeriesCache()
	}
	msg := fmt.Sprintf("Series normalize complete: %d series affected, %d books enqueued for write-back",
		len(affected), len(affected))
	_ = reporter.Log(slog.LevelInfo, msg)
	return err
}

// --- series-prune ---

func (p *Plugin) seriesPruneDef() sdk.OperationDef {
	sched := "0 3 * * 2" // 03:00 every Tuesday
	return sdk.OperationDef{
		ID:              "maintenance.series-prune",
		Plugin:          "maintenance",
		DisplayName:     "Prune duplicate series",
		Description:     "Merges duplicate series and deletes orphan series records.",
		ResumePolicy:    sdk.ResumeRequeue,
		DefaultPriority: sdk.PriorityLow,
		ConcurrencyKey:  "maintenance.series-prune",
		Cancellable:     false,
		Isolate:         false,
		Timeout:         30 * time.Minute,
		Schedule:        &sched,
		Capabilities:    []sdk.Capability{sdk.CapLibraryRead, sdk.CapLibraryWrite},
		Run:             p.runSeriesPrune,
	}
}

func (p *Plugin) runSeriesPrune(ctx context.Context, _ json.RawMessage, reporter sdk.Reporter) error {
	store := p.deps.Store()
	if store == nil {
		return fmt.Errorf("database not initialized")
	}
	opID := ctxOpID(ctx)
	return p.deps.ExecuteSeriesPrune(ctx, store, newOpsAdapter(reporter), opID)
}
