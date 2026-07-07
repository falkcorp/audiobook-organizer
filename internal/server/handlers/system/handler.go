// file: internal/server/handlers/system/handler.go
// version: 1.8.0
// guid: 8475f406-df31-4286-95b0-30787397603e
// last-edited: 2026-07-07

// Package system hosts the system-level HTTP handlers extracted from the server
// package: health, status, announcements, storage, logs, activity-log,
// reset/factory-reset, config get/update, the SSE event stream, backup CRUD,
// dashboard, blocked-hash CRUD, user-preference CRUD, policy-tags, and
// quick-queries.
//
// Dependencies that lived on the *Server receiver are reached through narrow
// interfaces (SystemStore, SystemService, ConfigUpdateService,
// PluginHealthChecker, EventStreamer, OperationLogsProvider) plus the concrete
// *metafetch.OpenLibraryService (factoryReset reaches its .Mu / .OLStore fields,
// which an interface cannot abstract) and three injected funcs (getDiskStats,
// resetLibrarySizeCache, appVersion, filterReviewedAuthorGroups) that wrap
// server-package helpers / build-tagged functions / mutable package vars that
// stay in package server. As a result package system never imports package
// server.

package system

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/falkcorp/audiobook-organizer/internal/backup"
	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/dedup"
	"github.com/falkcorp/audiobook-organizer/internal/httputil"
	"github.com/falkcorp/audiobook-organizer/internal/metafetch"
	"github.com/falkcorp/audiobook-organizer/internal/policy"
	"github.com/falkcorp/audiobook-organizer/internal/security/pathvalidation"
)

// Handler hosts the system-domain HTTP endpoints.
type Handler struct {
	// getStore resolves the database store lazily, at request time. The original
	// handlers read s.Store() at call time (late binding), and a router
	// integration test (TestBlockedHashes_CRUD) swaps server.store AFTER wiring to
	// inject a mock — so snapshotting the store at wire time would capture the
	// pre-swap store and miss the mock's expectations. The provider closure
	// performs the typed-nil guard. Mirrors the operations handler's getScheduler
	// and this handler's getHub seams.
	getStore     func() SystemStore
	systemSvc    SystemService
	configUpdate ConfigUpdateService
	plugins      PluginHealthChecker
	opLogs       OperationLogsProvider

	// getHub resolves the SSE event hub lazily, at request time. The original
	// handleEvents read s.hub at call time (late binding), and a test
	// (TestHandleEventsUnavailable) nils s.hub AFTER wiring to exercise the 503
	// guard — so snapshotting the hub at wire time would capture a live hub and
	// invoke HandleSSE instead of returning 503. The provider closure performs
	// the typed-nil guard so a nil *realtime.EventHub is never boxed into a
	// non-nil interface. Mirrors the operations handler's getScheduler seam.
	getHub func() EventStreamer

	// olService is taken as a concrete pointer (not an interface) because
	// factoryReset reaches its .Mu (mutex) and .OLStore fields directly; field
	// access cannot go through an interface. A nil concrete pointer is fine — the
	// handler nil-checks h.olService exactly as the original nil-checked
	// s.olService.
	olService *metafetch.OpenLibraryService

	// getDiskStats wraps the build-tagged server-package getDiskStats
	// (diskstats_unix.go / diskstats_windows.go), which stays in package server.
	getDiskStats func(path string) (total, free uint64, err error)

	// resetLibrarySizeCache wraps the server-package helper of the same name,
	// which is also called from library_size_refresh_op.go and so stays in
	// package server.
	resetLibrarySizeCache func()

	// appVersion returns the runtime app version (the mutable server-package
	// `appVersion` var, set at startup). A getter func preserves read-at-call-time
	// semantics rather than snapshotting at wire time.
	appVersion func() string

	// filterReviewedAuthorGroups wraps the server-private
	// *Server.filterReviewedAuthorGroups (in duplicates_handlers.go, also used by
	// the duplicates domain), which stays in package server. The controller
	// passes s.filterReviewedAuthorGroups.
	filterReviewedAuthorGroups func([]dedup.AuthorDedupGroup) []dedup.AuthorDedupGroup
}

