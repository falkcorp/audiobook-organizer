// file: internal/sysinfo/service.go
// version: 1.2.0
// guid: h8i9j0k1-l2m3-n4o5-p6q7-r8s9t0u1v2w3
// last-edited: 2026-08-18

package sysinfo

import (
	"fmt"
	"runtime"
	"sort"
	"strings"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// SystemServiceStore is the narrow slice of database.Store this service uses.
// SystemServiceStore is what this package actually calls, measured by emptying the
// interface and reading the compiler's enumeration: 4 methods. It was a
// pure pass-through of database.* embeds — 45 methods, none of them declared
// here and almost none of them used.
type SystemServiceStore interface {
	GetAllImportPaths() ([]database.ImportPath, error)
	GetDashboardStats() (*database.DashboardStats, error)
	GetOperationLogs(operationID string) ([]database.OperationLog, error)
	GetRecentOperations(limit int) ([]database.Operation, error)
}

// LibrarySizesFn computes library and import-path disk usage.
type LibrarySizesFn func(rootDir string, importFolders []database.ImportPath) (librarySize, importSize int64)

type SystemService struct {
	db         SystemServiceStore
	version    string
	libSizesFn LibrarySizesFn
	startTime  time.Time
}

// NewSystemService constructs a SystemService.
// version is the application version string (e.g. "1.2.3" or "dev").
// libSizesFn is called to compute library/import disk sizes; if nil a no-op
// implementation is used (both sizes returned as 0).
func NewSystemService(db SystemServiceStore, version string, libSizesFn LibrarySizesFn) *SystemService {
	if libSizesFn == nil {
		libSizesFn = func(_ string, _ []database.ImportPath) (int64, int64) { return 0, 0 }
	}
	return &SystemService{
		db:         db,
		version:    version,
		libSizesFn: libSizesFn,
		startTime:  time.Now(),
	}
}

type SystemStatus struct {
	Status              string               `json:"status"`
	Version             string               `json:"version"`
	LibraryBookCount    int                  `json:"library_book_count"`
	ImportBookCount     int                  `json:"import_book_count"`
	TotalBookCount      int                  `json:"total_book_count"`
	TotalFileCount      int                  `json:"total_file_count"`
	AuthorCount         int                  `json:"author_count"`
	SeriesCount         int                  `json:"series_count"`
	LibrarySizeBytes    int64                `json:"library_size_bytes"`
	ImportSizeBytes     int64                `json:"import_size_bytes"`
	TotalSizeBytes      int64                `json:"total_size_bytes"`
	RootDirectory       string               `json:"root_directory"`
	Library             SystemLibraryStatus  `json:"library"`
	ImportPaths         SystemImportStatus   `json:"import_paths"`
	Memory              SystemMemoryStatus   `json:"memory"`
	Runtime             SystemRuntimeStatus  `json:"runtime"`
	Operations          SystemOperationsInfo `json:"operations"`
	PluginHealth        map[string]string    `json:"plugin_health,omitempty"`
	AppUptimeSeconds    float64              `json:"app_uptime_seconds"`
	SystemUptimeSeconds float64              `json:"system_uptime_seconds"`
	BrokenFileCount     *int                 `json:"broken_file_count,omitempty"`
}

type SystemLibraryStatus struct {
	BookCount   int    `json:"book_count"`
	FolderCount int    `json:"folder_count"`
	TotalSize   int64  `json:"total_size"`
	Path        string `json:"path"`
}

type SystemImportStatus struct {
	BookCount   int   `json:"book_count"`
	FolderCount int   `json:"folder_count"`
	TotalSize   int64 `json:"total_size"`
}

type SystemMemoryStatus struct {
	AllocBytes      uint64 `json:"alloc_bytes"`
	TotalAllocBytes uint64 `json:"total_alloc_bytes"`
	SysBytes        uint64 `json:"sys_bytes"`
	NumGC           uint32 `json:"num_gc"`
	HeapAlloc       uint64 `json:"heap_alloc"`
	HeapSys         uint64 `json:"heap_sys"`
	SystemTotal     uint64 `json:"system_total"`
}

type SystemRuntimeStatus struct {
	GoVersion    string `json:"go_version"`
	NumGoroutine int    `json:"num_goroutine"`
	NumCPU       int    `json:"num_cpu"`
	OS           string `json:"os"`
	Arch         string `json:"arch"`
}

type SystemOperationsInfo struct {
	Recent []database.Operation `json:"recent"`
}

type SystemLogEntry struct {
	OperationID string    `json:"operation_id"`
	Timestamp   time.Time `json:"timestamp"`
	Level       string    `json:"level"`
	Message     string    `json:"message"`
	Details     *string   `json:"details,omitempty"`
}

// CollectSystemStatus gathers system status information
func (ss *SystemService) CollectSystemStatus() (*SystemStatus, error) {
	if ss.db == nil {
		return nil, fmt.Errorf("database not initialized")
	}

	importFolders, err := ss.db.GetAllImportPaths()
	if err != nil {
		importFolders = []database.ImportPath{}
	}

	rootDir := config.AppConfig.RootDir

	// One cached call covers books, files, authors, series, and size splits.
	dbStats, _ := ss.db.GetDashboardStats()
	if dbStats == nil {
		dbStats = &database.LibraryStats{}
	}

	recentOps, err := ss.db.GetRecentOperations(5)
	if err != nil {
		recentOps = []database.Operation{}
	}

	var memStats runtime.MemStats
	runtime.ReadMemStats(&memStats)

	// Use the DB-aggregated sizes from LibraryStats (computed in memdb at
	// ComputeLibraryStats time, free as part of the cache). The previous
	// filesystem-walk path took ~28s on cold cache for a 50K-book library
	// and gated the first /system/status request after restart on it.
	// Sonarr/Radarr work the same way: trust DB sizes after the initial
	// scan populates them, refresh on book mutation, never re-walk on read.
	librarySize := dbStats.OrganizedSize
	importSize := dbStats.UnorganizedSize
	totalSize := librarySize + importSize
	_ = importFolders // libSizesFn dependency now unused; kept on the type for backwards compat (tests, callers)
	_ = ss.libSizesFn

	brokenFiles := dbStats.BrokenFiles
	status := &SystemStatus{
		Status:           "running",
		Version:          ss.version,
		LibraryBookCount: dbStats.OrganizedBooks,
		ImportBookCount:  dbStats.UnorganizedBooks,
		TotalBookCount:   dbStats.TotalBooks,
		TotalFileCount:   dbStats.TotalFiles,
		AuthorCount:      dbStats.TotalAuthors,
		SeriesCount:      dbStats.TotalSeries,
		BrokenFileCount:  &brokenFiles,
		LibrarySizeBytes: librarySize,
		ImportSizeBytes:  importSize,
		TotalSizeBytes:   totalSize,
		RootDirectory:    rootDir,
		Library: SystemLibraryStatus{
			BookCount:   dbStats.OrganizedBooks,
			FolderCount: 1,
			TotalSize:   librarySize,
			Path:        rootDir,
		},
		ImportPaths: SystemImportStatus{
			BookCount:   dbStats.UnorganizedBooks,
			FolderCount: len(importFolders),
			TotalSize:   importSize,
		},
		Memory: SystemMemoryStatus{
			AllocBytes:      memStats.Alloc,
			TotalAllocBytes: memStats.TotalAlloc,
			SysBytes:        memStats.Sys,
			NumGC:           memStats.NumGC,
			HeapAlloc:       memStats.HeapAlloc,
			HeapSys:         memStats.HeapSys,
			SystemTotal:     GetTotalMemory(),
		},
		Runtime: SystemRuntimeStatus{
			GoVersion:    runtime.Version(),
			NumGoroutine: runtime.NumGoroutine(),
			NumCPU:       runtime.NumCPU(),
			OS:           runtime.GOOS,
			Arch:         runtime.GOARCH,
		},
		Operations: SystemOperationsInfo{
			Recent: recentOps,
		},
		AppUptimeSeconds:    time.Since(ss.startTime).Seconds(),
		SystemUptimeSeconds: GetSystemUptimeSeconds(),
	}

	return status, nil
}

// FilterLogsBySearch filters logs by search term (case-insensitive)
func (ss *SystemService) FilterLogsBySearch(logs []database.OperationLog, searchTerm string) []database.OperationLog {
	if searchTerm == "" {
		return logs
	}

	searchLower := strings.ToLower(searchTerm)
	filtered := make([]database.OperationLog, 0)

	for _, log := range logs {
		if strings.Contains(strings.ToLower(log.Message), searchLower) {
			filtered = append(filtered, log)
		}
	}

	return filtered
}

// SortLogsByTimestamp sorts logs by timestamp (descending)
func (ss *SystemService) SortLogsByTimestamp(logs []database.OperationLog) []database.OperationLog {
	sorted := make([]database.OperationLog, len(logs))
	copy(sorted, logs)

	// Bubble sort for small sets
	for i := 0; i < len(sorted); i++ {
		for j := 0; j < len(sorted)-1-i; j++ {
			if sorted[j].CreatedAt.Before(sorted[j+1].CreatedAt) {
				sorted[j], sorted[j+1] = sorted[j+1], sorted[j]
			}
		}
	}

	return sorted
}

// PaginateLogs returns a subset of logs for the given page
func (ss *SystemService) PaginateLogs(logs []database.OperationLog, page, pageSize int) []database.OperationLog {
	if page < 1 {
		page = 1
	}
	if pageSize < 1 {
		pageSize = 20
	}

	start := (page - 1) * pageSize
	if start >= len(logs) {
		return []database.OperationLog{}
	}

	end := start + pageSize
	if end > len(logs) {
		end = len(logs)
	}

	return logs[start:end]
}

// GetFormattedUptime returns uptime as a formatted string
func (ss *SystemService) GetFormattedUptime(startTime time.Time) string {
	return time.Since(startTime).String()
}

// CollectSystemLogs gathers logs for recent operations with filtering and pagination.
func (ss *SystemService) CollectSystemLogs(level, search string, limit, offset int) ([]SystemLogEntry, int, error) {
	if ss.db == nil {
		return nil, 0, fmt.Errorf("database not initialized")
	}

	if limit <= 0 {
		limit = 100
	}
	if offset < 0 {
		offset = 0
	}

	operations, err := ss.db.GetRecentOperations(50)
	if err != nil {
		return nil, 0, fmt.Errorf("failed to fetch operations")
	}

	var allLogs []SystemLogEntry
	searchLower := strings.ToLower(search)

	for _, op := range operations {
		logs, err := ss.db.GetOperationLogs(op.ID)
		if err != nil {
			continue
		}

		for _, logEntry := range logs {
			if level != "" && logEntry.Level != level {
				continue
			}

			if searchLower != "" {
				found := strings.Contains(strings.ToLower(logEntry.Message), searchLower)
				if !found && logEntry.Details != nil {
					found = strings.Contains(strings.ToLower(*logEntry.Details), searchLower)
				}
				if !found {
					continue
				}
			}

			allLogs = append(allLogs, SystemLogEntry{
				OperationID: op.ID,
				Timestamp:   logEntry.CreatedAt,
				Level:       logEntry.Level,
				Message:     logEntry.Message,
				Details:     logEntry.Details,
			})
		}
	}

	// Newest first. This runs over the FULL collected set before the
	// offset/limit slice below, so it is the whole log population, not a page --
	// the O(n^2) swap loop it replaces scaled with every operation retained.
	sort.SliceStable(allLogs, func(a, b int) bool {
		return allLogs[a].Timestamp.After(allLogs[b].Timestamp)
	})

	total := len(allLogs)
	start := offset
	end := offset + limit
	if start > total {
		start = total
	}
	if end > total {
		end = total
	}

	return allLogs[start:end], total, nil
}
