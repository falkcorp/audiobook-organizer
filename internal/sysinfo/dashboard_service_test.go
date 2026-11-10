// file: internal/sysinfo/dashboard_service_test.go
// version: 1.1.1
// guid: a0b1c2d3-e4f5-6a7b-8c9d-0e1f2a3b4c5d

package sysinfo

import (
	"testing"
	"time"

	database "github.com/falkcorp/audiobook-organizer/internal/books/store"
)

func statsFor(books, files, authors, series, organized, unorganized int) *database.LibraryStats {
	return &database.LibraryStats{
		TotalBooks:         books,
		TotalFiles:         files,
		TotalAuthors:       authors,
		TotalSeries:        series,
		OrganizedBooks:     organized,
		UnorganizedBooks:   unorganized,
		StateDistribution:  map[string]int{},
		FormatDistribution: map[string]int{},
		BooksByImportPath:  map[int]int{},
		SizeByImportPath:   map[int]int64{},
		ComputedAt:         time.Now(),
	}
}

func TestDashboardService_CollectDashboardMetrics_Success(t *testing.T) {
	mockDB := &database.MockStore{
		GetDashboardStatsFunc: func() (*database.DashboardStats, error) {
			return statsFor(42, 10, 2, 3, 0, 0), nil
		},
	}

	service := NewDashboardService(mockDB)
	metrics, err := service.CollectDashboardMetrics()

	if err != nil {
		t.Errorf("expected no error, got %v", err)
	}
	if metrics.Books != 42 {
		t.Errorf("expected 42 books, got %d", metrics.Books)
	}
	if metrics.Authors != 2 {
		t.Errorf("expected 2 authors, got %d", metrics.Authors)
	}
	if metrics.Series != 3 {
		t.Errorf("expected 3 series, got %d", metrics.Series)
	}
}

func TestDashboardService_CollectDashboardMetrics_ZeroValues(t *testing.T) {
	mockDB := &database.MockStore{
		GetDashboardStatsFunc: func() (*database.DashboardStats, error) {
			return statsFor(0, 0, 0, 0, 0, 0), nil
		},
	}

	service := NewDashboardService(mockDB)
	metrics, err := service.CollectDashboardMetrics()

	if err != nil {
		t.Errorf("expected no error, got %v", err)
	}
	if metrics == nil {
		t.Fatal("expected metrics, got nil")
	}
	if metrics.Books != 0 || metrics.Authors != 0 || metrics.Series != 0 {
		t.Error("expected all zero metrics")
	}
}

func TestDashboardService_GetHealthCheckResponse_Success(t *testing.T) {
	mockDB := &database.MockStore{
		GetDashboardStatsFunc: func() (*database.DashboardStats, error) {
			return statsFor(10, 5, 0, 0, 0, 0), nil
		},
	}

	service := NewDashboardService(mockDB)
	response := service.GetHealthCheckResponse("1.0.0")

	if response == nil {
		t.Fatal("expected response, got nil")
	}
	if response.Status != "ok" {
		t.Errorf("expected status 'ok', got %q", response.Status)
	}
	if response.Version != "1.0.0" {
		t.Errorf("expected version '1.0.0', got %q", response.Version)
	}
	if response.Metrics.Books != 10 {
		t.Errorf("expected 10 books in metrics, got %d", response.Metrics.Books)
	}
}

func TestDashboardService_CollectLibraryStats_Success(t *testing.T) {
	mockDB := &database.MockStore{
		GetDashboardStatsFunc: func() (*database.DashboardStats, error) {
			return statsFor(100, 0, 1, 1, 1, 1), nil
		},
	}

	service := NewDashboardService(mockDB)
	stats, err := service.CollectLibraryStats()

	if err != nil {
		t.Errorf("expected no error, got %v", err)
	}
	if stats.TotalBooks != 100 {
		t.Errorf("expected 100 total books, got %d", stats.TotalBooks)
	}
	if stats.OrganizedBooks != 1 {
		t.Errorf("expected 1 organized book, got %d", stats.OrganizedBooks)
	}
	if stats.UnorganizedBooks != 1 {
		t.Errorf("expected 1 unorganized book, got %d", stats.UnorganizedBooks)
	}
}

func TestDashboardService_CollectQuickMetrics_Success(t *testing.T) {
	mockDB := &database.MockStore{
		GetDashboardStatsFunc: func() (*database.DashboardStats, error) {
			return statsFor(50, 0, 3, 0, 0, 0), nil
		},
	}

	service := NewDashboardService(mockDB)
	metrics := service.CollectQuickMetrics()

	if metrics == nil {
		t.Fatal("expected metrics, got nil")
	}
	if metrics.BookCount != 50 {
		t.Errorf("expected 50 books, got %d", metrics.BookCount)
	}
	if metrics.AuthorCount != 3 {
		t.Errorf("expected 3 authors, got %d", metrics.AuthorCount)
	}
	if metrics.SeriesCount != 0 {
		t.Errorf("expected 0 series, got %d", metrics.SeriesCount)
	}
}
