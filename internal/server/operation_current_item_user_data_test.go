// file: internal/server/operation_current_item_user_data_test.go
// version: 1.0.0
// guid: 3e7a9c12-5d84-4b16-8f20-a6c1d9e4b573
// last-edited: 2026-10-26

// The label of an operation's current item can name a user ("moving state for
// user <id>"). The v2 event stream withholds that label from a caller without
// users.manage, copying the event rather than editing the one every other
// stream shares.

package server

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/activity"
)

// streamCurrentItemEvents opens /operations/events as token, publishes one
// op.current_item event per label for op opID through the shared hub, and
// returns what the stream carried once the end marker arrived.
func streamCurrentItemEvents(t *testing.T, f *repairsOpAccessFixture, token, opID string, labels ...string) string {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	req := httptest.NewRequest(http.MethodGet, "/api/v1/operations/events", nil).WithContext(ctx)
	req.Header.Set("Authorization", "Bearer "+token)
	rec := &syncRecorder{hdr: http.Header{}}
	done := make(chan struct{})
	go func() { defer close(done); f.srv.router.ServeHTTP(rec, req) }()
	require.Eventually(t, func() bool { return strings.Contains(rec.body(), ": heartbeat") },
		5*time.Second, 5*time.Millisecond, "the stream never opened: %s", rec.body())
	for _, l := range labels {
		require.NoError(t, f.srv.opHub.Publish(ctx, "op.current_item", map[string]any{"op_id": opID, "label": l}))
	}
	require.NoError(t, f.srv.opHub.Publish(ctx, "op.marker", map[string]any{"note": "end"}))
	require.Eventually(t, func() bool { return strings.Contains(rec.body(), "op.marker") },
		5*time.Second, 5*time.Millisecond, "the marker never arrived: %s", rec.body())
	cancel()
	<-done
	return rec.body()
}

// TestOperationEvents_CurrentItemNamingAUserNeedsUsersManage: a label that
// names a user reaches the admin whole and a settings.manage caller without
// users.manage as the withheld marker, on a Repairs op and a plain op alike; an
// ordinary label reaches both unchanged. Not parallel: it swaps the
// process-wide config.
func TestOperationEvents_CurrentItemNamingAUserNeedsUsersManage(t *testing.T) {
	f := newRepairsOpAccessFixture(t)
	f.srv.repairsOpAccess().WithLiveRecheck(0, nil)
	mgr := opsManagerToken(t, f)
	const userLabel = "moving state for user 01INVENTEDCURRENTITEM00A"
	const ordinary = "Year One / chapter 3"

	for _, id := range []string{f.defaultApply, f.plainScan} {
		got := streamCurrentItemEvents(t, f, mgr, id, userLabel, ordinary)
		require.Contains(t, got, ordinary, "%s: an ordinary label arrives unchanged", id)
		require.Contains(t, got, activity.WithheldUserText, "%s: the user-naming label is replaced", id)
		require.NotContains(t, got, "01INVENTEDCURRENTITEM00A", "%s: the label names an account", id)

		got = streamCurrentItemEvents(t, f, f.admin, id, userLabel, ordinary)
		require.Contains(t, got, "01INVENTEDCURRENTITEM00A", "%s: the admin reads the label whole", id)
		require.Contains(t, got, ordinary, "%s", id)
		require.NotContains(t, got, activity.WithheldUserText, "%s: nothing is withheld from the admin", id)
	}
}
