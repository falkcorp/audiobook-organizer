// file: internal/server/server_lifecycle.go
// version: 1.46.0
// guid: 2f98675b-61e1-45a0-94e9-e7fdeb8f273e
// last-edited: 2026-07-18

package server

import (
	"context"
	"crypto/tls"
	"fmt"
	"log/slog"

	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/auth"
	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/httputil"
	"github.com/falkcorp/audiobook-organizer/internal/logger"
	"github.com/falkcorp/audiobook-organizer/internal/maintenance"
	"github.com/falkcorp/audiobook-organizer/internal/metrics"
	"github.com/falkcorp/audiobook-organizer/internal/operations"
	opsregistry "github.com/falkcorp/audiobook-organizer/internal/operations/registry"
	"github.com/falkcorp/audiobook-organizer/internal/realtime"
	"github.com/falkcorp/audiobook-organizer/internal/scheduler"
	"github.com/falkcorp/audiobook-organizer/internal/search"
	servermiddleware "github.com/falkcorp/audiobook-organizer/internal/server/middleware"
	"github.com/falkcorp/audiobook-organizer/internal/serviceregistry"
	"github.com/falkcorp/audiobook-organizer/internal/transcode"
	"github.com/falkcorp/audiobook-organizer/internal/watcher"
	"github.com/gin-gonic/gin"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/quic-go/quic-go/http3"
	"golang.org/x/net/http2"
)

func (s *Server) resumeInterruptedOperations() {
	store := s.Store()
	if store == nil {
		return
	}

	interrupted, err := store.GetInterruptedOperations()
	if err != nil {
		slog.Warn("Failed to query interrupted operations", "err", err)
		return
	}

	// SYS-4 observability gate: count interrupted operations whose Type is a
	// pre-UOS v1 legacy name still handled by the resumeLegacyOp shim. This is
	// telemetry only (does not change resume behavior) and lays the groundwork
	// for a future task that deletes resumeLegacyOp once this count is verified
	// to stay at zero in production across a full release cycle.
	if n := countLegacyV1Ops(interrupted); n > 0 {
		slog.Info("legacy v1 op rows pending resume", "count", n)
	}

	for _, op := range interrupted {
		_ = store.UpdateOperationStatus(op.ID, "interrupted", op.Progress, op.Total, "server restarted")

		checkpoint, _ := operations.LoadCheckpoint(store, op.ID)
		phaseInfo := ""
		if checkpoint != nil {
			phaseInfo = fmt.Sprintf(" from %s at %d/%d", checkpoint.Phase, checkpoint.PhaseIndex, checkpoint.PhaseTotal)
		}
		slog.Info("Resuming interrupted operation", "op", op.ID, "type", op.Type, "phase", phaseInfo)

		// v2 path: look up the op definition in the registry and use its declared ResumePolicy.
		if s.opRegistry != nil {
			if def, ok := s.opRegistry.Def(op.Type); ok {
				s.resumeV2Op(op.ID, op.Type, def.ResumePolicy)
				continue
			}
		}

		// v1 legacy shim: handle pre-UOS op type names that aren't registered under their old name.
		s.resumeLegacyOp(op.ID, op.Type)
	}
}

// resumeV2Op handles resume for operations registered in the v2 registry,
// dispatching based on their declared ResumePolicy.
func (s *Server) resumeV2Op(opID, opType string, policy opsregistry.ResumePolicy) {
	switch policy {
	case opsregistry.ResumeRestart:
		// Reset existing row to "queued"; checkpoint saved under the original op ID remains accessible.
		_ = s.Store().UpdateOperationV2Status(opID, "queued", nil, nil, nil)
	case opsregistry.ResumeRequeue:
		// Re-enqueue from zero (idempotent op).
		if _, err := s.opRegistry.EnqueueOp(context.Background(), opType, nil); err != nil {
			slog.Warn("Failed to re-enqueue v2 op on resume", "opID", opID, "opType", opType, "err", err)
			_ = s.Store().UpdateOperationError(opID, "failed to resume: "+err.Error())
		}
	case opsregistry.ResumeDrop:
		_ = s.Store().UpdateOperationError(opID, fmt.Sprintf("interrupted during %s (dropped on restart)", opType))
		_ = operations.ClearState(s.Store(), opID)
	case opsregistry.ResumeAsk:
		// Surface in UI — mark as interrupted_ask so the frontend can prompt the user.
		slog.Info("Op requires user decision to resume or drop", "opID", opID, "opType", opType)
		now := time.Now()
		reason := "interrupted — waiting for user to choose resume or drop"
		_ = s.Store().UpdateOperationV2Status(opID, "interrupted_ask", nil, &now, &reason)
	default:
		_ = s.Store().UpdateOperationError(opID, fmt.Sprintf("interrupted during %s (unknown resume policy)", opType))
		_ = operations.ClearState(s.Store(), opID)
	}
}

// legacyV1OpTypes is the set of pre-UOS v1 operation type names still handled
// by the resumeLegacyOp switch below. Kept in sync with that switch; used by
// countLegacyV1Ops for the SYS-4 observability gate. The "maintenance:*"
// default branch is intentionally excluded — it is a namespaced prefix, not a
// fixed v1 type name, and is not a candidate for the future shim deletion.
var legacyV1OpTypes = map[string]struct{}{
	"itunes_import":         {},
	"scan":                  {},
	"organize":              {},
	"bulk_write_back":       {},
	"isbn-enrichment":       {},
	"metadata-refresh":      {},
	"itunes_path_reconcile": {},
	"itunes_path_repair":    {},
	"transcode":             {},
	"diagnostics_export":    {},
	"diagnostics_ai":        {},
	"itunes_sync":           {},
	"reconcile_scan":        {},
}

// countLegacyV1Ops returns how many of the given operations carry a pre-UOS v1
// legacy Type name (see legacyV1OpTypes / resumeLegacyOp). Pure function so the
// SYS-4 count gate is testable without constructing a Server.
func countLegacyV1Ops(ops []database.Operation) int {
	n := 0
	for _, op := range ops {
		if _, ok := legacyV1OpTypes[op.Type]; ok {
			n++
		}
	}
	return n
}

