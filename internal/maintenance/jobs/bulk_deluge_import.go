// file: internal/maintenance/jobs/bulk_deluge_import.go
// version: 1.14.0
// guid: a2b8c6d7-9e0f-1a2b-3c4d-5e6f7a8b9c0d
// last-edited: 2026-10-11

package jobs

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"path/filepath"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/deluge"
	"github.com/falkcorp/audiobook-organizer/internal/logger"
	"github.com/falkcorp/audiobook-organizer/internal/maintenance"
	"github.com/falkcorp/audiobook-organizer/internal/pathutil"
	"github.com/falkcorp/audiobook-organizer/internal/tagger"
	"github.com/falkcorp/audiobook-organizer/internal/util"
)

func init() { maintenance.Register(&bulkDelugeImportJob{}) }

// bdiLog is this job's logger for new log lines (internal/logger's
// sanitizing wrapper; the job's older lines still call log/slog directly
// under the slog guard's ratchet).
var bdiLog = logger.New("bulk-deluge-import")

type bulkDelugeImportJob struct{}

type bdi_params struct {
	DryRun   bool `json:"dry_run"`
	MaxBooks int  `json:"max_books,omitempty"`
}

func (j *bulkDelugeImportJob) ID() string       { return "bulk-deluge-import" }
func (j *bulkDelugeImportJob) Name() string     { return "Bulk Deluge Import" }
func (j *bulkDelugeImportJob) Category() string { return "Import" }
func (j *bulkDelugeImportJob) Description() string {
	return "Imports all book_files that have a deluge_hash but have not yet been copied into the library"
}
func (j *bulkDelugeImportJob) DefaultParams() any { return &bdi_params{DryRun: true} }
func (j *bulkDelugeImportJob) CanResume() bool    { return true }

