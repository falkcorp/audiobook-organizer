// file: internal/plugins/deluge/centralization.go
// version: 1.7.0
// guid: c3d4e5f6-a7b8-9c0d-1e2f-3a4b5c6d7e8f
// last-edited: 2026-10-11

package deluge

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync/atomic"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	delugeclient "github.com/falkcorp/audiobook-organizer/internal/deluge"
	"github.com/falkcorp/audiobook-organizer/internal/fileops"
	"github.com/falkcorp/audiobook-organizer/internal/logging"
	"github.com/falkcorp/audiobook-organizer/internal/operations/registry"
	"github.com/falkcorp/audiobook-organizer/internal/pathutil"
	"github.com/falkcorp/audiobook-organizer/internal/tagger"
	"github.com/falkcorp/audiobook-organizer/pkg/plugin/sdk"
)

func (p *Plugin) centralizationDef() sdk.OperationDef {
	return sdk.OperationDef{
		ID:              "deluge.centralize",
		Liveness:        sdk.LivenessRunItems,
		Plugin:          "deluge",
		DisplayName:     "Centralize Deluge books",
		Description:     "Moves Deluge-sourced audiobooks from protected paths into the main library.",
		ResumePolicy:    sdk.ResumeRestart,
		DefaultPriority: sdk.PriorityNormal,
		ConcurrencyKey:  "deluge.centralize",
		Cancellable:     true,
		Isolate:         false,
		Timeout:         24 * time.Hour,
		Run:             p.runCentralization,
		Capabilities: []sdk.Capability{
			sdk.CapLibraryRead,
			sdk.CapLibraryWrite,
			sdk.CapFilesRead,
			sdk.CapFilesWrite,
		},
		MinCheckpointInterval: 30 * time.Second,
	}
}

// centralizationCheckpoint tracks state across restarts.
type centralizationCheckpoint struct {
	ProcessedFiles int    `json:"processed_files"`
	TotalFiles     int    `json:"total_files"`
	LastBookFileID string `json:"last_book_file_id"`
	LastError      string `json:"last_error,omitempty"`
}

