// file: internal/plugins/deluge/centralization.go
// version: 1.10.0
// guid: c3d4e5f6-a7b8-9c0d-1e2f-3a4b5c6d7e8f
// last-edited: 2026-10-13

package deluge

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	delugeclient "github.com/falkcorp/audiobook-organizer/internal/deluge"
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

// centralizeConcurrency is the worker-pool size for deluge.centralize. Each
// item is one BOOK's pending files: disk copies, store writes and, when
// Deluge storage moves are enabled, Deluge RPCs, so the pool is bounded by what
// the Deluge daemon and the library disk tolerate rather than by core count.
// A fixed 4 (deluge.link-backfill sizes its pool min(NumCPU, 8)).
const centralizeConcurrency = 4

// centralizationCheckpoint is the op's liveness record: counts only. It is NOT
// a resume position. The pending query (GetBookFilesNeedingDelugeImportCore
// filters ImportedFromDelugeAt == nil) is the resume state: a restarted run
// lists exactly the rows still unimported, whatever order the previous run
// finished them in. An index into that list would be wrong the moment a row
// leaves it.
type centralizationCheckpoint struct {
	Completed int    `json:"completed"`
	Total     int    `json:"total"`
	LastError string `json:"last_error,omitempty"`
}

func (p *Plugin) runCentralization(ctx context.Context, _ json.RawMessage, reporter sdk.Reporter) error {
	cfg := &config.AppConfig

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

	sdk.NewProgress(reporter, total).Start(fmt.Sprintf("Centralizing %d files...", total))

	var successCount, skipCount, inPlaceCount, protectedCount, notLoadedCount, errCount atomic.Int64

	// The protected-path predicate for in-place marking: the plugin's cache
	// (register.go builds it as NewProtectedPathCache(client,
	// cfg.ProtectedPaths): active torrents' save_paths plus the configured
	// prefixes), built once per process, not per item. Left an untyped nil
	// when unset, so only cfg's static prefixes apply.
	var protected tagger.PathChecker
	if p.cache != nil {
		protected = p.cache
	}

	// ckptMu guards lastErr and orders the liveness checkpoint writes. The
	// workers set lastErr; the checkpoint is written from the group closure.
	var (
		ckptMu    sync.Mutex
		lastErr   string
		completed int
	)
	setLastError := func(msg string) {
		ckptMu.Lock()
		lastErr = msg
		ckptMu.Unlock()
	}
	// finishFile counts one handled file and writes the counts-only checkpoint.
	finishFile := func() {
		ckptMu.Lock()
		defer ckptMu.Unlock()
		completed++
		_ = reporter.Checkpoint(centralizationCheckpoint{Completed: completed, Total: total, LastError: lastErr})
	}

	// PARTITION: the pool's unit is a BOOK, not a file. A book's pending files
	// are processed one after another by one worker, so no two workers ever
	// touch the same book. Sibling files of one book share a Deluge torrent
	// (DelugeHash), and ImportToLibraryWith can ask Deluge to move that
	// torrent's storage per file, which would race a sibling's copy; and each
	// file write recomputes the book's aggregates from its file rows outside
	// ModifyBook, so two siblings committing together can leave a stale
	// snapshot as the last write. Different books share neither.
	byBook := make(map[string]int)
	var groups [][]*database.BookFileCore
	for _, bf := range toImport {
		gi, ok := byBook[bf.BookID]
		if !ok {
			gi = len(groups)
			byBook[bf.BookID] = gi
			groups = append(groups, nil)
		}
		groups[gi] = append(groups[gi], bf)
	}

	centralizeOne := func(bf *database.BookFileCore) {
		defer finishFile()
		srcPath := bf.FilePath
		if srcPath == "" {
			skipCount.Add(1)
			return
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
				setLastError(markErr.Error())
				reporter.Logger().Error("mark in-place file imported failed", "id", bf.ID, "error", markErr)
			case outcome == delugeclient.InPlaceMarked:
				inPlaceCount.Add(1)
				reporter.Logger().Debug("file already in the library; marked imported", "path", srcPath)
			case outcome == delugeclient.InPlaceProtected:
				protectedCount.Add(1)
				reporter.Logger().Info("file is its own library destination but protected; left pending", "id", bf.ID, "path", srcPath)
			case outcome == delugeclient.InPlaceListNotLoaded:
				notLoadedCount.Add(1)
				reporter.Logger().Info("file is its own library destination and the Deluge protected list has not loaded; left pending", "id", bf.ID, "path", srcPath)
			default:
				// Moved, removed or marked by another writer meanwhile.
				skipCount.Add(1)
			}
			return
		}

		// Hydrate the full row first -- bf is the Core (memdb-slim)
		// projection, and writing it back would silently wipe the
		// fingerprint-diagnostic fields (STOREFID PR-D) -- and before
		// anything touches the disk, so a failed read leaves no copy behind.
		// It used to come after the copy: a failed hydrate left an orphan at
		// dest that refused every later run with EEXIST.
		full, hydrateErr := p.store.GetBookFileByID(bf.BookID, bf.ID)
		if hydrateErr != nil || full == nil {
			errCount.Add(1)
			setLastError(fmt.Sprintf("hydrate book file %s: %v", bf.ID, hydrateErr))
			reporter.Logger().Error("hydrate book file failed", "id", bf.ID, "error", hydrateErr)
			return
		}

		// The importer /discovery/import and maintenance.bulk-deluge-import
		// use: it holds the destination lock, refuses a destination another
		// book_file row names and a source under a static protected prefix
		// (iTunes, config.ProtectedPaths -- this op used to copy those out
		// and repoint them), never overwrites, removes its copy when the row
		// update fails (so a retry is not refused by an orphan), and asks
		// Deluge to move the torrent's storage when that is enabled.
		newPath, importErr := delugeclient.ImportToLibraryWith(cfg, p.client, p.store, full, delugeclient.ImportOptions{
			Protected:          protected,
			ExpectedBookFileID: full.ID,
		})
		switch {
		case errors.Is(importErr, tagger.ErrProtectedListNotLoaded):
			// Checked before ErrProtectedPathWrite, which it also wraps.
			notLoadedCount.Add(1)
			reporter.Logger().Info("file left pending: the Deluge protected list has not loaded", "id", bf.ID, "path", srcPath)
			return
		case errors.Is(importErr, tagger.ErrProtectedPathWrite):
			protectedCount.Add(1)
			reporter.Logger().Info("file is protected; left pending", "id", bf.ID, "path", srcPath, "reason", importErr)
			return
		case importErr != nil:
			errCount.Add(1)
			setLastError(fmt.Sprintf("import %s: %v", srcPath, importErr))
			if newPath != "" {
				reporter.Logger().Error("import failed and its copy could not be removed", "id", bf.ID, "src", srcPath, "left_at", newPath, "error", importErr)
			} else {
				reporter.Logger().Error("import failed", "id", bf.ID, "src", srcPath, "dest", dest, "error", importErr)
			}
			return
		}
		dest = newPath

		n := successCount.Add(1)
		reporter.Logger().Debug("centralized file",
			"src", srcPath, "dest", dest, "progress", fmt.Sprintf("%d/%d", n, total))
	}

	runErr := registry.RunItems(ctx, reporter, groups, func(ctx context.Context, group []*database.BookFileCore) error {
		for _, bf := range group {
			if err := ctx.Err(); err != nil {
				return err
			}
			centralizeOne(bf)
		}
		return nil
	}, registry.RunItemsOptions{
		ErrMode:     registry.ErrModeCollect,
		Concurrency: centralizeConcurrency,
		Label: func(i, t int) string {
			return fmt.Sprintf("Centralizing book %d/%d", i+1, t)
		},
	})

	reporter.Logger().Info("deluge centralization complete",
		"succeeded", successCount.Load(),
		"already_in_library", inPlaceCount.Load(),
		"protected", protectedCount.Load(),
		"protected_list_not_loaded", notLoadedCount.Load(),
		"skipped", skipCount.Load(),
		"errors", errCount.Load())
	sdk.NewProgress(reporter, total).Done(
		fmt.Sprintf("Done: %d succeeded, %d already in library (marked imported), %d protected (left pending), "+
			"%d left pending (Deluge protected list not loaded), %d skipped, %d errors",
			successCount.Load(), inPlaceCount.Load(), protectedCount.Load(), notLoadedCount.Load(), skipCount.Load(), errCount.Load()),
	)
	return runErr
}
