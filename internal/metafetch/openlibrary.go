// file: internal/metafetch/openlibrary.go
// version: 1.3.0
// guid: d4e5f6a7-b8c9-0d1e-2f3a-4b5c6d7e8f90
// last-edited: 2026-08-30

package metafetch

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sync"

	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/openlibrary"
	"github.com/falkcorp/audiobook-organizer/internal/operations"
)

// OpenLibraryService manages the Open Library data dump lifecycle.
type OpenLibraryService struct {
	OLStore   *openlibrary.OLStore
	Tracker   *openlibrary.DownloadTracker
	Mu        sync.Mutex
	Importing map[string]bool
}

// GetOLDumpDir returns the configured dump directory, falling back to {RootDir}/openlibrary-dumps.
func GetOLDumpDir() string {
	if config.AppConfig.OpenLibraryDumpDir != "" {
		return config.AppConfig.OpenLibraryDumpDir
	}
	if config.AppConfig.RootDir != "" {
		return filepath.Join(config.AppConfig.RootDir, "openlibrary-dumps")
	}
	return ""
}

// NewOpenLibraryService creates a new service, optionally opening the existing store.
// If an existing oldb directory is found, it auto-enables Open Library dumps in config.
func NewOpenLibraryService() *OpenLibraryService {
	svc := &OpenLibraryService{
		Tracker:   openlibrary.NewDownloadTracker(),
		Importing: make(map[string]bool),
	}

	storePath := filepath.Join(GetOLDumpDir(), "oldb")
	if info, err := os.Stat(storePath); err == nil && info.IsDir() {
		// Auto-enable if store directory exists on disk
		if !config.AppConfig.OpenLibraryDumpEnabled {
			slog.Info("Found existing OL dump store at , auto-enabling OpenLibraryDumpEnabled", "path", storePath)
			config.AppConfig.OpenLibraryDumpEnabled = true
		}
		store, err := openlibrary.NewOLStore(storePath)
		if err != nil {
			slog.Warn("Failed to open OL dump store", "error", err)
		} else {
			svc.OLStore = store
		}
	}

	return svc
}

// Store returns the underlying OLStore (may be nil).
func (svc *OpenLibraryService) Store() *openlibrary.OLStore {
	return svc.OLStore
}

// Close closes the underlying store.
func (svc *OpenLibraryService) Close() {
	if svc.OLStore != nil {
		svc.OLStore.Close()
	}
}

// EnsureStore opens or creates the OL store if not already open.
// Returns an error if the store cannot be opened.
func (svc *OpenLibraryService) EnsureStore(targetDir string) error {
	svc.Mu.Lock()
	defer svc.Mu.Unlock()
	if svc.OLStore == nil {
		storePath := filepath.Join(targetDir, "oldb")
		store, err := openlibrary.NewOLStore(storePath)
		if err != nil {
			return err
		}
		svc.OLStore = store
	}
	return nil
}

// UploadedFileInfo describes a dump file on disk that hasn't been imported yet.
type UploadedFileInfo struct {
	Filename string `json:"filename"`
	Size     int64  `json:"size"`
	ModTime  string `json:"mod_time"`
}

// ValidDumpTypes lists the valid dump type names.
var ValidDumpTypes = map[string]bool{"editions": true, "authors": true, "works": true}

// Import performs the actual Open Library dump import logic.
// It imports the specified dump types from targetDir concurrently,
// updating the progress reporter as it goes.
func (svc *OpenLibraryService) Import(ctx context.Context, progress operations.ProgressReporter, targetDir string, types []string) error {
	if progress != nil {
		_ = progress.UpdateProgress(0, len(types), fmt.Sprintf("Starting Open Library import (%d dump types)", len(types)))
	}

	var importWg sync.WaitGroup
	var importErr error
	var mu sync.Mutex

	for i, dumpType := range types {
		svc.Mu.Lock()
		if svc.Importing[dumpType] {
			svc.Mu.Unlock()
			slog.Warn("OL import already in progress for", "value", dumpType)
			continue
		}
		svc.Importing[dumpType] = true
		svc.Mu.Unlock()

		if progress != nil {
			_ = progress.Log("info", fmt.Sprintf("Starting %s import", dumpType), nil)
		}

		dt, idx := dumpType, i
		importWg.Go(func() {
			defer func() {
				svc.Mu.Lock()
				delete(svc.Importing, dt)
				svc.Mu.Unlock()
			}()

			filePath := filepath.Join(targetDir, openlibrary.DumpFilename(dt))
			slog.Info("Starting OL dump import from", "value", dt, "path", filePath)

			lastReported := 0
			err := svc.OLStore.ImportDump(dt, filePath, func(count int) {
				if count-lastReported >= 50000 {
					lastReported = count
					if progress != nil {
						msg := fmt.Sprintf("Importing %s: %dk records", dt, count/1000)
						_ = progress.UpdateProgress(idx, len(types), msg)
					}
					slog.Info("OL import progress records", "value", dt, "count", count)
				}
			})

			if err != nil {
				slog.Error("OL dump import failed for", "value", dt, "error", err)
				if progress != nil {
					_ = progress.Log("error", fmt.Sprintf("%s import failed: %v", dt, err), nil)
				}
				mu.Lock()
				importErr = err
				mu.Unlock()
			} else {
				slog.Info("OL dump import complete", "value", dt)
				if progress != nil {
					_ = progress.Log("info", fmt.Sprintf("%s import complete", dt), nil)
				}
			}
		})
	}
	importWg.Wait()

	if progress != nil {
		if importErr != nil {
			_ = progress.UpdateProgress(len(types), len(types), fmt.Sprintf("Import finished with errors: %v", importErr))
		} else {
			_ = progress.UpdateProgress(len(types), len(types), "All Open Library dump imports complete")
		}
	}
	slog.Info("All OL dump imports complete")
	return importErr
}