func (p *Plugin) runCentralization(ctx context.Context, params json.RawMessage, reporter sdk.Reporter) error {
	cfg := &config.AppConfig

	// Load checkpoint if resuming from a restart.
	var checkpoint centralizationCheckpoint
	if err := reporter.Checkpoint(nil); err == nil && params != nil {
		_ = json.Unmarshal(params, &checkpoint)
	}

	sdk.NewProgress(reporter, 0).Start("Loading Deluge-imported files...")

	pending, err := p.store.GetBookFilesNeedingDelugeImportCore()
	if err != nil {
		return fmt.Errorf("load deluge-pending book files: %w", err)
	}

	toImport := make([]*database.BookFileCore, 0, len(pending))
	for i := range pending {
		toImport = append(toImport, &pending[i])
	}

	total := len(toImport)
	if total == 0 {
		sdk.NewProgress(reporter, 0).Done("No files to centralize")
		return nil
	}

	if checkpoint.ProcessedFiles == 0 {
		checkpoint.TotalFiles = total
	}

	// Pre-slice to the resume point so RunItems starts at the right position.
	// Progress labels show global position (baseIdx + local) for accurate display.
	baseIdx := checkpoint.ProcessedFiles
	resumeSlice := toImport[baseIdx:]

	sdk.NewProgress(reporter, total).Start(
		fmt.Sprintf("Centralizing %d files (%d remaining)...", total, len(resumeSlice)),
	)

	var successCount, skipCount, inPlaceCount, protectedCount, errCount atomic.Int64

	// The protected-path predicate for in-place marking: the plugin's cache
	// (register.go builds it as NewProtectedPathCache(client,
	// cfg.ProtectedPaths): active torrents' save_paths plus the configured
	// prefixes), built once per process, not per item. Left an untyped nil
	// when unset, so only cfg's static prefixes apply.
	var protected tagger.PathChecker
	if p.cache != nil {
		protected = p.cache
	}

	// localCount tracks how many items RunItems has dispatched so far.
	// Sequential (Concurrency=0 default), so plain increment is safe, but
	// atomic keeps the intent clear if concurrency is raised later.
	var localCount atomic.Int64

	runErr := registry.RunItems(ctx, reporter, resumeSlice, func(ctx context.Context, bf *database.BookFileCore) error {
		globalIdx := baseIdx + int(localCount.Add(1)) - 1

		srcPath := bf.FilePath
		if srcPath == "" {
			skipCount.Add(1)
			return nil
		}

		var destDir string
		rel, relErr := filepath.Rel(cfg.RootDir, filepath.Dir(srcPath))
		if relErr == nil && !filepath.IsAbs(rel) && !pathutil.RelEscapesBase(rel) {
			destDir = filepath.Join(cfg.RootDir, rel)
		} else {
			destDir = cfg.RootDir
		}

		dest := filepath.Join(destDir, filepath.Base(srcPath))
		if srcPath == dest {
			// Already at its library path: nothing to copy. Mark it imported
			// (ImportedFromDelugeAt only, under the row's lock), or it stays
			// on GetBookFilesNeedingDelugeImportCore and every run lists it
			// again. A row deluge.link-backfill linked is this case. A
			// protected path is left pending, as /discovery/import leaves it.
			outcome, markErr := delugeclient.MarkImportedInPlace(p.store, cfg, protected, bf.BookID, bf.ID, srcPath, time.Now())
			switch {
			case markErr != nil:
				errCount.Add(1)
				checkpoint.LastError = markErr.Error()
				reporter.Logger().Error("mark in-place file imported failed", "id", bf.ID, "error", markErr)
			case outcome == delugeclient.InPlaceMarked:
				inPlaceCount.Add(1)
				reporter.Logger().Debug("file already in the library; marked imported", "path", srcPath)
			case outcome == delugeclient.InPlaceProtected:
				protectedCount.Add(1)
				reporter.Logger().Info("file is its own library destination but protected; left pending", "id", bf.ID, "path", srcPath)
			default:
				// Moved, removed or marked by another writer meanwhile.
				skipCount.Add(1)
			}
			return nil
		}

		if err := os.MkdirAll(destDir, 0o755); err != nil {
			errCount.Add(1)
			checkpoint.LastError = fmt.Sprintf("mkdir %s: %v", destDir, err)
			reporter.Logger().Error("mkdir failed", "path", destDir, "error", err)
			return nil // non-fatal
		}

		// Clone into the central library. This used to call a local
		// reflinkCopy that was a permanent stub returning an error, so every
		// file this op ever centralized was byte-copied in full even though
		// the pool supports cloning.
		if err := fileops.ReflinkOrCopy(srcPath, dest); err != nil {
			errCount.Add(1)
			checkpoint.LastError = fmt.Sprintf("copy %s: %v", srcPath, err)
			reporter.Logger().Error("copy failed", "src", srcPath, "dest", dest, "error", err)
			return nil // non-fatal
		}

		// Hydrate the full row before writing back — bf is the Core (memdb-slim)
		// projection, and a naive UpdateBookFile(bf.ID, bf) here would silently
		// wipe the fingerprint-diagnostic fields (STOREFID PR-D).
		full, hydrateErr := p.store.GetBookFileByID(bf.BookID, bf.ID)
		if hydrateErr != nil || full == nil {
			errCount.Add(1)
			checkpoint.LastError = fmt.Sprintf("hydrate book file %s: %v", bf.ID, hydrateErr)
			reporter.Logger().Error("hydrate book file failed", "id", bf.ID, "error", hydrateErr)
			return nil // non-fatal
		}

		now := time.Now()
		full.DelugeOriginalPath = srcPath
		full.FilePath = dest
		full.ImportedFromDelugeAt = &now

		if err := p.store.UpdateBookFile(full.ID, full); err != nil {
			errCount.Add(1)
			checkpoint.LastError = fmt.Sprintf("update book file: %v", err)
			reporter.Logger().Error("update book file failed", "id", bf.ID, "error", err)
			return nil // non-fatal
		}

		if cfg.DelugeMoveEnabled && bf.DelugeHash != "" && p.client != nil {
			if moveErr := p.client.MoveStorage([]string{bf.DelugeHash}, filepath.Dir(dest)); moveErr != nil {
				reporter.Logger().Warn("deluge move_storage failed", "hash", bf.DelugeHash, "error", moveErr)
			} else {
				logging.Info(ctx, "deluge move_storage succeeded", "hash", bf.DelugeHash, "dir", filepath.Dir(dest))
			}
		}

		n := successCount.Add(1)
		checkpoint.ProcessedFiles = globalIdx + 1
		_ = reporter.Checkpoint(checkpoint)

		reporter.Logger().Debug("centralized file",
			"src", srcPath, "dest", dest, "progress", fmt.Sprintf("%d/%d", n, total))
		return nil
	}, registry.RunItemsOptions{ErrMode: registry.ErrModeCollect})

	reporter.Logger().Info("deluge centralization complete",
		"succeeded", successCount.Load(),
		"already_in_library", inPlaceCount.Load(),
		"protected", protectedCount.Load(),
		"skipped", skipCount.Load(),
		"errors", errCount.Load())
	sdk.NewProgress(reporter, total).Done(
		fmt.Sprintf("Done: %d succeeded, %d already in library (marked imported), %d protected (left pending), %d skipped, %d errors",
			successCount.Load(), inPlaceCount.Load(), protectedCount.Load(), skipCount.Load(), errCount.Load()),
	)
	return runErr
}
