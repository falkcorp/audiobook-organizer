// file: internal/metafetch/openlibrary.go
// version: 1.11.0
// guid: d4e5f6a7-b8c9-0d1e-2f3a-4b5c6d7e8f90
// last-edited: 2026-10-11

package metafetch

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/compactprogress"
	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/metrics"
	"github.com/falkcorp/audiobook-organizer/internal/openlibrary"
	"github.com/falkcorp/audiobook-organizer/internal/operations"
)

// OpenLibraryService manages the Open Library data dump lifecycle.
type OpenLibraryService struct {
	Tracker   *openlibrary.DownloadTracker
	Mu        sync.Mutex
	Importing map[string]bool

	// olStore is the open store, or nil. Guarded by Mu, and unexported so that
	// every write goes through setStoreLocked: a write from anywhere else would
	// leave live (what lookups read) pointing at a different store. Code that
	// does not hold Mu reads LiveStore instead.
	olStore *openlibrary.OLStore

	// pendingClose is a store whose Close timed out: a lookup or import
	// outlived the drain, so its engine is still open (and holds the
	// directory's lock file) while the store itself already refuses all
	// work. It is kept, not dropped, so the close can be finished once the
	// straggler lets go; see CloseStoreLocked. Guarded by Mu.
	pendingClose *openlibrary.OLStore
	// live is olStore published for readers that must not take Mu (a
	// metadata lookup: Mu is held across a Pebble open and across RemoveAll).
	// Written only by setStoreLocked, so it follows every change to olStore;
	// nil whenever olStore is nil, which includes while a close is pending.
	live atomic.Pointer[openlibrary.OLStore]
	// CloseTimeout bounds each close attempt's wait for in-flight accesses.
	// Zero means database.DefaultCloseDrainTimeout; tests shorten it.
	CloseTimeout time.Duration
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
			svc.setStoreLocked(store)
		}
	}

	return svc
}

// NewOpenLibraryServiceWithStore returns a service that already holds store,
// for callers (tests, tools) that open the engine themselves. It does not look
// at the dump directory or the config, unlike NewOpenLibraryService. The store
// goes through setStoreLocked like every other, so LiveStore sees it.
func NewOpenLibraryServiceWithStore(store *openlibrary.OLStore) *OpenLibraryService {
	svc := &OpenLibraryService{
		Tracker:   openlibrary.NewDownloadTracker(),
		Importing: make(map[string]bool),
	}
	svc.setStoreLocked(store)
	return svc
}

// setStoreLocked replaces the live store and publishes it to LiveStore. The
// caller holds Mu (or is the constructor).
func (svc *OpenLibraryService) setStoreLocked(store *openlibrary.OLStore) {
	svc.olStore = store
	svc.live.Store(store)
}

// LiveStore returns the store a lookup should use right now, or nil when there
// is none (never opened, deleted, or closed and not yet reopened). It never
// blocks. Metadata lookups call it on every request instead of holding a
// store: the store is replaced at runtime, and a holder of the old pointer
// would keep asking one that refuses all work.
//
// A caller that needs the store more than once in a row loads it ONCE into a
// local: a second call can see nil where the first saw a store (a delete or
// factory reset closing it in between). The olStore field is guarded by Mu;
// code that does not hold Mu reads this instead.
func (svc *OpenLibraryService) LiveStore() *openlibrary.OLStore {
	return svc.live.Load()
}

// CompactionStats samples the OL store's Pebble counters under Mu, the lock
// the delete-OL-data and factory-reset handlers hold while they close the
// store and nil it. Reading svc.olStore without it raced those handlers, and
// sampling a closed *pebble.DB is not safe. ok is false when there is no
// store; the returned Stats is then marked Absent so compactprogress.Moved
// never reads the store disappearing as "debt fell".
func (svc *OpenLibraryService) CompactionStats() (stats compactprogress.Stats, ok bool) {
	svc.Mu.Lock()
	defer svc.Mu.Unlock()
	if svc.olStore == nil {
		return compactprogress.Stats{Absent: true}, false
	}
	return svc.olStore.CompactionStats(), true
}

