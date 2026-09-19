// file: internal/database/iface_system.go
// version: 1.3.0
// guid: 92c85d7d-c1fb-43c0-a0a4-2ef742107420
// last-edited: 2026-09-19

package database

import (
	"context"
	"time"
)

// Process-level, settings, and operational state.
//
// Split out of iface_misc.go on 2026-08-18, which held 27 interface
// declarations in one file. A file named `misc` is where wide interfaces go to
// avoid review: BookFileStore reached 27 methods while living there.

// SettingsStore covers persistent encrypted configuration.
type SettingsStore interface {
	GetSetting(key string) (*Setting, error)
	SetSetting(key, value, typ string, isSecret bool) error
	GetAllSettings() ([]Setting, error)
	DeleteSetting(key string) error
}

// StatsStore covers aggregate counts and dashboard metrics.
type StatsStore interface {
	CountFiles() (int, error)
	CountAuthors() (int, error)
	CountSeries() (int, error)
	GetBookCountsByLocation(rootDir string) (library, import_ int, err error)
	GetBookSizesByLocation(rootDir string) (librarySize, importSize int64, err error)
	GetDashboardStats() (*DashboardStats, error)
	// SetRootDir tells the store which directory is the organized library root,
	// used to split OrganizedBooks vs UnorganizedBooks in LibraryStats.
	SetRootDir(rootDir string)
	// InvalidateLibraryStats drops the cached LibraryStats so the next call
	// to GetDashboardStats triggers a fresh recompute.
	InvalidateLibraryStats()
}

// SystemActivityStore covers cross-cutting system activity log.
type SystemActivityStore interface {
	AddSystemActivityLog(source, level, message string) error
	GetSystemActivityLogs(source string, limit int) ([]SystemActivityLog, error)
	PruneSystemActivityLogs(olderThan time.Time) (int, error)
}

// MaintenanceStore covers database maintenance and scan-cache.
type MaintenanceStore interface {
	Optimize(ctx context.Context) error
	// CompactionStats lets a caller report real progress while Optimize runs.
	// Optimize is one blocking call with no callback, so without this a
	// long compaction is indistinguishable from a wedged one.
	CompactionStats() CompactionStats
	GetScanCacheMap() (map[string]ScanCacheEntry, error)
	// GetScanCacheMapContext is GetScanCacheMap that stops with ctx's error
	// once ctx is done. The scan's startup uses it so a stand-down cancel is
	// seen mid-load instead of after it.
	GetScanCacheMapContext(ctx context.Context) (map[string]ScanCacheEntry, error)
	UpdateScanCache(bookID string, mtime int64, size int64) error
	MarkNeedsRescan(bookID string) error
	GetDirtyBookFolders() ([]string, error)
	// GetDirtyBookFoldersContext is GetDirtyBookFolders that stops with ctx's
	// error once ctx is done (same reason as GetScanCacheMapContext).
	GetDirtyBookFoldersContext(ctx context.Context) ([]string, error)
}

// RawKVStore covers the low-level key-value escape hatch.
type RawKVStore interface {
	SetRaw(key string, value []byte) error
	GetRaw(key string) ([]byte, error)
	DeleteRaw(key string) error
	// ScanPrefix loads EVERY matching pair into memory. Use it only for
	// keyspaces known to stay small; anything that grows with the library
	// pages through ScanPrefixPage instead.
	ScanPrefix(prefix string) ([]KVPair, error)
	// ScanPrefixPage returns at most limit pairs whose keys start with
	// prefix and sort strictly after `after` ("" starts at the beginning),
	// in key order. next is the cursor for the following call: the last key
	// returned when more matching keys remain, "" when the scan is
	// exhausted (including when the final page is exactly full). limit must
	// be positive. Memory is bounded by limit, not by the keyspace.
	ScanPrefixPage(prefix, after string, limit int) (pairs []KVPair, next string, err error)
	CountPrefix(prefix string) (int64, error)
	// DeleteRawBatch deletes every key in one atomic, durably-synced write
	// (one fsync for the whole slice instead of one per key, which is what
	// DeleteRaw costs). Missing keys are not an error.
	DeleteRawBatch(keys []string) error
}

// LifecycleStore covers store startup/teardown.
type LifecycleStore interface {
	Close() error
	Reset() error
}

// ImportPathStore covers managed import path CRUD.
type ImportPathStore interface {
	GetAllImportPaths() ([]ImportPath, error)
	GetImportPathByID(id int) (*ImportPath, error)
	GetImportPathByPath(path string) (*ImportPath, error)
	CreateImportPath(path, name string) (*ImportPath, error)
	UpdateImportPath(id int, importPath *ImportPath) error
	DeleteImportPath(id int) error
	// CountBooksByPathPrefix returns the total number of books whose FilePath
	// starts with the given prefix. Used to persist accurate BookCount on
	// ImportPath after a scan without doing a live count on every read.
	CountBooksByPathPrefix(prefix string) (int, error)
}