// New constructs a system Handler from its dependencies.
func New(
	getStore func() SystemStore,
	systemSvc SystemService,
	configUpdate ConfigUpdateService,
	plugins PluginHealthChecker,
	getHub func() EventStreamer,
	opLogs OperationLogsProvider,
	olService *metafetch.OpenLibraryService,
	getDiskStats func(path string) (total, free uint64, err error),
	resetLibrarySizeCache func(),
	appVersion func() string,
	filterReviewedAuthorGroups func([]dedup.AuthorDedupGroup) []dedup.AuthorDedupGroup,
) *Handler {
	return &Handler{
		getStore:                   getStore,
		systemSvc:                  systemSvc,
		configUpdate:               configUpdate,
		plugins:                    plugins,
		getHub:                     getHub,
		opLogs:                     opLogs,
		olService:                  olService,
		getDiskStats:               getDiskStats,
		resetLibrarySizeCache:      resetLibrarySizeCache,
		appVersion:                 appVersion,
		filterReviewedAuthorGroups: filterReviewedAuthorGroups,
	}
}

// resolveStore returns the live store via the lazy provider, or nil if no
// provider was supplied or the provider yields nil. Returned as the narrow
// SystemStore; callers keep the result in a local for the request.
func (h *Handler) resolveStore() SystemStore {
	if h.getStore == nil {
		return nil
	}
	return h.getStore()
}

// resolveHub returns the live SSE hub via the lazy provider, or nil if no
// provider was supplied (e.g. some unit tests) or the provider yields nil.
func (h *Handler) resolveHub() EventStreamer {
	if h.getHub == nil {
		return nil
	}
	return h.getHub()
}

// HealthCheck implements GET /health (and /api/health, /api/v1/health).
func (h *Handler) HealthCheck(c *gin.Context) {
	// Gather basic metrics; tolerate errors (don't fail health entirely)
	store := h.resolveStore()
	var bookCount, authorCount, seriesCount, playlistCount int
	var dbErr error
	var brokenFileCount int
	if store != nil {
		if bc, err := store.CountPrimaryBooks(); err == nil {
			bookCount = bc
		} else {
			dbErr = err
		}
		// Use Count* instead of GetAll* + len() to avoid materializing
		// every Author/Series struct on every health probe. On cold-start
		// (memdb not yet published) the GetAll* path falls back to a full
		// Pebble prefix scan + JSON unmarshal across the corpus, which is
		// the same bug class as PR #1149 — wasteful for a probe endpoint.
		// See docs/perf-audit-2026-05-29-getall-callers.md (Win 1).
		if ac, err := store.CountAuthors(); err == nil {
			authorCount = ac
		} else if dbErr == nil {
			dbErr = err
		}
		if sc, err := store.CountSeries(); err == nil {
			seriesCount = sc
		} else if dbErr == nil {
			dbErr = err
		}
		// Playlist count intentionally omitted — no reliable counting method yet

		// Try to read broken file count from underlying store (PebbleStore)
		if gf, ok := store.(interface{ GetBrokenFileCount() (int, error) }); ok {
			if cnt, err := gf.GetBrokenFileCount(); err == nil {
				brokenFileCount = cnt
			}
		} else if uw, ok := store.(interface{ Unwrap() database.Store }); ok {
			if inner, ok2 := uw.Unwrap().(interface{ GetBrokenFileCount() (int, error) }); ok2 {
				if cnt, err := inner.GetBrokenFileCount(); err == nil {
					brokenFileCount = cnt
				}
			}
		}
	}
	version := "dev"
	if h.appVersion != nil {
		version = h.appVersion()
	}
	resp := gin.H{
		"status":        "ok",
		"timestamp":     time.Now().Unix(),
		"version":       version,
		"database_type": config.AppConfig.DatabaseType,
		"metrics": gin.H{
			"books":             bookCount,
			"authors":           authorCount,
			"series":            seriesCount,
			"playlists":         playlistCount,
			"broken_file_count": brokenFileCount,
		},
		"broken_file_count": brokenFileCount,
	}
	if dbErr != nil {
		resp["partial_error"] = dbErr.Error()
	}
	httputil.RespondWithOK(c, resp)
}

