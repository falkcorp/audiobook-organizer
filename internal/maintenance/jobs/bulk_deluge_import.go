// file: internal/maintenance/jobs/bulk_deluge_import.go
// version: 1.3.0
// guid: a2b8c6d7-9e0f-1a2b-3c4d-5e6f7a8b9c0d
// last-edited: 2026-07-07

package jobs

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/deluge"
	"github.com/falkcorp/audiobook-organizer/internal/maintenance"
	"github.com/falkcorp/audiobook-organizer/internal/util"
)

func init() { maintenance.Register(&bulkDelugeImportJob{}) }

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

func (j *bulkDelugeImportJob) Run(ctx context.Context, store database.Store, reporter maintenance.ProgressReporter, dryRun bool) error {
	opID := maintenance.OperationIDFromCtx(ctx)

	maxBooks := 0
	if opID != "" {
		if raw, err := store.GetOperationParams(opID); err == nil && len(raw) > 0 {
			var p bdi_params
			if jerr := json.Unmarshal(raw, &p); jerr == nil {
				maxBooks = p.MaxBooks
				dryRun = p.DryRun
			}
		}
	}

	client := bdi_buildDelugeClient()

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

	imported, failed := 0, 0
	for i := range pending {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
		f := &pending[i]
		if dryRun {
			resultJSON, _ := json.Marshal(map[string]any{"path": f.FilePath, "action": "dry_run"})
			if opID != "" {
				_ = store.CreateOperationResult(&database.OperationResult{
					OperationID: opID,
					BookID:      f.ID,
					ResultJSON:  string(resultJSON),
					Status:      "dry_run",
				})
			}
			imported++
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
			newPath, importErr := bdi_importToLibrary(&config.AppConfig, client, store, full)
			if importErr != nil {
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
			} else {
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

	slog.Info("bulk-deluge-import done. imported failed", "opID", opID, "imported", imported, "failed", failed)
	slog.Info("imported failed total", "imported", imported, "failed", failed, "total", total)
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

// bdi_importToLibrary copies a book file into the library root and updates the DB record.
func bdi_importToLibrary(cfg *config.Config, delugeClient *deluge.Client, store database.Store, bookFile *database.BookFile) (newPath string, err error) {
	if bookFile == nil {
		return "", fmt.Errorf("bdi_importToLibrary: bookFile is nil")
	}
	if bookFile.ImportedFromDelugeAt != nil {
		slog.Info("bdi_importToLibrary already imported, skipping", "bookFile", bookFile.FilePath)
		return bookFile.FilePath, nil
	}
	src := filepath.Clean(bookFile.FilePath)
	if src == "" {
		return "", fmt.Errorf("bdi_importToLibrary: bookFile.FilePath is empty")
	}

	var destDir string
	rel, relErr := filepath.Rel(cfg.RootDir, filepath.Dir(src))
	if relErr == nil && !filepath.IsAbs(rel) && !bdi_isParentTraversal(rel) {
		var joinErr error
		destDir, joinErr = util.SafeJoin(cfg.RootDir, rel)
		if joinErr != nil {
			destDir = filepath.Clean(cfg.RootDir)
		}
	} else {
		destDir = filepath.Clean(cfg.RootDir)
	}

	dest, destErr := util.SafeJoin(destDir, filepath.Base(src))
	if destErr != nil {
		return "", fmt.Errorf("bdi_importToLibrary: unsafe dest path: %w", destErr)
	}
	if src == dest {
		slog.Info("bdi_importToLibrary source and dest are the same (), skipping copy", "src", src)
		return src, nil
	}

	if err := os.MkdirAll(destDir, 0o755); err != nil {
		return "", fmt.Errorf("bdi_importToLibrary: create dest dir %s: %w", destDir, err)
	}

	if copyErr := bdi_ioCopy(src, dest); copyErr != nil {
		return "", fmt.Errorf("bdi_importToLibrary: copy %s -> %s: %w", src, dest, copyErr)
	}

	now := time.Now()
	bookFile.DelugeOriginalPath = src
	bookFile.FilePath = dest
	bookFile.ImportedFromDelugeAt = &now

	if err := store.UpdateBookFile(bookFile.ID, bookFile); err != nil {
		return dest, fmt.Errorf("bdi_importToLibrary: UpdateBookFile %s: %w", bookFile.ID, err)
	}

	slog.Info("bdi_importToLibrary copied ->", "src", src, "dest", dest)

	if cfg.DelugeMoveEnabled && bookFile.DelugeHash != "" && delugeClient != nil {
		moveErr := delugeClient.MoveStorage([]string{bookFile.DelugeHash}, filepath.Dir(dest))
		if moveErr != nil {
			slog.Warn("bdi_importToLibrary MoveStorage for hash failed (non-fatal)", "bookFile", bookFile.DelugeHash, "moveErr", moveErr)
		}
	}

	return dest, nil
}

func bdi_isParentTraversal(rel string) bool {
	return len(rel) >= 2 && rel[:2] == ".."
}

func bdi_ioCopy(src, dest string) error {
	src = filepath.Clean(src)
	dest = filepath.Clean(dest)
	in, err := os.Open(src)
	if err != nil {
		return fmt.Errorf("open src: %w", err)
	}
	defer in.Close()

	out, err := os.Create(dest)
	if err != nil {
		return fmt.Errorf("create dest: %w", err)
	}
	defer func() {
		cerr := out.Close()
		if err == nil {
			err = cerr
		}
	}()

	if _, err = io.Copy(out, in); err != nil {
		return fmt.Errorf("io.Copy: %w", err)
	}
	return nil
}
