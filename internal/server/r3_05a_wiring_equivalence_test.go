// file: internal/server/r3_05a_wiring_equivalence_test.go
// version: 0.1.0
// guid: 5c0e7a41-2b8d-4f63-9a15-d4e6b1f07c28
// last-edited: 2026-11-22

package server

import (
	"fmt"
	"os"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/stretchr/testify/require"
)

// fieldPtr returns the address held by the named field of the struct x points
// to. The field is read through reflection because the services keep their
// collaborators unexported; this only compares identity, it never reads a
// value. An interface-typed field yields its concrete pointer.
func fieldPtr(t *testing.T, x any, field string) uintptr {
	t.Helper()
	v := reflect.ValueOf(x)
	require.Equal(t, reflect.Pointer, v.Kind(), "%T is not a pointer", x)
	f := v.Elem().FieldByName(field)
	require.True(t, f.IsValid(), "%T has no field %q", x, field)
	if f.Kind() == reflect.Interface {
		f = f.Elem()
	}
	require.Equal(t, reflect.Pointer, f.Kind(), "%T.%s is not a pointer", x, field)
	return f.Pointer()
}

// TestR3_05a_NewServerWiresTheClusters is the R3-05a behaviour-neutrality pin
// (dark proof N). It passes on the container-built wiring and must pass
// unchanged on the constructor-built wiring that replaces it: every handle the
// server holds for the nine clusters is non-nil, and the cross-service
// handles each cluster was given are the same instances the server holds.
//
//   - batch purges a trashed book through the audiobook service (the purger),
//   - audiobook records through the activity service,
//   - scan batches events through the activity writer and dedups on the
//     embedding store,
//   - quarantine publishes on the server's event bus and reads the live
//     config.
func TestR3_05a_NewServerWiresTheClusters(t *testing.T) {
	s, cleanup := setupTestServer(t)
	defer cleanup()

	require.NotNil(t, s.workService, "work")
	require.NotNil(t, s.audiobookService, "audiobook")
	require.NotNil(t, s.organizeService, "organize")
	require.NotNil(t, s.quarantineSvc, "quarantine")
	require.NotNil(t, s.scanService, "scan")
	require.NotNil(t, s.mergeService, "merge")
	require.NotNil(t, s.importPathService, "importpath")
	require.NotNil(t, s.batchService, "batch")
	require.NotNil(t, s.itunesSvc, "itunes")

	// The cross-service collaborators must exist for the identity checks
	// below to mean anything (the test server sets DatabasePath, so the
	// activity pair and the embedding store are built).
	require.NotNil(t, s.activityService, "activity")
	require.NotNil(t, s.activityWriter, "activitywriter")
	require.NotNil(t, s.embeddingStore, "embeddingstore")
	require.NotNil(t, s.eventBus, "eventbus")

	require.Equal(t, reflect.ValueOf(s.audiobookService).Pointer(), fieldPtr(t, s.batchService, "purger"),
		"batch's trash purger is the server's audiobook service")
	require.Equal(t, reflect.ValueOf(s.activityService).Pointer(), fieldPtr(t, s.audiobookService, "activityService"),
		"audiobook records through the server's activity service")
	require.Equal(t, reflect.ValueOf(s.activityWriter).Pointer(), fieldPtr(t, s.scanService, "activityWriter"),
		"scan batches through the server's activity writer")
	require.Equal(t, reflect.ValueOf(s.embeddingStore).Pointer(), fieldPtr(t, s.scanService, "embedStore"),
		"scan dedups on the server's embedding store")
	require.Equal(t, reflect.ValueOf(s.eventBus).Pointer(), fieldPtr(t, s.quarantineSvc, "events"),
		"quarantine publishes on the server's event bus")
	require.Equal(t, reflect.ValueOf(&config.AppConfig).Pointer(), fieldPtr(t, s.quarantineSvc, "cfg"),
		"quarantine reads the live config")
}

// TestR3_05a_RouteDump writes the sorted method+path list of the router to
// the file named by R3_05A_ROUTE_DUMP, so a before/after diff proves the slice
// serves the same routes. It does nothing when the variable is unset.
func TestR3_05a_RouteDump(t *testing.T) {
	out := os.Getenv("R3_05A_ROUTE_DUMP")
	if out == "" {
		t.Skip("R3_05A_ROUTE_DUMP not set")
	}
	s, cleanup := setupTestServer(t)
	defer cleanup()
	var lines []string
	for _, r := range s.router.Routes() {
		lines = append(lines, fmt.Sprintf("%s %s", r.Method, r.Path))
	}
	slices.Sort(lines)
	require.NoError(t, os.WriteFile(out, []byte(strings.Join(lines, "\n")+"\n"), 0o600))
}
