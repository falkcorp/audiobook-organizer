// file: internal/plugins/deluge/centralization.go
// version: 1.11.0
// guid: c3d4e5f6-a7b8-9c0d-1e2f-3a4b5c6d7e8f
// last-edited: 2026-10-13

package deluge

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
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
// item is one connected group of pending files (see the partition in
// runCentralization): disk copies, store writes and, when Deluge storage moves
// are enabled, Deluge RPCs, so the pool is bounded by what the Deluge daemon
// and the library disk tolerate rather than by core count. A fixed 4
// (deluge.link-backfill sizes its pool min(NumCPU, 8)).
//
// An operator pause is honoured between GROUPS, not files: RunItems parks the
// next group, and the files of a group already running finish first.
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

	// PARTITION: the pool's unit is a connected group of files, never a lone
	// file. Two pending files are connected when they belong to the same book
	// OR carry the same Deluge torrent hash, and groups are the connected
	// components of that relation (so a book holding hashes A and B, and
	// another book holding B, are one group). Within a group one worker
	// processes the files one after another. Why:
	//   - sibling files of one book share a torrent, and ImportToLibraryWith
	//     can ask Deluge to move that torrent's storage per file, which would
	//     race a sibling's copy;
	//   - two BOOKS can hold files of the same torrent too (the relink fixer
	//     reports this as a hash held "of another book"), so the book alone is
	//     not enough;
	//   - each file write recomputes its book's aggregates from the book's file
	//     rows outside ModifyBook, so two siblings committing together can
	//     leave a stale snapshot as the last write.
	// Different components share neither a book nor a torrent.
	groups := connectedFileGroups(toImport)

	// Progress counts GROUPS (the pool's unit) from Start to Done; the
	// checkpoint below counts files.
	prog := sdk.NewProgress(reporter, len(groups))
	prog.Start(fmt.Sprintf("Centralizing %d files in %d groups...", total, len(groups)))

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

	// ckptMu guards lastErr and completed. The checkpoint itself is written
	// OUTSIDE ckptMu (workers recording an error must not wait on a database
	// write), ordered by the completion count: a snapshot older than one
	// already written is dropped, so the stored checkpoint only moves forward.
	var (
		ckptMu      sync.Mutex
		lastErr     string
		completed   int
		writeMu     sync.Mutex
		lastWritten int
	)
	setLastError := func(msg string) {
		ckptMu.Lock()
		lastErr = msg
		ckptMu.Unlock()
	}
	// finishFile counts one handled file, stamps the stuck-op clock and writes
	// the counts-only checkpoint.
	//
	// The stamp is per FILE on purpose. RunItems reports progress once per
	// finished group, and a group can hold a long run of slow copies; the
	// registry's stuck-op watchdog reads only UpdateProgress and TouchLiveness
	// (Checkpoint does not move it), so per-group liveness would have the
	// watchdog time out a healthy op, typically on the tail of a run, and a
	// timed-out op does not resume. TouchLiveness writes nothing and leaves the
	// progress numbers alone, and it follows real work (a finished copy or a
	// failed read), so a genuinely wedged copy still goes quiet.
	finishFile := func() {
		registry.TouchLiveness(reporter)
		ckptMu.Lock()
		completed++
		snap := centralizationCheckpoint{Completed: completed, Total: total, LastError: lastErr}
		ckptMu.Unlock()

		writeMu.Lock()
		defer writeMu.Unlock()
		if snap.Completed > lastWritten {
			lastWritten = snap.Completed
			_ = reporter.Checkpoint(snap)
		}
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
		// sdk.Progress reserves a start and a done step around the N units
		// (total = N+2), so the pool reports against the same denominator
		// and the bar never changes units between Start, the pool and Done.
		ProgressTotal: len(groups) + 2,
		Label: func(i, _ int) string {
			return fmt.Sprintf("Centralizing group %d/%d", i+1, len(groups))
		},
	})

	reporter.Logger().Info("deluge centralization complete",
		"succeeded", successCount.Load(),
		"already_in_library", inPlaceCount.Load(),
		"protected", protectedCount.Load(),
		"protected_list_not_loaded", notLoadedCount.Load(),
		"skipped", skipCount.Load(),
		"errors", errCount.Load())
	prog.Done(
		fmt.Sprintf("Done: %d succeeded, %d already in library (marked imported), %d protected (left pending), "+
			"%d left pending (Deluge protected list not loaded), %d skipped, %d errors",
			successCount.Load(), inPlaceCount.Load(), protectedCount.Load(), notLoadedCount.Load(), skipCount.Load(), errCount.Load()),
	)
	return runErr
}

// connectedFileGroups splits files into the connected components of "same book
// or same Deluge torrent hash" (union-find over book and hash nodes), keeping
// the order of first appearance for both groups and the files inside each.
// A file with no hash is connected only through its book.
func connectedFileGroups(files []*database.BookFileCore) [][]*database.BookFileCore {
	parent := map[string]string{}
	find := func(k string) string {
		if _, ok := parent[k]; !ok {
			parent[k] = k
		}
		for parent[k] != k {
			parent[k] = parent[parent[k]] // path halving
			k = parent[k]
		}
		return k
	}
	union := func(a, b string) {
		if ra, rb := find(a), find(b); ra != rb {
			parent[rb] = ra
		}
	}
	bookKey := func(bf *database.BookFileCore) string { return "book:" + bf.BookID }
	for _, bf := range files {
		find(bookKey(bf))
		if bf.DelugeHash != "" {
			union(bookKey(bf), "hash:"+strings.ToLower(bf.DelugeHash))
		}
	}
	index := map[string]int{}
	var groups [][]*database.BookFileCore
	for _, bf := range files {
		root := find(bookKey(bf))
		gi, ok := index[root]
		if !ok {
			gi = len(groups)
			index[root] = gi
			groups = append(groups, nil)
		}
		groups[gi] = append(groups[gi], bf)
	}
	return groups
}