// GetSystemStatus implements GET /system/status.
func (h *Handler) GetSystemStatus(c *gin.Context) {
	status, err := h.systemSvc.CollectSystemStatus()
	if err != nil {
		httputil.InternalError(c, "failed to get system status", err)
		return
	}

	// Attach plugin health information
	if h.plugins != nil {
		pluginHealth := make(map[string]string)
		for id, err := range h.plugins.HealthCheckAll() {
			if err != nil {
				pluginHealth[id] = err.Error()
			} else {
				pluginHealth[id] = "ok"
			}
		}
		status.PluginHealth = pluginHealth
	}

	// BrokenFileCount is now populated by CollectSystemStatus via LibraryStats.BrokenFiles
	// (cached in the stats:library PebbleDB entry). No per-request scan needed.

	httputil.RespondWithOK(c, status)
}

// GetSystemAnnouncements implements GET /system/announcements.
func (h *Handler) GetSystemAnnouncements(c *gin.Context) {
	type Announcement struct {
		ID       string `json:"id"`
		Severity string `json:"severity"` // info, warning, error
		Message  string `json:"message"`
		Link     string `json:"link,omitempty"`
	}

	var announcements []Announcement
	store := h.resolveStore()

	// Check for duplicate authors
	authors, err := store.GetAllAuthors()
	if err == nil {
		bookCountFn := func(authorID int) int {
			books, err := store.GetBooksByAuthorIDWithRoleCore(authorID)
			if err != nil {
				return 0
			}
			return len(books)
		}
		groups := h.filterReviewedAuthorGroups(dedup.FindDuplicateAuthors(authors, 0.9, bookCountFn))
		if len(groups) > 0 {
			announcements = append(announcements, Announcement{
				ID:       "duplicate-authors",
				Severity: "warning",
				Message:  fmt.Sprintf("You have %d group(s) of duplicate authors to review", len(groups)),
				Link:     "/dedup?tab=authors",
			})
		}
	}

	// Check for missing files (sample first 100 books)
	books, err := store.GetAllBooksCore(100, 0)
	if err == nil {
		missingCount := 0
		for _, book := range books {
			if book.FilePath != "" {
				if _, statErr := os.Stat(book.FilePath); os.IsNotExist(statErr) {
					missingCount++
				}
			}
		}
		if missingCount > 0 {
			announcements = append(announcements, Announcement{
				ID:       "missing-files",
				Severity: "warning",
				Message:  fmt.Sprintf("%d book(s) have missing files on disk", missingCount),
				Link:     "/library",
			})
		}
	}

	httputil.RespondWithOK(c, gin.H{"announcements": announcements})
}

// GetSystemStorage implements GET /system/storage.
func (h *Handler) GetSystemStorage(c *gin.Context) {
	rootDir := strings.TrimSpace(config.AppConfig.RootDir)
	if rootDir == "" {
		httputil.RespondWithBadRequest(c, "root_dir is not configured")
		return
	}

	totalBytes, freeBytes, err := h.getDiskStats(rootDir)
	if err != nil {
		httputil.RespondWithInternalError(c, "failed to read filesystem stats")
		return
	}

	usedBytes := totalBytes - freeBytes
	percentUsed := 0.0
	if totalBytes > 0 {
		percentUsed = (float64(usedBytes) / float64(totalBytes)) * 100.0
	}

	httputil.RespondWithOK(c, gin.H{
		"path":                rootDir,
		"total_bytes":         totalBytes,
		"used_bytes":          usedBytes,
		"free_bytes":          freeBytes,
		"percent_used":        percentUsed,
		"quota_enabled":       config.AppConfig.EnableDiskQuota,
		"quota_percent":       config.AppConfig.DiskQuotaPercent,
		"user_quotas_enabled": config.AppConfig.EnableUserQuotas,
	})
}

// GetSystemLogs implements GET /system/logs.
func (h *Handler) GetSystemLogs(c *gin.Context) {
	// For operation-specific logs, redirect to the operations handler.
	if id := c.Query("operation_id"); id != "" {
		h.opLogs.GetOperationLogs(c)
		return
	}

	level := c.Query("level")
	params := httputil.ParsePaginationParams(c)

	logs, total, err := h.systemSvc.CollectSystemLogs(level, params.Search, params.Limit, params.Offset)
	if err != nil {
		httputil.InternalError(c, "failed to get system logs", err)
		return
	}

	httputil.RespondWithOK(c, gin.H{
		"logs":   logs,
		"limit":  params.Limit,
		"offset": params.Offset,
		"total":  total,
	})
}