func (j *bulkDelugeImportJob) Run(ctx context.Context, store maintenance.JobStore, reporter maintenance.ProgressReporter, dryRun bool) error {
	opID := maintenance.OperationIDFromCtx(ctx)

	// max_books arrives on the run's own params blob, via the context. This used
	// to read store.GetOperationParams(opID), whose only writer
	// (operations.SaveParams) has no caller on the maintenance path since the v1
	// op minter was retired (#2784), so max_books was always 0 — unlimited.
	// See maintenance.WithRawParams.
	//
	// `dryRun = p.DryRun` is deliberately NOT carried over. dryRun is already a
	// parameter of this function and the caller decodes it from these very same
	// bytes (server/maintenance_job_op.go decodes maintenanceJobOpParams.DryRun
	// off the run's params and passes it here), so the assignment was a SECOND
	// source of truth for the one flag whose zero value is destructive. It was
	// inert while this read returned nothing; going live it would have started
	// overwriting the resolved argument — including clobbering the advertised
	// dry_run:true default with false whenever the blob omitted the key, which is
	// precisely the preview-becomes-mutation failure the dispatcher's
	// advertisedDryRunDefault exists to prevent. The argument is authoritative.
	maxBooks := 0
	if raw := maintenance.RawParamsFromCtx(ctx); len(raw) > 0 {
		var p bdi_params
		if jerr := json.Unmarshal(raw, &p); jerr == nil {
			maxBooks = p.MaxBooks
		}
	}

	client := bdi_buildDelugeClient()
	// The protected-path predicate the server's importers use: active
	// torrents' save_paths (when Deluge is configured) plus
	// config.ProtectedPaths. Built once per run. A file that is its own
	// library destination under it is left pending (bdiStatusProtected), in
	// the preview as in the live run.
	protected := bdi_protectedPaths(client)
	// Probed once: while the Deluge list has never loaded, no in-place row
	// can be cleared, and asking per row cost two ListTorrents attempts each
	// (InPlaceProtection's ProtectedBy and Loaded both retry a refresh). The
	// in-place rows of such a run are reported bdiStatusListNotLoaded --
	// or protected, when a static prefix names them, which needs no list --
	// and left pending; the next run with Deluge reachable clears them.
	listLoaded := tagger.ProtectedListLoaded(protected)
	if !listLoaded {
		bdiLog.Warn("bulk-deluge-import: the Deluge protected-path list has not loaded; files already at their library destination are left pending this run (%s)", bdiStatusListNotLoaded)
	}

	pending, err := store.GetBookFilesNeedingDelugeImportCore()
	if err != nil {
		return fmt.Errorf("GetBookFilesNeedingDelugeImportCore: %w", err)
	}
	if maxBooks > 0 && len(pending) > maxBooks {
		pending = pending[:maxBooks]
	}

	total := len(pending)
	slog.Info("bulk-deluge-import files pending (dry_run)", "opID", opID, "total", total, "dryRun", dryRun)
	reporter.SetTotal(total)

	record := func(f *database.BookFileCore, status string, payload map[string]any) {
		if opID == "" {
			return
		}
		resultJSON, _ := json.Marshal(payload)
		_ = store.CreateOperationResult(&database.OperationResult{
			OperationID: opID,
			BookID:      f.ID,
			ResultJSON:  string(resultJSON),
			Status:      status,
		})
	}

	imported, inPlace, protectedN, notLoadedN, failed := 0, 0, 0, 0, 0
	for i := range pending {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
		f := &pending[i]
		if dryRun {
			// Say in the preview what the live run would do, with its own
			// predicates: mark a file already at its library destination
			// imported in place (or leave it pending when protected or the
			// list has not loaded), refuse a source under a static protected
			// prefix, and copy the rest.
			action := bdi_previewAction(&config.AppConfig, protected, listLoaded, f.FilePath)
			record(f, "dry_run", map[string]any{"path": f.FilePath, "action": action})
			switch action {
			case bdiStatusInPlace:
				inPlace++
			case bdiStatusProtected:
				protectedN++
			case bdiStatusListNotLoaded:
				notLoadedN++
			default:
				imported++
			}
		} else {
			// Hydrate the full row before writing back — f is the Core
			// (memdb-slim) projection, and passing it straight to
			// bdi_importToLibrary would silently wipe the
			// fingerprint-diagnostic fields on its UpdateBookFile call
			// (STOREFID PR-D).
			full, hydrateErr := store.GetBookFileByID(f.BookID, f.ID)
			if hydrateErr != nil || full == nil {
				errMsg := "book file not found"
				if hydrateErr != nil {
					errMsg = hydrateErr.Error()
				}
				slog.Warn("bulk-deluge-import hydrate failed", "opID", opID, "f", f.FilePath, "err", errMsg)
				record(f, "error", map[string]any{"path": f.FilePath, "error": errMsg})
				failed++
				continue
			}
			newPath, outcome, importErr := bdi_importToLibrary(&config.AppConfig, client, protected, listLoaded, store, full)
			switch outcome {
			case bdiProtected:
				// Protected (its own library destination under a protected
				// path, or a source under a static protected prefix): left
				// pending, nothing written, as /discovery/import leaves it.
				payload := map[string]any{"path": f.FilePath}
				if importErr != nil {
					payload["reason"] = importErr.Error()
				}
				record(f, bdiStatusProtected, payload)
				protectedN++
			case bdiListNotLoaded:
				// Unknown whether it is seeding: left pending, nothing
				// written, until a run finds the list loaded.
				record(f, bdiStatusListNotLoaded, map[string]any{"path": f.FilePath})
				notLoadedN++
			case bdiInPlace:
				// Already in the library: marked imported, nothing copied.
				record(f, bdiStatusInPlace, map[string]any{"path": f.FilePath, "new_path": newPath})
				inPlace++
			case bdiFailed:
				bdiLog.Warn("bulk-deluge-import: %s: %v", logger.SanitizeLogValue(f.FilePath), importErr)
				payload := map[string]any{"path": f.FilePath, "error": importErr.Error()}
				if newPath != "" {
					// ImportToLibraryWith could not remove a copy it made:
					// name the file left in the library.
					payload["left_at"] = newPath
				}
				record(f, "error", payload)
				failed++
			default:
				record(f, "imported", map[string]any{"path": f.FilePath, "new_path": newPath})
				imported++
			}
		}
		reporter.Increment()
	}

	slog.Info("bulk-deluge-import done. imported failed", "opID", opID, "imported", imported, "already_in_library", inPlace, "protected", protectedN, "failed", failed)
	bdiLog.Info("bulk-deluge-import: %d imported, %d already in library, %d protected, %d left pending (protected list not loaded), %d failed, of %d",
		imported, inPlace, protectedN, notLoadedN, failed, total)
	return nil
}

