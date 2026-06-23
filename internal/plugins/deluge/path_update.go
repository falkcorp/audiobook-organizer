// file: internal/plugins/deluge/path_update.go
// version: 1.1.0
// guid: d4e5f6a7-b8c9-0d1e-2f3a-4b5c6d7e8f9a
// last-edited: 2026-06-23

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
	"github.com/falkcorp/audiobook-organizer/internal/operations/registry"
	"github.com/falkcorp/audiobook-organizer/pkg/plugin/sdk"
)

func (p *Plugin) pathUpdateDef() sdk.OperationDef {
	return sdk.OperationDef{
		ID:              "deluge.path-update",
		Plugin:          "deluge",
		DisplayName:     "Update Deluge torrent path",
		Description:     "Updates a torrent's storage path in Deluge after a book is relocated.",
		ResumePolicy:    sdk.ResumeDrop,
		DefaultPriority: sdk.PriorityNormal,
		ConcurrencyKey:  "deluge.path-update",
		Cancellable:     false,
		Isolate:         false,
		Timeout:         1 * time.Minute,
		Run:             p.runPathUpdate,
		Capabilities: []sdk.Capability{
			sdk.CapLibraryRead,
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
}

func (p *Plugin) handleBookRelocated(ctx context.Context, payload any) error {
	// payload is expected to be a string (book ID) from the event bus.
	bookID, ok := payload.(string)
	if !ok {
		return fmt.Errorf("book.relocated payload is not a string")
	}

	params := pathUpdateParams{BookID: bookID}
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

	sdk.NewProgress(reporter, len(versions)).Start(
		fmt.Sprintf("Updating %d version(s) in Deluge...", len(versions)),
	)

	// Pre-compute active count so the RunItems closure can reference it without
	// iterating versions again on each call.
	activeCount := 0
	for _, v := range versions {
		if v.Status == database.BookVersionStatusActive {
			activeCount++
		}
	}
	primaryDir := filepath.Dir(book.FilePath)

	var updated atomic.Int64

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
		"book_id", bookID, "updated", updated.Load())
	return nil
}