// GetSystemActivityLog implements GET /system/activity-log.
func (h *Handler) GetSystemActivityLog(c *gin.Context) {
	source := c.Query("source")
	limit := 50
	if l, err := strconv.Atoi(c.Query("limit")); err == nil && l > 0 {
		limit = l
	}
	logs, err := h.resolveStore().GetSystemActivityLogs(source, limit)
	if err != nil {
		httputil.InternalError(c, "failed to get activity log", err)
		return
	}
	httputil.RespondWithOK(c, gin.H{"items": logs, "count": len(logs)})
}

// ResetSystem implements POST /system/reset.
func (h *Handler) ResetSystem(c *gin.Context) {
	store := h.resolveStore()
	// Reset database
	if err := store.Reset(); err != nil {
		httputil.InternalError(c, "failed to reset database", err)
		return
	}

	// Reset config to defaults
	config.ResetToDefaults()

	// Reset caches
	h.resetLibrarySizeCache()
	store.InvalidateLibraryStats()

	httputil.RespondWithOK(c, gin.H{"message": "System reset successfully"})
}

// FactoryReset implements POST /system/factory-reset.
func (h *Handler) FactoryReset(c *gin.Context) {
	var req struct {
		Confirm string `json:"confirm"`
	}
	if err := c.ShouldBindJSON(&req); err != nil || req.Confirm != "RESET" {
		httputil.RespondWithBadRequest(c, "request body must contain {\"confirm\": \"RESET\"}")
		return
	}

	slog.Info("Factory reset initiated")
	store := h.resolveStore()

	// Reset database (books, authors, series, settings)
	if err := store.Reset(); err != nil {
		slog.Error("Factory reset database reset failed", "err", err)
		httputil.InternalError(c, "failed to reset database", err)
		return
	}
	slog.Info("Factory reset database cleared")

	// Delete OL data (pebble store + dump files)
	if h.olService != nil {
		h.olService.Mu.Lock()
		if h.olService.OLStore != nil {
			h.olService.OLStore.Close()
			h.olService.OLStore = nil
		}
		h.olService.Mu.Unlock()

		targetDir := metafetch.GetOLDumpDir()
		if targetDir != "" {
			if err := os.RemoveAll(targetDir); err != nil {
				slog.Warn("Factory reset failed to remove OL data dir", "err", err)
			} else {
				slog.Info("Factory reset OL data deleted")
			}
		}
	}

	// Clear library folder contents (organized audiobooks)
	if config.AppConfig.RootDir != "" {
		libraryDir := config.AppConfig.RootDir
		if pathvalidation.IsDangerousRoot(libraryDir) {
			slog.Error("Factory reset refused: RootDir is a protected system directory", "rootDir", libraryDir)
			httputil.RespondWithBadRequest(c, "factory reset refused: library RootDir is a protected system directory")
			return
		}
		entries, err := os.ReadDir(libraryDir)
		if err == nil {
			for _, entry := range entries {
				entryPath := filepath.Join(libraryDir, entry.Name())
				if err := os.RemoveAll(entryPath); err != nil {
					slog.Warn("Factory reset failed to remove", "entryPath", entryPath, "err", err)
				}
			}
			slog.Info("Factory reset library folder cleared ()", "libraryDir", libraryDir)
		}
	}

	// Reset config to defaults, then clear paths so wizard re-shows
	config.ResetToDefaults()
	config.AppConfig.RootDir = ""
	config.AppConfig.SetupComplete = false
	if err := config.SaveConfigToDatabase(store); err != nil {
		slog.Warn("Factory reset failed to persist config", "err", err)
	}

	// Reset caches
	h.resetLibrarySizeCache()
	store.InvalidateLibraryStats()

	slog.Info("Factory reset complete")
	httputil.RespondWithOK(c, gin.H{"message": "factory reset complete"})
}

// GetConfig implements GET /config.
func (h *Handler) GetConfig(c *gin.Context) {
	httputil.RespondWithOK(c, gin.H{"config": h.configUpdate.MaskSecrets(config.Snapshot())})
}

