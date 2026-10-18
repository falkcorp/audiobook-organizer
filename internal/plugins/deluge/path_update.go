// file: internal/plugins/deluge/path_update.go
// version: 1.4.0
// guid: d4e5f6a7-b8c9-0d1e-2f3a-4b5c6d7e8f9a
// last-edited: 2026-10-18

package deluge

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"sync/atomic"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/operations/opmode"
	"github.com/falkcorp/audiobook-organizer/internal/operations/registry"
	"github.com/falkcorp/audiobook-organizer/pkg/plugin/sdk"
)

func (p *Plugin) pathUpdateDef() sdk.OperationDef {
	return sdk.OperationDef{
		ID:          "deluge.path-update",
		Preview:     sdk.PreviewSpec{Params: pathUpdateParams{}},
		Liveness:    sdk.LivenessRunItems,
		Plugin:      "deluge",
		DisplayName: "Update Deluge torrent path",
		Description: "Updates a torrent's storage path in Deluge after a book is relocated: moves each active version's " +
			"torrent to the book's folder with core.move_storage (only when deluge_move_enabled is on). " +
			"dry_run defaults to true: a run that states no mode logs and counts the moves it would make and calls Deluge " +
			"for none of them. Pass dry_run=false to move.",
		ResumePolicy:    sdk.ResumeDrop,
		DefaultPriority: sdk.PriorityNormal,
		ConcurrencyKey:  "deluge.path-update",
		Cancellable:     false,
		Isolate:         false,
		Timeout:         1 * time.Minute,
		Run:             p.runPathUpdate,
		// CapFilesWrite: core.move_storage relocates the torrent's files on
		// disk. CapNetworkGeneric: the call goes to the Deluge Web UI.
		Capabilities: []sdk.Capability{
			sdk.CapLibraryRead,
			sdk.CapFilesWrite,
			sdk.CapNetworkGeneric,
		},
		// Event-triggered: fires on book.relocated events.
		Triggers: []sdk.EventSubscription{
			{
				EventName: "book.relocated",
				Handler:   p.handleBookRelocated,
			},
		},
	}
}

// pathUpdateParams is the payload for path-update operations.
type pathUpdateParams struct {
	BookID string `json:"book_id"`
	// DryRun (dry_run) and DryRunCamel (dryRun) are one switch, resolved by
	// opmode.ResolveDryRun: omitted means preview, false moves torrents, and
	// the two spellings disagreeing is an error. A preview never calls
	// MoveStorage.
	DryRun      *bool `json:"dry_run,omitempty"`
	DryRunCamel *bool `json:"dryRun,omitempty"`
}

func (p *Plugin) handleBookRelocated(ctx context.Context, payload any) error {
	// payload is expected to be a string (book ID) from the event bus.
	bookID, ok := payload.(string)
	if !ok {
		return fmt.Errorf("book.relocated payload is not a string")
	}

	// A relocation event wants the move made, so the params say so: with the
	// op's preview default an omitted dry_run would queue a run that moves
	// nothing.
	params := pathUpdateParams{BookID: bookID, DryRun: opmode.Live()}
	paramsBytes, err := json.Marshal(params)
	if err != nil {
		return fmt.Errorf("marshal params: %w", err)
	}

	// The event handler doesn't have access to the registry for re-enqueueing,
	// so this is a placeholder. In practice, the event bus will call RunOperation
	// directly with these params.
	_ = paramsBytes
	return nil
}

func (p *Plugin) runPathUpdate(ctx context.Context, params json.RawMessage, reporter sdk.Reporter) error {
	cfg := &config.AppConfig

	var args pathUpdateParams
	if err := json.Unmarshal(params, &args); err != nil {
		return fmt.Errorf("unmarshal params: %w", err)
	}

	dryRun, err := opmode.ResolveDryRun("deluge.path-update", args.DryRun, args.DryRunCamel)
	if err != nil {
		return err
	}

	bookID := args.BookID
	if bookID == "" {
		return fmt.Errorf("book_id is required")
	}

	sdk.NewProgress(reporter, 0).Start(fmt.Sprintf("Loading book %s...", bookID))

	book, err := p.store.GetBookByID(bookID)
	if err != nil {
		return fmt.Errorf("load book: %w", err)
	}
	if book == nil {
		return fmt.Errorf("book not found")
	}

	versions, err := p.store.GetBookVersionsByBookID(bookID)
	if err != nil {
		return fmt.Errorf("load versions: %w", err)
	}

	progress := sdk.NewProgress(reporter, len(versions))
	if dryRun {
		progress.Start(fmt.Sprintf("DRY RUN: checking %d version(s) against Deluge, nothing will be moved...", len(versions)))
	} else {
		progress.Start(fmt.Sprintf("Updating %d version(s) in Deluge...", len(versions)))
	}

	// Pre-compute active count so the RunItems closure can reference it without
	// iterating versions again on each call.
	activeCount := 0
	for _, v := range versions {
		if v.Status == database.BookVersionStatusActive {
			activeCount++
		}
	}
	primaryDir := filepath.Dir(book.FilePath)

	var updated, planned atomic.Int64

	// runitems:sequential one book's versions (a handful); sequential by design
	if err := registry.RunItems(ctx, reporter, versions, func(ctx context.Context, v database.BookVersion) error {
		if v.TorrentHash == "" || v.Status != database.BookVersionStatusActive {
			return nil // skip non-Deluge or inactive versions
		}

		var dir string
		if activeCount == 1 {
			dir = primaryDir
		} else {
			dir = filepath.Join(primaryDir, ".versions", v.ID)
		}

		if cfg.DelugeMoveEnabled && p.client != nil {
			if dryRun {
				reporter.Logger().Info("would move_storage",
					"hash", v.TorrentHash, "path", dir)
				planned.Add(1)
				return nil
			}
			if err := p.client.MoveStorage([]string{v.TorrentHash}, dir); err != nil {
				reporter.Logger().Error("move_storage failed",
					"hash", v.TorrentHash, "path", dir, "error", err)
				return nil // non-fatal: log but continue
			}
			reporter.Logger().Info("move_storage succeeded",
				"hash", v.TorrentHash, "path", dir)
			updated.Add(1)
		}
		return nil
	}, registry.RunItemsOptions{ErrMode: registry.ErrModeCollect}); err != nil {
		return err
	}

	reporter.Logger().Info("deluge path-update complete",
		"book_id", bookID, "dry_run", dryRun, "planned", planned.Load(), "updated", updated.Load())
	mode := "APPLY"
	if dryRun {
		mode = "DRY RUN"
	}
	progress.Done(fmt.Sprintf("%s: %d move_storage planned, %d updated", mode, planned.Load(), updated.Load()))
	return nil
}
