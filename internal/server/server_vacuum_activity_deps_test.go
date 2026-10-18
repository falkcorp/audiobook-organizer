// file: internal/server/server_vacuum_activity_deps_test.go
// version: 1.0.0
// guid: 5b0f2c7e-91d4-4e63-a8b1-7c3e9d2f4a60
// last-edited: 2026-10-18

package server

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/activity"
	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// internal/database cannot import diskstats (SDK closure), so the server wires the
// real free-space probe in. Without that wiring the preview would always say
// would_refuse; this proves the production path reads a real figure.
func TestServerVacuumActivityDB_WiresTheRealFreeSpaceProbe(t *testing.T) {
	store, err := database.OpenSQLiteActivityStore(filepath.Join(t.TempDir(), "activity.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	s := &Server{activityService: activity.NewService(store)}

	rep, err := s.VacuumActivityDB(context.Background(), true)
	if err != nil || !rep.Supported {
		t.Fatalf("preview = %+v, %v", rep, err)
	}
	if rep.Before.PreflightNote != "" || rep.Before.FreeBytes <= 0 {
		t.Errorf("free-space probe not wired: %+v", rep.Before)
	}
}
