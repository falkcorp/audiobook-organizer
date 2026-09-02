// file: internal/sysinfo/service_test.go
// version: 1.0.1
// guid: g7h8i9j0-k1l2-m3n4-o5p6-q7r8s9t0u1v2
// last-edited: 2026-09-02

package sysinfo

import (
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

func TestSystemService_CollectSystemStatus_Success(t *testing.T) {
	mockDB := &database.MockStore{
		GetAllImportPathsFunc: func() ([]database.ImportPath, error) {
			return []database.ImportPath{}, nil
		},
	}
	service := NewSystemService(mockDB, "test", nil)

	status, err := service.CollectSystemStatus()

	if err != nil {
		t.Errorf("expected no error, got %v", err)
	}
	if status == nil {
		t.Error("expected status, got nil")
	}
}

func TestSystemService_FilterLogsBySearch_Match(t *testing.T) {
	service := NewSystemService(&database.MockStore{}, "test", nil)

	logs := []database.OperationLog{
		{
			Message: "Scanning folder /library",
		},
	}

	filtered := service.FilterLogsBySearch(logs, "Scanning")

	if len(filtered) != 1 {
		t.Errorf("expected 1 result, got %d", len(filtered))
	}
}

func TestSystemService_FilterLogsBySearch_NoMatch(t *testing.T) {
	service := NewSystemService(&database.MockStore{}, "test", nil)

	logs := []database.OperationLog{
		{
			Message: "Scanning folder",
		},
	}

	filtered := service.FilterLogsBySearch(logs, "Organizing")

	if len(filtered) != 0 {
		t.Errorf("expected 0 results, got %d", len(filtered))
	}
}

func TestSystemService_PaginateLogs_Success(t *testing.T) {
	service := NewSystemService(&database.MockStore{}, "test", nil)

	logs := make([]database.OperationLog, 100)
	for i := range 100 {
		logs[i] = database.OperationLog{Message: "Log"}
	}

	paginated := service.PaginateLogs(logs, 1, 20)

	if len(paginated) != 20 {
		t.Errorf("expected 20 logs, got %d", len(paginated))
	}
}
