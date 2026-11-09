// file: internal/server/handlers/system/handler.go
// version: 1.29.13
// guid: 8475f406-df31-4286-95b0-30787397603e
// last-edited: 2026-11-09

// Package system hosts the system-level HTTP handlers extracted from the server
// package: health, status, announcements, storage, logs, activity-log,
// reset/factory-reset, config get/update, the SSE event stream, backup CRUD,
// dashboard, blocked-hash CRUD, user-preference CRUD, policy-tags, and
// quick-queries.
//
// Dependencies that lived on the *Server receiver are reached through narrow
// interfaces (SystemStore, SystemService, ConfigUpdateService,
// PluginHealthChecker, EventStreamer, OperationLogsProvider) plus the concrete
// *metafetch.OpenLibraryService (factoryReset takes its .Mu and calls CloseStoreLocked,
// which an interface cannot abstract) and three injected funcs (getDiskStats,
// resetLibrarySizeCache, appVersion, filterReviewedAuthorGroups) that wrap
// server-package helpers / build-tagged functions / mutable package vars that
// stay in package server. As a result package system never imports package
// server.

package system

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/activity"
	"github.com/falkcorp/audiobook-organizer/internal/backup"
	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/dedup"
	"github.com/falkcorp/audiobook-organizer/internal/httputil"
	"github.com/falkcorp/audiobook-organizer/internal/logger"
	"github.com/falkcorp/audiobook-organizer/internal/metafetch"
	"github.com/falkcorp/audiobook-organizer/internal/organizer"
	"github.com/falkcorp/audiobook-organizer/internal/policy"
	"github.com/falkcorp/audiobook-organizer/internal/server/handlers/opaccess"
	servermiddleware "github.com/falkcorp/audiobook-organizer/internal/server/middleware"
	"github.com/falkcorp/audiobook-organizer/internal/startup"
	"github.com/falkcorp/audiobook-organizer/internal/sysinfo"
	"github.com/falkcorp/mediacore/library"
	"github.com/gin-gonic/gin"
)

// Handler hosts the system-domain HTTP endpoints.
type Handler struct {
	// getStore resolves the database store lazily, at request time. The original
	// handlers read s.Ops() at call time (late binding), and a router
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

	// now is the clock HealthCheck stamps its answer with; nil is time.Now.
	// Set by WithClock, so a test can pin the one volatile field of an
	// otherwise static body.
	now func() time.Time

	// olService is taken as a concrete pointer (not an interface) because
	// factoryReset takes its .Mu (mutex) field and calls CloseStoreLocked under
	// it; field access cannot go through an interface. A nil concrete pointer is fine — the
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

	// getDegraded resolves, at request time, the startup steps that failed
	// without stopping the process (internal/startup). Lazy because the
	// server stores its startup runner in Start, after the handlers are
	// wired. Nil, or a provider yielding nil, means no step is degraded.
	getDegraded func() DegradedSource

	// postureTokens yields, at request time, the authenticated-only posture
	// tokens raised now with their subjects (the storage_checkpoint_* tokens
	// from R1). Nil, or a provider yielding nil, raises none. See
	// WithPostureTokens.
	postureTokens func() []PostureToken

	// repairsAccess decides which Repairs operations (repairs.plan,
	// repairs.apply) the caller may see in GET /dashboard's recent_operations.
	// Unset (nil) hides every Repairs operation: see WithRepairsOpAccess.
	repairsAccess *opaccess.Repairs

	// annDiverged marks the live HNSW vector store diverged
	// (database.SnapshotDivergence) so this run's stop writes no clean-stop
	// marker. RestoreBackup calls it: see WithANNSnapshotDivergence.
	annDiverged func(reason string)
}

// WithRepairsOpAccess installs the check that keeps a Repairs operation out of
// GET /dashboard's recent_operations for callers who do not hold the
// permissions of the operation's fixer, as every other generic operations read
// does (opaccess). A setter rather than a New parameter because New already
// has twelve and many test call sites; omitting it fails closed (every Repairs
// operation is left out), and TestDashboard_RecentOperationsLeaveOutRepairsRuns
// pins the server's wiring.
func (h *Handler) WithRepairsOpAccess(a *opaccess.Repairs) *Handler {
	h.repairsAccess = a
	return h
}

// WithClock installs the clock HealthCheck reads its `timestamp` from (nil
// restores time.Now). A setter rather than a New parameter for the same reason
// as WithRepairsOpAccess. Production never calls it; the perimeter parity
// tests pin it so two probes of /health cannot straddle a second boundary.
func (h *Handler) WithClock(now func() time.Time) *Handler {
	h.now = now
	return h
}

// clock is the current time from the installed clock, or time.Now.
func (h *Handler) clock() time.Time {
	if h.now != nil {
		return h.now()
	}
	return time.Now()
}

// PostureToken is one raised posture token with what it is about
// (05-health §5H.3.1): carried only by GET /system/status's authenticated
// `degraded` (its Token) and `degraded_details` (the whole value), never by
// /health. Subject names the checkpoint, scratch entry or path; DeleteAfter
// is when a retained checkpoint goes (storage_checkpoint_retained);
// AgeSeconds is how old an open one is (storage_checkpoint_open).
type PostureToken struct {
	Token       string     `json:"token"`
	Subject     string     `json:"subject,omitempty"`
	DeleteAfter *time.Time `json:"delete_after,omitempty"`
	AgeSeconds  int64      `json:"age_seconds,omitempty"`
}

// WithPostureTokens installs the provider of the posture tokens GET
// /system/status reports (05-health §5H.10 R1: the authenticated degraded
// field grows the storage_checkpoint_* tokens). A setter, as
// WithRepairsOpAccess is, because New already has twelve parameters; unset
// reports none.
func (h *Handler) WithPostureTokens(f func() []PostureToken) *Handler {
	h.postureTokens = f
	return h
}