// UpdateConfig implements PUT /config.
func (h *Handler) UpdateConfig(c *gin.Context) {
	var payload map[string]any
	if err := c.ShouldBindJSON(&payload); err != nil {
		httputil.RespondWithBadRequest(c, err.Error())
		return
	}

	// WHY Snapshot/Mutate: saving the previous config and rolling it back on
	// error are writes to the global AppConfig; use the accessors so concurrent
	// HTTP requests or background goroutines see a consistent value.
	previousConfig := config.Snapshot()
	status, resp := h.configUpdate.UpdateConfig(payload)
	if status >= 400 {
		// Roll back to previous config under the write lock.
		config.Mutate(func(cfg *config.Config) { *cfg = previousConfig })
		errMsg, _ := resp["error"].(string)
		httputil.RespondWithError(c, status, errMsg, "CONFIG_ERROR")
		return
	}

	if snapForValidate := config.Snapshot(); snapForValidate.Validate() != nil {
		validateErr := snapForValidate.Validate()
		// Roll back to previous config under the write lock.
		config.Mutate(func(cfg *config.Config) { *cfg = previousConfig })
		httputil.RespondWithBadRequest(c, validateErr.Error())
		return
	}

	maskedConfig := h.configUpdate.MaskSecrets(config.Snapshot())
	response := gin.H{"config": maskedConfig}
	if raw, err := json.Marshal(maskedConfig); err == nil {
		var flat map[string]any
		if err := json.Unmarshal(raw, &flat); err == nil {
			for k, v := range flat {
				response[k] = v
			}
		}
	}
	httputil.RespondWithOK(c, response)
}

// HandleEvents handles Server-Sent Events (SSE) for real-time updates.
// Implements GET /api/events.
func (h *Handler) HandleEvents(c *gin.Context) {
	hub := h.resolveHub()
	if hub == nil {
		httputil.RespondWithError(c, 503, "event hub not initialized", "SERVICE_UNAVAILABLE")
		return
	}
	hub.HandleSSE(c)
}

// CreateBackup creates a database backup. Implements POST /backup/create.
func (h *Handler) CreateBackup(c *gin.Context) {
	var req struct {
		MaxBackups *int `json:"max_backups"`
	}
	_ = c.ShouldBindJSON(&req)

	backupConfig := backup.DefaultBackupConfig()
	if req.MaxBackups != nil {
		backupConfig.MaxBackups = *req.MaxBackups
	}

	// Get database path and type from app config
	dbPath := config.AppConfig.DatabasePath
	dbType := config.AppConfig.DatabaseType

	// Resolve backup dir relative to database directory so it's always absolute
	if dbPath != "" && !filepath.IsAbs(backupConfig.BackupDir) {
		backupConfig.BackupDir = filepath.Join(filepath.Dir(dbPath), backupConfig.BackupDir)
	}

	var info *backup.BackupInfo
	var err error
	// Only use the Checkpointable (Pebble-native) path when the configured DB
	// type is pebble. For sqlite (or any other type), fall through to the
	// file-copy path so the source-path existence check runs and returns an
	// appropriate error when the DB file is missing.
	if store := h.resolveStore(); store != nil && dbType == "pebble" {
		if cp, ok := store.(backup.Checkpointable); ok {
			info, err = backup.CreateBackupWithCheckpoint(cp, dbPath, dbType, backupConfig)
		} else {
			info, err = backup.CreateBackup(dbPath, dbType, backupConfig)
		}
	} else {
		info, err = backup.CreateBackup(dbPath, dbType, backupConfig)
	}
	if err != nil {
		httputil.InternalError(c, "failed to create backup", err)
		return
	}

	httputil.RespondWithOK(c, info)
}

// ListBackups lists all available backups. Implements GET /backup/list.
func (h *Handler) ListBackups(c *gin.Context) {
	backupConfig := backup.DefaultBackupConfig()
	if dbPath := config.AppConfig.DatabasePath; dbPath != "" && !filepath.IsAbs(backupConfig.BackupDir) {
		backupConfig.BackupDir = filepath.Join(filepath.Dir(dbPath), backupConfig.BackupDir)
	}

	backups, err := backup.ListBackups(backupConfig.BackupDir)
	if err != nil {
		httputil.InternalError(c, "failed to list backups", err)
		return
	}

	// Ensure we never return null - always return empty array
	if backups == nil {
		backups = []backup.BackupInfo{}
	}

	httputil.RespondWithOK(c, gin.H{
		"backups": backups,
		"count":   len(backups),
	})
}