// resumeLegacyOp handles resume for pre-UOS v1 op type names that are not
// registered in the v2 registry under their original names.
func (s *Server) resumeLegacyOp(opID, opType string) {
	store := s.Store()
	switch opType {
	case "itunes_import":
		// Migrated to UOS (itunes.import); re-enqueue via registry on resume.
		if s.opRegistry != nil {
			_, _ = s.opRegistry.EnqueueOp(context.Background(), "itunes.import", nil)
		} else {
			_ = store.UpdateOperationError(opID, "operation registry not available")
		}
	case "scan", "organize":
		// Pre-migration v1 op types. library.scan and library.organize have
		// ResumePolicy=Drop; restarting them via the v1 queue would race with
		// v2 workers. Mark failed so the user can re-trigger manually.
		_ = store.UpdateOperationError(opID, fmt.Sprintf("interrupted during %s, please retry", opType))
		_ = operations.ClearState(store, opID)
	case "bulk_write_back":
		// Migrated to UOS (library.bulk-write-back); re-enqueue via registry on resume.
		params, _ := operations.LoadParams[operations.BulkWriteBackParams](store, opID)
		if params == nil {
			slog.Warn("No params for interrupted bulk_write_back , marking failed", "opID", opID)
			_ = store.UpdateOperationError(opID, "no saved params, cannot resume")
			return
		}
		if s.opRegistry != nil {
			enqParams := bulkWriteBackOpParams{BookIDs: params.BookIDs, Rename: params.Rename}
			if _, enqErr := s.opRegistry.EnqueueOp(context.Background(), "library.bulk-write-back", enqParams); enqErr != nil {
				slog.Warn("Failed to re-enqueue bulk_write_back via v2", "opID", opID, "enqErr", enqErr)
				_ = store.UpdateOperationError(opID, "failed to resume: "+enqErr.Error())
			}
		} else {
			_ = store.UpdateOperationError(opID, "operation registry not available")
		}
	case "isbn-enrichment":
		// Migrated to UOS (scheduler.isbn-enrichment); re-enqueue via registry on resume.
		if s.opRegistry != nil {
			enqParams := schedulerExtraOpParams{LegacyOpID: opID}
			if _, enqErr := s.opRegistry.EnqueueOp(context.Background(), "scheduler.isbn-enrichment", enqParams); enqErr != nil {
				slog.Warn("Failed to re-enqueue isbn-enrichment via v2", "opID", opID, "enqErr", enqErr)
				_ = store.UpdateOperationError(opID, "failed to resume: "+enqErr.Error())
			}
		} else {
			_ = store.UpdateOperationError(opID, "operation registry not available")
		}
	case "metadata-refresh":
		// Migrated to UOS (scheduler.metadata-refresh); re-enqueue via registry on resume.
		if s.opRegistry != nil {
			enqParams := schedulerExtraOpParams{LegacyOpID: opID}
			if _, enqErr := s.opRegistry.EnqueueOp(context.Background(), "scheduler.metadata-refresh", enqParams); enqErr != nil {
				slog.Warn("Failed to re-enqueue metadata-refresh via v2", "opID", opID, "enqErr", enqErr)
				_ = store.UpdateOperationError(opID, "failed to resume: "+enqErr.Error())
			}
		} else {
			_ = store.UpdateOperationError(opID, "operation registry not available")
		}
	case "itunes_path_reconcile":
		// Migrated to UOS (itunes.path-reconcile); re-enqueue via registry on resume.
		if s.opRegistry != nil {
			_, _ = s.opRegistry.EnqueueOp(context.Background(), "itunes.path-reconcile", nil)
		} else {
			_ = store.UpdateOperationError(opID, "operation registry not available")
		}
	case "itunes_path_repair":
		// Migrated to UOS (itunes.path-repair); re-enqueue via registry on resume.
		if s.opRegistry != nil {
			_, _ = s.opRegistry.EnqueueOp(context.Background(), "itunes.path-repair", nil)
		} else {
			_ = store.UpdateOperationError(opID, "operation registry not available")
		}
	case "transcode", "diagnostics_export", "diagnostics_ai", "itunes_sync",
		// reconcile_scan: a 271K-file hash sweep that ignores ctx, runs
		// nightly via the scheduler, and pins both queue workers for ~45min
		// when auto-resumed. Repeated quick deploys produced a queue jam
		// where new ops (AcoustID, embed, etc.) sat queued behind two
		// stuck reconcile_scans that the cancel API couldn't actually
		// kill. Letting the scheduler re-run it tomorrow is fine.
		"reconcile_scan":
		// These are not resumable — mark as failed silently.
		_ = store.UpdateOperationError(opID, fmt.Sprintf("interrupted during %s, please retry", opType))
		_ = operations.ClearState(store, opID)
	default:
		// Try to resume as a maintenance job via v2 registry (maintenance.job).
		jobID := strings.TrimPrefix(opType, "maintenance:")
		if j, jobErr := maintenance.Get(jobID); jobErr == nil && j.CanResume() {
			if s.opRegistry != nil {
				enqParams := maintenanceJobOpParams{LegacyOpID: opID, JobID: jobID}
				if _, enqErr := s.opRegistry.EnqueueOp(context.Background(), "maintenance.job", enqParams); enqErr != nil {
					slog.Warn("Failed to re-enqueue maintenance job () via v2", "opID", opID, "jobID", jobID, "enqErr", enqErr)
					_ = store.UpdateOperationError(opID, "failed to resume: "+enqErr.Error())
				}
			} else {
				_ = store.UpdateOperationError(opID, "operation registry not available")
			}
		} else {
			_ = store.UpdateOperationError(opID, "interrupted, cannot resume")
			_ = operations.ClearState(store, opID)
		}
	}
}

