// file: internal/sysinfo/dashboard_service.go
// version: 1.4.0
// guid: 9c0d1e2f-3a4b-5c6d-7e8f-9a0b1c2d3e4f
// last-edited: 2026-08-18

package sysinfo

import (
	"fmt"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// dashboardStore is the narrow slice of database.Store this service uses.
// dashboardStore is what this package actually calls, measured by emptying the
// interface and reading the compiler's enumeration: 2 methods. It was a
// pure pass-through of database.* embeds — 13 methods, none of them declared
// here and almost none of them used.
type dashboardStore interface {
	GetDashboardStats() (*database.DashboardStats, error)
	GetPlaylistBySeriesID(seriesID int) (*database.Playlist, error)
}

// DashboardService handles dashboard statistics and metrics collection
type DashboardService struct {
	db dashboardStore
}

// NewDashboardService creates a new dashboard service
func NewDashboardService(db dashboardStore) *DashboardService {
	return &DashboardService{db: db}
}

// DashboardMetrics represents aggregated dashboard metrics
type DashboardMetrics struct {
	Books     int   `json:"books"`
	Files     int   `json:"files"`
	Authors   int   `json:"authors"`
	Series    int   `json:"series"`
	Playlists int   `json:"playlists"`
	Timestamp int64 `json:"timestamp"`
}

// HealthCheckResponse represents the health check response
type HealthCheckResponse struct {
	Status       string           `json:"status"`
	Timestamp    int64            `json:"timestamp"`
	Version      string           `json:"version"`
	DatabaseType string           `json:"database_type"`
	Metrics      DashboardMetrics `json:"metrics"`
	PartialError string           `json:"partial_error,omitempty"`
}

// CollectDashboardMetrics gathers all dashboard metrics from the cached LibraryStats.
func (ds *DashboardService) CollectDashboardMetrics() (*DashboardMetrics, error) {
	metrics := &DashboardMetrics{Timestamp: time.Now().Unix()}

	if ds.db == nil {
		return metrics, fmt.Errorf("database not initialized")
	}

	stats, err := ds.db.GetDashboardStats()
	if err != nil {
		return metrics, err
	}
	metrics.Books = stats.TotalBooks
	metrics.Files = stats.TotalFiles
	metrics.Authors = stats.TotalAuthors
	metrics.Series = stats.TotalSeries

	if playlists, err := ds.db.GetPlaylistBySeriesID(0); err == nil && playlists != nil {
		metrics.Playlists = 1
	}
	return metrics, nil
}

// GetHealthCheckResponse generates a health check response with metrics
func (ds *DashboardService) GetHealthCheckResponse(version string) *HealthCheckResponse {
	metrics, dbErr := ds.CollectDashboardMetrics()
	if metrics == nil {
		metrics = &DashboardMetrics{Timestamp: time.Now().Unix()}
	}

	response := &HealthCheckResponse{
		Status:       "ok",
		Timestamp:    time.Now().Unix(),
		Version:      version,
		DatabaseType: config.AppConfig.DatabaseType,
		Metrics:      *metrics,
	}

	if dbErr != nil {
		response.PartialError = dbErr.Error()
		response.Status = "degraded"
	}

	return response
}

// CollectLibraryStats returns the full cached LibraryStats.
func (ds *DashboardService) CollectLibraryStats() (*database.LibraryStats, error) {
	if ds.db == nil {
		return nil, fmt.Errorf("database not initialized")
	}
	return ds.db.GetDashboardStats()
}

// QuickMetrics returns a quick set of metrics for display
type QuickMetrics struct {
	BookCount   int `json:"book_count"`
	FileCount   int `json:"file_count"`
	AuthorCount int `json:"author_count"`
	SeriesCount int `json:"series_count"`
}

// CollectQuickMetrics gathers metrics for quick display from the cached LibraryStats.
func (ds *DashboardService) CollectQuickMetrics() *QuickMetrics {
	metrics := &QuickMetrics{}
	if ds.db == nil {
		return metrics
	}
	stats, err := ds.db.GetDashboardStats()
	if err != nil {
		return metrics
	}
	metrics.BookCount = stats.TotalBooks
	metrics.FileCount = stats.TotalFiles
	metrics.AuthorCount = stats.TotalAuthors
	metrics.SeriesCount = stats.TotalSeries
	return metrics
}
