// file: internal/server/server_lifecycle.go
// version: 4.62.0
// guid: 2f98675b-61e1-45a0-94e9-e7fdeb8f273e
// last-edited: 2026-10-25

package server

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
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
	"github.com/falkcorp/audiobook-organizer/internal/merge"
	"github.com/falkcorp/audiobook-organizer/internal/metrics"
	"github.com/falkcorp/audiobook-organizer/internal/opscatalog"
	"github.com/falkcorp/audiobook-organizer/internal/realtime"
	"github.com/falkcorp/audiobook-organizer/internal/remux"
	"github.com/falkcorp/audiobook-organizer/internal/scanner"
	"github.com/falkcorp/audiobook-organizer/internal/scheduler"
	"github.com/falkcorp/audiobook-organizer/internal/sdnotify"
	"github.com/falkcorp/audiobook-organizer/internal/search"
	systemhandlers "github.com/falkcorp/audiobook-organizer/internal/server/handlers/system"
	servermiddleware "github.com/falkcorp/audiobook-organizer/internal/server/middleware"
	"github.com/falkcorp/audiobook-organizer/internal/serviceregistry"
	"github.com/falkcorp/audiobook-organizer/internal/startup"
	"github.com/falkcorp/audiobook-organizer/internal/watcher"
	"github.com/gin-gonic/gin"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/quic-go/quic-go"
	"github.com/quic-go/quic-go/http3"
)

// opRegistrationGate reports whether the operations registry came up whole.
//
// An op that fails to register does not degrade -- it ceases to exist,
// silently, while every other subsystem reports healthy. Whatever enqueues it
// later gets "unknown op" from a server that looks fine, which is a far worse
// thing to diagnose than a refusal to start. Until 2026-08-16 these failures
// were logged at WARN in NewServer's registrar loop and dropped, because
// NewServer has no error return; they are collected on the Server now and
// enforced here, on the one path where a real deployment happens.
//
// Split out of Start so the condition can be tested without booting a server:
// Start's later stages spawn background goroutines that require a fully wired
// Server, so a "no errors" control exercised through Start panics rather than
// demonstrating anything.
var lifecycleLog = logger.New("server")

func (s *Server) opRegistrationGate() error {
	if len(s.opRegistrationErrs) == 0 {
		return nil
	}
	return fmt.Errorf("operation registration failed for %d op(s), refusing to start: %w",
		len(s.opRegistrationErrs), errors.Join(s.opRegistrationErrs...))
}

// opCatalogGate is the startup op-catalog check (05-PR7). It runs after
// container.Start, because the service-registry plugins register their ops
// from PostInit and opRegistrationGate, which runs first, cannot see them.
//
// It validates the whole live catalog (every v2 def wrapped by
// registry.FromV2, every native def as registered) with
// opscatalog.ValidateCatalog and resolves every line of the embedded op-ID
// ledger. A ledger ID resolves when it is a live def or alias, a def or
// FormerID of a plugin's static OperationDefs() list, a native bundle def
// built with zero Deps, or a tombstone. The static lists are what let a
// correctly configured server with no Deluge client (so no Deluge ops in its
// live registry) start; an ID in none of them is a renamed op without an
// alias, and the server refuses to start rather than strand every stored row
// that carries it.
func (s *Server) opCatalogGate() error {
	return s.checkOpCatalog(opscatalog.ParseLedger(embeddedOpLedger))
}

// checkOpCatalog is opCatalogGate with the ledger as a parameter, so a test
// can inject one.
func (s *Server) checkOpCatalog(ledger []string) error {
	if s.opRegistry == nil {
		return errors.New("op catalog check: no operations registry, refusing to start")
	}
	reg := s.opRegistry
	u := opscatalog.BuildUniverse(reg.ActiveDefs(), reg.Aliases(),
		opscatalog.StaticPluginDefs(), opscatalog.Bundles(opscatalog.Deps{}))
	if err := opscatalog.ValidateCatalog(reg.Catalog(), u, ledger); err != nil {
		return fmt.Errorf("op catalog check failed, refusing to start: %w", err)
	}
	return nil
}

// startSearchIndexing switches search indexing live once the Bleve index is
// open (s.searchIndex non-nil): it creates the queue enqueueIndex feeds, starts
// the worker and the dirty-set reconciler, and installs the server's store as
// the global store.
//
// It does NOT install anything on the store. NewServer installed the one
// change observer (installSearchObserver) before the service registry was
// built, so s.store, every registry service and the global store installed here
// are all the same object and every write already reaches enqueueIndex, which
// is a no-op until the queue exists.
//
// The queue is assigned under indexQueueMu because other goroutines can already
// be writing (anything NewServer spawned), and enqueueIndex reads s.indexQueue
// under the read lock.
func (s *Server) startSearchIndexing() {
	s.indexQueueMu.Lock()
	s.indexQueue = make(chan indexRequest, 1024)
	s.indexQueueMu.Unlock()
	database.SetGlobalStore(s.storeForWiring())
	// Reconciler for events the bounded queue had to drop. Enrolled in
	// bgWG and gated on bgCtx like every other background loop, so
	// Shutdown drains it before Pebble closes. Started before the worker
	// so a backlog left by the previous process starts draining even if
	// the worker is immediately saturated.
	s.bgWG.Go("search-reconciler", func() {
		s.runSearchReconciler()
	})
	s.bgWG.Go("index-worker", func() {
		s.runIndexWorker()
	})
	// Reports bleve writes that never return. Separate goroutine because a
	// wedged index blocks every writer, including the reconciler and worker.
	s.bgWG.Go("search-index-watchdog", func() {
		s.runSearchIndexWatchdog()
	})
	s.startMergeUserStateRepair()
	// Route the /audiobooks?search= path through Bleve.
	if s.audiobookService != nil {
		s.audiobookService.SetSearchIndex(s.searchIndex)
	}
}

// adoptSearchIndex takes the Bleve index the searchindex service opened and,
// when there is one, switches search indexing live (startSearchIndexing).
//
// Bleve open failures are logged inside IndexService.Start, which then
// returns nil with Index() nil, and the server runs without search. That
// contract is kept; the "searchindex" step (Degraded in startup.Table) is
// what makes the missing index visible as the search_index token in /health.
// There is no reopen path, so nothing Clears the token in this process.
func (s *Server) adoptSearchIndex(steps *startup.Steps) {
	if s.container != nil && s.searchIndex == nil {
		if idx, ok := serviceregistry.TryGet[*search.IndexService](s.container, "searchindex"); ok && idx != nil {
			s.searchIndex = idx.Index()
		}
	}
	_ = steps.Run("searchindex", func() error {
		if s.searchIndex == nil {
			return errors.New("search index not open")
		}
		return nil
	})
	if s.searchIndex != nil {
		s.startSearchIndexing()
	}
}

// deferredActivityFlushBudget bounds how long shutdown (or a failed Start)
// waits for RecordDeferred entries to reach the store. The unit's
// TimeoutStopSec is 30s and the HTTP drain and ops registry spend part of it
// first, so this stays small.
const deferredActivityFlushBudget = 5 * time.Second

// flushDeferredActivity is for a failed Start. It waits, at most
// deferredActivityFlushBudget, for the flush goroutine to write every entry
// still queued by Service.RecordDeferred. It deliberately does not close the
// store: on the configureAndStartHTTP failure the container, and with it the
// activity Writer, is still running and may still write through the store,
// so closing here would open exactly the write-after-close race Shutdown
// avoids. Start's caller exits right after, and SQLite recovers the WAL on the
// next open. Entries not confirmed by the deadline are logged with a count.
func (s *Server) flushDeferredActivity() {
	if s.activityService == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), deferredActivityFlushBudget)
	defer cancel()
	if err := s.activityService.FlushDeferred(ctx); err != nil {
		lifecycleLog.Warn("failed start: %v", err)
	}
}

// closeActivityStore writes any RecordDeferred entries and closes the
// activity store through Service.Close, which never closes it under an
// in-flight deferred write. On timeout the store is left open and the log
// line counts the entries not yet confirmed written.
//
// "Left open" protects less than it sounds: the Pebble activity store borrows
// the MAIN store's *database.GatedDB and its Close is a no-op, so the DB is
// actually closed by cmd's deferred closeStore after Start returns — which
// happens even when this call timed out. PebbleStore.Close waits (bounded) for
// a write already in flight, and refuses any later write with an error
// wrapping pebble.ErrClosed that Service.writeDeferred logs with a lost-entry
// count, not a process panic (internal/database/pebble_gate.go).
func (s *Server) closeActivityStore() {
	if s.activityService == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), deferredActivityFlushBudget)
	defer cancel()
	if err := s.activityService.Close(ctx); err != nil {
		lifecycleLog.Warn("activity log store not closed: %v", err)
		return
	}
	lifecycleLog.Info("Activity log store closed")
}

// startupStepsFor stores the runner Start uses for its steps, creating one
// when cmd did not supply it (tests, child mode), and returns it.
func (s *Server) startupStepsFor(cfg ServerConfig) *startup.Steps {
	steps := cfg.StartupSteps
	if steps == nil {
		steps = startup.New().SetErrorLog(lifecycleLog.Error)
	}
	s.startupSteps.Store(steps)
	return steps
}

// startupDegradedSource is /health's lazy provider for the degraded
// startup-step tokens: the runner Start stored, or nil before Start. The nil
// check keeps a nil *startup.Steps from being boxed into a non-nil interface.
func (s *Server) startupDegradedSource() systemhandlers.DegradedSource {
	if st := s.startupSteps.Load(); st != nil {
		return st
	}
	return nil
}

