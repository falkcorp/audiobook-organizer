// file: internal/maintenance/jobs/bulk_deluge_import.go
// version: 1.13.0
// guid: a2b8c6d7-9e0f-1a2b-3c4d-5e6f7a8b9c0d
// last-edited: 2026-10-11

package jobs

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
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

	imported, inPlace, protectedN, failed := 0, 0, 0, 0
	for i := range pending {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
		f := &pending[i]
		if dryRun {
			// A file already at its library destination would be marked
			// imported in place, not copied, or left pending when protected:
			// say so in the preview, with the live run's own predicate.
			action := "dry_run"
			if src := filepath.Clean(f.FilePath); f.FilePath != "" {
				if dest, derr := bdi_destination(config.AppConfig.RootDir, src); derr == nil && dest == src {
					action = bdiStatusInPlace
					if deluge.InPlaceProtection(&config.AppConfig, protected, f.FilePath) != "" {
						action = bdiStatusProtected
					}
				}
			}
			resultJSON, _ := json.Marshal(map[string]any{"path": f.FilePath, "action": action})
			if opID != "" {
				_ = store.CreateOperationResult(&database.OperationResult{
					OperationID: opID,
					BookID:      f.ID,
					ResultJSON:  string(resultJSON),
					Status:      "dry_run",
				})
			}
			switch action {
			case bdiStatusInPlace:
				inPlace++
			case bdiStatusProtected:
				protectedN++
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
				resultJSON, _ := json.Marshal(map[string]any{"path": f.FilePath, "error": errMsg})
				if opID != "" {
					_ = store.CreateOperationResult(&database.OperationResult{
						OperationID: opID,
						BookID:      f.ID,
						ResultJSON:  string(resultJSON),
						Status:      "error",
					})
				}
				failed++
				continue
			}
			newPath, outcome, importErr := bdi_importToLibrary(&config.AppConfig, client, protected, store, full)
			switch {
			case importErr == nil && outcome == bdiProtected:
				// Its own library destination, but protected: left pending,
				// nothing written, as /discovery/import leaves it.
				resultJSON, _ := json.Marshal(map[string]any{"path": f.FilePath})
				if opID != "" {
					_ = store.CreateOperationResult(&database.OperationResult{
						OperationID: opID,
						BookID:      f.ID,
						ResultJSON:  string(resultJSON),
						Status:      bdiStatusProtected,
					})
				}
				protectedN++
			case importErr == nil && outcome == bdiInPlace:
				// Already in the library: marked imported, nothing copied.
				resultJSON, _ := json.Marshal(map[string]any{"path": f.FilePath, "new_path": newPath})
				if opID != "" {
					_ = store.CreateOperationResult(&database.OperationResult{
						OperationID: opID,
						BookID:      f.ID,
						ResultJSON:  string(resultJSON),
						Status:      bdiStatusInPlace,
					})
				}
				inPlace++
			case importErr != nil:
				slog.Warn("bulk-deluge-import", "opID", opID, "f", f.FilePath, "importErr", importErr)
				resultJSON, _ := json.Marshal(map[string]any{"path": f.FilePath, "error": importErr.Error()})
				if opID != "" {
					_ = store.CreateOperationResult(&database.OperationResult{
						OperationID: opID,
						BookID:      f.ID,
						ResultJSON:  string(resultJSON),
						Status:      "error",
					})
				}
				failed++
			default:
				resultJSON, _ := json.Marshal(map[string]any{"path": f.FilePath, "new_path": newPath})
				if opID != "" {
					_ = store.CreateOperationResult(&database.OperationResult{
						OperationID: opID,
						BookID:      f.ID,
						ResultJSON:  string(resultJSON),
						Status:      "imported",
					})
				}
				imported++
			}
		}
		reporter.Increment()
	}

	slog.Info("bulk-deluge-import done. imported failed", "opID", opID, "imported", imported, "already_in_library", inPlace, "protected", protectedN, "failed", failed)
	slog.Info("imported failed total", "imported", imported, "already_in_library", inPlace, "protected", protectedN, "failed", failed, "total", total)
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

// bdiStatusProtected is the result status of a pending file that is its own
// library destination but protected (deluge.InPlaceProtection): left
// pending, nothing written.
const bdiStatusProtected = "protected"

// bdiOutcome is what bdi_importToLibrary did with a file it did not fail on.
type bdiOutcome int

const (
	// bdiCopied: copied into the library and the row repointed (or the row
	// was already marked imported, and nothing was done).
	bdiCopied bdiOutcome = iota
	// bdiInPlace: already at its library destination; marked imported.
	bdiInPlace
	// bdiProtected: at its library destination but protected; left pending.
	bdiProtected
)