// RestoreBackup restores from a backup file. Implements POST /backup/restore.
func (h *Handler) RestoreBackup(c *gin.Context) {
	var req struct {
		BackupFilename string `json:"backup_filename" binding:"required"`
		TargetPath     string `json:"target_path"`
		Verify         bool   `json:"verify"`
	}

	if err := c.ShouldBindJSON(&req); err != nil {
		httputil.RespondWithBadRequest(c, err.Error())
		return
	}

	backupConfig := backup.DefaultBackupConfig()
	if dbPath := config.AppConfig.DatabasePath; dbPath != "" && !filepath.IsAbs(backupConfig.BackupDir) {
		backupConfig.BackupDir = filepath.Join(filepath.Dir(dbPath), backupConfig.BackupDir)
	}
	safeFilename := pathvalidation.SanitizeFilename(req.BackupFilename)
	backupPath := filepath.Join(backupConfig.BackupDir, safeFilename)

	// Use current database path as target if not specified
	var targetPath string
	if req.TargetPath != "" {
		cleanTarget, err := pathvalidation.CleanAbsolutePath(req.TargetPath)
		if err != nil {
			httputil.RespondWithBadRequest(c, "invalid target_path: "+err.Error())
			return
		}
		if pathvalidation.IsDangerousRoot(cleanTarget) {
			httputil.RespondWithBadRequest(c, "restore target is a protected system directory")
			return
		}
		targetPath = cleanTarget
	} else {
		targetPath = filepath.Dir(config.AppConfig.DatabasePath)
	}

	if req.Verify {
		slog.Warn("backup restore: checksum verification requested but not yet implemented; proceeding without verification")
	}

	if err := backup.RestoreBackup(backupPath, targetPath, req.Verify); err != nil {
		httputil.InternalError(c, "failed to restore backup", err)
		return
	}

	httputil.RespondWithOK(c, gin.H{
		"message": "backup restored successfully",
		"target":  targetPath,
	})
}

// DeleteBackup deletes a backup file. Implements DELETE /backup/:filename.
func (h *Handler) DeleteBackup(c *gin.Context) {
	filename := c.Param("filename")
	if filename == "" {
		httputil.RespondWithBadRequest(c, "filename required")
		return
	}

	backupConfig := backup.DefaultBackupConfig()
	if dbPath := config.AppConfig.DatabasePath; dbPath != "" && !filepath.IsAbs(backupConfig.BackupDir) {
		backupConfig.BackupDir = filepath.Join(filepath.Dir(dbPath), backupConfig.BackupDir)
	}
	filename = pathvalidation.SanitizeFilename(filename)
	backupPath := filepath.Join(backupConfig.BackupDir, filename)

	if err := backup.DeleteBackup(backupPath); err != nil {
		httputil.InternalError(c, "failed to delete backup", err)
		return
	}

	httputil.RespondWithOK(c, gin.H{"message": "backup deleted successfully"})
}

// GetDashboard returns dashboard statistics. The store handles caching
// internally (PebbleDB: stats:library key with 10-min TTL; SQLite: SQL
// aggregation directly). Implements GET /dashboard.
func (h *Handler) GetDashboard(c *gin.Context) {
	store := h.resolveStore()
	if store == nil {
		httputil.RespondWithInternalError(c, "database not initialized")
		return
	}

	stats, err := store.GetDashboardStats()
	if err != nil {
		httputil.RespondWithInternalError(c, "failed to retrieve dashboard stats")
		return
	}

	recentOps, err := store.GetRecentOperations(5)
	if err != nil {
		recentOps = []database.Operation{}
	}

	// Try to read broken file count from underlying store (PebbleStore)
	brokenFileCount := 0
	if store != nil {
		if gf, ok := store.(interface{ GetBrokenFileCount() (int, error) }); ok {
			if cnt, err := gf.GetBrokenFileCount(); err == nil {
				brokenFileCount = cnt
			}
		} else if uw, ok := store.(interface{ Unwrap() database.Store }); ok {
			if inner, ok2 := uw.Unwrap().(interface{ GetBrokenFileCount() (int, error) }); ok2 {
				if cnt, err := inner.GetBrokenFileCount(); err == nil {
					brokenFileCount = cnt
				}
			}
		}
	}

	httputil.RespondWithOK(c, gin.H{
		"formatDistribution": stats.FormatDistribution,
		"stateDistribution":  stats.StateDistribution,
		"recentOperations":   recentOps,
		"totalSize":          stats.TotalSize,
		"totalBooks":         stats.TotalBooks,
		"totalDuration":      stats.TotalDuration,
		"organizedBooks":     stats.OrganizedBooks,
		"unorganizedBooks":   stats.UnorganizedBooks,
		"broken_file_count":  brokenFileCount,

		"fingerprintedBooks":          stats.FingerprintedBooks,
		"partiallyFingerprintedBooks": stats.PartiallyFingerprintedBooks,
		"unfingerprintedBooks":        stats.UnfingerprintedBooks,
		"fingerprintCoveragePercent":  stats.FingerprintCoveragePercent,
	})
}

