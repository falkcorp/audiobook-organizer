// file: internal/server/event_stream_live_access_test.go
// version: 1.1.1
// guid: 9c4e7a15-3d82-4b6f-a0e9-5f1b8d2c7a43
// last-edited: 2026-10-26

// An event stream stays open for hours and is checked per event against what
// the account holds now, not what it held when the stream opened: a caller whose
// role is lowered mid-stream stops receiving the events it may no longer read.

package server

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/auth"
	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// openStream opens path as token and returns a way to read what it carried and
// a function that closes it.
func openStream(t *testing.T, f *repairsOpAccessFixture, ctx context.Context, path, token string) (body func() string, wait func()) {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, path, nil).WithContext(ctx)
	req.Header.Set("Authorization", "Bearer "+token)
	rec := &syncRecorder{hdr: http.Header{}}
	done := make(chan struct{})
	go func() { defer close(done); f.srv.router.ServeHTTP(rec, req) }()
	require.Eventually(t, func() bool { return strings.Contains(rec.body(), ": heartbeat") },
		5*time.Second, 5*time.Millisecond, "the stream never opened: %s", rec.body())
	return rec.body, func() { <-done }
}

// setOpsManagerPermissions rewrites the permissions of the role opsManagerToken
// made, which is what an admin lowering a user's role does.
func setOpsManagerPermissions(t *testing.T, f *repairsOpAccessFixture, perms ...auth.Permission) {
	t.Helper()
	store, ok := f.srv.storeForWiring().(*database.PebbleStore)
	require.True(t, ok)
	role, err := store.GetRoleByID("role-ops-manager")
	require.NoError(t, err)
	require.NotNil(t, role)
	role.Permissions = perms
	require.NoError(t, store.UpdateRole(role))
}

// TestOperationsEvents_RoleLoweredMidStreamStopsTheEvents: /operations/events,
// opened by a caller with library.edit_metadata (so the default fixer's Repairs
// op is readable) and users.manage (so an owner-apply line is), carries both.
// Once users.manage is taken away the user-data op.log line stops while the
// Repairs op's clean line still arrives; once library.edit_metadata goes too the
// Repairs op's events stop, and the plain op's keep coming. Before this the
// verdicts were read once, at connect.
// Not parallel: it swaps the process-wide config.
func TestOperationsEvents_RoleLoweredMidStreamStopsTheEvents(t *testing.T) {
	f := newRepairsOpAccessFixture(t)
	f.srv.repairsOpAccess().WithLiveRecheck(0, nil) // no recheck bound: every event asks
	token := opsManagerToken(t, f)
	full := []auth.Permission{auth.PermLibraryView, auth.PermLibraryEditMetadata, auth.PermSettingsManage, auth.PermUsersManage}
	setOpsManagerPermissions(t, f, full...)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	body, wait := openStream(t, f, ctx, "/api/v1/operations/events", token)
	publish := func(op, msg string) {
		require.NoError(t, f.srv.opHub.Publish(ctx, "op.log", map[string]any{"op_id": op, "level": "info", "message": msg}))
	}
	arrives := func(marker string) bool {
		return strings.Contains(body(), marker)
	}
	// drained: everything published before it has been written or withheld.
	drained := func(tag string) {
		require.NoError(t, f.srv.opHub.Publish(ctx, "op.marker", map[string]any{"note": tag}))
		require.Eventually(t, func() bool { return arrives(tag) }, 5*time.Second, 5*time.Millisecond, "marker %s: %s", tag, body())
	}

	const ownerLine = "owner apply by user 01INVENTEDLIVEOWNER00000A (session): 1 of 1 owner row(s) written"
	publish(f.defaultApply, "LIVE-REPAIRS-BEFORE")
	publish(f.defaultApply, ownerLine)
	drained("m1")
	require.True(t, arrives("LIVE-REPAIRS-BEFORE"), "a Repairs op's line reaches a caller who may read it: %s", body())
	require.True(t, arrives("01INVENTEDLIVEOWNER00000A"), "and so does a user-data line for users.manage: %s", body())

	// users.manage is taken away: the user-data line stops, the clean one stays.
	setOpsManagerPermissions(t, f, auth.PermLibraryView, auth.PermLibraryEditMetadata, auth.PermSettingsManage)
	publish(f.defaultApply, "LIVE-REPAIRS-AFTER-USERS")
	publish(f.plainScan, "owner apply by user 01INVENTEDLIVEOWNER00000B (session)")
	drained("m2")
	require.True(t, arrives("LIVE-REPAIRS-AFTER-USERS"), "the clean line of the Repairs op still arrives: %s", body())
	require.False(t, arrives("01INVENTEDLIVEOWNER00000B"), "a user-data line stops once users.manage is gone: %s", body())

	// library.edit_metadata goes too: the Repairs op is no longer readable.
	setOpsManagerPermissions(t, f, auth.PermLibraryView, auth.PermSettingsManage)
	publish(f.defaultApply, "LIVE-REPAIRS-AFTER-EDIT")
	publish(f.plainScan, "LIVE-PLAIN-AFTER-EDIT")
	drained("m3")
	require.False(t, arrives("LIVE-REPAIRS-AFTER-EDIT"), "a Repairs op's line stops once its fixer's permission is gone: %s", body())
	require.True(t, arrives("LIVE-PLAIN-AFTER-EDIT"), "a plain op's line keeps coming: %s", body())

	cancel()
	wait()
}