// bdi_buildDelugeClient creates a Deluge client from application config.
func bdi_buildDelugeClient() *deluge.Client {
	url := config.AppConfig.DelugeWebURL
	pass := config.AppConfig.DelugeWebPassword
	if url == "" {
		dc := config.AppConfig.DownloadClient.Torrent.Deluge
		if dc.Host != "" {
			port := dc.Port
			if port == 0 {
				port = 8112
			}
			url = fmt.Sprintf("http://%s:%d", dc.Host, port)
			pass = dc.Password
		}
	}
	if url == "" {
		return nil
	}
	if pass == "" {
		pass = "deluge"
	}
	c, err := deluge.New(url, pass)
	if err != nil {
		slog.Warn("bulk-deluge-import failed to create deluge client", "err", err)
		return nil
	}
	return c
}

// bdi_protectedPaths is the protected-path predicate for this run:
// deluge.NewProtectedPathCache over client (nil when Deluge is not
// configured, leaving only the static prefixes) and config.ProtectedPaths,
// the same pair the server's ProtectedPathCache holds.
func bdi_protectedPaths(client *deluge.Client) tagger.PathChecker {
	return deluge.NewProtectedPathCache(client, config.AppConfig.ProtectedPaths)
}

// bdiStatusInPlace is the result status of a pending file that was already at
// its library destination: marked imported, nothing copied.
const bdiStatusInPlace = "already_in_library"

// bdiStatusProtected is the result status of a pending file left pending
// because it is protected: its own library destination under a protected
// path (deluge.InPlaceProtection), or a source under a static protected
// prefix (deluge.StaticProtection: config.ProtectedPaths, the iTunes
// library), which is never copied out. Nothing written.
const bdiStatusProtected = "protected"

// bdiStatusListNotLoaded is the result status of a pending file already at its
// library destination while the Deluge protected-path list has never loaded:
// whether a torrent seeds from it is unknown, so it is left pending with
// nothing written. Not "protected": Deluge being unreachable is no verdict on
// the file, and the next run with the list loaded clears it.
const bdiStatusListNotLoaded = "protected_list_not_loaded"

// bdiOutcome is what bdi_importToLibrary did.
type bdiOutcome int

const (
	// bdiCopied: copied into the library and the row repointed (or the row
	// was already marked imported, and nothing was done).
	bdiCopied bdiOutcome = iota
	// bdiInPlace: already at its library destination; marked imported.
	bdiInPlace
	// bdiProtected: protected; left pending.
	bdiProtected
	// bdiListNotLoaded: at its library destination, and the Deluge list has
	// not loaded; left pending.
	bdiListNotLoaded
	// bdiFailed: the import failed; err says why.
	bdiFailed
)

// bdiBookFileStore is what bdi_importToLibrary reads and writes:
// deluge.ImportToLibraryWith's store (the path-index ownership check and the
// whole-row repoint) and ModifyBookFile for marking a file already in place.
type bdiBookFileStore interface {
	deluge.Store
	deluge.BookFileModifier
}

// bdi_destination is where src lands in the library: under the same relative
// directory when src is inside root (pathutil.RelEscapesBase, shared with the
// other importers), directly under root otherwise.
func bdi_destination(root, src string) (string, error) {
	var destDir string
	rel, relErr := filepath.Rel(root, filepath.Dir(src))
	if relErr == nil && !filepath.IsAbs(rel) && !pathutil.RelEscapesBase(rel) {
		var joinErr error
		destDir, joinErr = util.SafeJoin(root, rel)
		if joinErr != nil {
			destDir = filepath.Clean(root)
		}
	} else {
		destDir = filepath.Clean(root)
	}
	return util.SafeJoin(destDir, filepath.Base(src))
}