func (s *Server) Start(cfg ServerConfig) error {
	s.externalURL = strings.TrimRight(cfg.ExternalURL, "/")

	// SERVER-LIFECYCLE-FLIP: drive Starter services via the container.
	// Container.Start runs services in resolved dep order; failures
	// abort startup and roll back already-started services.
	if s.container != nil {
		if err := s.container.Start(s.bgCtx); err != nil {
			return fmt.Errorf("container start: %w", err)
		}
	}

	// Start embed queue debounce timer (activated after container start so
	// Ollama daemon wiring is complete before any drain fires).
	if s.embedQueue != nil {
		s.embedQueue.Start(s.bgCtx)
	}

	// Pull the now-open Bleve index out of the searchindex service
	// (opened above by Container.Start) and install the indexedStore
	// decorator BEFORE any background goroutines or HTTP handlers
	// start using s.store. Doing the wrap first eliminates the race
	// between bg goroutines (stripMovementAtoms / remuxMalformedM4BFiles
	// / backfills) reading s.store and the indexedStore install — pre-PR
	// #903 this happened later and was timing-dependent.
	//
	// Bleve open failures are already logged inside IndexService.Start;
	// when Index() returns nil the server runs without search.
	if s.container != nil && s.searchIndex == nil {
		if idx, ok := serviceregistry.TryGet[*search.IndexService](s.container, "searchindex"); ok && idx != nil {
			s.searchIndex = idx.Index()
		}
	}
	if s.searchIndex != nil {
		s.indexQueue = make(chan indexRequest, 1024)
		inner := s.Store()
		wrapped := &indexedStore{Store: inner, server: s}
		s.store = wrapped
		database.SetGlobalStore(wrapped)
		s.bgWG.Add("index-worker")
		go func() {
			defer s.bgWG.Done("index-worker")
			s.runIndexWorker()
		}()
		// Route the /audiobooks?search= path through Bleve.
		if s.audiobookService != nil {
			s.audiobookService.SetSearchIndex(s.searchIndex)
		}
	}

	// HNSW snapshot is now loaded earlier, in NewServer (between Build and PostInit),
	// so that dedup.Engine.PostInit can detect the populated store and skip the
	// redundant HydrateChromem walk. Nothing to do here. (HNSW-CRASH-2026-06-18)

	s.startCacheWarmers()

	if err := s.configureAndStartHTTP(cfg); err != nil {
		return err
	}

	s.seedRolesAndTokens()

	// Resume any operations that were interrupted by a previous shutdown/crash
	s.resumeInterruptedOperations()

	// Recover interrupted file I/O operations (cover embed, tag write, rename)
	RecoverInterruptedFileOps(s.fileIOPool)

	// Resume interrupted metadata candidate fetch operations
	s.resumeInterruptedMetadataFetch()

	s.startBackfills()

	// Start periodic cleanup of stale transcode temp files
	if s.Store() != nil {
		if paths, err := s.Store().GetAllImportPaths(); err == nil {
			for _, p := range paths {
				stopCleanup := transcode.StartCleanupTicker(p.Path, 1*time.Hour, 2*time.Hour)
				defer stopCleanup()
			}
		}
	}

	// Heartbeat: push periodic system.status events via SSE (every 5s) while running
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	shutdown := make(chan struct{})
	var backgroundWG sync.WaitGroup

	// Start unified task scheduler (replaces individual iTunes sync and purge tickers)
	s.scheduler = scheduler.NewTaskScheduler(scheduler.SchedulerDeps{
		Store:      s.Store,
		OpRegistry: s.opRegistry,
		HasDedupEngine: func() bool {
			return s.dedupEngine != nil
		},
		HasMetadataFetchSvc: func() bool {
			return s.metadataFetchService != nil && s.metadataFetchService.ISBNEnrichment() != nil
		},
		HasActivitySvc: func() bool {
			return s.activityService != nil
		},
		HasBatchPoller: func() bool {
			return s.batchPoller != nil
		},
		PollBatches: func(ctx context.Context) (int, error) {
			if s.batchPoller == nil {
				return 0, nil
			}
			return s.batchPoller.Poll(ctx)
		},
	})
	s.scheduler.Start(shutdown, &backgroundWG)

	ticker := time.NewTicker(5 * time.Second)
	backgroundWG.Add(1)
	go func() {
		defer backgroundWG.Done()
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				if s.hub != nil {
					// Gather lightweight metrics
					var alloc runtime.MemStats
					runtime.ReadMemStats(&alloc)
					bookCount := 0
					folderCount := 0
					if s.Store() != nil {
						if bc, err := s.Store().CountPrimaryBooks(); err == nil {
							bookCount = bc
						}
						if folders, err := s.Store().GetAllImportPaths(); err == nil {
							folderCount = len(folders)
						}
					}

					// Update Prometheus metrics
					metrics.SetBooks(bookCount)
					metrics.SetFolders(folderCount)
					metrics.SetMemoryAlloc(alloc.Alloc)
					metrics.SetGoroutines(runtime.NumGoroutine())

					s.hub.SendSystemStatus(map[string]any{
						"books":        bookCount,
						"folders":      folderCount,
						"memory_alloc": alloc.Alloc,
						"goroutines":   runtime.NumGoroutine(),
						"timestamp":    time.Now().Unix(),
					})
				}
			case <-shutdown:
				return
			}
		}
	}()

	// Persist cache observability snapshots to SQLite every 5 minutes so
	// hit/miss trends survive restarts. PebbleDB-backed deployments skip
	// persistence inside runCacheStatsSnapshotter.
	backgroundWG.Add(1)
	go func() {
		defer backgroundWG.Done()
		s.runCacheStatsSnapshotter(shutdown)
	}()

	// Start auto-scan file watchers if enabled. ONE watcher per enabled
	// import path — previously only the first enabled path was watched,
	// so users with multiple import locations had silent blind spots on
	// every path after the first.
	var fileWatchers []*watcher.Watcher
	if config.AppConfig.AutoScanEnabled && s.Store() != nil {
		importPaths, err := s.Store().GetAllImportPaths()
		if err == nil && len(importPaths) > 0 {
			var watchPaths []string
			for _, ip := range importPaths {
				if ip.Enabled {
					watchPaths = append(watchPaths, ip.Path)
				}
			}
			if len(watchPaths) > 0 {
				debounce := 5 * time.Second
				if config.AppConfig.AutoScanDebounceSeconds > 0 {
					debounce = time.Duration(config.AppConfig.AutoScanDebounceSeconds) * time.Second
				}
				watchLog := logger.NewWithActivityLog("auto-scan", s.Store())
				// The same callback is reused across watchers because
				// each watcher invokes it with its own root path, so
				// the scan target is correct per event.
				cb := func(path string) {
					watchLog.Info("Auto-scan triggered for: %s", path)
					if s.hub != nil {
						s.hub.Broadcast(&realtime.Event{
							Type: "scan.auto_triggered",
							Data: map[string]any{"path": path},
						})
					}
					if s.scanService != nil && s.opRegistry != nil {
						go func() {
							scanPath := path
							if _, enqErr := s.opRegistry.EnqueueOp(context.Background(), "library.scan", libraryScanParams{FolderPath: &scanPath}); enqErr != nil {
								watchLog.Error("Auto-scan: failed to enqueue: %v", enqErr)
							}
						}()
					}
				}
				for _, wp := range watchPaths {
					fw := watcher.New(cb, debounce)
					if startErr := fw.Start(wp); startErr != nil {
						watchLog.Warn("Failed to start file watcher for %s: %v", wp, startErr)
						continue
					}
					fileWatchers = append(fileWatchers, fw)
					watchLog.Info("Auto-scan file watcher started for %s", wp)
				}
			}
		}
	}

	// Periodic cleanup of expired/revoked auth sessions.
	if s.Store() != nil {
		sessionLog := logger.NewWithActivityLog("session-cleanup", s.Store())
		sessionCleanupTicker := time.NewTicker(10 * time.Minute)
		backgroundWG.Add(1)
		go func() {
			defer backgroundWG.Done()
			defer sessionCleanupTicker.Stop()
			for {
				select {
				case <-sessionCleanupTicker.C:
					if deleted, err := s.Store().DeleteExpiredSessions(time.Now()); err != nil {
						sessionLog.Warn("failed to clean up expired sessions: %v", err)
					} else if deleted > 0 {
						sessionLog.Info("cleaned up %d expired/revoked sessions", deleted)
					}
				case <-shutdown:
					return
				}
			}
		}()
	}

	// Periodically mark stale operations as failed.
	if s.Store() != nil && config.AppConfig.OperationTimeoutMinutes > 0 {
		staleTimeout := time.Duration(config.AppConfig.OperationTimeoutMinutes) * time.Minute
		staleTicker := time.NewTicker(1 * time.Minute)
		backgroundWG.Add(1)
		go func() {
			defer backgroundWG.Done()
			defer staleTicker.Stop()
			for {
				select {
				case <-staleTicker.C:
					s.failStaleOperations(staleTimeout)
				case <-shutdown:
					return
				}
			}
		}()
	}

	// Wait for interrupt signal to gracefully shutdown the server
	<-quit
	close(shutdown)
	signal.Stop(quit)

	slog.Info("Shutting down server...")

	// Broadcast shutdown event to all connected clients FIRST
	if s.hub != nil {
		s.hub.Broadcast(&realtime.Event{
			Type: "system.shutdown",
			Data: map[string]any{
				"message": "Server is shutting down",
			},
		})
		// Give clients a moment to receive the event
		time.Sleep(500 * time.Millisecond)
	}

	// Stop accepting HTTP requests BEFORE closing any stores.
	// This prevents panics from requests hitting closed PebbleDB instances.
	slog.Info("Stopping HTTP servers...")
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if s.http3Server != nil {
		if err := s.http3Server.Close(); err != nil {
			slog.Warn("HTTP/3 server close error", "err", err)
		}
	}
	if err := s.httpServer.Shutdown(ctx); err != nil {
		slog.Warn("HTTP server forced shutdown", "err", err)
	}

	// Drain the UOS-02 operations registry before canceling bgCtx so that
	// in-flight ops get a clean shutdown signal via their per-run ctx.
	if s.opRegistry != nil {
		regCtx, regCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer regCancel()
		if err := s.opRegistry.Shutdown(regCtx); err != nil {
			slog.Warn("ops registry shutdown", "err", err)
		}
	}

	// Cancel fire-and-forget background work (embedding backfill, async
	// dedup scans) and wait for it to return. This MUST happen before
	// embeddingStore.Close() and before Start() returns (which triggers
	// the deferred closeStore() in cmd/root.go). Without it, the backfill
	// goroutine keeps iterating Pebble while CloseStore runs, and Pebble's
	// FileCache.Unref panics with "element has outstanding references"
	// during shutdown — which has been killing every restart mid-cycle.
	if s.bgCancel != nil {
		slog.Info("Canceling background goroutines...")
		s.bgCancel()
	}
	// Close the index queue so the index worker goroutine can
	// finish its range loop and decrement bgWG. Leaving it open
	// would deadlock the wait below because the worker doesn't
	// listen on bgCtx — its termination signal is the queue close.
	s.closeIndexQueue()
	bgDone := make(chan struct{})
	go func() {
		s.bgWG.Wait()
		close(bgDone)
	}()
	select {
	case <-bgDone:
		slog.Info("Background goroutines stopped")
	case <-time.After(30 * time.Second):
		slog.Warn("Background goroutines did not stop within 30s — proceeding with shutdown anyway",
			"still_running", s.bgWG.Running())
	}

	// Stop the file I/O pool — waits for in-flight jobs to finish
	if p := s.fileIOPool; p != nil {
		slog.Info("Waiting for file I/O operations to complete...")
		p.Stop()
	}

	// Flush the ITL write-back batcher
	if s.writeBackBatcher != nil {
		slog.Info("Flushing iTunes write-back batcher...")
		_ = s.writeBackBatcher.Stop(context.Background())
	}

	// Shut down the iTunes service (no-op in PR 1 since NewDisabled is
	// always used; PR 2 onward may have live sub-components to flush).
	if s.itunesSvc != nil {
		if err := s.itunesSvc.Shutdown(30 * time.Second); err != nil {
			slog.Warn("itunes service shutdown", "err", err)
		}
	}

	// Shut down all plugins before closing stores
	if s.pluginRegistry != nil {
		slog.Info("Shutting down plugins...")
		s.pluginRegistry.ShutdownAll(ctx)
	}

	// Search index is closed by Container.Stop below — IndexService.Stop
	// runs in reverse-resolved order alongside the other registered
	// Stoppers and clears its handle so a double-call is a no-op.

	// SERVER-LIFECYCLE-FLIP: drive remaining Stoppers via the container.
	// Runs in reverse resolved order; covers activityWriter (replaces
	// the inline Stop here pre-flip), updateScheduler (previously never
	// stopped — a leak), and any future Stopper additions. Inline Stops
	// above remain the source of truth for the carefully-sequenced
	// teardown (opRegistry drain before bgCancel, writeBackBatcher flush
	// before itunesSvc.Shutdown, etc.); Container.Stop is idempotent on
	// already-stopped services for those.
	if s.container != nil {
		if err := s.container.Stop(context.Background()); err != nil {
			slog.Warn("container stop", "err", err)
		}
	}

	// Close activity log store
	if s.activityService != nil {
		if err := s.activityService.Store().Close(); err != nil {
			slog.Warn("Failed to close activity log store", "err", err)
		} else {
			slog.Info("Activity log store closed")
		}
	}

	// Stop every file watcher (one per import path).
	for _, fw := range fileWatchers {
		fw.Stop()
	}
	if len(fileWatchers) > 0 {
		slog.Info("File watchers stopped ()", "fileWatchers_count", len(fileWatchers))
	}

	// Persist HNSW snapshot for fast next-boot startup (before embedding store
	// closes so the graph data is still intact and consistent).
	if s.hnswPersistDir != "" {
		if raw, ok := serviceregistry.TryGet[database.VectorANNStore](s.container, "chromemstore"); ok && raw != nil {
			if hnswStore, ok := raw.(*database.HNSWEmbeddingStore); ok {
				if err := hnswStore.Export(s.hnswPersistDir); err != nil {
					slog.Error("hnsw: export failed", "err", err)
				} else {
					slog.Info("hnsw: snapshot saved", "dir", s.hnswPersistDir)
				}
			}
		}
	}

	// Stop embed queue and Ollama daemon.
	if s.embedQueue != nil {
		s.embedQueue.Stop()
	}
	if s.ollamaDaemon != nil {
		if err := s.ollamaDaemon.StopWhenIdle(context.Background()); err != nil {
			slog.Warn("ollama: stop failed", "err", err)
		}
	}

	// Close embedding store
	if s.embeddingStore != nil {
		if err := s.embeddingStore.Close(); err != nil {
			slog.Warn("Failed to close embedding store", "err", err)
		} else {
			slog.Info("Embedding store closed")
		}
	}

	// Close AI scan store
	if s.aiScanStore != nil {
		if err := s.aiScanStore.Close(); err != nil {
			slog.Warn("Failed to close AI scan store", "err", err)
		} else {
			slog.Info("AI scan store closed")
		}
		s.aiScanStore = nil
	}

	backgroundWG.Wait()
	slog.Info("Server exited")
	return nil
}