func (s *Server) Start(cfg ServerConfig) error {
	// Every step below runs through the process's startup runner, which
	// applies the step's class from startup.Table (Fatal returns the error,
	// Degraded records a /health token and continues).
	steps := s.startupStepsFor(cfg)

	// Refuse to serve with a hole in the operations registry, before touching
	// anything else -- nothing below this line should run on a server that is
	// about to be rejected.
	if err := steps.Run("opRegistrationGate", s.opRegistrationGate); err != nil {
		return err
	}

	// Start the runtime flight recorder before the container starts the ops
	// registry's watchdog, so its first strike already has a trace to save.
	s.startFlightRecorder()
	// Every return below that is not the normal shutdown (a failed container
	// start, a failed step) must not leave the runtime recorder tracing in
	// the process. Stop is idempotent; the shutdown path still stops it
	// explicitly, ahead of the background-goroutine wait.
	defer s.flight.Stop()

	s.externalURL = strings.TrimRight(cfg.ExternalURL, "/")

	// With authority_evidence_enabled on, start the authority snapshot load
	// now rather than on the first credit resolve, so a scan that starts
	// right after boot already has the lists. A no-op with the flag off.
	s.authorityEvidence.Prime()

	// SERVER-LIFECYCLE-FLIP: drive Starter services via the container.
	// Container.Start runs services in resolved dep order; failures
	// abort startup and roll back already-started services.
	if s.container != nil {
		if err := steps.Run("container.Start", func() error {
			if err := s.container.Start(s.bgCtx); err != nil {
				return fmt.Errorf("container start: %w", err)
			}
			return nil
		}); err != nil {
			s.flushDeferredActivity()
			return err
		}
	}

	// The second registry gate: the whole catalog and the op-ID ledger,
	// once PostInit has registered the service-registry plugins' ops.
	if err := steps.Run("opCatalogGate", s.opCatalogGate); err != nil {
		s.flushDeferredActivity()
		return err
	}

	// Delete what the removed iTunes write-back left in the store (its
	// durable queue, held removes, status and outbox rows). Idempotent: once
	// they are gone this is one empty scan per prefix.
	if st := s.storeForWiring(); st != nil {
		purgeLegacyITunesWriteBackKeys(st)
	}

	// Start embed queue debounce timer (activated after container start so
	// Ollama daemon wiring is complete before any drain fires).
	if s.embedQueue != nil {
		s.embedQueue.Start(s.bgCtx)
	}

	// Pull the now-open Bleve index out of the searchindex service
	// (opened above by Container.Start) and switch indexing live (create the
	// queue the store's change observer feeds) BEFORE any background
	// goroutines or HTTP handlers
	// start writing through s.store, so no write made after Start can
	// miss the queue (pre-PR #903 this happened later and was
	// timing-dependent).
	s.adoptSearchIndex(steps)

	// HNSW snapshot is now loaded earlier, in NewServer (between Build and PostInit),
	// so that dedup.Engine.PostInit can detect the populated store and skip the
	// redundant HydrateChromem walk. Nothing to do here. (HNSW-CRASH-2026-06-18)

	s.startCacheWarmers()

	// Fatal. configureAndStartHTTP binds the main listener (and loads the
	// TLS key pair) synchronously, so a port in use or an unreadable cert
	// returns here and Start fails instead of a goroutine exiting the
	// process after the fact. Only a Serve error AFTER a successful bind
	// still exits from its goroutine (H113).
	if err := steps.Run("http", func() error { return s.configureAndStartHTTP(cfg) }); err != nil {
		s.flushDeferredActivity()
		return err
	}

	// The last Fatal step has passed and the port accepts connections: tell
	// systemd (Type=notify) the service is started. This is deliberately
	// NOT after the in-memory warmup (130-200 s on prod), which would turn a
	// slow disk into a start timeout and a restart loop; /health reports
	// `ready` for that and the deploy waits on it. A notify failure is
	// logged, never fatal: under Type=simple there is no socket at all.
	if err := sdnotify.Ready(); err != nil {
		lifecycleLog.Warn("systemd readiness notification failed: %v", err)
	}
	if err := sdnotify.Status("Serving; readiness is reported by the health endpoint"); err != nil {
		lifecycleLog.Warn("systemd status notification failed: %v", err)
	}

	// The listener is started: activity entries queued on the startup path
	// (Service.RecordDeferred) may now be written, in the background. Nothing
	// before this line may wait on the activity store -- see NewServer's
	// startup record for the 2026-09-14 outage that rule comes from.
	if s.activityService != nil {
		s.activityService.StartDeferredFlush()
	}

	s.seedRolesAndTokens()

	// One-time: re-order every cached candidate row that holds an
	// owner-rejected candidate, so it leaves slot 0 (owner_rejected_rerank.go).
	// Background, bgWG-tracked; marker-gated, so it runs until it succeeds once.
	s.startOwnerRejectedRerank()

	// Operations interrupted by a previous shutdown/crash are resumed by the
	// operations registry: Registry.Start runs resumeAfterStartup, which reads
	// ListResumableOperationsV2 and applies each def's declared ResumePolicy.
	// A v1 sweep used to run here as well; it was deleted on 2026-09-07 because
	// its only candidate source was the `operation:` keyspace, whose minter was
	// retired 2026-08-23.

	// Recover interrupted file I/O operations (cover embed, tag write, rename)
	RecoverInterruptedFileOps(s.fileIOPool)

	// Interrupted metadata candidate fetches are resumed by the operations
	// registry, not from here: metadata.candidate-fetch declares
	// ResumePolicy=ResumeRestart and its Run skips books that already have
	// result rows. The hand-rolled resume that used to run here read the v1
	// interrupted-ops list, which no longer has rows for this op.

	s.startBackfills()

	// Stale transcode temp files under the import paths are swept by the
	// hourly transcode_temp_cleanup scheduler task (maintenance.temp-file-cleanup
	// with import_paths_only), not by goroutines started here.

	// Heartbeat: push periodic system.status events via SSE (every 5s) while running
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	shutdown := make(chan struct{})
	var backgroundWG sync.WaitGroup

	// Start unified task scheduler (replaces individual iTunes sync and purge tickers)
	s.scheduler = scheduler.NewTaskScheduler(s.schedulerDeps())
	// ops.Schedule declarations (05-PR8): registered before Start so the
	// scheduler's one v3 loop and the maintenance window see them. A def that
	// fails here is skipped and reported; the rest still schedule.
	if err := s.scheduler.RegisterSchedules(s.opRegistry.Catalog()); err != nil {
		lifecycleLog.Warn("ops.Schedule registration: %v", err)
	}
	s.scheduler.Start(shutdown, &backgroundWG)

	ticker := time.NewTicker(5 * time.Second)
	libraryCountTick, bookCount, folderCount := 0, 0, 0 // the counts are re-read once a minute, re-sent every tick
	backgroundWG.Go(func() {
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				if s.hub != nil {
					// Gather lightweight metrics
					var alloc runtime.MemStats
					runtime.ReadMemStats(&alloc)
					// The library counts walk every book row (CountPrimaryBooks
					// is a full Pebble scan when the memdb is not serving it):
					// 27% of all CPU on prod 2026-10-03 at the old 5 s cadence,
					// so they refresh once a minute; the process gauges stay at 5 s.
					if libraryCountTick%12 == 0 && s.Ops() != nil {
						if bc, err := s.Ops().CountPrimaryBooks(); err == nil {
							bookCount = bc
							metrics.SetBooks(bc)
						}
						if folders, err := s.Ops().GetAllImportPaths(); err == nil {
							folderCount = len(folders)
							metrics.SetFolders(folderCount)
						}
					}
					libraryCountTick++

					// Update Prometheus metrics
					metrics.SetMemoryAlloc(alloc.Alloc)
					metrics.SetGoroutines(runtime.NumGoroutine())
					s.updateSearchIndexMetrics()

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
	})

	// Persist cache observability snapshots to SQLite every 5 minutes so
	// hit/miss trends survive restarts. PebbleDB-backed deployments skip
	// persistence inside runCacheStatsSnapshotter.
	backgroundWG.Go(func() {
		s.runCacheStatsSnapshotter(shutdown)
	})

	// Start auto-scan file watchers if enabled. ONE watcher per enabled
	// import path — previously only the first enabled path was watched,
	// so users with multiple import locations had silent blind spots on
	// every path after the first.
	//
	// Enumeration used to happen exactly ONCE here, from a boot-time snapshot,
	// so an import path added later got no watcher until the process was
	// restarted; and the read was `if err == nil && len(importPaths) > 0`,
	// which turned a failed GetAllImportPaths into zero watchers with no log
	// line at all. Both are now handled by watcher.Supervisor, which re-reads
	// the desired set on a ticker and reports every failure.
	//
	// These watchers remain the LOW-LATENCY path only. The guaranteed
	// catch-all is the periodic scheduled scan (scheduled.library_scan):
	// fsnotify does not see writes made by remote NFS/SMB clients and can
	// exhaust the inotify watch limit on a large tree, so it must never be
	// the only discovery mechanism.
	var watcherSupervisor *watcher.Supervisor
	if config.AppConfig.AutoScanEnabled && s.Ops() != nil {
		debounce := 5 * time.Second
		if config.AppConfig.AutoScanDebounceSeconds > 0 {
			debounce = time.Duration(config.AppConfig.AutoScanDebounceSeconds) * time.Second
		}
		watchLog := logger.NewWithActivityLog("auto-scan", s.storeForWiring())
		// The same callback is reused across watchers because each watcher
		// invokes it with its own root path, so the scan target is correct
		// per event.
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
					if _, enqErr := s.opRegistry.EnqueueOp(context.Background(), "library.scan", scanner.LibraryScanParams{FolderPath: &scanPath}); enqErr != nil {
						watchLog.Error("Auto-scan: failed to enqueue: %v", enqErr)
					}
				}()
			}
		}
		// Re-read on every reconcile, not once: this is what makes a path
		// added through the UI get a watcher without a restart.
		paths := func() ([]string, error) {
			importPaths, err := s.Ops().GetAllImportPaths()
			if err != nil {
				return nil, fmt.Errorf("GetAllImportPaths: %w", err)
			}
			var enabled []string
			for _, ip := range importPaths {
				if ip.Enabled {
					enabled = append(enabled, ip.Path)
				}
			}
			return enabled, nil
		}
		watcherSupervisor = watcher.NewSupervisor(paths, cb, debounce, 0)
		watcherSupervisor.SetErrorHook(func(err error) {
			// Surfaced in the activity log too, so an operator sees it in the
			// UI rather than only in journalctl.
			watchLog.Error("Auto-scan watcher problem: %v", err)
		})
		backgroundWG.Go(func() {
			watcherSupervisor.Run(shutdown)
		})
	}

	// Periodic cleanup of expired/revoked auth sessions.
	if s.Ops() != nil {
		sessionLog := logger.NewWithActivityLog("session-cleanup", s.storeForWiring())
		sessionCleanupTicker := time.NewTicker(10 * time.Minute)
		backgroundWG.Go(func() {
			defer sessionCleanupTicker.Stop()
			for {
				select {
				case <-sessionCleanupTicker.C:
					pruneExpiredSessions(s.Ops(), sessionLog, time.Now())
				case <-shutdown:
					return
				}
			}
		})
	}

	// Everything is started; the only thing left is the signal. Announce that
	// so a test can send SIGTERM the moment it is safe (TASK-205).
	s.armShutdown.Do(func() { close(s.shutdownArmed) })

	// Wait for interrupt signal to gracefully shutdown the server
	<-quit
	// Tell systemd the stop has begun before anything slow runs (the client
	// broadcast below sleeps, the registry drain can take 10 s).
	if err := sdnotify.Stopping(); err != nil {
		lifecycleLog.Warn("systemd stopping notification failed: %v", err)
	}
	if err := sdnotify.Status("Shutting down"); err != nil {
		lifecycleLog.Warn("systemd status notification failed: %v", err)
	}
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

	// End the v3 operations event streams, or the graceful HTTP shutdown
	// below waits on them for its whole timeout.
	s.closeOpsV3Streams()

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
	if s.httpRedirectServer != nil {
		if err := s.httpRedirectServer.Shutdown(ctx); err != nil {
			lifecycleLog.Warn("HTTP->HTTPS redirect server forced shutdown: %v", err)
		}
	}
	// No more scrapes can arrive; drop the pebble metrics closures so they stop
	// pinning this Server and its stores.
	s.unregisterPebbleMetricsSources()

	// Stop the metadata-apply ASIN backfill queue BEFORE the registry
	// drains: its debounce timer enqueues metafetch.asin-backfill runs, and
	// one firing mid-shutdown would enqueue into a registry that is going
	// away. Pending ids are dropped; their no-match markers were cleared on
	// Add, so the next scheduled walk reaches them.
	s.stopASINBackfillQueue()

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
	// Stop the flight recorder with the rest of the background work. Waits at
	// most 5s for a snapshot being written; later Snapshot calls are no-ops.
	s.flight.Stop()
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
	// Whatever the index worker did not pop (wedged, or the wait above timed
	// out) is only in memory; record it so the reconciler repairs it next boot.
	s.markPendingIndexDirty()

	// Stop the file I/O pool — waits for in-flight jobs to finish
	if p := s.fileIOPool; p != nil {
		slog.Info("Waiting for file I/O operations to complete...")
		p.Stop()
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

	// Detach from the store and from credit resolution only now that every
	// writer this server owns has stopped (ops drained or timed out, background
	// work and the file I/O pool waited for, plugins shut down): an op still
	// running after the registry's bounded shutdown resolves credits against the
	// authority lists and writes books through the observer until here. Both are
	// released by identity, so neither removes what a newer server registered.
	s.releaseSearchAndAuthority()

	// Search index is closed by Container.Stop below — IndexService.Stop
	// runs in reverse-resolved order alongside the other registered
	// Stoppers and clears its handle so a double-call is a no-op.

	// SERVER-LIFECYCLE-FLIP: drive remaining Stoppers via the container.
	// Runs in reverse resolved order; covers activityWriter (replaces
	// the inline Stop here pre-flip), updateScheduler (previously never
	// stopped — a leak), and any future Stopper additions. Inline Stops
	// above remain the source of truth for the carefully-sequenced
	// teardown (opRegistry drain before bgCancel, etc.); Container.Stop is idempotent on
	// already-stopped services for those.
	if s.container != nil {
		if err := s.container.Stop(context.Background()); err != nil {
			slog.Warn("container stop", "err", err)
		}
	}

	// Close the activity log store, after writing anything still queued by
	// RecordDeferred (a stop that lands right after boot would otherwise lose
	// the startup entry). Container.Stop above has already stopped the
	// activity Writer, so the deferred flush is the only writer left.
	s.closeActivityStore()

	// Stop every file watcher (one per import path). The supervisor goroutine
	// has already returned via `shutdown`; this only tears down the live
	// watchers it left running.
	if watcherSupervisor != nil {
		stopped := len(watcherSupervisor.WatchedPaths())
		watcherSupervisor.StopAll()
		if stopped > 0 {
			slog.Info("File watchers stopped", "fileWatchers_count", stopped)
		}
	}

	// Persist HNSW snapshot for fast next-boot startup (before embedding store
	// closes so the graph data is still intact and consistent).
	if s.hnswPersistDir != "" {
		if raw, ok := serviceregistry.TryGet[database.VectorANNStore](s.container, "chromemstore"); ok && raw != nil {
			if hnswStore, ok := raw.(*database.HNSWEmbeddingStore); ok {
				// Record the Pebble truth count alongside the graph. Without it
				// the next boot cannot tell a fresh snapshot from a stale one
				// and must discard it — the graph is a filtered projection of
				// the emb: rows, so its own size proves nothing about currency.
				var truthCount func(string) (int, error)
				if embStore, ok := serviceregistry.TryGet[*database.EmbeddingStore](s.container, serviceregistry.KeyEmbeddingStore); ok && embStore != nil {
					truthCount = embStore.CountByType
				}
				if err := hnswStore.ExportWithTruth(s.hnswPersistDir, truthCount); err != nil {
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

// updateSearchIndexMetrics pushes the search index's own document count to
// Prometheus (search_index_docs_total, TODO L3433) — the missing counterpart
// to books_total that lets a Grafana/Prometheus dashboard graph the two
// against each other and catch a divergence like the 2026-08-14 incident
// (67,824 indexed docs vs 63,871 live books) instead of it going unnoticed.
//
// Called from the same 5s metrics ticker as metrics.SetBooks (see Start,
// above). DocCount() is a native Bleve call, not a document scan, so it is
// safe at that cadence — unlike AllDocIDs(), which walks every document and
// is reserved for the boot-time coverage reconciler.
//
// Extracted as its own method (rather than inlined in the ticker closure) so
// it can be exercised directly in tests without driving the ticker.
func (s *Server) updateSearchIndexMetrics() {
	if s.searchIndex == nil {
		// Same nil guard as reconcileSearchIndexCoverage (search_coverage.go):
		// the index may not be open yet (or ever, in a search-disabled config).
		// Skipping here is not a disqualifying condition — just nothing to report.
		return
	}
	n, err := s.searchIndex.DocCount()
	if err != nil {
		slog.Warn("search index metrics: DocCount failed", "err", err)
		return
	}
	metrics.SetSearchIndexDocs(n)
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
// warmerRecover logs and swallows a panic in a fire-and-forget cache warmer.
// Warmers are best-effort by design (they only pre-populate a TTL cache), so a
// warmup fault — a transient store read error, a store read that trips a
// dependency bug — must degrade to a cold cache, never crash the whole server.
// Without this, an unrecovered panic in one warmer goroutine takes the process
// down at startup.
func warmerRecover(name string) {
	if r := recover(); r != nil {
		slog.Error("cache warmer panicked — continuing with a cold cache", "warmer", name, "panic", r)
	}
}

func (s *Server) startCacheWarmers() {
	// Pre-warm facets cache (genres/languages) - lightweight, <1 second
	s.bgWG.Go("facets-warmer", func() {
		defer warmerRecover("facets")
		if s.bgCtx.Err() != nil {
			return // server already shutting down — skip, never warm a closing store
		}
		s.warmFacetsCache()
	})
	// Pre-warm library size cache via filesystem walk so any later refresh
	// path (nightly maintenance, manual rescan) starts with current data.
	// The hot path of /system/status reads DB stats (PR #1137); this just
	// keeps the FS-based numbers fresh in the 24h-TTL package cache.
	s.bgWG.Go("library-sizes-warmer", func() {
		defer warmerRecover("library-sizes")
		if s.bgCtx.Err() != nil {
			return // server already shutting down — skip, never warm a closing store
		}
		s.warmLibrarySizes()
	})
	// Pre-warm the audiobook list cache after memdb is published. Fires
	// the most common library-page queries (title asc/desc, -review:matched,
	// library_state filter) so the user's first load doesn't pay the full
	// cold-miss cost (~3 min on 50K-book library). Enrolled in bgWG and
	// gated on bgCtx (it also spawns the ~30-min trickle warmer): the old
	// fire-and-forget launch outlived test servers and kept querying the
	// store after Close() — panic "pebble: closed" from a trickle-warmer
	// tick minutes into an internal/server package run
	// (PEBBLE-CLOSED-SWEEPTICK-RESIDUAL family, warmer leg).
	s.bgWG.Go("library-list-warmer", func() {
		defer warmerRecover("library-list")
		s.warmAudiobookListCache()
	})
	s.bgWG.Go("metadata-review-warmer", func() {
		defer warmerRecover("metadata-review")
		s.warmMetadataReviewSnapshot()
	})
	s.bgWG.Go("authors-warmer", func() {
		defer warmerRecover("authors")
		if s.bgCtx.Err() != nil {
			return // server already shutting down — skip, never warm a closing store
		}
		s.warmAuthorsCache()
	})
	s.bgWG.Go("series-warmer", func() {
		defer warmerRecover("series")
		if s.bgCtx.Err() != nil {
			return // server already shutting down — skip, never warm a closing store
		}
		s.warmSeriesCache()
	})
	// Pre-warm the metadata-results set. Memoised with a 60s TTL since #2142, but
	// nothing populated it at boot, so the cache was cold after every restart and
	// the first request into the match UI paid the whole build (~34s measured).
	// Memoising moved that cost onto one unlucky request; warming removes it.
	s.bgWG.Go("metadata-results-warmer", func() {
		defer warmerRecover("metadata-results")
		if s.bgCtx.Err() != nil {
			return // server already shutting down — skip, never warm a closing store
		}
		s.warmMetadataResultsCache()
	})

	// Low-frequency background sweep that WARN-logs API keys approaching
	// expiry or lacking one entirely (legacy keys) — observability only,
	// never enforcement (SEC-1/PROC-6).
	s.bgWG.Go("apikey-expiry-sweep", func() {
		s.warnExpiringAPIKeys()
	})
}

// configureAndStartHTTP builds s.httpServer (and, when TLS is configured,
// s.http3Server plus the HTTP->HTTPS redirect listener), binds the listeners
// and launches their Serve goroutines. cfg is taken by value so the local
// TLS-fallback mutations stay scoped to this phase.
//
// The main listener is bound HERE, synchronously, and the TLS key pair is
// loaded here too. That gives two guarantees Start relies on: a port in use
// or an unreadable key pair is returned as a startup error instead of a
// goroutine exiting the process later, and when this returns nil the port
// already accepts connections (the kernel queues them before the first
// Accept), which is when Start sends READY=1 to systemd.
func (s *Server) configureAndStartHTTP(cfg ServerConfig) error {
	// Every listener's handler is wrapped by the peer allow-list, outside gin,
	// so a peer that is not allowed never reaches routing (peer_allowlist.go).
	if err := startupPeerAllowList(); err != nil {
		return err
	}
	if err := startupLoginSaturated(); err != nil {
		return err
	}
	gate := s.peerGatedHandler()
	s.httpServer = &http.Server{
		Addr:              fmt.Sprintf("%s:%s", cfg.Host, cfg.Port),
		Handler:           gate,
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

		// Explicitly enable HTTP/1.1 and HTTP/2 over TLS on the stdlib server
		// (replaces the deprecated x/net http2.ConfigureServer).
		protocols := new(http.Protocols)
		protocols.SetHTTP1(true)
		protocols.SetHTTP2(true)
		s.httpServer.Protocols = protocols

		// Add Alt-Svc header to advertise HTTP/3 if enabled
		if cfg.HTTP3Port != "" {
			s.router.Use(func(c *gin.Context) {
				c.Header("Alt-Svc", fmt.Sprintf(`h3=":%s"; ma=2592000`, cfg.HTTP3Port))
				c.Next()
			})
		}

		// Load the key pair now rather than inside ServeTLS: a key pair
		// that cannot be read must fail Start, before READY=1, instead of
		// exiting the process just after systemd was told it started.
		// ServeTLS(ln, "", "") then serves tlsConfig.Certificates.
		cert, err := tls.LoadX509KeyPair(cfg.TLSCertFile, cfg.TLSKeyFile)
		if err != nil {
			return fmt.Errorf("load TLS key pair: %w", err)
		}
		tlsConfig.Certificates = []tls.Certificate{cert}

		ln, err := s.bindMainListener()
		if err != nil {
			return err
		}
		serving := "HTTPS/HTTP2"
		if cfg.HTTP3Port != "" {
			serving = "HTTPS/HTTP2 (HTTP/3 on UDP port " + cfg.HTTP3Port + ")"
		}
		lifecycleLog.Info("Serving %s on %s", serving, ln.Addr())
		go func() {
			if err := s.httpServer.ServeTLS(ln, "", ""); err != nil && !errors.Is(err, http.ErrServerClosed) {
				// The bind succeeded, so this is the listener failing while
				// serving. A process whose main listener is gone is useless
				// but looks alive (on 2026-08-11 an e2e run got a 200 from a
				// SIBLING that owned the port and gated a change against the
				// wrong build). Exit so supervisors and scripts see it (H113).
				lifecycleLog.Error("HTTPS server stopped serving; exiting: %v", err)
				os.Exit(1)
			}
		}()

		// Start HTTP/3 server if configured. Optional and non-fatal, as
		// before. quic-go binds the UDP socket inside ListenAndServeTLS, so
		// "started without error" is the most this function knows about it;
		// READY=1 does not wait on HTTP/3. (Binding the UDP socket here and
		// handing it to http3.Server.Serve would need the shutdown path to
		// close it too: quic-go's Close leaves a caller-supplied conn open.)
		if cfg.HTTP3Port != "" {
			s.http3Server = &http3.Server{
				Addr:      fmt.Sprintf("%s:%s", cfg.Host, cfg.HTTP3Port),
				Handler:   gate,
				TLSConfig: tlsConfig,
				// quic-go defaults http3 to 0-RTT, which is accepted before the
				// peer's address is validated (peer_allowlist.go).
				QUICConfig: http3ServerQUICConfig(),
			}
			lifecycleLog.Info("Starting HTTP/3 (QUIC) server on UDP %s:%s", cfg.Host, cfg.HTTP3Port)
			go func() {
				if err := s.http3Server.ListenAndServeTLS(cfg.TLSCertFile, cfg.TLSKeyFile); err != nil && !errors.Is(err, http.ErrServerClosed) {
					lifecycleLog.Error("HTTP/3 server failed: %v", err)
				}
			}()
		}

		// HTTP to HTTPS redirect server on port 80. Bound synchronously like
		// the main listener, but a failure stays non-fatal: port 80 may need
		// privileges this process does not have.
		redirectAddr := fmt.Sprintf("%s:80", cfg.Host)
		httpsPort := cfg.Port
		if httpsPort == "80" {
			httpsPort = "443" // Don't redirect 80->80
		}
		redirectLn, err := net.Listen("tcp", redirectAddr)
		if err != nil {
			lifecycleLog.Warn("HTTP->HTTPS redirect server not started (port 80 may require privileges): %v", err)
		} else {
			lifecycleLog.Info("Serving HTTP->HTTPS redirect on %s (to port %s)", redirectLn.Addr(), httpsPort)
			redirectServer := &http.Server{
				Addr:    redirectAddr,
				Handler: redirectServerHandler(cfg.Host, httpsPort),
			}
			s.httpRedirectServer = redirectServer
			go func() {
				if err := redirectServer.Serve(redirectLn); err != nil && !errors.Is(err, http.ErrServerClosed) {
					lifecycleLog.Warn("HTTP->HTTPS redirect server stopped: %v", err)
				}
			}()
		}
	} else {
		// HTTP/1.1 server without TLS
		ln, err := s.bindMainListener()
		if err != nil {
			return err
		}
		lifecycleLog.Info("Serving HTTP/1.1 on %s (use --tls-cert and --tls-key for HTTP/2, add --http3-port for HTTP/3)", ln.Addr())
		go func() {
			if err := s.httpServer.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
				// See the TLS twin above (H113).
				lifecycleLog.Error("HTTP server stopped serving; exiting: %v", err)
				os.Exit(1)
			}
		}()
	}
	return nil
}

// http3ServerQUICConfig is the QUIC configuration of the HTTP/3 server: 0-RTT
// off. With it on, a refused peer could obtain a session ticket and then send
// early data with a spoofed source address that the peer gate and
// trustedClientIP would believe.
func http3ServerQUICConfig() *quic.Config {
	return &quic.Config{Allow0RTT: false}
}

// redirectServerHandler is the port-80 listener's handler: the HTTPS redirect
// behind the peer allow-list. A function of its own so a test can drive it
// without binding port 80.
func redirectServerHandler(host, httpsPort string) http.Handler {
	return newPeerAllowListHandler(newHTTPSRedirectHandler(host, httpsPort))
}

// peerGatedHandler is the handler every serving listener uses: the router
// behind the peer allow-list.
func (s *Server) peerGatedHandler() http.Handler {
	return newPeerAllowListHandler(s.router)
}

// bindMainListener binds s.httpServer.Addr and records the bound address in
// s.httpBoundAddr. A failure (most often the port already in use) is returned,
// so it fails Start rather than leaving a process that looks alive.
func (s *Server) bindMainListener() (net.Listener, error) {
	ln, err := net.Listen("tcp", s.httpServer.Addr)
	if err != nil {
		return nil, fmt.Errorf("bind HTTP listener: %w", err)
	}
	addr := ln.Addr().String()
	s.httpBoundAddr.Store(&addr)
	return ln, nil
}

// seedRolesAndTokens seeds/refreshes multi-user roles and the system user, then
// initializes the one-time bootstrap token and (optionally) the startup
// read-only key. Extracted verbatim from Start; all failures remain non-fatal.
func (s *Server) seedRolesAndTokens() {
	// Seed / refresh the multi-user roles (spec 3.7). Idempotent: if
	// the permission set in auth.SeedRoles has grown since last boot,
	// existing roles pick up the new entries automatically.
	if created, updated, err := auth.SeedRoles(s.storeForWiring()); err != nil {
		slog.Warn("seed roles", "err", err)
	} else if created > 0 || updated > 0 {
		slog.Info("seed roles created, updated", "created", created, "updated", updated)
	}
	if err := auth.SeedSystemUser(s.storeForWiring()); err != nil {
		slog.Warn("seed system user", "err", err)
	}

	// Every API key expires since 2026-10-07. Keys created before that with
	// no expiry are listed and given one (now + 30 days), once; see
	// stampNeverExpiringAPIKeys. Non-fatal: a failure retries next start.
	if _, err := stampNeverExpiringAPIKeys(s.storeForWiring(), time.Now()); err != nil {
		slog.Error("api key expiry stamp failed; will retry next start", "err", err)
	}
	logBootstrapKeyTTLConfig()

	// Initialize the one-time bootstrap token and startup read-only key.
	//
	// These go in the secure state directory, which is a CONSTANT. They used to
	// go in filepath.Dir(database_path), and that is what broke the
	// server-bootstrap runbook on 2026-09-09: the database moved to
	// <root_dir>/.appdata and took the token with it, to a path no sudoers rule
	// named and mode 0700 the operator could not read. Nothing about a
	// credential should depend on which pool the database is sitting on today.
	//
	// The `if dbPath != ""` guard is gone with it. It existed because the path
	// was derived from database_path, so an empty one produced a garbage
	// relative path; a constant has no such failure mode, and keeping the guard
	// would mean an install with no database configured also silently has no
	// emergency access token.
	if dataDir, err := config.EnsureSecureStateDir(); err != nil {
		slog.Error("Failed to create the state directory; no bootstrap token will be written",
			"dir", dataDir, "err", err)
	} else {
		if err := InitBootstrapToken(s.storeForWiring(), dataDir); err != nil {
			slog.Info("Failed to init bootstrap token", "err", err)
		}
		if config.AppConfig.WriteStartupReadOnlyKey {
			if err := InitStartupReadOnlyKey(s.storeForWiring(), dataDir); err != nil {
				slog.Info("Failed to init startup read-only key", "err", err)
			}
		}
	}
}

// startBackfills launches the one-time, idempotent backfill and maintenance
// work. Each goroutine here is enrolled in s.bgWG so shutdown drains it before
// the store closes (SYS-1); the work that runs as registered ops (bootOps,
// acoustid.backfill) is drained by Registry.Shutdown instead.
func (s *Server) startBackfills() {
	// One-time whole-library backfills and file repairs run as registered ops
	// (04-P6), not as bgWG goroutines: they show on the Operations page with
	// progress, can be cancelled, and wait for the memdb warmup in the
	// registry's dispatcher (D44). See enqueueBootOps for the list and for
	// how shutdown still drains them before the store closes.
	if s.opRegistry == nil {
		lifecycleLog.Warn("startup: no operations registry; the boot ops were not enqueued: the one-time " +
			"backfills, the every-boot opchange_by_book index ensure and activity filter-index reconcile, and " +
			"the book_atpath index build, so GetBookChanges, the activity filters and LiveBookIDsAtPath " +
			"stay on their slower paths until a boot with a registry verifies or builds the index")
	} else {
		checks := bootOpChecks{settingTrue: func(key string) bool {
			st, err := s.Ops().GetSetting(key)
			return err == nil && st != nil && st.Value == "true"
		}}
		checks.filterIndexDone = s.activityFilterIndexDoneCheck()
		if l, ok := database.AsCapability[opStatusLookup](s.Ops()); ok {
			checks.hasStatus = l.HasOperationV2WithStatus
		} else {
			lifecycleLog.Warn("startup: store has no HasOperationV2WithStatus; boot enqueues the transcode without the interrupted_ask check")
		}
		enqueueBootOps(s.bgCtx, func(ctx context.Context, id string, params any) (string, error) {
			return s.opRegistry.EnqueueOp(ctx, id, params)
		}, checks)
	}

	// AcoustID fingerprint backfill. CONC-9 removed the serial server-side
	// duplicate (backfillAcoustIDs) in favor of the parallel plugin op
	// acoustid.backfill (internal/plugins/acoustid/backfill.go, registry.RunItems
	// with bounded Concurrency). That deletion left startup no longer
	// auto-fingerprinting — coverage only advanced when an operator ran the
	// on-demand maintenance.library-optimize sweep. Restore the original boot behavior by
	// enqueuing the parallel op once at startup. It is idempotent (skips files
	// that already have a fingerprint) and runs in the registry's own worker
	// pool, so this is fire-and-forget — no bgWG enrollment (the op's lifecycle
	// is owned by opRegistry, not the backfill WaitGroup).
	// Gated by maintenance.acoustid_backfill (default OFF as of 2026-08-11): the op's
	// load phase pulls the whole book table into memory before it starts and held
	// ~862 MB of live heap in production, implicated in three OOM kills in one night.
	// The startup enqueue is automatic and unattended, so it is the trigger that must
	// respect the flag. A direct EnqueueOp("acoustid.backfill") via the ops API stays
	// ungated — that is the deliberate opt-in path.
	go func() {
		if err := s.bgCtx.Err(); err != nil {
			return
		}
		if !config.AppConfig.Maintenance.AcoustIDBackfill {
			slog.Info("startup: acoustid.backfill skipped (maintenance.acoustid_backfill=false)",
				"reason", "load phase holds the full book table in memory; see config.go")
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
	//
	// Resolution is deliberately in resolveVGBackfiller (below) rather than
	// inline here: a test can call that, but it cannot call the body of this
	// goroutine, and a guard that cannot reach the real call site does not
	// guard it.
	s.bgWG.Go("versiongroup-backfill", func() {
		if err := s.bgCtx.Err(); err != nil {
			return
		}
		b, ok := resolveVGBackfiller(s.Ops())
		if !ok {
			// Not a silent fall-through: this is the production repair for an
			// under-reporting version-group index, and if the store is ever
			// wrapped or decorated the assertion misses and NOTHING happens.
			// That is indistinguishable from a completed run unless it says so.
			// CodeQL go/clear-text-logging (alert #1595, drifted from the
			// #1472 dismissal at this same log line's earlier location): %T
			// renders only the dynamic type name (e.g. *database.PebbleStore),
			// never a struct field value, so no credential reachable through
			// s.Ops() can appear in this log record. Re-verified 2026-08-23 at
			// this new location and dismissed via the code-scanning API.
			slog.Warn("versiongroup-backfill: store does not implement BackfillVersionGroupIndex, index will NOT be rebuilt",
				"store_type", fmt.Sprintf("%T", s.Ops()))
			return
		}
		if err := b.BackfillVersionGroupIndex(); err != nil {
			slog.Warn("versiongroup-backfill", "err", err)
		}
	})

	// Book identification index (database/ident_state.go): rebuilt from
	// scratch when its marker is absent or old -- ident: queries answer 503
	// and the review summary reports ready=false until it finishes -- and
	// otherwise reconciled in place while it keeps serving, which repairs
	// rows a build that predates the index wrote. Waits for memdb warmup so
	// its full book scan does not compete with warmup's; never blocks startup.
	// SuperviseIdentIndex retries a failed boot build with backoff, then logs
	// an ERROR naming the manual op (maintenance.ident-index-rebuild) and
	// returns.
	s.bgWG.Go("ident-index", func() {
		if err := s.bgCtx.Err(); err != nil {
			return
		}
		b, ok := database.AsCapability[identIndexBuilder](s.Ops())
		if !ok {
			return
		}
		warm := make(chan struct{})
		go func() { b.WaitForWarmup(); close(warm) }()
		select {
		case <-warm:
		case <-s.bgCtx.Done():
			return
		}
		b.SuperviseIdentIndex(s.bgCtx)
	})

	// opv2 timeline index (pebble_store_ops_v2_timeline.go): opv2:open: and
	// opv2:done: keys that let ListOperationsV2Since skip decoding every
	// operation row. Same trust model as the opchange_by_book: index (bootOps): the index
	// is UNTRUSTED at every boot and the timeline serves from the full scan
	// (correct, only slower) until this boot's reconcile has checked and
	// repaired it, because a rollback to a build that predates the index
	// writes rows with no index keys and nothing persisted could know. The
	// reconcile is read-mostly, grows with the operation count until op
	// retention (D2) lands (see the cost note in
	// pebble_store_ops_v2_timeline.go), and logs its duration.
	// Waits for memdb warmup first. Resolved through AsCapability for the
	// same reason as the version-group backfill above.
	s.bgWG.Go("opsv2-timeline-reconcile", func() {
		if err := s.bgCtx.Err(); err != nil {
			return
		}
		r, ok := resolveOpsV2TimelineReconciler(s.Ops())
		if !ok {
			// %T renders only the dynamic type name, never a field value (same
			// reasoning as the version-group warn above).
			lifecycleLog.Warn("opsv2-timeline-reconcile: store does not implement ReconcileOpsV2TimelineIndex, index will NOT be used; the operations timeline stays on the full scan: store_type=%T",
				s.Ops())
			return
		}
		warm := make(chan struct{})
		go func() { r.WaitForWarmup(); close(warm) }()
		select {
		case <-warm:
		case <-s.bgCtx.Done():
			lifecycleLog.Info("opsv2-timeline-reconcile: shutdown before memdb warmup finished; the timeline used the full scan this boot")
			return
		}
		if _, err := r.ReconcileOpsV2TimelineIndex(s.bgCtx); err != nil {
			if errors.Is(err, context.Canceled) {
				lifecycleLog.Info("opsv2-timeline-reconcile: interrupted by shutdown; the timeline used the full scan this boot: err=%s",
					logger.SanitizeLogValue(err.Error()))
				return
			}
			// Error, not Warn: the index is not in use this boot.
			lifecycleLog.Error("opsv2-timeline-reconcile: index not trusted this boot; the timeline stays on the full scan and the next boot retries: err=%s",
				logger.SanitizeLogValue(err.Error()))
		}
	})

	// Dynamic collections are evaluated for the viewer (owner decision 2026-10-25),
	// so a collection with a per-user query stores no member list. Clear the lists
	// the old creator-evaluated behaviour left on existing rows: they are the
	// creator's listening state. Idempotent, over the collection rows only (see
	// purgePerViewerCollectionMembers for why it is sequential), and independent of
	// the search index: it only removes stored data.
	s.bgWG.Go("collection-peruser-purge", func() {
		if err := s.bgCtx.Err(); err != nil {
			return
		}
		st, ok := database.AsCapability[collectionPurgeStore](s.Ops())
		if !ok {
			lifecycleLog.Warn("collection-peruser-purge: store has no collection operations; stored member lists of per-user collections are not served but were not cleared")
			return
		}
		if _, err := purgePerViewerCollectionMembers(st,
			logger.NewWithActivityLog("collection-peruser-purge", s.storeForWiring())); err != nil {
			lifecycleLog.Warn("collection-peruser-purge failed; the next boot retries: err=%s", logger.SanitizeLogValue(err.Error()))
		}
	})

	// Build the search index on first startup (or if it got wiped).
	// Tracked via bgWG so shutdown can wait for in-flight indexing
	// instead of letting it run under a closing DB.
	if s.searchIndex != nil {
		s.bgWG.Go("build-search-index", func() {
			if s.searchIndex.RecreatedForMappingChange() {
				// The index was just deleted and recreated empty because the
				// mapping version changed. An empty index is precisely what
				// buildSearchIndexIfEmpty triggers on — and that build is
				// non-resumable and bgCtx-cancellable, which is exactly how a
				// permanent 24.7% coverage gap was created on 2026-08-13.
				// Running it here would deliberately re-enter that bug at
				// full library scale.
				//
				// Skip it and let reconcileSearchIndexCoverage seed the
				// DURABLE dirty set instead: a cancellation mid-sweep then
				// costs nothing, because the marks survive the restart.
				// "The index is empty" must not be the signal to bulk-build;
				// only a genuinely first-time index should take that path.
				slog.Warn("search index was recreated for a mapping change; " +
					"skipping bulk backfill and seeding the durable dirty " +
					"set instead")
			} else {
				s.buildSearchIndexIfEmpty()
			}
			// buildSearchIndexIfEmpty only runs on an EMPTY index, so a
			// build that was cancelled by a previous shutdown leaves a
			// permanent gap it will never revisit. Seed the dirty set for
			// the shortfall so the reconciler repairs it. See
			// search_coverage.go.
			s.reconcileSearchIndexCoverage()
		})
	}

}

// bootOp is one whole-library maintenance op enqueued at every boot, with the
// params that give it the behaviour of the goroutine it replaced.
type bootOp struct {
	id     string
	params map[string]any
	// skipWhileAsk: do not enqueue while a row of this def is interrupted_ask
	// (an interrupted ResumeAsk run waiting for an operator).
	skipWhileAsk bool
	// skipWhenAllSet: do not enqueue when every one of these settings is
	// "true" (the run's done flags), since the run would do nothing.
	skipWhenAllSet []string
	// paramsFor, when set, picks the params for this boot (and a note for the
	// log line when it departs from params) instead of the static params.
	paramsFor func(checks bootOpChecks) (params map[string]any, note string)
}

// activityFilterIndexDoneCheck is bootOpChecks.filterIndexDone for this server:
// the Pebble activity store's sentinel read, or nil when there is no Pebble
// activity store (the activity op then keeps the reconcile, which no-ops).
func (s *Server) activityFilterIndexDoneCheck() func() bool {
	if b := s.ActivityFilterIndexBackfiller(); b != nil {
		return b.FilterIndexBackfillDone
	}
	return nil
}

// bootOpChecks answers the skip checks in enqueueBootOps. A nil func, or a
// lookup that fails, never skips: the op is enqueued as before (fail open).
type bootOpChecks struct {
	hasStatus   func(defID, status string) (bool, error)
	settingTrue func(key string) bool
	// filterIndexDone reports whether the activity filter indexes are complete
	// (the sentinel is set). nil when there is no Pebble activity store.
	filterIndexDone func() bool
}

// opStatusLookup is resolved through database.AsCapability, so it is not
// part of Store (database/pebble_store_ops_v2_status_lookup.go).
type opStatusLookup interface {
	HasOperationV2WithStatus(defID, status string) (bool, error)
}

// bootOps are the ops startBackfills enqueues (04-P6). Each was a bgWG
// goroutine with no progress and no cancel. The registered ops were the
// operator's FORCE variants, so each takes a param for the boot behaviour:
//
//   - external-id-backfill {"if_needed": true}: the sentinel-gated pass
//     (itunes.BackfillExternalIDsOnce); without it the op rescans in full.
//   - movement-atom-cleanup, malformed-m4b-remux: no params; the op calls the
//     same once-gated function the goroutine did.
//   - malformed-m4b-transcode {"quarantine_after": true}: the transcode, then
//     quarantineKnownBadFiles, the goroutine's two steps.
//   - opchange-book-index-rebuild {"ensure": true} (04-P7, was the
//     opchange-index-backfill goroutine): EnsureOpChangeByBookIndex, i.e.
//     backfill if needed, verify every journal row against the
//     opchange_by_book: index, rebuild on a dirty verify, and only then let
//     GetBookChanges read it. Enqueued on EVERY boot with no skip check: a
//     rollback to a build that predates the index writes rows with no entry,
//     the sentinel cannot know, so the verify is the check. Until it trusts
//     the index, GetBookChanges serves from the full journal scan. Without
//     params the op previews.
//   - book-atpath-index-backfill {"if_needed": true} (04-P6 part 2, was the
//     book-atpath-backfill goroutine): the sentinel-gated BackfillBookAtPathIndex
//     instead of the operator's forced RebuildBookAtPathIndex. Until it
//     finishes, LiveBookIDsAtPath serves from a full scan (slow, not wrong).
//     A first build is a full book scan and reports progress per chunk.
//   - activity-filter-index-backfill {"reconcile": true} (04-P6 part 2, was
//     reconcileFilterIndexAtBoot, started from the activity service's Build):
//     ReconcileFilterIndexCoverage, which checks the rows above the start mark
//     the store keeps (persisted at open, so it survives a restart before the
//     check ran) and indexes any gap a rolled-back build left. When the
//     filter-index sentinel is unset (never built, or withdrawn by a reconcile
//     that failed after it found a gap) boot enqueues the full backfill (no
//     params) instead (owner decision 2026-10-15; activityFilterIndexBootParams).
//
// Every def is PriorityLow, so these never get ahead of a user's op. A run
// with nothing to do logs with opsregistry.NoOpAttr, so the activity feed
// hides it by default.
//
// Readiness: the registry's dispatcher holds every def without NoWarmupWait
// (all seven) until the memdb warmup finishes, bounded by
// registry.WarmupWaitTimeout. The first four goroutines did not wait for it at
// all; the opchange and the book-atpath ones waited without a bound, so after
// the bound those ops now run on a store that is still warming (correct, only
// competing for reads). The activity reconcile used to run the moment the
// activity service was built, before any warmup hold; it now waits for it, so
// rows a rolled-back build wrote without filter keys stay unindexed behind a
// complete sentinel for that window, so a source/type/level filter omits them
// (an unfiltered view still lists them) until the check withdraws the sentinel
// and reindexes.
//
// Shutdown: the goroutines were in bgWG so shutdown waited for them before
// CloseStore (up to 30s). Registry.Shutdown, which Server.Shutdown calls
// before bgCancel and before any store closes, cancels every run's ctx and
// waits for the runs to return: about 12s at most, the caller's 10s drain
// (Server.Shutdown's regCtx) plus the registry's 2s goroutine escape
// (registry.go). An op still iterating Pebble after that would race
// CloseStore, so each op here runs on that ctx and checks it at a short
// interval, including the external-id backfill and the quarantine step,
// which used to watch only bgCtx or nothing. The opchange ensure checks it
// at least every 10,000 journal rows (every 5,000-row backfill or rebuild
// chunk, every 10,000 rows of the verify, each a point read per row) and
// its wait for the index-pass slot is ctx-aware, so a cancel ends it well
// inside the bound. The book_atpath build checks it per row in each worker and
// in its producer's select, and the reconcile checks it every 64 rows of the
// check and per 2,000-row reindex chunk.
//
// Restart mid-run: the five ResumeDrop ops (the atpath one included) are
// dropped and the next boot enqueues them again. The activity reconcile is
// ResumeRestart: an interrupted run resumes with the same params, and since a
// reconcile never checkpoints, it starts over from the store's start mark,
// which is durable (ActivityFilterIndexReconcileFromKey: written at open when
// rows sit above the persisted mark, deleted only by a clean check or a
// completed backfill), so a restart before the check ran, or a failed run,
// does not lose a rollback gap. The transcode is ResumeAsk: an interrupted (or
// never-started queued) row becomes interrupted_ask and waits for an
// operator. That row is not queued or running, so the enqueue dedupe does not
// see it; enqueueBootOps therefore skips the transcode while such a row exists
// (owner decision 2026-10-15), so boot never runs around the confirmation.
// It also skips the transcode when both of its done flags (remux.TranscodeKey
// and the quarantine's) are set, so a ResumeAsk row is only ever created by a
// boot that had real work to do (owner decision 2026-10-15).
var bootOps = []bootOp{
	{id: "maintenance.external-id-backfill", params: map[string]any{"if_needed": true}},
	{id: "maintenance.movement-atom-cleanup", params: map[string]any{}},
	{id: "maintenance.malformed-m4b-remux", params: map[string]any{}},
	{
		id: "maintenance.malformed-m4b-transcode", params: map[string]any{"quarantine_after": true},
		skipWhileAsk: true, skipWhenAllSet: []string{remux.TranscodeKey, quarantineKnownBadKey},
	},
	{id: "maintenance.opchange-book-index-rebuild", params: map[string]any{"ensure": true}},
	{id: "maintenance.book-atpath-index-backfill", params: map[string]any{"if_needed": true}},
	{
		id: "maintenance.activity-filter-index-backfill", params: map[string]any{"reconcile": true},
		paramsFor: activityFilterIndexBootParams,
	},
}

// bootParamsJSON renders op params as the JSON the op receives, for the log
// ("{}" rather than the Go map syntax "map[]").
func bootParamsJSON(params map[string]any) string {
	b, err := json.Marshal(params)
	if err != nil {
		return fmt.Sprintf("%v", params)
	}
	return string(b)
}

// activityFilterIndexBootParams picks the activity op's mode for this boot
// (owner decision 2026-10-15). With the indexes complete it is the reconcile,
// the rollback check. With the sentinel unset (never built, or withdrawn by a
// reconcile that failed or was cancelled after it found a gap) the reconcile
// would return at once and filters would stay on the budgeted scan until an
// operator ran the op, so boot enqueues the full backfill instead, which
// builds the indexes and sets the sentinel. An unknown answer (no Pebble
// activity store) keeps the reconcile, which no-ops in that case.
func activityFilterIndexBootParams(checks bootOpChecks) (map[string]any, string) {
	if checks.filterIndexDone != nil && !checks.filterIndexDone() {
		return map[string]any{}, "the activity filter indexes are not complete (never built, or withdrawn by a " +
			"failed reconcile), so this is the full backfill rather than the reconcile"
	}
	return map[string]any{"reconcile": true}, ""
}

// enqueueBootOps enqueues each of bootOps. A failed enqueue is logged and the
// rest still go. enqueue is Registry.EnqueueOp in production; it is a
// parameter because Server.opRegistry is a concrete *Registry with no fake.
// checks answers the skip checks (see bootOpChecks).
func enqueueBootOps(ctx context.Context, enqueue func(ctx context.Context, id string, params any) (string, error),
	checks bootOpChecks) {
	for _, op := range bootOps {
		if ctx.Err() != nil {
			return
		}
		if len(op.skipWhenAllSet) > 0 && checks.settingTrue != nil && allSet(op.skipWhenAllSet, checks.settingTrue) {
			lifecycleLog.Debug("startup: not enqueueing %s: its done flags are set, so it would do nothing", op.id)
			continue
		}
		if op.skipWhileAsk && checks.hasStatus != nil {
			waiting, err := checks.hasStatus(op.id, "interrupted_ask")
			if err != nil {
				lifecycleLog.Warn("startup: could not check for an interrupted %s waiting for confirmation; enqueueing it: err=%s",
					op.id, logger.SanitizeLogValue(err.Error()))
			} else if waiting {
				lifecycleLog.Info("startup: not enqueueing %s: an interrupted run is waiting for an operator to confirm or dismiss it", op.id)
				continue
			}
		}
		params := op.params
		if op.paramsFor != nil {
			var note string
			if params, note = op.paramsFor(checks); note != "" {
				lifecycleLog.Info("startup: enqueueing %s with params %s: %s", op.id, bootParamsJSON(params), note)
			}
		}
		opID, err := enqueue(ctx, op.id, params)
		if err != nil {
			lifecycleLog.Warn("startup: could not enqueue %s; it runs on the next boot or by hand: err=%s",
				op.id, logger.SanitizeLogValue(err.Error()))
			continue
		}
		lifecycleLog.Info("startup: enqueued %s as op %s", op.id, opID)
	}
}

func allSet(keys []string, settingTrue func(key string) bool) bool {
	for _, k := range keys {
		if !settingTrue(k) {
			return false
		}
	}
	return true
}

func (s *Server) perm(p auth.Permission) gin.HandlerFunc {
	if !config.AppConfig.EnableAuth {
		return func(c *gin.Context) { c.Next() }
	}
	return servermiddleware.RequirePermission(s.storeForWiring(), p)
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
	// OAuth2/OIDC login + Cloudflare Access passthrough (both no-ops unless
	// configured). Built FIRST, ahead of every route registration below, because
	// gin binds a route's middleware chain at registration time: a top-level
	// route can only inherit cfMW if cfMW already exists when that route is
	// registered, and a later router.Use() cannot retrofit it. Both /metrics and
	// /api/events depend on that. buildOAuthWiring is a pure constructor (it
	// reads config and builds handlers/verifiers; it registers no routes), so
	// hoisting it is safe, and it is still called exactly once.
	oauthH, cfMW, cfRecoveryMW := s.buildOAuthWiring()

	// Health check endpoint
	// Prometheus metrics endpoint (standard path). AUTHENTICATED.
	//
	// This was previously unauthenticated as an accepted risk (pen-test MED-1),
	// on the stated grounds that "Prometheus scrapers don't send auth" and that
	// the endpoint could be restricted "at the network layer (firewall /
	// separate internal port)".
	//
	// Both halves of that were wrong in practice. Prometheus DOES authenticate —
	// scrape_configs supports authorization / bearer_token / bearer_token_file /
	// basic_auth / oauth2 — so the premise that gating it breaks scraping is
	// simply false. And the network-layer restriction it deferred to was never
	// built: the origin listens on 0.0.0.0, so /metrics and /health were the only
	// two endpoints any host on the LAN could read without a credential.
	//
	// A credential is now required. Any valid credential works — an `abk_` API
	// key via Authorization: Bearer is the intended one for a scraper (see
	// deploy/prometheus/scrape-config.yml). No permission beyond authentication
	// is demanded: the payload is aggregate counters and Go runtime stats with
	// no per-book or per-user labels, so gating it further would add operator
	// friction without protecting anything more.
	metricsAuth := gin.HandlerFunc(func(c *gin.Context) { c.Next() })
	if config.AppConfig.EnableAuth {
		metricsAuth = servermiddleware.RequireAuth(s.storeForWiring())
	}
	s.router.GET("/metrics", buildTopLevelAuthChain(cfMW, metricsAuth,
		gin.WrapH(promhttp.Handler()))...)

	// Health check endpoint (both paths for compatibility). Registered here on
	// s.router BEFORE the /api/* redirect middleware (below) so they keep
	// bypassing it; they delegate to the migrated system handler, which is wired
	// later in wireHandlers (always before any request is served).
	s.router.GET("/health", func(c *gin.Context) { s.systemHandler.HealthCheck(c) })
	s.router.GET("/api/health", func(c *gin.Context) { s.systemHandler.HealthCheck(c) })
	s.router.GET("/api/v1/health", func(c *gin.Context) { s.systemHandler.HealthCheck(c) })

	// Content-Security-Policy violation reports (csp.go points the policy here).
	// Unauthenticated, rate limited and size capped (csp_report.go); registered
	// before the /api/* redirect below so it keeps its /api/v1 path.
	s.router.POST(cspReportPath, defaultCSPReporter.handle)

	// Real-time events (SSE). Same pre-middleware-ordering rationale as /health.
	// Gated behind auth (pen-test finding MED-2): the stream carries library
	// events (imports, scan progress, metadata updates) that anonymous clients
	// must not see. Anonymous clients get 401. When auth is disabled (local
	// single-user mode) this is a no-op, like the rest of the API. Built inline
	// because the shared authMiddleware is constructed later in this function
	// (and routes registered before a router.Use() don't inherit it).
	//
	// cfMW runs FIRST and fail-open, exactly as it does on /api/v1: it binds the
	// user resolved from a valid Cf-Access-Jwt-Assertion so the RequireAuth below
	// short-circuits, and otherwise passes through untouched. Without it this
	// route was the one authenticated endpoint the Cloudflare Access passthrough
	// never reached, because a browser under Access SSO has NO session cookie —
	// identity arrives only in that header — so EventSource 401'd in a reconnect
	// loop and the UI showed a permanent "Connection lost" banner while every
	// /api/v1 call succeeded.
	eventsAuth := gin.HandlerFunc(func(c *gin.Context) { c.Next() })
	if config.AppConfig.EnableAuth {
		eventsAuth = servermiddleware.RequireAuth(s.storeForWiring())
	}
	s.router.GET("/api/events", buildTopLevelAuthChain(cfMW, eventsAuth,
		func(c *gin.Context) { s.systemHandler.HandleEvents(c) })...)

	// Public temp-login consumer at the root so URLs are short and
	// browser-friendly. Validates the token, deletes it (single-use),
	// creates a 24h session, sets the cookie, redirects to the SPA.
	s.router.GET("/auth/temp-login", s.consumeTempLoginToken)

	// Redirect /api/* to /api/v1/* for v1 compatibility
	s.router.Use(func(c *gin.Context) {
		path := c.Request.URL.Path
		// If path starts with /api/ but not /api/v1/ and not /api/health and not /api/events
		//
		// absReservedPath excludes the Audiobookshelf-compatible surface: the ABS
		// protocol is UNVERSIONED, so GET /api/me is a real ABS endpoint. Without this
		// exclusion it would 301 to /api/v1/me and the client would follow the redirect
		// into the app API — the route would look implemented and behave broken.
		if strings.HasPrefix(path, "/api/") &&
			!strings.HasPrefix(path, "/api/v1/") &&
			// /api/v3/ops is the operations v3 API: its own version, not a
			// v1 path missing its prefix.
			!strings.HasPrefix(path, "/api/v3/") &&
			!strings.HasPrefix(path, "/api/health") &&
			!strings.HasPrefix(path, "/api/events") &&
			!strings.HasPrefix(path, "/api/metrics") &&
			!absReservedPath(path) &&
			// Gated on the flag, unlike every other exclusion here: these paths
			// have a live /api/v1 twin, so reserving them while ABS is OFF would
			// 404 a working app route instead of redirecting it.
			!(config.AppConfig.ABSAPIEnabled && absCollisionDetailReserved(c.Request.Method, path)) {
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
	// EnableRateLimit is the master switch and APIRateLimitPerMinute is the
	// threshold: both must hold for the real limiter to be installed, so
	// EnableRateLimit=false always yields the passthrough handler regardless
	// of what APIRateLimitPerMinute is set to.
	apiRateLimiter := gin.HandlerFunc(func(c *gin.Context) { c.Next() })
	if config.AppConfig.EnableRateLimit && config.AppConfig.APIRateLimitPerMinute > 0 {
		rpm := config.AppConfig.APIRateLimitPerMinute
		burst := max(rpm/5, 10)
		limiter := servermiddleware.NewIPRateLimiter(rpm, burst)
		// SV-04: idle-entry eviction runs on a background ticker instead of
		// the request path. bgCtx is cancelled by the s.bgCancel() call in
		// Start's shutdown sequence, which ends the sweeper goroutine.
		limiter.Start(s.bgCtx)
		apiRateLimiter = limiter.Middleware()
	}
	bodyLimitMiddleware := servermiddleware.MaxRequestBodySize(jsonLimitBytes, uploadLimitBytes)
	authMiddleware := gin.HandlerFunc(func(c *gin.Context) {
		c.Next()
	})
	if config.AppConfig.EnableAuth {
		authMiddleware = servermiddleware.RequireAuth(s.storeForWiring())
	} else {
		slog.Warn("authentication is disabled (enable_authfalse) — do not expose this server to untrusted networks")
	}
	if !config.AppConfig.EnableRateLimit && config.AppConfig.APIRateLimitPerMinute > 0 {
		slog.Warn("rate limiting is disabled (enable_rate_limit=false) despite api_rate_limit_per_minute being set — the API is vulnerable to abuse. Set enable_rate_limit=true in config.yaml for production deployments")
	}

	// oauthH and cfMW were built earlier, before the /api/events route that also
	// needs cfMW — see the comment at that call site.

	// Audiobookshelf-compatible surface. A separate TOP-LEVEL group, not a child of
	// /api/v1, with its own FAIL-CLOSED identity middleware — it must not inherit the
	// fail-open Cloudflare-Access behaviour applied to /api/v1 below, where an
	// unverifiable assertion falls through to RequireAuth. Feature-flagged off by
	// default; registers nothing unless ABS_API_ENABLED is set. See wire_abs_routes.go.
	s.wireABSRoutes()

	// API routes (auth + rate limits + request-size limits). The Cloudflare Access
	// middleware runs early and FAIL-OPEN: when a valid Cf-Access-Jwt-Assertion is
	// present it binds the resolved user so the downstream RequireAuth skips its
	// session check; otherwise it passes through. Harmless on public routes.
	api := s.router.Group("/api/v1")
	if cfMW != nil {
		api.Use(apiRateLimiter, bodyLimitMiddleware, cfMW)
	} else {
		api.Use(apiRateLimiter, bodyLimitMiddleware)
	}
	{
		protected := api.Group("")
		protected.Use(authMiddleware)

		s.wireHandlers(api, authMiddleware, protected, oauthH)
		s.wireFingerprintWorkerRoutes(api, authMiddleware)
		{
			// Audiobook routes.
			// NOTE: the main audiobooks list / CRUD domain (list, count, facets,
			// soft-delete/restore/purge, get, tags, update, delete, cover,
			// segments, files, changelog, external-ids,
			// extract-track-info, relocate, batch, batch-operations, user tags,
			// tags-detailed, batch-tags, metadata-history,
			// undo, field-states, changes) was migrated to the
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
			// /authors/:id, /narrators[/count],
			// /series[/count], /series/:id[/books,/name,/split], PATCH /series/:id,
			// /series/bulk-delete, and the work routes migrated to the
			// handlers/entities sub-package (wire_handlers.go).
			// /audiobooks/:id/narrators is a 410 stub in wire_entities_routes.go.
			// /authors/duplicates(/refresh),
			// /series/duplicates(/refresh), /series/deduplicate, /series/merge,
			// /series/prune(/preview) and /dedup/validate migrated to the
			// handlers/duplicates sub-package (wire_handlers.go).
			// /audiobooks/merge, /series/normalize(/preview) and
			// /authors/duplicates/ai-review[/apply] were removed (01-P72); the
			// ops behind them (dedup.book-merge, dedup.series-normalize,
			// ai.author-review / ai.author-merge-apply) stay registered and
			// start through the generic operations endpoints.

			// Embedding-based dedup
			//
			// NOTE: the dedup-domain routes (GET /dedup/candidates,
			// /dedup/candidates/export, /dedup/stats,
			// /dedup/candidates/series-summary; POST
			// /dedup/candidates/:id/{link,reject}, /dedup/candidates/
			// {bulk-link,link-cluster,reject-cluster,remove-from-cluster,
			// link-series}, /dedup/{scan,scan-llm,scan-acoustid,
			// purge-stale,reset-acoustid,embed} and POST /audiobooks/:id/compare-acoustid) were
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
			protected.GET("/operations/active", s.perm(auth.PermLibraryView), gone("GET /api/v1/operations/timeline"))
			protected.GET("/operations/recent", s.perm(auth.PermLibraryView), gone("GET /api/v1/operations/timeline"))

			// UOS-06 operations v2 routes (timeline, events, v2/:id, op-defs)
			// are registered in wireHandlers via OperationsV2Handler.

			protected.GET("/file-ops/pending", s.perm(auth.PermLibraryView), s.handleListPendingFileOps)
			protected.GET("/operations/:id/results", s.perm(auth.PermLibraryView), s.handleGetOperationResults)
			protected.GET("/operations/reconcile/preview", s.perm(auth.PermLibraryView), s.reconcilePreview)
			protected.POST("/operations/reconcile", s.perm(auth.PermScanTrigger), s.startReconcile)
			protected.POST("/operations/reconcile/scan", s.perm(auth.PermScanTrigger), s.startReconcileScan)
			protected.GET("/operations/reconcile/scan/latest", s.perm(auth.PermLibraryView), s.latestReconcileScan)
			protected.POST("/operations/cleanup-version-groups", s.perm(auth.PermSettingsManage), s.cleanupDuplicateVersionGroupsHandler)
			protected.POST("/operations/mark-broken-segments", s.perm(auth.PermSettingsManage), s.markBrokenSegmentBooksHandler)
			protected.POST("/operations/merge-novg-duplicates", s.perm(auth.PermSettingsManage), s.mergeNoVGDuplicatesHandler)
			protected.POST("/operations/assign-orphan-vgs", s.perm(auth.PermSettingsManage), s.assignOrphanVGsHandler)
			protected.POST("/operations/elect-missing-primaries", s.perm(auth.PermSettingsManage), s.electMissingPrimariesHandler)
			// Must be run once BEFORE deploying the per-file scan-cache reader:
			// until it has, every book_file row reads as "never scanned" and the
			// first scan re-reads the whole library. See scan_cache_backfill.go.
			protected.POST("/operations/backfill-scan-cache", s.perm(auth.PermSettingsManage), s.backfillScanCacheHandler)

			// Import routes
			// Retired (01-P81i, owner decision D9): no caller in the repo and no
			// replacement. The stub is removed after one release; auth and the
			// permission guard run first.
			protected.POST("/import/collision-preview", s.perm(auth.PermLibraryView), gone(""))

			// iTunes import routes
			itunesGroup := protected.Group("/itunes")
			{
				// iTunes is an import-only source (owner decision 2026-10-07).
				// Every route that wrote the iTunes library -- rebuild,
				// rebuild-full, export-partial, relocate, adopt-base,
				// cleanup-merged, write-back[/all/preview], writeback/status,
				// writeback/held/release, writeback/requeue[-remove],
				// library/upload, library/restore, library/backups -- was
				// removed with the write-back subsystem. removed_itunes_routes_test.go
				// pins that they stay gone.
				//
				// NOTE: the core iTunes routes (validate, test-mapping, import,
				// library-stats, books, import-status[/bulk], library-status,
				// sync) are handlers.ITunesHandler routes registered in
				// wireHandlers (wire_handlers.go). The survivors below stay here
				// because they still call *Server methods directly.
				//
				// PID-integrity: read-only census of duplicate book_file iTunes PIDs
				// (a PID must identify exactly one row).
				itunesGroup.GET("/pid-integrity", s.perm(auth.PermLibraryEditMetadata), s.pidIntegrityHandler)
				// POST /pid-repair (clear duplicate PIDs on database rows) had no caller
				// in the repo (01-P81k). It answers 410 Gone for one release (owner
				// decision D9) and is then removed. It keeps its s.perm guard, so an
				// unauthenticated caller still gets 401 and only an allowed caller
				// learns it is gone. The read-only plan preview survives as
				// `pid-census --repair`.
				itunesGroup.POST("/pid-repair", s.perm(auth.PermLibraryEditMetadata), gone(""))

				// ITL download (6.4): a read of the library file.
				itunesGroup.GET("/library/download", s.perm(auth.PermIntegrationsManage), s.itunesSvcGuard(func(c *gin.Context) { s.itunesSvc.Transfer.HandleDownload(c) }))
			}

			// Cover art
			protected.GET("/covers/proxy", s.perm(auth.PermLibraryView), s.handleCoverProxy)
			protected.GET("/covers/local/:filename", s.perm(auth.PermLibraryView), s.handleLocalCover)
			// Retired (01-P81i, owner decision D9): no caller in the repo and no
			// replacement. The stubs are removed after one release; auth and each
			// route's permission guard run first.
			protected.GET("/audiobooks/:id/cover-history", s.perm(auth.PermLibraryView), gone(""))
			protected.POST("/audiobooks/:id/cover-history/restore", s.perm(auth.PermLibraryEditMetadata), gone(""))

			// Unified task/scheduler routes and the maintenance-window routes
			// (GET /tasks, POST /tasks/:name/run, PUT /tasks/:name, POST
			// /maintenance-window/run, GET /maintenance-window/status, PUT
			// /maintenance-window/config) were migrated to operations.Handler
			// and are now registered in wireHandlers (wire_handlers.go).

			// Result-getter GETs (not job triggers — these poll async results)
			protected.GET("/maintenance/scan-composer-tags/:id", s.perm(auth.PermSettingsManage), s.handleGetComposerScanResults)
			// Retired with the repair-missing-files job (04-P14e, D27/D9): 410 behind the
			// same permission gate so only an allowed caller learns it is gone.
			protected.GET("/maintenance/repair-missing-files/:id", s.perm(auth.PermSettingsManage),
				gone("the maintenance.missing-file-repoint or maintenance.recover-missing-files operation (POST /api/v1/operations/v2)"))
			// Hash stats endpoints
			protected.GET("/maintenance/book-file-hash-stats", s.perm(auth.PermSettingsManage), s.handleGetBookFileHashStats)
			protected.GET("/maintenance/book-metadata-hash-stats", s.perm(auth.PermSettingsManage), s.handleGetBookMetadataHashStats)
			protected.GET("/maintenance/acoustid-stats", s.perm(auth.PermSettingsManage), s.handleGetAcoustIDStats)
			// Per-signal coverage (hash, fingerprint, duration, tags, embeddings).
			protected.GET("/signals/coverage", s.perm(auth.PermSettingsManage), s.handleGetSignalCoverage)
			// Unified maintenance job dispatcher
			protected.GET("/maintenance/jobs", s.perm(auth.PermSettingsManage), s.listMaintenanceJobs)
			// Route-level permission guard (pen-test finding MED-3). The handler
			// also checks permissions per-job, but only when EnableAuth is true;
			// gating at the route is defense-in-depth and protects against a
			// future job that forgets to implement PermissionAware.
			protected.POST("/maintenance/jobs/:job_id", s.perm(auth.PermSettingsManage), s.runMaintenanceJob)

			// Admin-only (retired wipe stub)
			adminOnly := protected.Group("")
			adminOnly.Use(servermiddleware.RequireAdmin())
			{
				// The library wipe is retired. It stays registered as a 410 stub,
				// behind RequireAdmin, so a stale client gets "gone" rather than a
				// 404 and a non-admin still gets 403. The stub is removed one
				// release later (01-P81 series, final follow-up).
				adminOnly.POST("/maintenance/wipe", gone(""))
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
			// Retired (01-P81i, owner decision D9): no caller in the repo. Starting
			// a preview is POST /operations/v2 with def_id
			// metadata.bulk-apply-preview (it takes book_ids, all_cached or
			// operation_id, like the old route). The two batch-apply endpoints are
			// dry runs by default too (send dry_run:false to apply). The per-book
			// report the GET read is no longer served; the finished operation's
			// message carries the summary. The stubs are removed after one
			// release; auth and each route's permission guard run first.
			protected.POST("/metadata/bulk-apply-preview", s.perm(auth.PermLibraryView), gone(bulkApplyPreviewGone))
			protected.GET("/metadata/bulk-apply-preview/:id", s.perm(auth.PermLibraryView), gone(bulkApplyPreviewReportGone))
			protected.POST("/metadata/batch-reject-candidates", s.perm(auth.PermLibraryEditMetadata), s.handleRejectCandidates)
			protected.POST("/metadata/batch-unreject-candidates", s.perm(auth.PermLibraryEditMetadata), s.handleUnrejectCandidates)
			protected.POST("/metadata/unreject-candidate", s.perm(auth.PermLibraryEditMetadata), s.handleUnrejectCandidate)
			// fetch-metadata / search-metadata / apply-metadata / mark-no-match /
			// revert-metadata / metadata-rejections / cow-versions(+prune) /
			// write-back migrated to the handlers/metadata sub-package (wireHandlers).
			// Retired (01-P81i, owner decision D9): no caller in the repo. The stub is
			// removed after one release; auth and the permission guard run first.
			protected.GET("/audiobooks/:id/similar", s.perm(auth.PermLibraryView), gone("GET /api/v1/audiobooks?search=author:... (the library search takes the same author: and series: terms)"))

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
			s.credRoute(protected, http.MethodPost, "/update/apply", s.perm(auth.PermSettingsManage), s.applyUpdate)

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

	// Operations v3 API (/api/v3/ops/*): the same middleware chain as /api/v1,
	// its own version prefix.
	apiV3 := s.router.Group("/api/v3")
	if cfMW != nil {
		apiV3.Use(apiRateLimiter, bodyLimitMiddleware, cfMW)
	} else {
		apiV3.Use(apiRateLimiter, bodyLimitMiddleware)
	}
	protectedV3 := apiV3.Group("")
	protectedV3.Use(authMiddleware)
	s.wireOpsV3Routes(protectedV3)

	// Serve static files (React frontend)
	// Implementation is in static_embed.go or static_nonembed.go depending on build tags
	// cfRecoveryMW (nil without Cloudflare Access) runs on the SPA page fallback:
	// the one place browser page navigations are served. See cf_recovery.go.
	s.setupStaticFiles(cfRecoveryMW)
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
	if s.Ops() == nil {
		return nil, fmt.Errorf("database not initialized")
	}

	ops, err := s.Ops().GetRecentOperations(500)
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

func GetDefaultServerConfig() ServerConfig {
	return ServerConfig{
		Port:         "8484",
		Host:         "localhost",
		ReadTimeout:  15 * time.Second,  // Allow slow clients without stalling forever
		WriteTimeout: 0,                 // Disable write timeout so SSE streams stay open
		IdleTimeout:  120 * time.Second, // 2 minute idle timeout
	}
}

// vgBackfiller is the version-group index repair, which lives on *PebbleStore
// and deliberately outside database.Store.
type vgBackfiller interface{ BackfillVersionGroupIndex() error }

// resolveVGBackfiller finds the version-group backfill on the running store.
//
// It uses database.AsCapability, NOT a bare type assertion. BackfillVersionGroupIndex
// is a *PebbleStore method outside database.Store, so it is invisible through any
// decorator that embeds the Store INTERFACE; AsCapability walks the Unwrap chain
// and finds it on the inner store. The server's own store is the raw store since
// 07-S3 (the indexedStore decorator was removed), but a decorator can sit on the
// chain again (tests and other layers install them), and the lookup must not
// depend on which.
//
// MEASURED IN PRODUCTION 2026-08-10 23:07:40, when the server's store was the
// since-removed indexedStore decorator, hours after the caller's warning
// shipped and minutes after it was first deployed:
//
//	versiongroup-backfill: store does not implement BackfillVersionGroupIndex,
//	index will NOT be rebuilt   store_type=*server.indexedStore
//
// The bare assertion had been missing on every boot since that decorator was
// installed, so this "one-time production repair" had NEVER ONCE RUN, silently.
// That is the likely origin of the under-reporting version-group index
// reproduced in #2277: the index was never built, rather than built and then
// corrupted.
//
// This is the THIRD capability lost to that decorator (after the
// sync-identity/bookmark set on 2026-07-30 and the maintenance wipe fixups), so
// it is a function rather than an inline assertion purely so
// TestStoreExposesVersionGroupBackfill can execute the real resolution
// path. A guard that cannot reach the production call site does not guard it.
func resolveVGBackfiller(s any) (vgBackfiller, bool) {
	return database.AsCapability[vgBackfiller](s)
}

// identIndexBuilder is the identification index's supervisor (boot build
// plus retries) and the warmup gate it waits on (*PebbleStore methods outside
// database.Store).
type identIndexBuilder interface {
	WaitForWarmup()
	SuperviseIdentIndex(ctx context.Context)
}

// opsV2TimelineReconciler is the opv2 timeline index boot reconcile plus the
// warmup gate it waits on. Both are *PebbleStore methods outside
// database.Store.
type opsV2TimelineReconciler interface {
	WaitForWarmup()
	ReconcileOpsV2TimelineIndex(ctx context.Context) (database.OpsV2TimelineReconcileResult, error)
}

// resolveOpsV2TimelineReconciler finds the timeline reconcile through the
// store's decorator chain (see resolveVGBackfiller for why a bare assertion
// misses). A named function so a test can exercise the production
// resolution path.
func resolveOpsV2TimelineReconciler(s any) (opsV2TimelineReconciler, bool) {
	return database.AsCapability[opsV2TimelineReconciler](s)
}

// expiredSessionPruner is the narrow slice of the ops store that the periodic
// session cleanup actually needs. Declaring it here rather than taking the full
// ServerOpsStore keeps the cleanup body reachable from a test with a two-method
// fake instead of a whole store implementation.
type expiredSessionPruner interface {
	DeleteExpiredSessions(now time.Time) (int, error)
	DeleteExpiredABSSessions(now time.Time) (int, error)
}

// absExpiredSessionRetention is how long an EXPIRED (not revoked) ABS session row
// is kept before the sweep deletes it. While the row exists, a client that comes
// back with its refresh token gets the same 401 but the audit log says
// session-not-live (with the session id) instead of refresh-token-unknown, which is
// what tells a real expiry apart from a forged or foreign token. The row cannot
// authenticate: every path checks Live() and ExtendABSSession refuses expired rows.
// Revoked rows are still deleted on the next tick.
const absExpiredSessionRetention = 7 * 24 * time.Hour

// pruneExpiredSessions deletes expired/revoked auth sessions and their ABS
// counterparts, logging each outcome. A failure in either delete is logged and
// does not prevent the other from running: the two stores are independent, and
// skipping the ABS sweep because the auth sweep failed would let ABS sessions
// accumulate indefinitely.
//
// Extracted from the session-cleanup ticker so the behaviour is testable without
// driving a 10-minute ticker.
func pruneExpiredSessions(ops expiredSessionPruner, sessionLog *logger.StandardLogger, now time.Time) {
	if deleted, err := ops.DeleteExpiredSessions(now); err != nil {
		sessionLog.Warn("failed to clean up expired sessions: %v", err)
	} else if deleted > 0 {
		sessionLog.Info("cleaned up %d expired/revoked sessions", deleted)
	}
	if deletedABS, err := ops.DeleteExpiredABSSessions(now.Add(-absExpiredSessionRetention)); err != nil {
		sessionLog.Warn("failed to clean up expired ABS sessions: %v", err)
	} else if deletedABS > 0 {
		sessionLog.Info("cleaned up %d expired/revoked ABS sessions", deletedABS)
	}
}

// newHTTPSRedirectHandler answers plain-HTTP requests with a 301 to HTTPS.
//
// The debug log line goes through RedactLogPath (credential query values and
// invite-token path segments): the redirected URL keeps its query string, and
// ABS clients put their bearer credential in ?token=, so logging r.URL.String()
// verbatim wrote it to the journal. RedactLogPath only recognises a request
// path, so the target is redacted as base + redacted request URI, never as a
// whole "https://host/..." URL.
func newHTTPSRedirectHandler(cfgHost, httpsPort string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		base := "https://" + r.Host
		if httpsPort != "443" {
			base = fmt.Sprintf("https://%s:%s", cfgHost, httpsPort)
		}
		target := base + r.URL.RequestURI()
		loggedURI := servermiddleware.RedactLogPath(r.URL.RequestURI())

		slog.Debug("HTTP->HTTPS redirect",
			"url", logger.SanitizeLogValue(loggedURI),
			"target", logger.SanitizeLogValue(base+loggedURI))
		http.Redirect(w, r, target, http.StatusMovedPermanently)
	}
}

// Pending user-state repair ticker cadence (internal/merge/pending_repair.go).
// A record younger than mergeUserStateRepairMinAge is left alone: its merge
// may still be running and will delete it itself.
const (
	mergeUserStateRepairInterval = 15 * time.Minute
	mergeUserStateRepairMinAge   = 5 * time.Minute
)

// startMergeUserStateRepair runs merge.PendingRepairLoop on bgWG, gated on
// bgCtx, so Shutdown stops it before Pebble closes. It is deliberately not an
// operation: a scheduled op runs with {} and {} must be a preview, while this
// must write. It completes only the explicit loser -> survivor records merges
// left, each under the merge lock.
func (s *Server) startMergeUserStateRepair() {
	db := s.storeForWiring()
	if db == nil {
		return
	}
	s.bgWG.Go("merge-user-state-repair", func() {
		merge.PendingRepairLoop(s.bgCtx, db, mergeUserStateRepairInterval, mergeUserStateRepairMinAge,
			func(r merge.PendingSweepResult) { metrics.SetMergeUserStatePending(r.Remaining) })
	})
}

// stopASINBackfillQueue stops the metafetch ASIN backfill queue's debounce
// timer. Idempotent and nil-safe; the metafetch plugin's Stop (run later by
// Container.Stop) calls the same Stop as a backstop.
func (s *Server) stopASINBackfillQueue() {
	if s.metadataFetchService == nil {
		return
	}
	s.metadataFetchService.ASINBackfillQueue().Stop()
}

// schedulerDeps builds the scheduler's dependency hooks. It is its own method
// so a test can assert the wiring (a dropped CollectionsRefreshPending line
// would leave a nil hook, and nil means always enqueue).
func (s *Server) schedulerDeps() scheduler.SchedulerDeps {
	return scheduler.SchedulerDeps{
		// Adapter, not a plain method value: function types must be identical
		// for assignment, so a func() database.Store cannot be assigned to a
		// func() scheduler.SchedulerStore even though the interface satisfies
		// it. A nil database.Store converts to a nil SchedulerStore, so the
		// documented "may return nil before the DB is up" contract holds.
		Store: func() scheduler.SchedulerStore {
			st := s.storeForWiring()
			if st == nil {
				return nil
			}
			return st
		},
		OpRegistry: s.opRegistry,
		// Same condition as the dedup plugin's Build (dedup/register.go).
		HasDedupPlugin: func() bool {
			return s.dedupEngine != nil && s.embeddingStore != nil
		},
		HasMetadataFetchSvc: func() bool {
			return s.metadataFetchService != nil
		},
		HasActivitySvc: func() bool {
			return s.activityService != nil
		},
		HasBatchPoller: func() bool {
			return s.batchPoller != nil
		},
		CollectionsRefreshPending: s.collectionsRefreshPending,
		PollBatches: func(ctx context.Context) (int, error) {
			if s.batchPoller == nil {
				return 0, nil
			}
			return s.batchPoller.Poll(ctx)
		},
	}
}