// bdi_inPlaceUnloaded is the verdict on a file at its own library destination
// while the Deluge list has not loaded: protected when a static prefix names
// it (cfg's prefixes need no list, and a nil checker asks Deluge nothing),
// list-not-loaded otherwise. Never "may be marked": a nil checker alone counts
// as loaded, so it is used here only for the static half.
func bdi_inPlaceUnloaded(cfg *config.Config, src string) bdiOutcome {
	if deluge.StaticProtection(cfg, nil, src) != "" {
		return bdiProtected
	}
	return bdiListNotLoaded
}

// bdi_previewAction is the dry run's verdict on one pending file, from the
// live run's own predicates.
func bdi_previewAction(cfg *config.Config, protected tagger.PathChecker, listLoaded bool, path string) string {
	if path == "" {
		return "dry_run"
	}
	src := filepath.Clean(path)
	dest, derr := bdi_destination(cfg.RootDir, src)
	if derr != nil {
		return "dry_run"
	}
	if dest != src {
		// A static protected source is refused by ImportToLibraryWith. When
		// the list has not loaded, cfg's prefixes alone (the cache's static
		// list is cfg.ProtectedPaths) answer without asking Deluge.
		checker := protected
		if !listLoaded {
			checker = nil
		}
		if deluge.StaticProtection(cfg, checker, src) != "" {
			return bdiStatusProtected
		}
		return "dry_run"
	}
	if !listLoaded {
		if bdi_inPlaceUnloaded(cfg, src) == bdiProtected {
			return bdiStatusProtected
		}
		return bdiStatusListNotLoaded
	}
	if deluge.InPlaceProtection(cfg, protected, path) != "" {
		return bdiStatusProtected
	}
	return bdiStatusInPlace
}

// bdi_importToLibrary imports one pending book file.
//
// A file already at its library destination is not copied: it is marked
// imported (bdiInPlace: ImportedFromDelugeAt only, deluge.MarkImportedInPlace),
// or it would stay pending and be listed on every run -- unless protected
// (deluge.InPlaceProtection over protected and cfg: bdiProtected) or the
// Deluge list has not loaded (listLoaded false, probed once per run:
// bdiListNotLoaded), which leave it pending with nothing written.
//
// Every other file goes through deluge.ImportToLibraryWith, the importer
// /discovery/import uses, with the caller's row as ExpectedBookFileID. That
// is what holds the destination lock and refuses a destination another
// book_file row already names: this job used to call deluge.CopyIntoLibrary
// itself, without either, so two pending rows with one basename and the same
// bytes both landed on RootDir/<basename> (the first copied it, the second
// adopted it) and two rows named one file. It also refuses a source under a
// static protected prefix (the iTunes library, config.ProtectedPaths), which
// this job used to copy out and repoint, and on a failed row update it
// removes the copy it made so the next run does not find it in the way. A
// refusal wrapping tagger.ErrProtectedPathWrite is bdiProtected (err says
// why); any other error is bdiFailed, and newPath then names a copy left in
// the library when one could not be removed.
func bdi_importToLibrary(cfg *config.Config, delugeClient *deluge.Client, protected tagger.PathChecker, listLoaded bool, store bdiBookFileStore, bookFile *database.BookFile) (newPath string, outcome bdiOutcome, err error) {
	if bookFile == nil {
		return "", bdiFailed, fmt.Errorf("bdi_importToLibrary: bookFile is nil")
	}
	if bookFile.ImportedFromDelugeAt != nil {
		slog.Info("bdi_importToLibrary already imported, skipping", "bookFile", bookFile.FilePath)
		return bookFile.FilePath, bdiCopied, nil
	}
	if bookFile.FilePath == "" {
		return "", bdiFailed, fmt.Errorf("bdi_importToLibrary: bookFile.FilePath is empty")
	}
	src := filepath.Clean(bookFile.FilePath)

	dest, destErr := bdi_destination(cfg.RootDir, src)
	if destErr != nil {
		return "", bdiFailed, fmt.Errorf("bdi_importToLibrary: unsafe dest path: %w", destErr)
	}
	if src == dest {
		if !listLoaded {
			got := bdi_inPlaceUnloaded(cfg, src)
			bdiLog.Info("bdi_importToLibrary: %s is its own library destination and the Deluge protected list has not loaded; left pending", logger.SanitizeLogValue(src))
			return src, got, nil
		}
		marked, markErr := deluge.MarkImportedInPlace(store, cfg, protected, bookFile.BookID, bookFile.ID, bookFile.FilePath, time.Now())
		switch {
		case markErr != nil:
			return "", bdiFailed, fmt.Errorf("bdi_importToLibrary: %w", markErr)
		case marked == deluge.InPlaceProtected:
			bdiLog.Info("bdi_importToLibrary: %s is its own library destination but protected; left pending", logger.SanitizeLogValue(src))
			return src, bdiProtected, nil
		case marked == deluge.InPlaceListNotLoaded:
			return src, bdiListNotLoaded, nil
		case marked == deluge.InPlaceMoved, marked == deluge.InPlaceGone:
			return "", bdiFailed, fmt.Errorf("bdi_importToLibrary: book file %s changed or was removed while being marked imported in place at %s; nothing written", bookFile.ID, src)
		}
		slog.Info("bdi_importToLibrary source is already at its library destination; marked imported, nothing copied", "src", src)
		return src, bdiInPlace, nil
	}

	newPath, err = deluge.ImportToLibraryWith(cfg, delugeClient, store, bookFile, deluge.ImportOptions{
		Protected:          protected,
		ExpectedBookFileID: bookFile.ID,
	})
	switch {
	case errors.Is(err, tagger.ErrProtectedListNotLoaded):
		// ImportToLibraryWith resolved the destination to the source itself
		// and the list has not loaded. Checked before ErrProtectedPathWrite,
		// which this error also wraps.
		return src, bdiListNotLoaded, nil
	case errors.Is(err, tagger.ErrProtectedPathWrite):
		bdiLog.Info("bdi_importToLibrary: %s is protected; left pending: %v", logger.SanitizeLogValue(src), err)
		return src, bdiProtected, err
	case err != nil:
		return newPath, bdiFailed, fmt.Errorf("bdi_importToLibrary: %w", err)
	}
	return newPath, bdiCopied, nil
}