// startCacheWarmers launches the fire-and-forget cache pre-warmers plus the
// API-key expiry sweep. Extracted verbatim from Start as part of the SYS-2
// decomposition; all four cache warmers (warmFacetsCache / warmLibrarySizes /
// warmAuthorsCache / warmSeriesCache) are now bgWG-enrolled per #1794 (the
// follow-up to #1781, which enrolled library-list-warmer + apikey-expiry-sweep
// first) so a Shutdown drains every warmer before the store closes instead of
// letting it outlive Close() and hit a closed Pebble store
// (PEBBLE-CLOSED family). Each launch also skips on an already-canceled
// bgCtx — a SKIP-on-shutdown guard, not a startup gate: a live bgCtx must
// still run every warmer.
func (s *Server) startCacheWarmers() {
	// Pre-warm facets cache (genres/languages) - lightweight, <1 second
	s.bgWG.Add("facets-warmer")
	go func() {
		defer s.bgWG.Done("facets-warmer")
		if s.bgCtx.Err() != nil {
			return // server already shutting down — skip, never warm a closing store
		}
		s.warmFacetsCache()
	}()
	// Pre-warm library size cache via filesystem walk so any later refresh
	// path (nightly maintenance, manual rescan) starts with current data.
	// The hot path of /system/status reads DB stats (PR #1137); this just
	// keeps the FS-based numbers fresh in the 24h-TTL package cache.
	s.bgWG.Add("library-sizes-warmer")
	go func() {
		defer s.bgWG.Done("library-sizes-warmer")
		if s.bgCtx.Err() != nil {
			return // server already shutting down — skip, never warm a closing store
		}
		s.warmLibrarySizes()
	}()
	// Pre-warm the audiobook list cache after memdb is published. Fires
	// the most common library-page queries (title asc/desc, -review:matched,
	// library_state filter) so the user's first load doesn't pay the full
	// cold-miss cost (~3 min on 50K-book library). Enrolled in bgWG and
	// gated on bgCtx (it also spawns the ~30-min trickle warmer): the old
	// fire-and-forget launch outlived test servers and kept querying the
	// store after Close() — panic "pebble: closed" from a trickle-warmer
	// tick minutes into an internal/server package run
	// (PEBBLE-CLOSED-SWEEPTICK-RESIDUAL family, warmer leg).
	s.bgWG.Add("library-list-warmer")
	go func() {
		defer s.bgWG.Done("library-list-warmer")
		s.warmAudiobookListCache()
	}()
	s.bgWG.Add("authors-warmer")
	go func() {
		defer s.bgWG.Done("authors-warmer")
		if s.bgCtx.Err() != nil {
			return // server already shutting down — skip, never warm a closing store
		}
		s.warmAuthorsCache()
	}()
	s.bgWG.Add("series-warmer")
	go func() {
		defer s.bgWG.Done("series-warmer")
		if s.bgCtx.Err() != nil {
			return // server already shutting down — skip, never warm a closing store
		}
		s.warmSeriesCache()
	}()

	// Low-frequency background sweep that WARN-logs API keys approaching
	// expiry or lacking one entirely (legacy keys) — observability only,
	// never enforcement (SEC-1/PROC-6).
	s.bgWG.Add("apikey-expiry-sweep")
	go func() {
		defer s.bgWG.Done("apikey-expiry-sweep")
		s.warnExpiringAPIKeys()
	}()
}