// TestOpsV3Events_RoleLoweredMidStreamStopsTheRuns: /api/v3/ops/events carries a
// Repairs run only while the caller holds its fixer's permissions, asked again
// for every run event.
// Not parallel: it swaps the process-wide config.
func TestOpsV3Events_RoleLoweredMidStreamStopsTheRuns(t *testing.T) {
	f := newRepairsOpAccessFixture(t)
	f.srv.repairsOpAccess().WithLiveRecheck(0, nil) // no recheck bound: every event asks
	token := opsManagerToken(t, f)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	body, wait := openStream(t, f, ctx, "/api/v3/ops/events", token)
	announce := func(ids ...string) {
		for _, id := range ids {
			require.NoError(t, f.srv.opHub.Publish(ctx, "op.updated", map[string]any{"op_id": id}))
		}
	}
	count := func(id string) int { return strings.Count(body(), `"id":"`+id+`"`) }
	waitFor := func(id string, n int) {
		require.Eventually(t, func() bool { return count(id) >= n }, 5*time.Second, 5*time.Millisecond,
			"%s: want %d events, have %d: %s", id, n, count(id), body())
	}

	// The broker starts on its first client and subscribes to the hub from its
	// own goroutine, so the heartbeat can arrive before it listens and an
	// announce can land in that gap. Re-announce until both runs are on the
	// stream, then drain once more so every repeat has been written before the
	// counts below are taken as the baseline.
	require.Eventually(t, func() bool {
		if count(f.defaultApply) >= 1 && count(f.plainScan) >= 1 {
			return true
		}
		announce(f.defaultApply, f.plainScan)
		return false
	}, 30*time.Second, 50*time.Millisecond, "the runs never arrived: %s", body())
	plain := count(f.plainScan)
	announce(f.plainScan)
	waitFor(f.plainScan, plain+1)
	repairs, plain := count(f.defaultApply), count(f.plainScan)

	setOpsManagerPermissions(t, f, auth.PermLibraryView, auth.PermSettingsManage)
	announce(f.defaultApply)
	announce(f.plainScan)
	waitFor(f.plainScan, plain+1)
	// A later flush of the plain run is queued behind everything the first one
	// published, so seeing it means the default run's second event was decided.
	announce(f.plainScan)
	waitFor(f.plainScan, plain+2)
	require.Equal(t, repairs, count(f.defaultApply), "the Repairs run's event after the demotion was withheld: %s", body())

	cancel()
	wait()
}
