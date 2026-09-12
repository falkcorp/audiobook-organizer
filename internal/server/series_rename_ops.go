// file: internal/server/series_rename_ops.go
// version: 1.0.0
// guid: f9b6a9b0-62cc-40eb-8c93-e281b6dce050
// last-edited: 2026-09-12

// series_rename_ops registers entities.series-rename, the queued operation
// behind PUT /series/:id/name and PATCH /series/:id. Both endpoints used to
// rename synchronously and record nothing, so a rename could not be undone.
// The op performs the same rename and journals one series_rename change row
// under its own op id, which RevertService.RevertOperation already knows how
// to reverse (compare-and-set against the recorded new name).

package server

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/auth"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/logger"
	opsregistry "github.com/falkcorp/audiobook-organizer/internal/operations/registry"
	"github.com/falkcorp/audiobook-organizer/internal/undo"
	ulid "github.com/oklog/ulid/v2"
)

// seriesRenameOpID is the OperationDef id the entities handlers enqueue.
const seriesRenameOpID = "entities.series-rename"

// seriesRenameOpParams holds the parameters for entities.series-rename. The
// entities package mirrors it; the json tags MUST stay identical.
//
// There is deliberately no old-name field: the op reads the current name
// itself, immediately before the write, so a rename queued behind another
// rename journals the name it actually replaced.
type seriesRenameOpParams struct {
	SeriesID int    `json:"series_id"`
	Name     string `json:"name"`
}

// seriesRenameStore is the store surface the op needs.
type seriesRenameStore interface {
	GetSeriesByID(id int) (*database.Series, error)
	UpdateSeriesName(id int, name string) error
	CreateOperationChange(change *database.OperationChange) error
}

// runSeriesRename renames series seriesID to name and journals the undo row.
// It returns changed=false, and writes nothing, when the series already has
// that exact name.
//
// The journal row is written BEFORE the rename, so every rename that lands has
// its undo row. If the rename then fails, the row names a change that never
// happened; the revert's compare-and-set treats that row as already restored
// (the series still holds OldValue), so it is harmless. The other order would
// leave a completed rename with no way back whenever the journal write failed.
//
// The write is UpdateSeriesName, exactly as the synchronous handlers did. That
// method does not refuse a name another series under the same author already
// holds (it overwrites the series:name: index key), and neither did the
// handlers, so collisions behave as before.
func runSeriesRename(store seriesRenameStore, opID string, seriesID int, name string) (changed bool, err error) {
	series, err := store.GetSeriesByID(seriesID)
	if err != nil {
		return false, fmt.Errorf("series-rename: read series %d: %w", seriesID, err)
	}
	if series == nil {
		return false, fmt.Errorf("series-rename: series %d not found", seriesID)
	}
	oldName := series.Name
	if oldName == name {
		return false, nil
	}
	id := seriesID
	if err := store.CreateOperationChange(&database.OperationChange{
		ID:          ulid.Make().String(),
		OperationID: opID,
		SeriesID:    &id,
		ChangeType:  undo.ChangeTypeSeriesRename,
		FieldName:   "series_name",
		OldValue:    oldName,
		NewValue:    name,
	}); err != nil {
		return false, fmt.Errorf("series-rename: journal undo row for series %d: %w", seriesID, err)
	}
	if err := store.UpdateSeriesName(seriesID, name); err != nil {
		return false, fmt.Errorf("series-rename: rename series %d: %w", seriesID, err)
	}
	return true, nil
}

// RegisterSeriesRenameOp registers the "entities.series-rename" v2 OperationDef.
func (s *Server) RegisterSeriesRenameOp(reg *opsregistry.Registry) error {
	return reg.RegisterOp(opsregistry.OperationDef{
		ID:              seriesRenameOpID,
		Liveness:        opsregistry.LivenessManual,
		Plugin:          "entities",
		DisplayName:     "Series Rename",
		Description:     "Rename a series. Undoable from the operation's revert.",
		DefaultPriority: opsregistry.PriorityNormal,
		Cancellable:     false,
		Isolate:         false,
		Timeout:         5 * time.Minute,
		// ResumeDrop: the journal row is written before the rename, so a re-run
		// after a crash between the two could add a second row under the same op
		// id. The rename is one write; an interrupted one surfaces as
		// interrupted_dropped for the user to re-issue.
		ResumePolicy:   opsregistry.ResumeDrop,
		ConcurrencyKey: seriesRenameOpID,
		Permissions:    []auth.Permission{auth.PermLibraryEditMetadata},
		Capabilities:   []opsregistry.Capability{opsregistry.CapLibraryRead, opsregistry.CapLibraryWrite},
		Run: func(ctx context.Context, rawParams json.RawMessage, reporter opsregistry.Reporter) error {
			var p seriesRenameOpParams
			if err := json.Unmarshal(rawParams, &p); err != nil {
				return fmt.Errorf("series-rename: decode params: %w", err)
			}
			if p.SeriesID <= 0 || p.Name == "" {
				return fmt.Errorf("series-rename: invalid params (series_id %d, empty name %t)", p.SeriesID, p.Name == "")
			}
			opID := opsregistry.ReporterOpID(reporter)
			// s.store, not s.Ops(): ServerOpsStore does not carry
			// UpdateSeriesName, and the synchronous handlers wrote through the
			// same full store.
			changed, err := runSeriesRename(s.store, opID, p.SeriesID, p.Name)
			if err != nil {
				return err
			}
			if changed {
				if s.dedupCache != nil {
					s.dedupCache.Invalidate("series-duplicates")
				}
				if s.seriesCache != nil {
					s.seriesCache.InvalidateAll()
				}
			}
			msg := fmt.Sprintf("Renamed series %d to %q", p.SeriesID, p.Name)
			if !changed {
				msg = fmt.Sprintf("Series %d already named %q; nothing to do", p.SeriesID, p.Name)
			}
			_ = reporter.Log(slog.LevelInfo, msg)
			slog.Info("series rename op finished", "op", logger.SanitizeLogValue(opID),
				"series_id", p.SeriesID, "name", logger.SanitizeLogValue(p.Name), "changed", changed)
			return nil
		},
	})
}

func init() {
	addOpRegistrar(func(s *Server, reg *opsregistry.Registry) error { return s.RegisterSeriesRenameOp(reg) })
}