// configureAndStartHTTP builds s.httpServer (and, when TLS is configured,
// s.http3Server plus the HTTP->HTTPS redirect listener) and launches the
// listener goroutines. Extracted verbatim from Start; cfg is taken by value so
// the local TLS-fallback mutations stay scoped to this phase exactly as before.
func (s *Server) configureAndStartHTTP(cfg ServerConfig) error {
	s.httpServer = &http.Server{
		Addr:              fmt.Sprintf("%s:%s", cfg.Host, cfg.Port),
		Handler:           s.router,
		ReadHeaderTimeout: cfg.ReadTimeout, // Only limit header read, not body (allows large uploads)
		WriteTimeout:      cfg.WriteTimeout,
		IdleTimeout:       cfg.IdleTimeout,
		MaxHeaderBytes:    1 << 20, // 1MB
	}

	if cfg.TLSCertFile != "" && cfg.TLSKeyFile != "" {
		if _, err := os.Stat(cfg.TLSCertFile); err != nil {
			slog.Warn("TLS certificate not available () . Falling back to HTTP-only mode.", "cfg", cfg.TLSCertFile, "err", err)
			cfg.TLSCertFile = ""
			cfg.TLSKeyFile = ""
			cfg.HTTP3Port = ""
		} else if _, err := os.Stat(cfg.TLSKeyFile); err != nil {
			slog.Warn("TLS key not available () . Falling back to HTTP-only mode.", "cfg", cfg.TLSKeyFile, "err", err)
			cfg.TLSCertFile = ""
			cfg.TLSKeyFile = ""
			cfg.HTTP3Port = ""
		}
	}

	// Enable HTTP/2 if TLS is configured
	if cfg.TLSCertFile != "" && cfg.TLSKeyFile != "" {
		// Configure TLS with HTTP/2 (and optionally HTTP/3)
		nextProtos := []string{"h2", "http/1.1"}
		if cfg.HTTP3Port != "" {
			// Add h3 to advertised protocols
			nextProtos = append([]string{"h3"}, nextProtos...)
		}
		tlsConfig := &tls.Config{
			MinVersion: tls.VersionTLS12,
			NextProtos: nextProtos,
		}
		s.httpServer.TLSConfig = tlsConfig

		// Explicitly configure HTTP/2
		if err := http2.ConfigureServer(s.httpServer, &http2.Server{}); err != nil {
			return fmt.Errorf("failed to configure HTTP/2: %w", err)
		}

		// Add Alt-Svc header to advertise HTTP/3 if enabled
		if cfg.HTTP3Port != "" {
			s.router.Use(func(c *gin.Context) {
				c.Header("Alt-Svc", fmt.Sprintf(`h3=":%s"; ma=2592000`, cfg.HTTP3Port))
				c.Next()
			})
		}

		// Start HTTPS server with HTTP/2
		go func() {
			protocols := "HTTPS/HTTP2"
			if cfg.HTTP3Port != "" {
				protocols = "HTTPS/HTTP2 (HTTP/3 on UDP port " + cfg.HTTP3Port + ")"
			}
			slog.Info("Starting server on", "protocols", protocols, "addr", s.httpServer.Addr)
			if err := s.httpServer.ListenAndServeTLS(cfg.TLSCertFile, cfg.TLSKeyFile); err != nil && err != http.ErrServerClosed {
				slog.Error("Failed to start HTTPS server", "err", err)
			}
		}()

		// Start HTTP/3 server if configured
		if cfg.HTTP3Port != "" {
			s.http3Server = &http3.Server{
				Addr:      fmt.Sprintf("%s:%s", cfg.Host, cfg.HTTP3Port),
				Handler:   s.router,
				TLSConfig: tlsConfig,
			}
			go func() {
				slog.Info("Starting HTTP/3 (QUIC) server on UDP", "host", cfg.Host, "port", cfg.HTTP3Port)
				if err := s.http3Server.ListenAndServeTLS(cfg.TLSCertFile, cfg.TLSKeyFile); err != nil {
					slog.Error("Failed to start HTTP/3 server", "err", err)
				}
			}()
		}

		// Start HTTP to HTTPS redirect server on port 80
		go func() {
			redirectAddr := fmt.Sprintf("%s:80", cfg.Host)
			httpsPort := cfg.Port
			if httpsPort == "80" {
				httpsPort = "443" // Don't redirect 80->80
			}

			redirectHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				// Build HTTPS URL
				target := "https://" + r.Host
				// Add port if not default HTTPS port
				if httpsPort != "443" {
					target = fmt.Sprintf("https://%s:%s", cfg.Host, httpsPort)
				}
				target += r.URL.RequestURI()

				slog.Debug("HTTP->HTTPS redirect", "url", r.URL.String(), "target", target)
				http.Redirect(w, r, target, http.StatusMovedPermanently)
			})

			slog.Info("Starting HTTP->HTTPS redirect server on (redirects to )", "redirectAddr", redirectAddr, "httpsPort", httpsPort)
			httpRedirectServer := &http.Server{
				Addr:    redirectAddr,
				Handler: redirectHandler,
			}
			if err := httpRedirectServer.ListenAndServe(); err != nil {
				// Don't fatal - port 80 might require sudo
				slog.Warn("Warning HTTP redirect server failed (port 80 may require sudo)", "err", err)
			}
		}()
	} else {
		// Start HTTP/1.1 server without TLS
		go func() {
			slog.Info("Starting HTTP/1.1 server on (use --tls-cert and --tls-key for HTTP/2, add --http3-port for HTTP/3)", "addr", s.httpServer.Addr)
			if err := s.httpServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
				slog.Error("Failed to start server", "err", err)
			}
		}()
	}
	return nil
}

// seedRolesAndTokens seeds/refreshes multi-user roles and the system user, then
// initializes the one-time bootstrap token and (optionally) the startup
// read-only key. Extracted verbatim from Start; all failures remain non-fatal.
func (s *Server) seedRolesAndTokens() {
	// Seed / refresh the multi-user roles (spec 3.7). Idempotent: if
	// the permission set in auth.SeedRoles has grown since last boot,
	// existing roles pick up the new entries automatically.
	if created, updated, err := auth.SeedRoles(s.Store()); err != nil {
		slog.Warn("seed roles", "err", err)
	} else if created > 0 || updated > 0 {
		slog.Info("seed roles created, updated", "created", created, "updated", updated)
	}
	if err := auth.SeedSystemUser(s.Store()); err != nil {
		slog.Warn("seed system user", "err", err)
	}

	// Initialize the one-time bootstrap token and startup read-only key.
	// dataDir is derived and sanitised once here so both callers receive a
	// clean path — mirrors the ConsumeBootstrapToken(store, dataDir, …) pattern.
	if dbPath := config.AppConfig.DatabasePath; dbPath != "" {
		dataDir := filepath.Clean(filepath.Dir(dbPath))
		if err := InitBootstrapToken(s.Store(), dataDir); err != nil {
			slog.Info("Failed to init bootstrap token", "err", err)
		}
		if config.AppConfig.WriteStartupReadOnlyKey {
			if err := InitStartupReadOnlyKey(s.Store(), dataDir); err != nil {
				slog.Info("Failed to init startup read-only key", "err", err)
			}
		}
	}
}

// startBackfills launches the one-time, idempotent bgWG-tracked backfill and
// maintenance goroutines. Extracted verbatim from Start; every goroutine stays
// enrolled in s.bgWG so shutdown drains them before the store closes (SYS-1).
func (s *Server) startBackfills() {
	// Backfill external ID mappings from existing iTunes PIDs (one-time,
	// idempotent). Tracked via bgWG for the same reason as the embedding
	// backfill: we can't let it hold Pebble iterators while CloseStore runs.
	s.bgWG.Add("external-id-backfill")
	go func() {
		defer s.bgWG.Done("external-id-backfill")
		// This startup-goroutine path has no op reporter (only the UOS plugin
		// op maintenance.external-id-backfill does), so progress is surfaced
		// via a plain slog.Info instead of reporter.UpdateProgress — this is
		// the path that actually runs at every boot, so it's the one that
		// needs the "more than one log line" fix (H7), not just the UOS
		// plugin op (unclear whether that path is ever enqueued). The error
		// is no longer silently swallowed either.
		if err := s.backfillExternalIDs(startupProgressLogger("external-id-backfill")); err != nil {
			slog.Warn("startup external-id-backfill failed", "err", err)
		}
	}()

	// AcoustID fingerprint backfill. CONC-9 removed the serial server-side
	// duplicate (backfillAcoustIDs) in favor of the parallel plugin op
	// acoustid.backfill (internal/plugins/acoustid/backfill.go, registry.RunItems
	// with bounded Concurrency). That deletion left startup no longer
	// auto-fingerprinting — coverage only advanced when an operator ran the
	// on-demand library.optimize sweep. Restore the original boot behavior by
	// enqueuing the parallel op once at startup. It is idempotent (skips files
	// that already have a fingerprint) and runs in the registry's own worker
	// pool, so this is fire-and-forget — no bgWG enrollment (the op's lifecycle
	// is owned by opRegistry, not the backfill WaitGroup).
	go func() {
		if err := s.bgCtx.Err(); err != nil {
			return
		}
		if _, err := s.opRegistry.EnqueueOp(s.bgCtx, "acoustid.backfill", nil); err != nil {
			slog.Warn("startup acoustid.backfill enqueue failed", "err", err)
		} else {
			slog.Info("startup: enqueued acoustid.backfill fingerprint op")
		}
	}()

	// PERF-VERSIONS: write the book:versiongroup:<gid>:<id> secondary
	// index for every existing book once so /audiobooks/:id/versions
	// stops full-scanning. Idempotent and gated by a sentinel key.
	s.bgWG.Add("versiongroup-backfill")
	go func() {
		defer s.bgWG.Done("versiongroup-backfill")
		if err := s.bgCtx.Err(); err != nil {
			return
		}
		type vgBackfiller interface{ BackfillVersionGroupIndex() error }
		if b, ok := s.Store().(vgBackfiller); ok {
			if err := b.BackfillVersionGroupIndex(); err != nil {
				slog.Warn("versiongroup-backfill", "err", err)
			}
		}
	}()

	// Strip shwm/©mvi/©mvn atoms from audiobook files (one-time). These
	// classical-music atoms crash Apple Devices for Windows at sync.
	// Checks bgCtx per file (SYS-1) so shutdown stops the walk early instead
	// of blowing the 30s grace period on a first-run walk over a large
	// library; a canceled run resumes on the next startup.
	s.bgWG.Add("strip-movement-atoms")
	go func() {
		defer s.bgWG.Done("strip-movement-atoms")
		s.stripMovementAtoms(s.bgCtx)
	}()

	// Re-mux M4B/M4A files with malformed atom structures so taglib,
	// AtomicParsley, and Apple Devices can read them (one-time).
	// Checks bgCtx per file (SYS-1); same early-stop behavior as
	// stripMovementAtoms above.
	s.bgWG.Add("remux-malformed-m4b")
	go func() {
		defer s.bgWG.Done("remux-malformed-m4b")
		// See the external-id-backfill goroutine above for why this logs
		// progress via slog rather than a reporter (C2).
		if err := s.remuxMalformedM4BFiles(s.bgCtx, startupProgressLogger("remux-malformed-m4b")); err != nil {
			slog.Warn("startup remux-malformed-m4b failed", "err", err)
		}
	}()

	// Build the search index on first startup (or if it got wiped).
	// Tracked via bgWG so shutdown can wait for in-flight indexing
	// instead of letting it run under a closing DB.
	if s.searchIndex != nil {
		s.bgWG.Add("build-search-index")
		go func() {
			defer s.bgWG.Done("build-search-index")
			s.buildSearchIndexIfEmpty()
		}()
	}

	// One-time startup jobs: transcode malformed M4B files, then quarantine any
	// that remained permanently unreadable. Run sequentially in a bgWG goroutine
	// so shutdown waits for them and they don't race against the HTTP server.
	// transcodeMalformedM4BFiles checks bgCtx per file (SYS-1); same
	// early-stop behavior as stripMovementAtoms above.
	s.bgWG.Add("transcode+quarantine")
	go func() {
		defer s.bgWG.Done("transcode+quarantine")
		// See the external-id-backfill goroutine above for why this logs
		// progress via slog rather than a reporter (C2).
		if err := s.transcodeMalformedM4BFiles(s.bgCtx, startupProgressLogger("transcode-malformed-m4b")); err != nil {
			slog.Warn("startup transcode-malformed-m4b failed", "err", err)
		}
		s.quarantineKnownBadFiles()
	}()
}