// WithANNSnapshotDivergence installs fn, which marks the live HNSW vector
// store diverged. POST /backup/restore calls it because extraction writes
// Pebble files directly, behind the GatedDB whose writes the store counts
// against its clean-stop marker: without it, a run that restores and then
// stops quietly writes a marker, and the next boot trusts the old graph for
// the restored rows. A setter for the same reason as WithRepairsOpAccess;
// TestRestoreBackup_NoCleanStopMarkerAfterARestore pins the server's wiring.
func (h *Handler) WithANNSnapshotDivergence(fn func(reason string)) *Handler {
	h.annDiverged = fn
	return h
}

// DegradedSource reports the fixed tokens of startup steps that failed but
// were classified Degraded (startup.Table), e.g. "search_index".
// Implemented by *startup.Steps; the handler sees only this.
type DegradedSource interface {
	Tokens() []string
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
	getDegraded func() DegradedSource,
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
		getDegraded:                getDegraded,
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

// readinessSource is implemented by a store that warms an in-memory read layer
// after start. ready: the layer is published. done: warmup finished, either
// published or fallen back to the slow path, so there is nothing left to wait
// for. ms: how long warmup took (meaningful once done).
type readinessSource interface {
	WarmupStatus() (ready, done bool, ms int64)
}

// DegradedMemdbWarming is the machine token reported in /health's `degraded`
// list while the in-memory read layer is still warming.
const DegradedMemdbWarming = "memdb_warming"

// unauthenticatedToken reports whether tok is in 05-health §5H.3.1's fixed
// set, the only tokens /health may carry: memdb_warming, the token of a
// startup step classified Degraded (startup.Table) and
// basic_gate_no_password. Every other token is a posture token, reported on
// GET /system/status only. Pinned by TestHealth_UnauthenticatedTokenSetGolden.
func unauthenticatedToken(tok string) bool {
	if tok == DegradedMemdbWarming || tok == servermiddleware.BasicGateNoPasswordToken {
		return true
	}
	return slices.ContainsFunc(startup.Table, func(st startup.Step) bool {
		return st.Class == startup.Degraded && st.Token != "" && st.Token == tok
	})
}

// readiness is the store's warmup state: ready once the in-memory read
// layer is published or warmup fell back (done); a store with no warmup to
// report on is ready.
func readiness(store SystemStore) (ready, memReady, done bool, ms int64) {
	ready, memReady = true, true
	if store == nil {
		return ready, memReady, false, 0
	}
	// AsCapability sees through decorators (search index, audit).
	if src, ok := database.AsCapability[readinessSource](store); ok {
		memReady, done, ms = src.WarmupStatus()
		ready = done
	}
	return ready, memReady, done, ms
}

// fixedTokens is HEAD's fixed-token source: memdb_warming while the read
// layer warms, and the tokens getDegraded yields (failed Degraded startup
// steps, basic_gate_no_password). R1 splits it in two (05-health §5H.10):
// /health carries the tokens of the unauthenticated set, and GET
// /system/status's degraded carries every token.
func (h *Handler) fixedTokens(ready bool) []string {
	tokens := []string{}
	if !ready {
		tokens = append(tokens, DegradedMemdbWarming)
	}
	if h.getDegraded != nil {
		if src := h.getDegraded(); src != nil {
			tokens = append(tokens, src.Tokens()...)
		}
	}
	return tokens
}

// HealthCheck implements GET /health (and /api/health, /api/v1/health).
//
// LIVENESS PLUS READINESS — it deliberately says nothing about what this server
// is or what it holds. The only additions to liveness are the readiness flags
// (ready, memdb_ready, warmup_ms) and `degraded`, a list of fixed machine
// tokens; see the body of HealthCheck.
//
// This endpoint is necessarily unauthenticated: it is how a caller decides
// whether the server is up at all, including the SPA's reconnect loop, which
// runs while logged out. It used to answer with the exact build string and the
// library's book/author/series counts, so any host that could reach the port
// learned the precise version to look up advisories against and the size of the
// collection, without presenting a credential. Every consumer in this repo only
// ever checks reachability — web/src/App.tsx tests `response.ok`, the scripts
// under scripts/ discard the body — so nothing needed those fields. They live
// on at GET /api/v1/system/status, behind settings.manage.
//
// It is also now cheap. The old implementation ran CountPrimaryBooks +
// CountAuthors + CountSeries + GetBrokenFileCount on EVERY probe, and the SPA
// polls this every 5 seconds while reconnecting. A single tiny read is enough
// to answer the only question being asked: can the store respond?
func (h *Handler) HealthCheck(c *gin.Context) {
	// Readiness is reported next to liveness, never instead of it. The HTTP status
	// stays 200 while the in-memory read layer warms (a probe or the SPA's
	// reconnect loop must keep treating the process as up); callers that need to
	// know whether reads are on the fast path read `ready`. Every value below is a
	// bool, a number of milliseconds or a fixed machine token -- never an error
	// string, path, version or count.
	resp := gin.H{
		"status":    "ok",
		"timestamp": h.clock().Unix(),
	}
	store := h.resolveStore()
	ready, memReady, warmupDone, warmupMS := readiness(store)
	if store != nil {
		// CountAuthors is the cheapest of the aggregates and, like the others,
		// fails when the store is unreachable — which is the whole signal. The
		// value is read and discarded on purpose.
		if _, err := store.CountAuthors(); err != nil {
			// Degraded, not dead: the process is serving, the store is not
			// answering. The error string is NOT echoed — it can carry
			// filesystem paths and internal detail to an anonymous caller.
			resp["status"] = "degraded"
		}
	}
	// The unauthenticated set only (05-health §5H.3.1): fixed machine
	// tokens, never error text, sorted so the list is stable across probes.
	// A token outside the set is left out here and reported on GET
	// /system/status, which carries every token.
	degraded := slices.DeleteFunc(h.fixedTokens(ready), func(tok string) bool { return !unauthenticatedToken(tok) })
	slices.Sort(degraded)
	degraded = slices.Compact(degraded)
	resp["ready"] = ready
	resp["memdb_ready"] = memReady
	if warmupDone {
		resp["warmup_ms"] = warmupMS
	}
	resp["degraded"] = degraded
	httputil.RespondWithOK(c, resp)
}

// DegradedAutoUpdatePaused is the posture token GET /system/status's
// `degraded` field carries while the operator asked for automatic update
// (auto_update.enabled) and the updater only checks (the R0 hotfix: no
// unattended apply). It is a posture token, so it is NEVER reported on
// /health, which is unauthenticated (05-health 5H.3.1); pinned by
// TestHealth_UnauthenticatedTokenSetGolden.
const DegradedAutoUpdatePaused = "auto_update_paused"

// The storage_checkpoint_* posture tokens (05-storage §5S.3.12; 05-health
// §5H.3.1), reported from R1 on GET /system/status only, each with its
// subject in degraded_details: a checkpoint is retained (with its
// delete_after), an open checkpoint or scratch entry exists (with its age),
// a checkpoint directory or file is group- or world-accessible (naming it).
const (
	DegradedStorageCheckpointRetained = "storage_checkpoint_retained"
	DegradedStorageCheckpointOpen     = "storage_checkpoint_open"
	DegradedStorageCheckpointMode     = "storage_checkpoint_mode"
)

// systemStatusResponse is GET /system/status's body: the collected status plus
// `degraded`, the authenticated list of every token raised now. R0 added the
// field with one posture token, auto_update_paused; R1 makes it every token,
// /health's fixed set included, and extends it with the storage_checkpoint_*
// posture tokens, whose subjects travel `degraded_details` (05-health
// 5H.10).
type systemStatusResponse struct {
	*sysinfo.SystemStatus
	Degraded        []string       `json:"degraded"`
	DegradedDetails []PostureToken `json:"degraded_details"`
}

// authenticatedDegraded returns every token raised now, sorted and
// de-duplicated, and the posture tokens' details. Neither is nil, so an
// empty list encodes as [].
func (h *Handler) authenticatedDegraded() ([]string, []PostureToken) {
	ready, _, _, _ := readiness(h.resolveStore())
	degraded := h.fixedTokens(ready)
	if config.Snapshot().AutoUpdate.Enabled {
		degraded = append(degraded, DegradedAutoUpdatePaused)
	}
	details := []PostureToken{}
	if h.postureTokens != nil {
		for _, p := range h.postureTokens() {
			degraded = append(degraded, p.Token)
			details = append(details, p)
		}
	}
	slices.Sort(degraded)
	return slices.Compact(degraded), details
}

// GetSystemStatus implements GET /system/status.
func (h *Handler) GetSystemStatus(c *gin.Context) {
	// The recent-operations panel is judged per caller: a Repairs operation the
	// caller may not read is left out and no operation's result_data is served.
	status, err := h.systemSvc.CollectSystemStatus(h.repairsAccess.For(c).MayRead)
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

	degraded, details := h.authenticatedDegraded()
	httputil.RespondWithOK(c, systemStatusResponse{SystemStatus: status, Degraded: degraded, DegradedDetails: details})
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
				Link:     "/review?lane=authors",
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
		h.opLogs.OperationLogsByID(c, id)
		return
	}

	level := c.Query("level")
	params := httputil.ParsePaginationParams(c)

	// A line that names other users' own data (the predicate the activity log,
	// the diagnostics export and /system/activity-log use) is left out unless the
	// caller holds users.manage, and the total counts only what is served.
	mayRead := func(sysinfo.SystemLogEntry) bool { return true }
	if !h.repairsAccess.For(c).MayReadUserData() {
		mayRead = func(e sysinfo.SystemLogEntry) bool {
			if activity.NamesOthersUserLog("", e.Message) {
				return false
			}
			return e.Details == nil || !activity.NamesOthersUserLog("", *e.Details)
		}
	}
	logs, total, err := h.systemSvc.CollectSystemLogs(level, params.Search, params.Limit, params.Offset, mayRead)
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
	// settings.manage reads this route, and the table holds the abs, merge,
	// invite and auth subsystems' lines: other users' listening state and
	// accounts. Without users.manage those lines and the user-id column are left
	// out, by the predicate the diagnostics export and the activity filter use.
	if !h.repairsAccess.For(c).MayReadUserData() {
		readable := make([]database.SystemActivityLog, 0, len(logs))
		for _, l := range logs {
			if activity.NamesOthersUserLog(l.Source, l.Message) {
				continue
			}
			l.UserID = ""
			readable = append(readable, l)
		}
		logs = readable
	}
	httputil.RespondWithOK(c, gin.H{"items": logs, "count": len(logs)})
}

