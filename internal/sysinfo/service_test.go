// file: internal/sysinfo/service_test.go
// version: 1.2.0
// guid: g7h8i9j0-k1l2-m3n4-o5p6-q7r8s9t0u1v2
// last-edited: 2026-10-26

package sysinfo

import (
	"errors"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

func TestSystemService_CollectSystemStatus_Success(t *testing.T) {
	mockDB := &database.MockStore{
		GetAllImportPathsFunc: func() ([]database.ImportPath, error) {
			return []database.ImportPath{}, nil
		},
	}
	service := NewSystemService(mockDB, "test", nil)

	status, err := service.CollectSystemStatus(func(*database.OperationV2Row) bool { return true })

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

type recentRowsStore struct {
	rows []database.OperationV2Row
	err  error
}

func (r recentRowsStore) ListOperationsV2Since(_ time.Time, limit int) ([]database.OperationV2Row, error) {
	if r.err != nil {
		return nil, r.err
	}
	if limit < len(r.rows) {
		return r.rows[:limit], nil
	}
	return r.rows, nil
}

// RecentOperations leaves out the rows the caller may not read, still fills the
// list from the window behind them, drops result_data from every row, serves
// nothing without a check, and serves nothing on a store error.
func TestRecentOperations(t *testing.T) {
	res := `{"params":{"target_user_id":"user-invented-1"}}`
	var rows []database.OperationV2Row
	for i := range 8 {
		def := "library.scan"
		if i < 6 {
			def = "repairs.apply"
		}
		rows = append(rows, database.OperationV2Row{ID: "op-" + strconv.Itoa(i), DefID: def, Status: "completed", ResultData: &res})
	}
	store := recentRowsStore{rows: rows}
	notRepairs := func(row *database.OperationV2Row) bool { return row.DefID != "repairs.apply" }

	got := RecentOperations(store, time.Now(), notRepairs)
	if len(got) != 2 || got[0].ID != "op-6" || got[1].ID != "op-7" {
		t.Fatalf("the six hidden rows must not shorten the list: %+v", got)
	}
	for _, op := range got {
		if op.ResultData != nil {
			t.Errorf("%s serves result_data", op.ID)
		}
	}

	all := RecentOperations(store, time.Now(), func(*database.OperationV2Row) bool { return true })
	if len(all) != RecentOperationsShown {
		t.Errorf("a full list is %d rows, got %d", RecentOperationsShown, len(all))
	}
	for _, op := range all {
		if op.ResultData != nil {
			t.Errorf("%s serves result_data to a caller who may read the op", op.ID)
		}
	}

	if got := RecentOperations(store, time.Now(), nil); got == nil || len(got) != 0 {
		t.Errorf("no check serves no operations, got %+v", got)
	}
	failing := recentRowsStore{err: errors.New("store down")}
	if got := RecentOperations(failing, time.Now(), notRepairs); got == nil || len(got) != 0 {
		t.Errorf("a store error serves an empty, non-nil list, got %+v", got)
	}
}

// CollectSystemLogs applies the caller's read check before counting and
// slicing, so the total and every page agree with what the caller may read, and
// a nil check serves nothing.
func TestCollectSystemLogs_MayRead(t *testing.T) {
	base := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	store := &database.MockStore{
		GetRecentOperationsFunc: func(int) ([]database.Operation, error) {
			return []database.Operation{{ID: "op-year-one"}}, nil
		},
		GetOperationLogsFunc: func(string) ([]database.OperationLog, error) {
			var logs []database.OperationLog
			for i := range 6 {
				msg := "scan Year One " + strconv.Itoa(i)
				if i%2 == 1 {
					msg = "merge for user 01INVENTEDUSER " + strconv.Itoa(i)
				}
				logs = append(logs, database.OperationLog{Level: "info", Message: msg, CreatedAt: base.Add(time.Duration(i) * time.Minute)})
			}
			return logs, nil
		},
	}
	svc := NewSystemService(store, "test", nil)
	notUsers := func(e SystemLogEntry) bool { return !strings.Contains(e.Message, "user") }

	got, total, err := svc.CollectSystemLogs("", "", 2, 0, notUsers)
	if err != nil {
		t.Fatal(err)
	}
	if total != 3 || len(got) != 2 {
		t.Fatalf("total=%d page=%d, want 3 and 2: the filter runs before the count and the slice", total, len(got))
	}
	got, _, _ = svc.CollectSystemLogs("", "", 2, 2, notUsers)
	if len(got) != 1 || strings.Contains(got[0].Message, "user") {
		t.Fatalf("second page = %+v, want the one remaining readable line", got)
	}

	got, total, _ = svc.CollectSystemLogs("", "", 10, 0, func(SystemLogEntry) bool { return true })
	if total != 6 || len(got) != 6 {
		t.Fatalf("an allow-all check serves every line, got total=%d page=%d", total, len(got))
	}
	got, total, _ = svc.CollectSystemLogs("", "", 10, 0, nil)
	if total != 0 || len(got) != 0 {
		t.Fatalf("a nil check must serve nothing, got total=%d page=%d", total, len(got))
	}
}