// startupProgressLogger returns a C2/H7 progress callback that logs via slog,
// for use by the raw startBackfills goroutines above, which run outside the
// UOS op registry and so have no reporter.UpdateProgress to call. Without
// this, these ops — which run unconditionally at every boot — would still
// show only a single "starting"/"complete" log line across a multi-hour run,
// even after threading progress through the dep layer, because nil progress
// is a no-op.
func startupProgressLogger(op string) func(processed, total int, msg string) {
	return func(processed, total int, msg string) {
		slog.Info("startup progress", "op", op, "processed", processed, "total", total, "msg", msg)
	}
}

func (s *Server) perm(p auth.Permission) gin.HandlerFunc {
	if !config.AppConfig.EnableAuth {
		return func(c *gin.Context) { c.Next() }
	}
	return servermiddleware.RequirePermission(s.Store(), p)
}

func (s *Server) itunesSvcGuard(fn gin.HandlerFunc) gin.HandlerFunc {
	return func(c *gin.Context) {
		if s.itunesSvc == nil || !s.itunesSvc.Enabled() {
			httputil.RespondWithServiceUnavailable(c, "iTunes service is disabled")
			return
		}
		fn(c)
	}
}

func (s *Server) setupRoutes() {
	// Health check endpoint
	// Prometheus metrics endpoint (standard path).
	//
	// Intentionally left UNAUTHENTICATED (pen-test finding MED-1, accepted risk).
	// Prometheus scrapers don't send auth/cookies, and the data here is
	// operational metrics (counts, cache/op rates) — not user data or secrets.
	// This matches common practice (e.g. internal metrics endpoints). If this
	// server is ever exposed to an untrusted network, restrict /metrics at the
	// network layer (firewall / separate internal port) rather than app auth.
	s.router.GET("/metrics", gin.WrapH(promhttp.Handler()))

	// Health check endpoint (both paths for compatibility). Registered here on
	// s.router BEFORE the /api/* redirect middleware (below) so they keep
	// bypassing it; they delegate to the migrated system handler, which is wired
	// later in wireHandlers (always before any request is served).
	s.router.GET("/health", func(c *gin.Context) { s.systemHandler.HealthCheck(c) })
	s.router.GET("/api/health", func(c *gin.Context) { s.systemHandler.HealthCheck(c) })
	s.router.GET("/api/v1/health", func(c *gin.Context) { s.systemHandler.HealthCheck(c) })

	// Real-time events (SSE). Same pre-middleware-ordering rationale as /health.
	// Gated behind auth (pen-test finding MED-2): the stream carries library
	// events (imports, scan progress, metadata updates) that anonymous clients
	// must not see. A browser EventSource automatically sends the HttpOnly
	// session cookie, so the logged-in UI keeps working; anonymous clients get
	// 401. When auth is disabled (local single-user mode) this is a no-op, like
	// the rest of the API. Built inline because the shared authMiddleware is
	// constructed later in this function (and routes registered before a
	// router.Use() don't inherit it).
	eventsAuth := gin.HandlerFunc(func(c *gin.Context) { c.Next() })
	if config.AppConfig.EnableAuth {
		eventsAuth = servermiddleware.RequireAuth(s.Store())
	}
	s.router.GET("/api/events", eventsAuth, func(c *gin.Context) { s.systemHandler.HandleEvents(c) })

	// Public temp-login consumer at the root so URLs are short and
	// browser-friendly. Validates the token, deletes it (single-use),
	// creates a 24h session, sets the cookie, redirects to the SPA.
	s.router.GET("/auth/temp-login", s.consumeTempLoginToken)

	// Redirect /api/* to /api/v1/* for v1 compatibility
	s.router.Use(func(c *gin.Context) {
		path := c.Request.URL.Path
		// If path starts with /api/ but not /api/v1/ and not /api/health and not /api/events
		if strings.HasPrefix(path, "/api/") &&
			!strings.HasPrefix(path, "/api/v1/") &&
			!strings.HasPrefix(path, "/api/health") &&
			!strings.HasPrefix(path, "/api/events") &&
			!strings.HasPrefix(path, "/api/metrics") {
			// Redirect to /api/v1/
			newPath := strings.Replace(path, "/api/", "/api/v1/", 1)
			c.Redirect(http.StatusMovedPermanently, newPath)
			c.Abort()
			return
		}
		c.Next()
	})

	jsonLimitBytes := int64(config.AppConfig.JSONBodyLimitMB) * 1024 * 1024
	uploadLimitBytes := int64(config.AppConfig.UploadBodyLimitMB) * 1024 * 1024

	// Rate limiting is opt-in. Default 0 means disabled (local/single-user server).
	apiRateLimiter := gin.HandlerFunc(func(c *gin.Context) { c.Next() })
	if rpm := config.AppConfig.APIRateLimitPerMinute; rpm > 0 {
		burst := rpm / 5
		if burst < 10 {
			burst = 10
		}
		apiRateLimiter = servermiddleware.NewIPRateLimiter(rpm, burst).Middleware()
	}
	bodyLimitMiddleware := servermiddleware.MaxRequestBodySize(jsonLimitBytes, uploadLimitBytes)
	authMiddleware := gin.HandlerFunc(func(c *gin.Context) {
		c.Next()
	})
	if config.AppConfig.EnableAuth {
		authMiddleware = servermiddleware.RequireAuth(s.Store())
	} else {
		slog.Warn("authentication is disabled (enable_authfalse) — do not expose this server to untrusted networks")
	}
	if !config.AppConfig.EnableRateLimit {
		slog.Warn("rate limiting is disabled (enable_rate_limitfalse) — the API is vulnerable to abuse. Set enable_rate_limit true in config.yaml for production deployments")
	}

	// API routes (auth + rate limits + request-size limits)
	api := s.router.Group("/api/v1")
	api.Use(apiRateLimiter, bodyLimitMiddleware)
	{
		protected := api.Group("")
		protected.Use(authMiddleware)

		s.wireHandlers(api, authMiddleware, protected)
		{
			// Audiobook routes.
			// NOTE: the main audiobooks list / CRUD domain (list, count, facets,
			// soft-delete/restore/purge, rescan, get, tags, update, delete, cover,
			// segments, files, changelog, path-history, external-ids,
			// extract-track-info, relocate, batch, batch-operations, user tags,
			// tags-detailed, batch-tags, alternative-titles, metadata-history,
			// undo, field-states, changes — 36 handlers) was migrated to the
			// handlers/audiobooks sub-package (wire_handlers.go). The sibling
			// /audiobooks/:id/* routes below stay here because they belong to
			// OTHER domains (quarantine, rating, sample, write-back).
			protected.GET("/audiobooks/quarantined", s.perm(auth.PermLibraryView), s.listQuarantinedBooks)
			protected.POST("/audiobooks/:id/quarantine", s.perm(auth.PermSettingsManage), s.quarantineBook)
			protected.DELETE("/audiobooks/:id/quarantine", s.perm(auth.PermSettingsManage), s.unquarantineBook)
			protected.GET("/audiobooks/:id/sample", s.perm(auth.PermLibraryView), s.handleAudioSample)

			// Author, narrator, and series routes.
			// NOTE: /authors, /authors/count, /authors/merge,
			// /authors/:id/{reclassify-as-narrator,name,split,resolve-production,
			// aliases,books}, /authors/:id/aliases/:aliasId, /authors/bulk-delete,
			// /authors/:id, /narrators[/count], /audiobooks/:id/narrators,
			// /series[/count], /series/:id[/books,/name,/split], PATCH /series/:id,
			// /series/bulk-delete, and the work routes migrated to the
			// handlers/entities sub-package (wire_handlers.go).
			// /authors/duplicates(/refresh), /audiobooks/merge,
			// /series/duplicates(/refresh), /series/deduplicate, /series/merge,
			// /series/prune(/preview), /series/normalize(/preview) and
			// /dedup/validate migrated to the handlers/duplicates sub-package
			// (wire_handlers.go).
			// /authors/duplicates/ai-review[/apply] migrated to AIHandler (wire_handlers.go)

			// Embedding-based dedup
			//
			// NOTE: the dedup-domain routes (GET /dedup/candidates,
			// /dedup/candidates/export, /dedup/stats,
			// /dedup/candidates/series-summary; POST
			// /dedup/candidates/:id/{merge,dismiss}, /dedup/candidates/
			// {bulk-merge,merge-cluster,dismiss-cluster,remove-from-cluster,
			// merge-series}, /dedup/{scan,scan-llm,scan-acoustid,
			// scan-book-signature,refresh,purge-stale,reset-acoustid,embed,
			// embed-async} and POST /audiobooks/:id/compare-acoustid) were
			// migrated to deduphandler.Handler and are now registered in
			// wireHandlers (wire_handlers.go). The survivor below stays here
			// because it lives in fingerprint_rescan.go, not the migrated
			// dedup_handlers.go.
			protected.POST("/dedup/fingerprint-rescan", s.perm(auth.PermScanTrigger), s.triggerFingerprintRescan)

			// Operation routes
			//
			// NOTE: the operations-domain routes (GET /operations,
			// /operations/stale, POST /operations/{scan,organize,transcode,
			// optimize,clear-stale,optimize-database,sweep-tombstones,
			// set-internal-flag}, GET /operations/audit-files, GET
			// /operations/:id/{status,logs,result,changes}, GET
			// /operations/:id/undo/preflight, DELETE /operations/:id, DELETE
			// /operations/history, POST /operations/:id/revert, the /tasks
			// routes and the /maintenance-window routes) were migrated to
			// operations.Handler and are now registered in wireHandlers
			// (wire_handlers.go). The survivors below stay here because they
			// still call *Server methods directly.
			//
			// UOS-14: /operations/active and /operations/recent are removed — return 410 Gone.
			// Use GET /operations/timeline instead.
			protected.GET("/operations/active", s.perm(auth.PermLibraryView), func(c *gin.Context) {
				c.JSON(http.StatusGone, gin.H{"error": "gone", "message": "this endpoint has been removed; use GET /api/v1/operations/timeline instead"})
			})
			protected.GET("/operations/recent", s.perm(auth.PermLibraryView), func(c *gin.Context) {
				c.JSON(http.StatusGone, gin.H{"error": "gone", "message": "this endpoint has been removed; use GET /api/v1/operations/timeline instead"})
			})

			// UOS-06 operations v2 routes (timeline, events, v2/:id, op-defs)
			// are registered in wireHandlers via OperationsV2Handler.

			protected.GET("/file-ops/pending", s.perm(auth.PermLibraryView), s.handleListPendingFileOps)
			protected.GET("/operations/:id/results", s.perm(auth.PermLibraryView), s.handleGetOperationResults)
			protected.GET("/operations/reconcile/preview", s.perm(auth.PermLibraryView), s.reconcilePreview)
			protected.POST("/operations/reconcile", s.perm(auth.PermScanTrigger), s.startReconcile)
			protected.POST("/operations/reconcile/scan", s.perm(auth.PermScanTrigger), s.startReconcileScan)
			protected.GET("/operations/reconcile/scan/latest", s.perm(auth.PermLibraryView), s.latestReconcileScan)
			protected.POST("/operations/itunes-path-reconcile", s.perm(auth.PermScanTrigger), s.itunesSvcGuard(s.handleITunesPathReconcile))
			protected.POST("/operations/itunes-path-repair", s.perm(auth.PermScanTrigger), s.itunesSvcGuard(s.handleITunesPathRepair))
			protected.POST("/operations/cleanup-version-groups", s.perm(auth.PermSettingsManage), s.cleanupDuplicateVersionGroupsHandler)
			protected.POST("/operations/mark-broken-segments", s.perm(auth.PermSettingsManage), s.markBrokenSegmentBooksHandler)
			protected.POST("/operations/merge-novg-duplicates", s.perm(auth.PermSettingsManage), s.mergeNoVGDuplicatesHandler)
			protected.POST("/operations/assign-orphan-vgs", s.perm(auth.PermSettingsManage), s.assignOrphanVGsHandler)

			// Import routes
			protected.POST("/import/collision-preview", s.perm(auth.PermLibraryView), s.handleImportCollisionPreview)

			// iTunes import routes
			itunesGroup := protected.Group("/itunes")
			{
				// NOTE: the 12 core iTunes routes (validate, test-mapping,
				// import, write-back[/all/preview], library-stats, books,
				// import-status[/bulk], library-status, sync) were migrated
				// to handlers.ITunesHandler and are now registered in
				// wireHandlers (wire_handlers.go). The survivors below stay
				// here because they still call *Server methods directly.
				//
				// REMOVED in v5: cleanup-orphans was a bulk-remove
				// endpoint that inferred "what should not be in iTunes"
				// from the DB. With a stale or partially-cleared DB
				// (or with manually-managed iTunes content), it would
				// wipe legitimate tracks. Targeted-only removes (one
				// PID per explicit user delete via the per-book delete
				// path) are the only safe pattern. Any future bulk
				// reconciliation must be opt-in, dry-run-by-default,
				// preview-required, and reviewed item-by-item.
				// Diff-and-batch rebuild: computes the full diff
				// between the DB and the current ITL file, then
				// applies all adds/removes/updates in one atomic
				// safeWriteITL call. Supports dry_run=true to
				// preview without applying. Backlog 7.9.
				itunesGroup.POST("/rebuild", s.perm(auth.PermLibraryEditMetadata), s.rebuildITLHandler)
				// Full rebuild: strip all tracks, re-insert all DB books (7.9 nuclear path).
				itunesGroup.POST("/rebuild-full", s.perm(auth.PermLibraryEditMetadata), s.rebuildITLFullHandler)
				// Partial export: build ITL containing only specified book IDs (6.4 partial).
				itunesGroup.POST("/export-partial", s.perm(auth.PermIntegrationsManage), s.exportITLPartialHandler)

				// ITL file transfer (6.4)
				itunesGroup.GET("/library/download", s.perm(auth.PermIntegrationsManage), s.itunesSvcGuard(func(c *gin.Context) { s.itunesSvc.Transfer.HandleDownload(c) }))
				itunesGroup.POST("/library/upload", s.perm(auth.PermIntegrationsManage), s.itunesSvcGuard(func(c *gin.Context) { s.itunesSvc.Transfer.HandleUpload(c) }))
				itunesGroup.GET("/library/backups", s.perm(auth.PermIntegrationsManage), s.itunesSvcGuard(func(c *gin.Context) { s.itunesSvc.Transfer.HandleBackupList(c) }))
				itunesGroup.POST("/library/restore", s.perm(auth.PermIntegrationsManage), s.itunesSvcGuard(func(c *gin.Context) { s.itunesSvc.Transfer.HandleRestore(c) }))
			}

			// Cover art
			protected.GET("/covers/proxy", s.perm(auth.PermLibraryView), s.handleCoverProxy)
			protected.GET("/covers/local/:filename", s.perm(auth.PermLibraryView), s.handleLocalCover)
			protected.GET("/audiobooks/:id/cover-history", s.perm(auth.PermLibraryView), s.handleListCoverHistory)
			protected.POST("/audiobooks/:id/cover-history/restore", s.perm(auth.PermLibraryEditMetadata), s.handleRestoreCover)

			// Unified task/scheduler routes and the maintenance-window routes
			// (GET /tasks, POST /tasks/:name/run, PUT /tasks/:name, POST
			// /maintenance-window/run, GET /maintenance-window/status, PUT
			// /maintenance-window/config) were migrated to operations.Handler
			// and are now registered in wireHandlers (wire_handlers.go).

			// Result-getter GETs (not job triggers — these poll async results)
			protected.GET("/maintenance/scan-composer-tags/:id", s.perm(auth.PermSettingsManage), s.handleGetComposerScanResults)
			protected.GET("/maintenance/repair-missing-files/:id", s.perm(auth.PermSettingsManage), s.handleGetMissingFileRepairResults)
			// Hash stats endpoints
			protected.GET("/maintenance/book-file-hash-stats", s.perm(auth.PermSettingsManage), s.handleGetBookFileHashStats)
			protected.GET("/maintenance/book-metadata-hash-stats", s.perm(auth.PermSettingsManage), s.handleGetBookMetadataHashStats)
			protected.GET("/maintenance/acoustid-stats", s.perm(auth.PermSettingsManage), s.handleGetAcoustIDStats)
			// Unified maintenance job dispatcher
			protected.GET("/maintenance/jobs", s.perm(auth.PermSettingsManage), s.listMaintenanceJobs)
			// Route-level permission guard (pen-test finding MED-3). The handler
			// also checks permissions per-job, but only when EnableAuth is true;
			// gating at the route is defense-in-depth and protects against a
			// future job that forgets to implement PermissionAware.
			protected.POST("/maintenance/jobs/:job_id", s.perm(auth.PermSettingsManage), s.runMaintenanceJob)

			// Admin-only destructive endpoints
			adminOnly := protected.Group("")
			adminOnly.Use(servermiddleware.RequireAdmin())
			{
				adminOnly.POST("/maintenance/wipe", s.handleWipe)
			}

			// Policy, system, config, dashboard, and backup routes migrated to the
			// handlers/system sub-package (wireHandlers).

			// Enhanced metadata routes
			// batch-update / validate / export / import / search / fields /
			// bulk-fetch migrated to the handlers/metadata sub-package (wireHandlers).
			protected.POST("/metadata/batch-fetch-candidates", s.perm(auth.PermLibraryEditMetadata), s.handleBatchFetchCandidates)
			protected.GET("/metadata/recent-fetches", s.perm(auth.PermLibraryView), s.handleGetLatestMetadataFetch)
			// Unified metadata-results listing — preferred over /metadata/pending-review.
			// Returns books with their latest fetch status + by_status counts; supports
			// repeatable ?status= filtering for the Library page toggles + Resume Review.
			protected.GET("/library/metadata-results", s.perm(auth.PermLibraryView), s.handleListMetadataResults)
			// /library/quick-queries migrated to the handlers/system sub-package
			// (wireHandlers).
			protected.POST("/metadata/batch-apply-candidates", s.perm(auth.PermLibraryEditMetadata), s.handleBatchApplyCandidates)
			protected.POST("/metadata/batch-reject-candidates", s.perm(auth.PermLibraryEditMetadata), s.handleRejectCandidates)
			protected.POST("/metadata/batch-unreject-candidates", s.perm(auth.PermLibraryEditMetadata), s.handleUnrejectCandidates)
			// fetch-metadata / search-metadata / apply-metadata / mark-no-match /
			// revert-metadata / metadata-rejections / cow-versions(+prune) /
			// write-back migrated to the handlers/metadata sub-package (wireHandlers).
			protected.GET("/audiobooks/:id/similar", s.perm(auth.PermLibraryView), s.handleSimilarBooks)

			// AI parsing, scan-pipeline, metadata-source-test, and parse-with-ai
			// routes migrated to AIHandler (wire_handlers.go).

			// Open Library dump routes
			protected.GET("/openlibrary/status", s.perm(auth.PermIntegrationsManage), s.getOLStatus)
			protected.POST("/openlibrary/download", s.perm(auth.PermIntegrationsManage), s.startOLDownload)
			protected.POST("/openlibrary/import", s.perm(auth.PermIntegrationsManage), s.startOLImport)
			protected.POST("/openlibrary/upload", s.perm(auth.PermIntegrationsManage), s.uploadOLDump)
			protected.DELETE("/openlibrary/data", s.perm(auth.PermIntegrationsManage), s.deleteOLData)

			// Work routes (logical title-level grouping) and the singular /work
			// compatibility routes migrated to the handlers/entities sub-package
			// (wire_handlers.go).

			// Version management routes are registered in wireHandlers
			// (VersionsHandler).

			// Update routes
			protected.GET("/update/status", s.perm(auth.PermSettingsManage), s.getUpdateStatus)
			protected.POST("/update/check", s.perm(auth.PermSettingsManage), s.checkForUpdate)
			protected.POST("/update/apply", s.perm(auth.PermSettingsManage), s.applyUpdate)

			// Blocked hashes management routes migrated to the handlers/system
			// sub-package (wireHandlers).

			// Diagnostics routes (db-health/export/submit-ai/ai-results/
			// apply-suggestions migrated to DiagnosticsHandler in
			// wire_handlers.go; fingerprint-failures stays here for now).
			protected.GET("/diagnostics/fingerprint-failures", s.perm(auth.PermSettingsManage), s.getFingerprintFailures)

			// AI Jobs observability route migrated to AIHandler (wire_handlers.go)

			// Bench routes (only available with -tags bench)
			s.setupUserTagRoutes(protected)
			s.registerVersionLifecycleRoutes(protected)
			s.registerEntityTagRoutes(protected)
			s.registerDelugeRoutes(protected)
			s.setupBenchRoutes(protected)
		}
	}

	// Serve static files (React frontend)
	// Implementation is in static_embed.go or static_nonembed.go depending on build tags
	s.setupStaticFiles()
}