// bdiBookFileStore is what bdi_importToLibrary writes: the whole-row update of
// a copied file, and ModifyBookFile for marking one already in place.
type bdiBookFileStore interface {
	bookFileWriter
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

// bdi_importToLibrary copies a book file into the library root and updates the
// DB record. A file already at its library destination is not copied: it is
// marked imported (bdiInPlace: ImportedFromDelugeAt only,
// deluge.MarkImportedInPlace), or it would stay pending and be listed on every
// run -- unless protected (deluge.InPlaceProtection over protected and cfg),
// which leaves it pending with nothing written (bdiProtected), as
// ImportToLibraryWith refuses it.
func bdi_importToLibrary(cfg *config.Config, delugeClient *deluge.Client, protected tagger.PathChecker, store bdiBookFileStore, bookFile *database.BookFile) (newPath string, outcome bdiOutcome, err error) {
	if bookFile == nil {
		return "", bdiCopied, fmt.Errorf("bdi_importToLibrary: bookFile is nil")
	}
	if bookFile.ImportedFromDelugeAt != nil {
		slog.Info("bdi_importToLibrary already imported, skipping", "bookFile", bookFile.FilePath)
		return bookFile.FilePath, bdiCopied, nil
	}
	if bookFile.FilePath == "" {
		return "", bdiCopied, fmt.Errorf("bdi_importToLibrary: bookFile.FilePath is empty")
	}
	src := filepath.Clean(bookFile.FilePath)

	dest, destErr := bdi_destination(cfg.RootDir, src)
	if destErr != nil {
		return "", bdiCopied, fmt.Errorf("bdi_importToLibrary: unsafe dest path: %w", destErr)
	}
	destDir := filepath.Dir(dest)
	if src == dest {
		marked, markErr := deluge.MarkImportedInPlace(store, cfg, protected, bookFile.BookID, bookFile.ID, bookFile.FilePath, time.Now())
		switch {
		case markErr != nil:
			return "", bdiCopied, fmt.Errorf("bdi_importToLibrary: %w", markErr)
		case marked == deluge.InPlaceProtected:
			bdiLog.Info("bdi_importToLibrary: %s is its own library destination but protected; left pending", logger.SanitizeLogValue(src))
			return src, bdiProtected, nil
		case marked == deluge.InPlaceMoved, marked == deluge.InPlaceGone:
			return "", bdiCopied, fmt.Errorf("bdi_importToLibrary: book file %s changed or was removed while being marked imported in place at %s; nothing written", bookFile.ID, src)
		}
		slog.Info("bdi_importToLibrary source is already at its library destination; marked imported, nothing copied", "src", src)
		return src, bdiInPlace, nil
	}

	if err := os.MkdirAll(destDir, 0o755); err != nil {
		return "", bdiCopied, fmt.Errorf("bdi_importToLibrary: create dest dir %s: %w", destDir, err)
	}

	// The importers' shared copy step: it never overwrites. This used to be
	// bdi_ioCopy, which opened dest with os.Create and so truncated whatever
	// was there: a second pending file with the same basename, or a file a
	// torrent was seeding from RootDir. An identical dest (a copy an earlier
	// run made and could not record) is adopted, a different one refuses the
	// file with an error.
	if _, copyErr := deluge.CopyIntoLibrary(src, dest); copyErr != nil {
		return "", bdiCopied, fmt.Errorf("bdi_importToLibrary: %w", copyErr)
	}

	now := time.Now()
	bookFile.DelugeOriginalPath = src
	bookFile.FilePath = dest
	bookFile.ImportedFromDelugeAt = &now

	if err := store.UpdateBookFile(bookFile.ID, bookFile); err != nil {
		return dest, bdiCopied, fmt.Errorf("bdi_importToLibrary: UpdateBookFile %s: %w", bookFile.ID, err)
	}

	slog.Info("bdi_importToLibrary copied ->", "src", src, "dest", dest)

	if cfg.DelugeMoveEnabled && bookFile.DelugeHash != "" && delugeClient != nil {
		moveErr := delugeClient.MoveStorage([]string{bookFile.DelugeHash}, filepath.Dir(dest))
		if moveErr != nil {
			slog.Warn("bdi_importToLibrary MoveStorage for hash failed (non-fatal)", "bookFile", bookFile.DelugeHash, "moveErr", moveErr)
		}
	}

	return dest, bdiCopied, nil
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