// ListBlockedHashes returns all blocked hashes. Implements GET /blocked-hashes.
func (h *Handler) ListBlockedHashes(c *gin.Context) {
	store := h.resolveStore()
	if store == nil {
		httputil.RespondWithInternalError(c, "database not initialized")
		return
	}

	hashes, err := store.GetAllBlockedHashes()
	if err != nil {
		httputil.InternalError(c, "failed to get blocked hashes", err)
		return
	}

	httputil.RespondWithOK(c, gin.H{
		"items": hashes,
		"total": len(hashes),
	})
}

// AddBlockedHash adds a hash to the blocklist. Implements POST /blocked-hashes.
func (h *Handler) AddBlockedHash(c *gin.Context) {
	store := h.resolveStore()
	if store == nil {
		httputil.RespondWithInternalError(c, "database not initialized")
		return
	}

	var req struct {
		Hash   string `json:"hash" binding:"required"`
		Reason string `json:"reason" binding:"required"`
	}

	if err := c.ShouldBindJSON(&req); err != nil {
		httputil.RespondWithBadRequest(c, err.Error())
		return
	}

	// Validate hash format (should be 64 character hex string for SHA256)
	if len(req.Hash) != 64 {
		httputil.RespondWithBadRequest(c, "hash must be 64 characters (SHA256)")
		return
	}

	err := store.AddBlockedHash(req.Hash, req.Reason)
	if err != nil {
		httputil.InternalError(c, "failed to add blocked hash", err)
		return
	}

	httputil.RespondWithCreated(c, gin.H{
		"message": "hash blocked successfully",
		"hash":    req.Hash,
		"reason":  req.Reason,
	})
}

// RemoveBlockedHash removes a hash from the blocklist. Implements DELETE
// /blocked-hashes/:hash.
func (h *Handler) RemoveBlockedHash(c *gin.Context) {
	store := h.resolveStore()
	if store == nil {
		httputil.RespondWithInternalError(c, "database not initialized")
		return
	}

	hash := c.Param("hash")
	if hash == "" {
		httputil.RespondWithBadRequest(c, "hash parameter required")
		return
	}

	err := store.RemoveBlockedHash(hash)
	if err != nil {
		httputil.InternalError(c, "failed to remove blocked hash", err)
		return
	}

	httputil.RespondWithOK(c, gin.H{
		"message": "hash unblocked successfully",
		"hash":    hash,
	})
}

// GetUserPreference returns a single user preference by key.
// Unset preferences return 200 with an empty value rather than 404 so that
// browsers don't surface "Failed to load resource" console noise for
// optional client-side prefs (library_column_config, dialog state, etc.)
// that legitimately have no saved value yet. Clients should treat an
// empty `value` as "not set" — matching the existing frontend pattern.
// Implements GET /preferences/:key.
func (h *Handler) GetUserPreference(c *gin.Context) {
	key := c.Param("key")
	if key == "" {
		httputil.RespondWithBadRequest(c, "key is required")
		return
	}
	pref, err := h.resolveStore().GetUserPreference(key)
	if err != nil {
		httputil.RespondWithInternalError(c, "failed to get preference")
		return
	}
	if pref == nil {
		httputil.RespondWithOK(c, gin.H{"key": key, "value": ""})
		return
	}
	httputil.RespondWithOK(c, gin.H{"key": pref.Key, "value": pref.Value})
}