func isStaleOperationStatus(status string) bool {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case "running", "queued", "in_progress":
		return true
	default:
		return false
	}
}

func operationStartedOrCreatedAt(op database.Operation) time.Time {
	if op.StartedAt != nil && !op.StartedAt.IsZero() {
		return *op.StartedAt
	}
	return op.CreatedAt
}

func (s *Server) collectStaleOperations(timeout time.Duration) ([]database.Operation, error) {
	if timeout <= 0 {
		return []database.Operation{}, nil
	}
	if s.Store() == nil {
		return nil, fmt.Errorf("database not initialized")
	}

	ops, err := s.Store().GetRecentOperations(500)
	if err != nil {
		return nil, err
	}
	threshold := time.Now().Add(-timeout)
	stale := make([]database.Operation, 0)
	for _, op := range ops {
		if !isStaleOperationStatus(op.Status) {
			continue
		}
		started := operationStartedOrCreatedAt(op)
		if started.IsZero() || started.After(threshold) {
			continue
		}
		stale = append(stale, op)
	}
	return stale, nil
}

func (s *Server) failStaleOperations(timeout time.Duration) {
	staleLog := logger.NewWithActivityLog("reaper", s.Store())
	stale, err := s.collectStaleOperations(timeout)
	if err != nil {
		staleLog.Warn("stale operation check failed: %v", err)
		return
	}
	if len(stale) == 0 {
		return
	}

	for _, op := range stale {
		msg := fmt.Sprintf("operation timed out after %s", timeout)
		if err := s.Store().UpdateOperationError(op.ID, msg); err != nil {
			staleLog.Warn("failed to mark stale operation %s as failed: %v", op.ID, err)
			continue
		}
		if s.hub != nil {
			s.hub.SendOperationStatus(op.ID, "failed", map[string]any{
				"error": msg,
			})
		}
		staleLog.Warn("marked stale operation as failed: id=%s type=%s", op.ID, op.Type)
	}
}

func GetDefaultServerConfig() ServerConfig {
	return ServerConfig{
		Port:         "8484",
		Host:         "localhost",
		ReadTimeout:  15 * time.Second,  // Allow slow clients without stalling forever
		WriteTimeout: 0,                 // Disable write timeout so SSE streams stay open
		IdleTimeout:  120 * time.Second, // 2 minute idle timeout
	}
}
