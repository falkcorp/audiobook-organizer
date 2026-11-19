// file: internal/plugins/deluge/centralization.go
// version: 1.12.0
// guid: c3d4e5f6-a7b8-9c0d-1e2f-3a4b5c6d7e8f
// last-edited: 2026-11-19

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

	"github.com/falkcorp/mediacore/tasks"

	database "github.com/falkcorp/audiobook-organizer/internal/books/store"
	"github.com/falkcorp/audiobook-organizer/internal/config"
	delugeclient "github.com/falkcorp/audiobook-organizer/internal/deluge"
	"github.com/falkcorp/audiobook-organizer/internal/pathutil"
	"github.com/falkcorp/audiobook-organizer/internal/tagger"
	"github.com/falkcorp/audiobook-organizer/pkg/plugin/sdk"
)

// centralizeKind is rule R3's native kind for deluge.centralize.
const centralizeKind = tasks.Kind("books.deluge.centralize")

// centralizeHelp is HEAD's Description and the native Help (rule R5).
const centralizeHelp = "Moves Deluge-sourced audiobooks from protected paths into the main library."

func (p *Plugin) centralizationDef() sdk.OperationDef {
	return sdk.OperationDef{
		ID:              "deluge.centralize",
		Liveness:        sdk.LivenessRunItems,
		Plugin:          "deluge",
		DisplayName:     "Centralize Deluge books",
		Description:     centralizeHelp,
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

// orderedCheckpoint writes centralization checkpoints so the stored one only
// moves forward. Workers take their snapshot under one lock and write it under
// another, so a snapshot taken earlier can reach put after a later one; put drops
// it. A named type so a test drives the out-of-order arrival directly.
type orderedCheckpoint struct {
	mu    sync.Mutex
	last  int
	write func(any) error
}

// put writes snap if its completion count is past the last one written. A failed
// write is not retried (the next, later snapshot supersedes it) and not an error:
// the checkpoint is a liveness record, not a resume position.
func (o *orderedCheckpoint) put(snap centralizationCheckpoint) {
	o.mu.Lock()
	defer o.mu.Unlock()
	if snap.Completed > o.last {
		o.last = snap.Completed
		_ = o.write(snap)
	}
}

// centralizeStore is the reads the core takes: the pending list, the full row
// before a copy, the path index, and the retired-row reader that
// delugeclient.ImportToLibraryWith resolves on the store it is handed
// (database.BookFileByPathIncludingRetired).
type centralizeStore interface {
	GetBookFilesNeedingDelugeImportCore() ([]database.BookFileCore, error)
	GetBookFileByID(bookID, fileID string) (*database.BookFile, error)
	GetBookFileByPath(filePath string) (*database.BookFile, error)
	database.RetiredBookFileReader
}

// centralizeWriter is the writes the core makes: ModifyBookFile (the
// in-place mark, delugeclient.MarkImportedInPlace) and UpdateBookFile (the
// whole-row repoint ImportToLibraryWith makes, guarded by ExpectedBookFileID).
type centralizeWriter interface {
	ModifyBookFile(bookID, fileID string, fn func(*database.BookFile) error) (*database.BookFile, error)
	UpdateBookFile(id string, file *database.BookFile) error
}

// libraryImporter is delugeclient.ImportToLibraryWith with the Deluge client
// bound inside: the Deluge client is not a store write and stays in the core,
// reached through this owner-declared interface.
type libraryImporter interface {
	ImportToLibrary(cfg *config.Config, store delugeclient.Store, bookFile *database.BookFile, opts delugeclient.ImportOptions) (string, error)
}

// centralizeEnv is what the core takes from its shell. Protected is the
// protected-path predicate for in-place marking: the plugin's cache, or an
// untyped nil when unset (so only cfg's static prefixes apply).
type centralizeEnv struct {
	kindEnv
	Cfg       *config.Config
	Store     centralizeStore
	Writer    centralizeWriter
	Importer  libraryImporter
	Protected tagger.PathChecker
}

// centralizeImportStore is the store ImportToLibraryWith is handed: reads from
// the env's store (so the importer's retired-row capability lookup finds the
// store's own reader through the embedded interface) and the repoint through the
// env's writer.
type centralizeImportStore struct {
	centralizeStore
	w centralizeWriter
}

var (
	_ delugeclient.Store             = centralizeImportStore{}
	_ database.RetiredBookFileReader = centralizeImportStore{}
)

func (s centralizeImportStore) UpdateBookFile(id string, file *database.BookFile) error {
	return s.w.UpdateBookFile(id, file)
}

// centralizeResult is the native run's result. HEAD stored none: its counts
// went to the log and the final progress line.
type centralizeResult struct {
	Total                  int   `json:"total"`
	Groups                 int   `json:"groups"`
	Succeeded              int64 `json:"succeeded"`
	AlreadyInLibrary       int64 `json:"already_in_library"`
	Protected              int64 `json:"protected"`
	ProtectedListNotLoaded int64 `json:"protected_list_not_loaded"`
	Skipped                int64 `json:"skipped"`
	Errors                 int64 `json:"errors"`
}

// centralizeParallelism is the pool of the group loop (rule R4): HEAD's fixed
// centralizeConcurrency.
func centralizeParallelism() tasks.Parallelism {
	return tasks.Fixed(centralizeConcurrency,
		"each item is a connected group of pending files doing disk copies, store writes and Deluge RPCs, bounded by what the Deluge daemon and the library disk tolerate")
}

// centralizeCore is the body of runCentralization, shared by both shells.
func centralizeCore(ctx context.Context, env centralizeEnv) (centralizeResult, error) {
	cfg := env.Cfg

	newProgress(env.kindEnv, 0).Start("Loading Deluge-imported files...")

	pending, err := env.Store.GetBookFilesNeedingDelugeImportCore()
	if err != nil {
		return centralizeResult{}, fmt.Errorf("load deluge-pending book files: %w", err)
	}

	toImport := make([]*database.BookFileCore, 0, len(pending))
	for i := range pending {
		toImport = append(toImport, &pending[i])
	}

	total := len(toImport)
	if total == 0 {
		newProgress(env.kindEnv, 0).Done("No files to centralize")
		return centralizeResult{}, nil
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
	prog := newProgress(env.kindEnv, len(groups))
	prog.Start(fmt.Sprintf("Centralizing %d files in %d groups...", total, len(groups)))

	var successCount, skipCount, inPlaceCount, protectedCount, notLoadedCount, errCount atomic.Int64

	// The protected-path predicate for in-place marking: the plugin's cache
	// (register.go builds it as NewProtectedPathCache(client,
	// cfg.ProtectedPaths): active torrents' save_paths plus the configured
	// prefixes), built once per process, not per item. Left an untyped nil
	// when unset, so only cfg's static prefixes apply (the shell sets
	// env.Protected only when it has a cache).
	protected := env.Protected

	// ckptMu guards lastErr and completed. The checkpoint itself is written
	// OUTSIDE ckptMu (workers recording an error must not wait on a database
	// write), ordered by the completion count: a snapshot older than one
	// already written is dropped, so the stored checkpoint only moves forward.
	var (
		ckptMu    sync.Mutex
		lastErr   string
		completed int
	)
	ckpt := &orderedCheckpoint{write: env.Checkpoint}
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
		env.Touch()
		ckptMu.Lock()
		completed++
		snap := centralizationCheckpoint{Completed: completed, Total: total, LastError: lastErr}
		ckptMu.Unlock()

		ckpt.put(snap)
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
			outcome, markErr := delugeclient.MarkImportedInPlace(env.Writer, cfg, protected, bf.BookID, bf.ID, srcPath, time.Now())
			switch {
			case markErr != nil:
				errCount.Add(1)
				setLastError(markErr.Error())
				env.Logger.Error("mark in-place file imported failed", "id", bf.ID, "error", markErr)
			case outcome == delugeclient.InPlaceMarked:
				inPlaceCount.Add(1)
				env.Logger.Debug("file already in the library; marked imported", "path", srcPath)
			case outcome == delugeclient.InPlaceProtected:
				protectedCount.Add(1)
				env.Logger.Info("file is its own library destination but protected; left pending", "id", bf.ID, "path", srcPath)
			case outcome == delugeclient.InPlaceListNotLoaded:
				notLoadedCount.Add(1)
				env.Logger.Info("file is its own library destination and the Deluge protected list has not loaded; left pending", "id", bf.ID, "path", srcPath)
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
		full, hydrateErr := env.Store.GetBookFileByID(bf.BookID, bf.ID)
		if hydrateErr != nil || full == nil {
			errCount.Add(1)
			setLastError(fmt.Sprintf("hydrate book file %s: %v", bf.ID, hydrateErr))
			env.Logger.Error("hydrate book file failed", "id", bf.ID, "error", hydrateErr)
			return
		}

		// The importer /discovery/import and maintenance.bulk-deluge-import
		// use: it holds the destination lock, refuses a destination another
		// book_file row names and a source under a static protected prefix
		// (iTunes, config.ProtectedPaths -- this op used to copy those out
		// and repoint them), never overwrites, removes its copy when the row
		// update fails (so a retry is not refused by an orphan), and asks
		// Deluge to move the torrent's storage when that is enabled.
		newPath, importErr := env.Importer.ImportToLibrary(cfg, centralizeImportStore{centralizeStore: env.Store, w: env.Writer}, full, delugeclient.ImportOptions{
			Protected:          protected,
			ExpectedBookFileID: full.ID,
		})
		switch {
		case errors.Is(importErr, tagger.ErrProtectedListNotLoaded):
			// Checked before ErrProtectedPathWrite, which it also wraps.
			notLoadedCount.Add(1)
			env.Logger.Info("file left pending: the Deluge protected list has not loaded", "id", bf.ID, "path", srcPath)
			return
		case errors.Is(importErr, tagger.ErrProtectedPathWrite):
			protectedCount.Add(1)
			env.Logger.Info("file is protected; left pending", "id", bf.ID, "path", srcPath, "reason", importErr)
			return
		case importErr != nil:
			errCount.Add(1)
			setLastError(fmt.Sprintf("import %s: %v", srcPath, importErr))
			if newPath != "" {
				env.Logger.Error("import failed and its copy could not be removed", "id", bf.ID, "src", srcPath, "left_at", newPath, "error", importErr)
			} else {
				env.Logger.Error("import failed", "id", bf.ID, "src", srcPath, "dest", dest, "error", importErr)
			}
			return
		}
		dest = newPath

		n := successCount.Add(1)
		env.Logger.Debug("centralized file",
			"src", srcPath, "dest", dest, "progress", fmt.Sprintf("%d/%d", n, total))
	}

	runErr := env.Items(ctx, centralizeParallelism(), len(groups), kindItemsOptions{
		Collect: true,
		// sdk.Progress reserves a start and a done step around the N units
		// (total = N+2), so the pool reports against the same denominator
		// and the bar never changes units between Start, the pool and Done.
		ProgressTotal: len(groups) + 2,
		// Pure: it reads the item index and len(groups), never a counter.
		PureLabel: func(i, _ int) string {
			return fmt.Sprintf("Centralizing group %d/%d", i+1, len(groups))
		},
	}, func(ctx context.Context, gi int) error {
		for _, bf := range groups[gi] {
			if err := ctx.Err(); err != nil {
				return err
			}
			centralizeOne(bf)
		}
		return nil
	})

	env.Logger.Info("deluge centralization complete",
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
	return centralizeResult{
		Total:                  total,
		Groups:                 len(groups),
		Succeeded:              successCount.Load(),
		AlreadyInLibrary:       inPlaceCount.Load(),
		Protected:              protectedCount.Load(),
		ProtectedListNotLoaded: notLoadedCount.Load(),
		Skipped:                skipCount.Load(),
		Errors:                 errCount.Load(),
	}, runErr
}

// runCentralization is the v2 shell: HEAD's Run over centralizeCore.
func (p *Plugin) runCentralization(ctx context.Context, _ json.RawMessage, reporter sdk.Reporter) error {
	env := centralizeEnv{
		kindEnv:  headEnv(reporter, true),
		Cfg:      &config.AppConfig,
		Store:    p.headStore(),
		Writer:   p.headWriter(),
		Importer: p.headImporter(),
	}
	// An untyped nil unless the plugin has a cache, so only cfg's static
	// prefixes apply otherwise (HEAD's `if p.cache != nil { protected = p.cache }`).
	if p.cache != nil {
		env.Protected = p.cache
	}
	_, err := centralizeCore(ctx, env)
	return err
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
