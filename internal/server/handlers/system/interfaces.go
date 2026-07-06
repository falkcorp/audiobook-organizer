// file: internal/server/handlers/system/interfaces.go
// version: 1.2.0
// guid: 7a91ad40-5c96-4423-ad24-715acb791cf8
// last-edited: 2026-07-05

// Narrow dependency interfaces for the system domain handlers (health, status,
// announcements, storage, logs, activity-log, reset/factory-reset, config
// get/update, SSE events, backups CRUD, dashboard, blocked-hashes CRUD,
// user-preferences CRUD, policy-tags, quick-queries). Each interface lists only
// what the handlers actually call so package system stays decoupled from the
// concrete sysinfo / config / plugin / realtime / store implementations and
// never imports package server (which would create an import cycle).

package system

import (
	"github.com/gin-gonic/gin"
	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/sysinfo"
)

// SystemStore is the narrow database.Store subset the system handlers require.
// The concrete database.Store implementations satisfy it. Methods are listed
// individually (rather than embedding the composed sub-interfaces) because the
// system domain pulls only a handful of methods from each of several small
// interfaces (StatsStore, HashBlocklistStore, UserPreferenceStore,
// LifecycleStore, OperationStore, AuthorStore, BookStore, SystemActivityStore).
//
// healthCheck / getDashboard / getQuickQueries additionally perform dynamic
// type-assertions on the live store value (GetBrokenFileCount, Unwrap,
// GetQuickQueryCounts) — those hit the dynamic type behind this interface, not
// the static method set here, so they are intentionally NOT listed.
//
// database.SettingsStore is embedded (rather than method-listed) because
// factoryReset passes the store opaquely to config.SaveConfigToDatabase, whose
// param is a database.SettingsStore — structural satisfaction requires the full
// SettingsStore method set, not a hand-list.
type SystemStore interface {
	database.SettingsStore // factoryReset -> config.SaveConfigToDatabase

	// health metrics
	CountPrimaryBooks() (int, error)   // BookStore
	CountAuthors() (int, error) // StatsStore
	CountSeries() (int, error)  // StatsStore

	// announcements
	GetAllAuthors() ([]database.Author, error) // AuthorStore
	// GetBooksByAuthorIDWithRoleCore is Core-typed (STOREFID P3-W2b) — see
	// docs/specs/2026-07-05-store-getter-fidelity-unification.md.
	GetBooksByAuthorIDWithRoleCore(authorID int) ([]database.BookCore, error) // AuthorStore
	GetAllBooks(limit, offset int) ([]database.Book, error)                  // BookStore

	// activity log
	GetSystemActivityLogs(source string, limit int) ([]database.SystemActivityLog, error) // SystemActivityStore

	// reset / factory-reset
	Reset() error             // LifecycleStore
	InvalidateLibraryStats()  // StatsStore

	// dashboard
	GetDashboardStats() (*database.DashboardStats, error)        // StatsStore
	GetRecentOperations(limit int) ([]database.Operation, error) // OperationStore

	// blocked hashes
	GetAllBlockedHashes() ([]database.DoNotImport, error) // HashBlocklistStore
	AddBlockedHash(hash, reason string) error             // HashBlocklistStore
	RemoveBlockedHash(hash string) error                  // HashBlocklistStore

	// user preferences
	GetUserPreference(key string) (*database.UserPreference, error) // UserPreferenceStore
	SetUserPreference(key, value string) error                      // UserPreferenceStore
}

// SystemService is the narrow *sysinfo.SystemService subset used by
// getSystemStatus / getSystemLogs.
type SystemService interface {
	CollectSystemStatus() (*sysinfo.SystemStatus, error)
	CollectSystemLogs(level, search string, limit, offset int) ([]sysinfo.SystemLogEntry, int, error)
}

// ConfigUpdateService is the narrow *config.UpdateService subset used by
// updateConfig.
type ConfigUpdateService interface {
	MaskSecrets(cfg config.Config) config.Config
	UpdateConfig(payload map[string]any) (int, map[string]any)
}

// PluginHealthChecker is the narrow *plugin.Registry subset used by
// getSystemStatus to attach plugin health to the status response.
type PluginHealthChecker interface {
	HealthCheckAll() map[string]error
}

// EventStreamer is the narrow *realtime.EventHub subset used by handleEvents to
// serve the Server-Sent Events stream.
type EventStreamer interface {
	HandleSSE(c *gin.Context)
}

// OperationLogsProvider lets getSystemLogs delegate the operation_id branch to
// the already-migrated operations-domain handler without importing the
// operations sub-package (avoiding coupling). The controller passes
// *operations.Handler, which satisfies this interface via its GetOperationLogs
// method.
type OperationLogsProvider interface {
	GetOperationLogs(c *gin.Context)
}