// SetUserPreference creates or updates a user preference. Implements PUT
// /preferences/:key.
func (h *Handler) SetUserPreference(c *gin.Context) {
	key := c.Param("key")
	if key == "" {
		httputil.RespondWithBadRequest(c, "key is required")
		return
	}
	var body struct {
		Value string `json:"value"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		httputil.RespondWithBadRequest(c, "invalid request body")
		return
	}
	if err := h.resolveStore().SetUserPreference(key, body.Value); err != nil {
		httputil.RespondWithInternalError(c, "failed to save preference")
		return
	}
	httputil.RespondWithOK(c, gin.H{"key": key, "value": body.Value})
}

// DeleteUserPreference removes a user preference by setting it to empty.
// Implements DELETE /preferences/:key.
func (h *Handler) DeleteUserPreference(c *gin.Context) {
	key := c.Param("key")
	if key == "" {
		httputil.RespondWithBadRequest(c, "key is required")
		return
	}
	// Set to empty string to "delete" (store doesn't have a delete method)
	if err := h.resolveStore().SetUserPreference(key, ""); err != nil {
		httputil.RespondWithInternalError(c, "failed to delete preference")
		return
	}
	httputil.RespondWithOK(c, gin.H{"message": "preference deleted"})
}

// HandlePolicyTags returns the catalogue of recognised policy tags.
// Implements GET /policy/tags.
func (h *Handler) HandlePolicyTags(c *gin.Context) {
	httputil.RespondWithOK(c, policy.KnownPolicyTags())
}

// GetQuickQueries returns the six preset quick-filter counts for the Library
// header kebab menu. Counts are served from the per-query PebbleDB cache when
// fresh; stale or dirty entries are recomputed inline. broken_files reuses the
// existing stats:library cache so it never incurs an extra scan.
// Implements GET /library/quick-queries.
func (h *Handler) GetQuickQueries(c *gin.Context) {
	store := h.resolveStore()
	if store == nil {
		httputil.RespondWithInternalError(c, "database not initialized")
		return
	}

	type quickQueryGetter interface {
		GetQuickQueryCounts() ([]database.QuickQueryResult, error)
	}

	var getter quickQueryGetter
	if g, ok := store.(quickQueryGetter); ok {
		getter = g
	} else if uw, ok := store.(interface{ Unwrap() database.Store }); ok {
		if g, ok2 := uw.Unwrap().(quickQueryGetter); ok2 {
			getter = g
		}
	}

	if getter == nil {
		// Store implementation does not support quick queries (e.g. SQLite in tests).
		httputil.RespondWithOK(c, gin.H{"queries": []interface{}{}})
		return
	}

	results, err := getter.GetQuickQueryCounts()
	if err != nil {
		httputil.RespondWithInternalError(c, "failed to compute quick-query counts")
		return
	}

	httputil.RespondWithOK(c, gin.H{"queries": results})
}

// GetTranscribeStats implements GET /api/v1/maintenance/transcribe-stats. It
// returns the live aggregate counters written by maintenance.transcribe-book-intros
// (stats:transcribe). A monitor polls this single endpoint instead of scanning
// books or scraping op logs. Returns {data: null} when no run has ever written
// stats (so the monitor reports "never run").
func (h *Handler) GetTranscribeStats(c *gin.Context) {
	store := h.resolveStore()
	if store == nil {
		httputil.RespondWithInternalError(c, "store unavailable")
		return
	}

	var ts database.TranscribeStatsStore
	if s, ok := store.(database.TranscribeStatsStore); ok {
		ts = s
	} else if uw, ok := store.(interface{ Unwrap() database.Store }); ok {
		if s, ok2 := uw.Unwrap().(database.TranscribeStatsStore); ok2 {
			ts = s
		}
	}
	if ts == nil {
		// Store does not support transcribe stats (e.g. SQLite in tests).
		httputil.RespondWithOK(c, nil)
		return
	}

	stats, err := ts.GetTranscribeStats()
	if err != nil {
		httputil.RespondWithInternalError(c, "failed to read transcribe stats")
		return
	}
	httputil.RespondWithOK(c, stats)
}
