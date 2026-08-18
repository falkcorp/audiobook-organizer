// file: internal/database/iface_system.go
// version: 1.0.0
// guid: 92c85d7d-c1fb-43c0-a0a4-2ef742107420
// last-edited: 2026-08-18

package database

import (
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
	Optimize() error
	GetScanCacheMap() (map[string]ScanCacheEntry, error)
	UpdateScanCache(bookID string, mtime int64, size int64) error
	MarkNeedsRescan(bookID string) error
	GetDirtyBookFolders() ([]string, error)
}

// RawKVStore covers the low-level key-value escape hatch.
type RawKVStore interface {
	SetRaw(key string, value []byte) error
	GetRaw(key string) ([]byte, error)
	DeleteRaw(key string) error
	ScanPrefix(prefix string) ([]KVPair, error)
	CountPrefix(prefix string) (int64, error)
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