// Policy: ResumeRestart. CanResume() is true and this job checkpoints nothing,
// so a resume re-runs it; ResumeRestart is what allows that to happen at all.
//
// SCOPE: this makes the declaration correct, and correct is only consulted on
// one path. resumeAfterStartup takes its candidates from ListActiveOperationsV2
// (the opv2:act: index = queued|running), and every clean shutdown writes a
// status that deletes that key -- so a job stopped by a deploy is invisible to
// the sweep whatever its policy says, and only a hard kill leaves a row it can
// act on. That gap is pre-existing, affects every v2 op, and is tracked in
// todo.d/20260823-v2-resume-sweep-is-blind-to-interrupted-rows.md. Declaring
// ResumeDrop here would additionally throw the run away on the one path that
// does work.
//
// This was ResumeDrop until 2026-08-23, on the reasoning that a dry_run:true job
// could not take ResumeRequeue because server.resumeV2Op re-enqueues with nil
// params, under which DryRun resolves to false and a preview runs for real. That
// reasoning no longer applies, on two independent grounds. First, resumeV2Op is
// unreachable for maintenance: its one caller is fed from GetInterruptedOperations
// (v1 rows) and dispatches only when opRegistry.Def(op.Type) resolves, but v1
// maintenance rows are typed "maintenance:<job>" while v2 defs are
// "maintenance.<job>", and RegisterOp rejects ids containing ":". Second, and
// decisively, ResumeRestart never requeues at all — it updates the existing row
// in place, so Params (dry_run included) is preserved by construction rather than
// reconstructed. TestResume_PreservesParamsAcrossRestartAndRequeue pins that.
//
// ResumeDrop was not a no-op choice: until the v1 op minter was retired, these
// jobs were resumed by server.resumeLegacyOp's default branch off the v1 row, so
// the declared policy never had to be correct. That branch is gone, and without
// this a job advertising CanResume() would silently never resume.
func (j *bulkDelugeImportJob) Policy() maintenance.ExecutionPolicy {
	return maintenance.RestartPolicy()
}