// PebbleMetricsSample samples the OL store's Pebble metrics under Mu, for the
// same reason CompactionStats does: the delete and factory-reset handlers
// close the store and nil it while holding that lock. ok is false when there
// is no store.
//
// It uses TryLock, not Lock: deleteOLData (os.RemoveAll) and EnsureStore
// (Pebble open and WAL replay) hold Mu across slow I/O, and a blocking Lock here
// would hang the whole /metrics scrape behind them. When Mu is busy the OL
// store is simply absent from that scrape (ok false); every other series is
// unaffected.
func (svc *OpenLibraryService) PebbleMetricsSample() (metrics.PebbleSample, bool) {
	if !svc.Mu.TryLock() {
		return metrics.PebbleSample{}, false
	}
	defer svc.Mu.Unlock()
	if svc.olStore == nil {
		return metrics.PebbleSample{}, false
	}
	return svc.olStore.PebbleMetricsSample()
}

// closeTimeout is the wait for in-flight accesses per close attempt.
func (svc *OpenLibraryService) closeTimeout() time.Duration {
	if svc.CloseTimeout > 0 {
		return svc.CloseTimeout
	}
	return database.DefaultCloseDrainTimeout
}

// CloseStoreLocked closes the live store, if any, and finishes any close an
// earlier call timed out on. The caller holds Mu. It returns nil only when no
// Open Library engine is left open, which is the only state in which the
// data directory may be deleted or the store reopened.
//
// A store whose Close times out (a lookup or import outlived the drain) has
// already begun refusing work and its engine is still open. It moves to
// pendingClose, svc.olStore becomes nil, and the error is returned. Dropping
// it there would strand the engine: it holds the directory's lock file, so
// EnsureStore could never reopen the store and a retried delete would remove
// files under a live engine. Keeping it lets the next call (a retried
// delete, an EnsureStore for the next import, or shutdown) retry the close,
// which succeeds as soon as the straggler has released.
func (svc *OpenLibraryService) CloseStoreLocked() error {
	if svc.olStore != nil {
		svc.pendingClose = svc.olStore
		svc.setStoreLocked(nil)
	}
	if svc.pendingClose == nil {
		return nil
	}
	if err := svc.pendingClose.CloseWithin(svc.closeTimeout()); err != nil {
		return err
	}
	svc.pendingClose = nil
	return nil
}

// Close closes the underlying store. A close that times out is left for the
// process exit (the WAL is synced; see database.NewOwnedGatedDB).
func (svc *OpenLibraryService) Close() {
	svc.Mu.Lock()
	defer svc.Mu.Unlock()
	_ = svc.CloseStoreLocked()
}

// EnsureStore opens or creates the OL store if not already open.
// Returns an error if the store cannot be opened. If an earlier close of the
// store timed out, it first finishes that close, and fails with an error
// saying so while the straggler still holds the engine, rather than a
// confusing "lock held by current process" from opening a second engine on
// the same directory.
func (svc *OpenLibraryService) EnsureStore(targetDir string) error {
	svc.Mu.Lock()
	defer svc.Mu.Unlock()
	if svc.olStore == nil {
		if svc.pendingClose != nil {
			if err := svc.CloseStoreLocked(); err != nil {
				return fmt.Errorf("the previous Open Library store has not finished closing: %w", err)
			}
		}
		storePath := filepath.Join(targetDir, "oldb")
		store, err := openlibrary.NewOLStore(storePath)
		if err != nil {
			return err
		}
		svc.setStoreLocked(store)
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
		// Load the store once, here, and let the goroutine use that local. A
		// read of svc.olStore inside the goroutine raced a delete or factory
		// reset niling it: a nil deref there is a panic on a goroutine, which
		// takes the whole process down. A store closed after this load
		// refuses the import with pebble.ErrClosed, which is reported below.
		store := svc.LiveStore()
		importWg.Go(func() {
			defer func() {
				svc.Mu.Lock()
				delete(svc.Importing, dt)
				svc.Mu.Unlock()
			}()

			filePath := filepath.Join(targetDir, openlibrary.DumpFilename(dt))
			slog.Info("Starting OL dump import from", "value", dt, "path", filePath)

			lastReported := 0
			var err error
			if store == nil {
				err = fmt.Errorf("the Open Library store is not open (it was closed or deleted before the %s import started)", dt)
			} else {
				err = store.ImportDump(dt, filePath, func(count int) {
					if count-lastReported >= 50000 {
						lastReported = count
						if progress != nil {
							msg := fmt.Sprintf("Importing %s: %dk records", dt, count/1000)
							_ = progress.UpdateProgress(idx, len(types), msg)
						}
						slog.Info("OL import progress records", "value", dt, "count", count)
					}
				})
			}

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