// ResetSystem implements POST /system/reset.
func (h *Handler) ResetSystem(c *gin.Context) {
	// Require the same explicit {"confirm":"RESET"} token that the more
	// destructive FactoryReset requires — ResetSystem also wipes the entire
	// database (books, authors, series, settings), so a bare POST must not be
	// enough to trigger it. Keep the token string identical to FactoryReset so
	// callers use one confirmation contract for both.
	var req struct {
		Confirm string `json:"confirm"`
	}
	if err := c.ShouldBindJSON(&req); err != nil || req.Confirm != "RESET" {
		httputil.RespondWithBadRequest(c, "request body must contain {\"confirm\": \"RESET\"}")
		return
	}

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

var systemLog = logger.New("handlers.system")

// afterOLCloseHook, unset outside tests, runs in deleteOLDataLocked after the
// Open Library store has closed cleanly and before its data directory is
// removed: the window in which the service's Mu must still be held. It is an
// atomic pointer so a test can install it while the handler is live.
var afterOLCloseHook atomic.Pointer[func()]

// beforeRestoreExtractHook, unset outside tests, runs in RestoreBackup just
// before the extraction's first write: after the divergence mark and the
// first marker removal.
var beforeRestoreExtractHook atomic.Pointer[func()]

// deleteOLDataLocked closes the Open Library store and removes its data
// directory, holding the service's Mu from the close through the RemoveAll.
// Releasing Mu between the two let an import's EnsureStore open a new engine
// in the directory this was about to delete. Holding it across the RemoveAll
// is what the delete-OL-data handler does too.
func (h *Handler) deleteOLDataLocked() {
	svc := h.olService
	svc.Mu.Lock()
	defer svc.Mu.Unlock()

	olCloseErr := svc.CloseStoreLocked()
	targetDir := metafetch.GetOLDumpDir()
	if olCloseErr != nil {
		// A failed Close may have left the engine open (an in-flight
		// lookup or import outlived the drain timeout). Removing its
		// directory would pull files out from under a live engine, so
		// the OL data is kept and the rest of the reset goes on. The
		// service keeps the half-closed store, so the next delete or
		// import retries the close.
		systemLog.Warn("Factory reset kept OL data: the OL store did not close: %v", olCloseErr)
		return
	}
	if targetDir == "" {
		return
	}
	if hook := afterOLCloseHook.Load(); hook != nil {
		(*hook)()
	}
	if err := os.RemoveAll(targetDir); err != nil {
		systemLog.Warn("Factory reset failed to remove OL data dir: %v", err)
		return
	}
	systemLog.Info("Factory reset OL data deleted")
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
		h.deleteOLDataLocked()
	}

	// Clear library folder contents (organized audiobooks)
	if config.AppConfig.RootDir != "" {
		libraryDir := config.AppConfig.RootDir
		if library.IsDangerousRoot(libraryDir) {
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
	config.Mutate(func(c *config.Config) {
		c.RootDir = ""
		c.SetupComplete = false
	})
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
	httputil.RespondWithOK(c, gin.H{"config": withEnvLocks(h.configUpdate.MaskSecrets(config.Snapshot()))})
}

// withEnvLocks returns the config as a map carrying an extra read-only "env_locked"
// key: the settings the environment is currently forcing.
//
// It is grafted onto the config object rather than sent as a sibling of it because
// the client reads `data.config` and discards everything beside it, and because the
// alternative — a field on config.Config — would be persisted into the config blob,
// where a snapshot of one host's environment would then be restored onto another.
//
// On a marshal failure the plain config is returned. That is a deliberate fail-open:
// losing the annotation degrades the Settings page to its previous behaviour, while
// failing the request would take the whole page down over an advisory hint.
func withEnvLocks(cfg config.Config) any {
	raw, err := json.Marshal(cfg)
	if err != nil {
		return cfg
	}
	// UseNumber, not a plain Unmarshal. Decoding into map[string]any turns every
	// number into a float64, and re-encoding one loses precision above 2^53 —
	// measured: 1234567890123456789 comes back as 1234567890123456800. Config's
	// two int64 fields are byte counts, so nothing is corrupt at today's values,
	// but this function re-encodes the WHOLE config on the primary read path, and
	// a field added later holding a nanosecond timestamp or an ID would be
	// silently mangled. json.Number keeps the original digits verbatim.
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var flat map[string]any
	if err := dec.Decode(&flat); err != nil {
		return cfg
	}
	flat["env_locked"] = config.EnvLockedSettings()
	// Same membership as env_locked, with WHY attached: an environment variable
	// name, or a command-line flag. A field disabled with no reason, or with the
	// wrong reason, sends the operator to edit the wrong half of the unit file.
	flat["setting_locks"] = config.SettingLocks()
	// The placeholders a folder/file naming pattern may use, straight from the
	// resolver, so the Settings help text cannot advertise one BuildPath rejects.
	flat["naming_pattern_tokens"] = organizer.NamingPatternTokens()
	return flat
}

// UpdateConfig implements PUT /config.
func (h *Handler) UpdateConfig(c *gin.Context) {
	var payload map[string]any
	if err := c.ShouldBindJSON(&payload); err != nil {
		httputil.RespondWithBadRequest(c, err.Error())
		return
	}

	// Protected settings (sign-in, executables, database and server paths)
	// are refused for an API key inside UpdateService, on the decoded config,
	// so the check cannot disagree with the decoder about which key was meant.
	status, resp := h.configUpdate.UpdateConfig(c.Request.Context(), payload)
	if status >= 400 {
		// No rollback here: UpdateService owns the in-memory/persisted state
		// on every failure it reports. A 400 changed nothing; a save failure
		// was already rolled back (to a deep copy); and a 500 with
		// resp["saved"]==true means the config IS saved and in effect but the
		// dedup re-band hand-off failed — rolling memory back to previousConfig
		// there (as this handler used to on every >=400) would put memory on
		// the old ladder while the blob and the engine keep the new one.
		// previousConfig is also a SHALLOW Snapshot, so it could never restore
		// map contents anyway.
		errMsg, _ := resp["error"].(string)
		// A refused unknown key is named in errMsg, and also returned as a list
		// so a client can point at the exact setting without parsing prose.
		if unknown, ok := resp["unknown_keys"].([]string); ok && len(unknown) > 0 {
			httputil.RespondWithErrorFields(c, status, errMsg, "CONFIG_ERROR",
				map[string]any{"unknown_keys": unknown})
			return
		}
		if refused, ok := resp["refused_keys"].([]string); ok && len(refused) > 0 {
			servermiddleware.LogCredentialChangeRefusal(c)
			httputil.RespondWithErrorFields(c, status, errMsg, "FORBIDDEN",
				map[string]any{"refused_keys": refused})
			return
		}
		httputil.RespondWithError(c, status, errMsg, "CONFIG_ERROR")
		return
	}

	// No Validate-and-roll-back here any more. It used to run AFTER
	// UpdateConfig had already persisted, and undid the change in memory only —
	// leaving the blob, and the dedup engine, holding a config that memory had
	// just reverted. UpdateService now validates the CANDIDATE before the swap
	// and returns 400 with nothing written, so there is nothing to undo.

	// Same env_locked annotation the GET carries, so a client that refreshes its
	// view from the save response sees the identical shape it got from the GET
	// rather than a config that has quietly lost the annotation. (Today's web
	// client re-reads only openai_api_key here, so nothing depends on it yet —
	// the point is that the two endpoints do not disagree about the schema.)
	maskedConfig := withEnvLocks(h.configUpdate.MaskSecrets(config.Snapshot()))
	response := gin.H{"config": maskedConfig}
	if opID, ok := resp["dedup_rescore_op_id"].(string); ok && opID != "" {
		// The dedup ladder changed; the stored-candidate re-band was queued as
		// an operation. Surface its id so the caller can follow it.
		response["dedup_rescore_op_id"] = opID
	}
	// Historically this re-marshalled the config and decoded it back into a map to
	// mirror every field at the response's top level. withEnvLocks already returns
	// that map, so copy it directly: a second round-trip would re-run the float64
	// widening its json.Number decoding exists to avoid. The fail-open branch of
	// withEnvLocks hands back a plain Config, which has no top-level mirror to make
	// — the nested "config" key above still carries it.
	if flat, ok := maskedConfig.(map[string]any); ok {
		maps.Copy(response, flat)
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
	// max_backups drives backup ROTATION, which deletes. A malformed body left it
	// nil, DefaultBackupConfig()'s retention was used instead of the caller's, and
	// rotation could then delete backups the caller had explicitly asked to keep.
	// A silently-substituted retention count is data-loss-adjacent, so an
	// unreadable body is refused. An absent body still means "use the default",
	// which is the documented behaviour of this endpoint.
	var req struct {
		MaxBackups *int `json:"max_backups"`
	}
	if err := c.ShouldBindJSON(&req); err != nil && !errors.Is(err, io.EOF) {
		httputil.RespondWithBadRequest(c, "invalid request body: "+err.Error())
		return
	}

	backupConfig := backup.DefaultBackupConfig()
	if req.MaxBackups != nil {
		backupConfig.MaxBackups = *req.MaxBackups
	}
	backupConfig.MaxTotalBytes = backup.ResolveMaxTotalBytes(config.AppConfig.BackupMaxTotalBytes)
	backupConfig.Compression = config.AppConfig.BackupCompression
	backupConfig.CompressionLevel = config.AppConfig.BackupCompressionLevel

	// Get database path and type from app config
	dbPath := config.AppConfig.DatabasePath
	dbType := config.AppConfig.DatabaseType

	// Resolve backup dir relative to database directory so it's always absolute
	backupConfig.BackupDir = backup.ResolveDir(config.AppConfig.BackupDir, dbPath)

	var info *backup.BackupInfo
	var err error
	// Only use the Checkpointable (Pebble-native) path when the configured DB
	// type is pebble. For sqlite (or any other type), fall through to the
	// file-copy path so the source-path existence check runs and returns an
	// appropriate error when the DB file is missing.
	if store := h.resolveStore(); store != nil && dbType == "pebble" {
		// database.AsCapability, NOT a bare `store.(backup.Checkpointable)`.
		// The production store is wrapped by the search-index decorator, whose
		// method set does not include Checkpoint, so a bare assertion fails and
		// silently drops to the live-directory walk that races Pebble
		// compaction. Same shape as the version-group backfill bug (PR #2295).
		if cp, ok := database.AsCapability[backup.Checkpointable](store); ok {
			info, err = backup.CreateBackupWithCheckpoint(cp, dbPath, dbType, backupConfig)
		} else {
			slog.Warn("backup: store does not expose Checkpoint, falling back to a live-directory archive",
				"store_type", fmt.Sprintf("%T", store))
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
	backupConfig.BackupDir = backup.ResolveDir(config.AppConfig.BackupDir, config.AppConfig.DatabasePath)

	// Checksums are OPT-IN because computing them reads every byte of every
	// archive. Measured on production 2026-08-29, with ~16 GB of archives this
	// endpoint did not respond within two minutes; at the sizes this application
	// produces, hashing on every list makes the backup screen unusable.
	//
	// A caller that actually wants to verify integrity asks for it, and accepts
	// the wait. The default answer to "what backups exist" must be cheap.
	var (
		backups []backup.BackupInfo
		err     error
	)
	if c.Query("checksums") == "true" {
		backups, err = backup.ListBackupsWithChecksums(backupConfig.BackupDir)
	} else {
		backups, err = backup.ListBackups(backupConfig.BackupDir)
	}
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
	backupConfig.BackupDir = backup.ResolveDir(config.AppConfig.BackupDir, config.AppConfig.DatabasePath)
	safeFilename := library.SanitizeFilename(req.BackupFilename)

	// Use current database path as target if not specified
	var targetPath string
	if req.TargetPath != "" {
		cleanTarget, err := library.CleanAbsolutePath(req.TargetPath)
		if err != nil {
			httputil.RespondWithBadRequest(c, "invalid target_path: "+err.Error())
			return
		}
		// CleanAbsolutePath's checks are comparisons CodeQL's
		// go/path-injection does not read, so every path built from the
		// target was reported (R1-15 round-6 verification review, SF2).
		// This restates its absoluteness as a prefix check on the cleaned
		// path, which it does read. A target_path is an absolute directory
		// the operator chooses, so the only base it has is its volume's
		// root, and this check adds no containment: what keeps a restore
		// where it belongs is the route's permission (settings manage),
		// IsDangerousRoot and CheckPathsApart below.
		if !strings.HasPrefix(cleanTarget, filepath.VolumeName(cleanTarget)+string(filepath.Separator)) {
			httputil.RespondWithBadRequest(c, "invalid target_path: not an absolute path")
			return
		}
		if library.IsDangerousRoot(cleanTarget) {
			httputil.RespondWithBadRequest(c, "restore target is a protected system directory")
			return
		}
		// A target that is the live HNSW snapshot directory, lies above it
		// or lies inside it lets an ordinary entry name land in it (R1-15
		// round-4 verification review, SHOULD-FIX 1). Refused here, before
		// anything is prepared or marked. CheckPathsApart compares
		// directories by identity, so a symlink to it or a case-insensitive
		// spelling (HNSW, hnſw) counts (round-5 verification review,
		// S1-r5), and refuses a target it cannot compare because a symlink
		// on either path does not resolve (round 6, B1-r6). The default
		// target lies above it too, and is how a restore goes over the live
		// database: there, PrepareRestoreIn and Extract keep entries out of
		// the directory.
		if dbPath := config.AppConfig.DatabasePath; dbPath != "" {
			if err := backup.CheckPathsApart(cleanTarget, database.HNSWSnapshotDir(dbPath)); err != nil {
				if errors.Is(err, backup.ErrUnresolvableSymlink) {
					httputil.RespondWithConflict(c, "restore target cannot be compared with the HNSW snapshot directory: "+err.Error())
					return
				}
				httputil.RespondWithBadRequest(c, "restore target overlaps the HNSW snapshot directory; omit target_path to restore over the live database")
				return
			}
		}
		targetPath = cleanTarget
	} else {
		targetPath = filepath.Dir(config.AppConfig.DatabasePath)
	}

	// PrepareRestoreIn (not a path-taking variant) so req.BackupFilename is
	// only ever used as a directory-listing lookup key, never joined directly
	// into a filesystem path -- see backup.PrepareRestoreIn's doc comment
	// (CodeQL go/path-injection: sanitizing the filename is not a credited
	// barrier in this repo, only resolving it against os.ReadDir's own
	// results is).
	prepared, err := backup.PrepareRestoreIn(backupConfig.BackupDir, safeFilename, req.Verify)
	if err != nil {
		// PrepareRestoreIn writes nothing, so a refused restore leaves the HNSW
		// snapshot's trust alone. It distinguishes two verify=true failure
		// shapes, both fail-closed (no extraction performed) rather than
		// silently restoring unverified, and one archive it refuses whatever
		// verify says:
		//
		//   - ErrChecksumMismatch: the archive's current bytes disagree with
		//     the checksum recorded when it was created -- i.e. the request
		//     conflicts with the actual state of the stored resource. 409 is
		//     the closer fit than 422: nothing about the JSON body is
		//     malformed, the archive on disk no longer matches what it's
		//     supposed to be.
		//   - ErrVerificationUnsupported: no checksum sidecar exists at all
		//     (a legacy backup from before this feature), so there is nothing
		//     to compare against. 400: the caller's request cannot be
		//     satisfied as specified.
		//   - ErrArchiveCarriesHNSWSnapshot, ErrArchiveEntryEscapes: the
		//     archive has an entry in the HNSW snapshot directory, or one
		//     that climbs out of the target, which no real backup has (R1-15
		//     round 4). 400, like the case above: the archive named cannot be
		//     restored, and that is a refusal, not a server fault.
		//
		// Any other error (missing file, unreadable backup directory or
		// archive, an archive with no entries: ErrArchiveEmpty) is a 500 via
		// httputil.InternalError, the same as a truncated archive.
		switch {
		case errors.Is(err, backup.ErrChecksumMismatch):
			httputil.RespondWithConflict(c, err.Error())
		case errors.Is(err, backup.ErrVerificationUnsupported),
			errors.Is(err, backup.ErrArchiveCarriesHNSWSnapshot),
			errors.Is(err, backup.ErrArchiveEntryEscapes):
			httputil.RespondWithBadRequest(c, err.Error())
		default:
			httputil.InternalError(c, "failed to restore backup", err)
		}
		return
	}

	// The snapshot directories the extraction keeps entries out of must
	// resolve: one whose path, or an ancestor's, is a symlink that does not
	// has no identity to compare an entry's destination with, and an entry
	// could create the symlink's target, the snapshot directory from then on
	// (R1-15 round-6 verification review, B1-r6). Checked before the mark
	// and before anything is written, so such a restore costs nothing. A
	// 409: the server's directory, not the request, stands in the way.
	// Extract checks again for each entry.
	snapshotDirs := annSnapshotDirs(targetPath, prepared.Root())
	if err := backup.CheckSnapshotDirs(snapshotDirs); err != nil {
		httputil.RespondWithConflict(c, "backup not restored: "+err.Error())
		return
	}

	// The extraction below writes database files behind the write gate. The
	// live HNSW store is marked diverged before its first write, not after
	// it: the kernel's drain does not wait for handler goroutines, so a stop
	// can run MarkCleanStop while this extraction is mid-write and the
	// process can exit before this handler returns (R1-15 round-3 review,
	// B1-r3). After the mark, such a stop refuses the marker, and a marker an
	// earlier stop wrote is removed by the mark itself.
	h.markANNSnapshotDiverged()
	// The on-disk markers are removed before the extraction's first write,
	// like the mark: a marker already beside a custom target's database
	// (the mark covers only the live store) would otherwise outlive a crash
	// or exit during the extraction and vouch for a snapshot the
	// half-restored database does not match (R1-15 round-5 verification
	// review, the B1r-5 order). The checks keep archive entries out of both
	// snapshot directories: PrepareRestoreIn refuses an entry named into the
	// snapshot directory or climbing out of the target; Extract compares
	// every directory on the way to each destination with them by identity,
	// so symlinks and case-insensitive spellings count, and does not write a
	// file through a symlink (B1-r5); and a target that overlaps the live
	// one, compared the same way, was refused above. The removal runs again
	// after the extraction, deferred so that it runs whether the extraction
	// finished, failed partway or panicked, as the backstop should an entry
	// still reach one.
	// A marker the first removal can neither remove nor empty would go on
	// vouching, so the restore fails there, before its first write (R1-15
	// round-6 verification review, NIT). Any other error is about a marker
	// that vouched for nothing (it did not parse) and is gone. The
	// backstop's error can only be logged, as the restore has written by
	// then.
	if err := h.removeANNCleanStopMarkers(snapshotDirs); err != nil {
		if errors.Is(err, database.ErrCleanStopNotDiscarded) {
			httputil.InternalError(c, "backup not restored: a clean-stop marker beside a snapshot directory could be neither removed nor emptied, and nothing was written", err)
			return
		}
		systemLog.Warn("backup restore: %v", err)
	}
	defer func() {
		if err := h.removeANNCleanStopMarkers(snapshotDirs); err != nil {
			systemLog.Error("backup restore: %v", err)
		}
	}()
	if hook := beforeRestoreExtractHook.Load(); hook != nil {
		(*hook)()
	}
	if err := prepared.Extract(targetPath, snapshotDirs); err != nil {
		// The extraction can stop after it has written entries, so every
		// response from here says so (restorePartialNote). A refusal is
		// the status the same refusal has before the mark: a snapshot
		// directory, or a symlink on an entry's path, that does not
		// resolve (B1-r6, B1-r8) is the 409 above; an entry that would
		// land in a snapshot directory, which only the extraction can see
		// when a symlink routes it there, or that climbs out of the target,
		// is the prepare step's 400 with its reason (R1-15 round-8
		// verification review, N1). It was a bare 500.
		switch {
		case errors.Is(err, backup.ErrUnresolvableSymlink):
			httputil.RespondWithConflict(c, "backup restore stopped: "+err.Error()+restorePartialNote)
		case errors.Is(err, backup.ErrArchiveCarriesHNSWSnapshot),
			errors.Is(err, backup.ErrArchiveEntryEscapes):
			httputil.RespondWithBadRequest(c, "backup restore stopped: "+err.Error()+restorePartialNote)
		default:
			httputil.InternalError(c, "failed to restore backup"+restorePartialNote, err)
		}
		return
	}

	// Reaching here means either verify=false (verification not requested) or
	// verify=true and the checksum matched -- backup.PrepareRestoreIn returns
	// an error in every other case, so "verified" always equals what was asked
	// for. The fields are included explicitly (rather than omitted) so a
	// caller parsing the response, not just the server log, can tell whether
	// verification actually happened.
	httputil.RespondWithOK(c, gin.H{
		"message":          "backup restored successfully",
		"target":           targetPath,
		"verified":         req.Verify,
		"verify_requested": req.Verify,
	})
}

// annRestoreReason is the divergence reason a restore gives the HNSW store.
const annRestoreReason = "a backup restore wrote database files the write gate did not see"

// restorePartialNote ends every response to a restore whose extraction
// failed: what the failure leaves on disk, which the caller cannot see from
// the reason alone.
const restorePartialNote = "; entries extracted before it may have been written, and the HNSW snapshot is marked diverged, so it is reconciled against the database rather than trusted"

// markANNSnapshotDiverged stops this run's stop from vouching for the HNSW
// snapshot once a restore is about to write (R1-15 round-2 review B1, and
// round-3 B1-r3 for when). Extraction writes Pebble files with plain file
// writes, so the GatedDB verdict watch never counts them, and the restored
// rows can differ from the graph in every way. It marks the live store for
// every target: a custom target can still be the live database directory
// under another spelling (a symlink, a bind mount), and the cost of a
// needless mark is one boot that reconciles the snapshot instead of trusting
// it.
func (h *Handler) markANNSnapshotDiverged() {
	if h.annDiverged != nil {
		h.annDiverged(annRestoreReason)
	}
}

// annSnapshotDirs returns the HNSW snapshot directories of a restore into
// targetPath of an archive whose root is root: the live database's, and the
// restored database's. Extract keeps every entry out of them and
// removeANNCleanStopMarkers empties them of markers. Without a configured
// database path there are none.
func annSnapshotDirs(targetPath, root string) []string {
	dbPath := config.AppConfig.DatabasePath
	if dbPath == "" {
		return nil
	}
	live := database.HNSWSnapshotDir(dbPath)
	// The restore writes the archive's database at targetPath/<root>, under
	// the name it had when the backup was taken, which need not be today's
	// filepath.Base(dbPath) (R1-15 round-4 verification review, NIT).
	restored := database.HNSWSnapshotDir(filepath.Join(targetPath, root))
	return slices.Compact([]string{live, restored})
}

// removeANNCleanStopMarkers removes a clean-stop marker in each of the
// restore's snapshot directories (annSnapshotDirs). It tries every
// directory and joins their errors (database.TakeHNSWCleanStop):
// database.ErrCleanStopNotDiscarded is in it when a marker may still
// vouch.
func (h *Handler) removeANNCleanStopMarkers(snapshotDirs []string) error {
	var errs []error
	for _, dir := range snapshotDirs {
		if _, err := database.TakeHNSWCleanStop(dir); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// DeleteBackup deletes a backup file. Implements DELETE /backup/:filename.
func (h *Handler) DeleteBackup(c *gin.Context) {
	filename := c.Param("filename")
	if filename == "" {
		httputil.RespondWithBadRequest(c, "filename required")
		return
	}

	backupConfig := backup.DefaultBackupConfig()
	backupConfig.BackupDir = backup.ResolveDir(config.AppConfig.BackupDir, config.AppConfig.DatabasePath)
	filename = library.SanitizeFilename(filename)

	// DeleteBackupIn (not DeleteBackup): same reasoning as PrepareRestoreIn
	// above -- filename is only ever used as a directory-listing lookup key.
	if err := backup.DeleteBackupIn(backupConfig.BackupDir, filename); err != nil {
		httputil.InternalError(c, "failed to delete backup", err)
		return
	}

	httputil.RespondWithOK(c, gin.H{"message": "backup deleted successfully"})
}

// RecentOperationsWindow is how many of the newest operations GET /dashboard
// reads to find the ones it lists (sysinfo.RecentOperations).
const RecentOperationsWindow = sysinfo.RecentOperationsWindow

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

	// Same panel as GET /system/status (sysinfo.RecentOperations): a Repairs
	// operation the caller may not read is left out, and result_data is not
	// served. Repointed from the frozen v1 keyspace rather than removed even
	// though no frontend code reads `recent_operations` from this endpoint
	// (grepped 2026-10-25: the Dashboard page reads /system/status): it is a
	// public response field, and serving correct data costs one call.
	recentOps := sysinfo.RecentOperations(store, time.Now(), h.repairsAccess.For(c).MayRead)

	// Try to read broken file count from underlying store (PebbleStore)
	brokenFileCount := 0
	if store != nil {
		if gf, ok := database.AsCapability[interface{ GetBrokenFileCount() (int, error) }](store); ok {
			if cnt, err := gf.GetBrokenFileCount(); err == nil {
				brokenFileCount = cnt
			}
		}
	}

	httputil.RespondWithOK(c, gin.H{
		"format_distribution": stats.FormatDistribution,
		"state_distribution":  stats.StateDistribution,
		"recent_operations":   recentOps,
		"total_size":          stats.TotalSize,
		"total_books":         stats.TotalBooks,
		"total_duration":      stats.TotalDuration,
		"organized_books":     stats.OrganizedBooks,
		"unorganized_books":   stats.UnorganizedBooks,
		"broken_file_count":   brokenFileCount,

		"fingerprinted_books":           stats.FingerprintedBooks,
		"partially_fingerprinted_books": stats.PartiallyFingerprintedBooks,
		"unfingerprinted_books":         stats.UnfingerprintedBooks,
		"fingerprint_coverage_percent":  stats.FingerprintCoveragePercent,
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

// reservedPreferenceMessage is the 400 body for a PUT or DELETE of a
// reserved preference key (database.IsReservedPreferenceKey): the storage
// format stamp and migration marker, db_version and migration_<n>. A bad
// value in any of them makes the next boot refuse to open the store or
// replay migrations, so no client may write them.
const reservedPreferenceMessage = "preference key is reserved for the application and cannot be changed through this API"

// SetUserPreference creates or updates a user preference. Implements PUT
// /preferences/:key. Reserved keys are rejected with 400.
func (h *Handler) SetUserPreference(c *gin.Context) {
	key := c.Param("key")
	if key == "" {
		httputil.RespondWithBadRequest(c, "key is required")
		return
	}
	if database.IsReservedPreferenceKey(key) {
		httputil.RespondWithBadRequest(c, reservedPreferenceMessage)
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
		if errors.Is(err, database.ErrReservedPreferenceKey) {
			httputil.RespondWithBadRequest(c, reservedPreferenceMessage)
			return
		}
		httputil.RespondWithInternalError(c, "failed to save preference")
		return
	}
	httputil.RespondWithOK(c, gin.H{"key": key, "value": body.Value})
}

// DeleteUserPreference removes a user preference. Implements DELETE
// /preferences/:key. It deletes the row rather than writing "" — an empty
// value is a value, and a tombstone is indistinguishable from a preference
// the user really set to blank.
func (h *Handler) DeleteUserPreference(c *gin.Context) {
	key := c.Param("key")
	if key == "" {
		httputil.RespondWithBadRequest(c, "key is required")
		return
	}
	if database.IsReservedPreferenceKey(key) {
		httputil.RespondWithBadRequest(c, reservedPreferenceMessage)
		return
	}
	if err := h.resolveStore().DeleteUserPreference(key); err != nil {
		if errors.Is(err, database.ErrReservedPreferenceKey) {
			httputil.RespondWithBadRequest(c, reservedPreferenceMessage)
			return
		}
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

	getter, _ := database.AsCapability[quickQueryGetter](store)

	if getter == nil {
		// Store implementation does not support quick queries (e.g. SQLite in tests).
		httputil.RespondWithOK(c, gin.H{"queries": []any{}})
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

	ts, _ := database.AsCapability[database.TranscribeStatsStore](store)
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

// GetFileIntroStats implements GET /api/v1/maintenance/file-intro-stats: the
// live counters of the most recent maintenance.transcribe-file-intros run
// (stats:transcribe-files), counted per book_file with coverage at that grain.
// It is a separate key and endpoint from GetTranscribeStats because the two ops
// count different things and either can be running while the other's numbers
// are read. Returns {data: null} when no run has ever written stats.
func (h *Handler) GetFileIntroStats(c *gin.Context) {
	store := h.resolveStore()
	if store == nil {
		httputil.RespondWithInternalError(c, "store unavailable")
		return
	}
	fs, _ := database.AsCapability[database.FileIntroStatsStore](store)
	if fs == nil {
		// A typed nil, not a bare nil: SuccessResponse.Data is omitempty, so an
		// untyped nil drops the "data" key while a typed nil pointer serialises
		// as {"data":null}, the same shape the "no run yet" path returns.
		httputil.RespondWithOK(c, (*database.FileIntroStats)(nil))
		return
	}
	stats, err := fs.GetFileIntroStats()
	if err != nil {
		httputil.RespondWithInternalError(c, "failed to read file intro stats")
		return
	}
	httputil.RespondWithOK(c, stats)
}
