// file: internal/server/operation_logs_user_data_test.go
// version: 1.1.0
// guid: 5b8e2c47-91d3-4f60-a7c2-3e9d1b6f0a85
// last-edited: 2026-10-26

// A log line that names a user -- the Repairs engine's "owner apply by user
// <id>" line, or any other operation's -- is read only by a caller who holds
// users.manage, on every route that serves an operation's log lines, and the
// system activity log is held to the same rule.

package server

import (
	"compress/gzip"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// engineOwnerApplyLine is the line repairs.Engine logs for an owner apply
// (internal/repairs/engine.go), with an invented account id.
const (
	engineOwnerApplyLine = "repairs.apply maintenance.audible-read-status: owner apply by user 01INVENTEDOWNER0000000000A (cf-access): 1 of 1 owner row(s) written"
	ownerApplyMarker     = "01INVENTEDOWNER0000000000A"
	ordinaryLogLine      = "scanned Year One and Hush"

	// A line whose message is clean but whose attributes name a user.
	attrLogLine  = "applied Knightfall to the book"
	attrLogAttrs = `{"user_id":"01INVENTEDATTRUSER000000A","username":"bruce_wayne_attr","book":"Knightfall","detail":{"owner_email":"bruce.wayne@example.invalid","kept":"Year Two"},"note":"progress for user 01INVENTEDATTRUSER000000B"}`
)

var attrMarkers = []string{"01INVENTEDATTRUSER000000A", "01INVENTEDATTRUSER000000B", "bruce_wayne_attr", "bruce.wayne@example.invalid"}

// streamOpLogEvents opens /operations/events as token, publishes one op.log
// event per message for op opID, and returns what the stream carried.
func streamOpLogEvents(t *testing.T, f *repairsOpAccessFixture, token, opID string, messages ...string) string {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	req := httptest.NewRequest(http.MethodGet, "/api/v1/operations/events", nil).WithContext(ctx)
	req.Header.Set("Authorization", "Bearer "+token)
	rec := &syncRecorder{hdr: http.Header{}}
	done := make(chan struct{})
	go func() { defer close(done); f.srv.router.ServeHTTP(rec, req) }()
	require.Eventually(t, func() bool { return strings.Contains(rec.body(), ": heartbeat") },
		5*time.Second, 5*time.Millisecond, "the stream never opened: %s", rec.body())
	for _, m := range messages {
		require.NoError(t, f.srv.opHub.Publish(ctx, "op.log", map[string]any{"op_id": opID, "level": "info", "message": m}))
	}
	require.NoError(t, f.srv.opHub.Publish(ctx, "op.marker", map[string]any{"note": "end"}))
	require.Eventually(t, func() bool { return strings.Contains(rec.body(), "op.marker") },
		5*time.Second, 5*time.Millisecond, "the marker never arrived: %s", rec.body())
	cancel()
	<-done
	return rec.body()
}

// TestOperationLogs_UserLinesNeedUsersManage: with the owner-apply line and an
// ordinary line in the log of a Repairs op the caller may read and of a plain
// op, a settings.manage caller without users.manage reads the ordinary line and
// not the owner-apply one on the single-op read,
// the log download and the op.log event; the admin reads both. The system
// activity log drops a user-data subsystem's line and the user-id column the
// same way. Before this the owner-apply line (the account id and sign-in
// method) went to every caller who could read the op.
// Not parallel: it swaps the process-wide config.
func TestOperationLogs_UserLinesNeedUsersManage(t *testing.T) {
	f := newRepairsOpAccessFixture(t)
	mgr := opsManagerToken(t, f)
	store := f.srv.storeForWiring()
	now := time.Now().UTC()
	for _, id := range []string{f.defaultApply, f.plainScan} {
		require.NoError(t, store.AppendOpLogsV2([]database.OpLogV2Row{
			{OperationID: id, Level: "info", Message: engineOwnerApplyLine, CreatedAt: now.Add(time.Second)},
			{OperationID: id, Level: "info", Message: ordinaryLogLine, CreatedAt: now.Add(2 * time.Second)},
			{OperationID: id, Level: "info", Message: attrLogLine, Attrs: attrLogAttrs, CreatedAt: now.Add(3 * time.Second)},
		}))
	}
	require.NoError(t, store.AddSystemActivityLog("abs", "info", "seek to Year One chapter 3"))
	require.NoError(t, store.AddSystemActivityLog("scanner", "info", "scan complete count=12"))

	download := func(token, id string) string {
		w := getAs(f.srv, token, "/api/v1/operations/v2/"+id+"/logs/download")
		require.Equal(t, http.StatusOK, w.Code, w.Body.String())
		zr, err := gzip.NewReader(w.Body)
		require.NoError(t, err)
		b, err := io.ReadAll(zr)
		require.NoError(t, err)
		return string(b)
	}

	for _, id := range []string{f.defaultApply, f.plainScan} {
		reads := map[string]string{
			"single-op read": getAs(f.srv, mgr, "/api/v1/operations/v2/"+id).Body.String(),
			"log download":   download(mgr, id),
		}
		for route, body := range reads {
			require.Contains(t, body, "Year One", "%s %s: the ordinary line stays", id, route)
			require.NotContains(t, body, ownerApplyMarker, "%s %s: the owner-apply line names an account", id, route)
			require.NotContains(t, body, "owner apply by user", "%s %s", id, route)
			require.Contains(t, body, "Knightfall", "%s %s: the clean line stays", id, route)
			require.Contains(t, body, "Year Two", "%s %s: and its attributes that name no user", id, route)
			for _, m := range attrMarkers {
				require.NotContains(t, body, m, "%s %s: an attribute names a user", id, route)
			}
		}
		admin := map[string]string{
			"single-op read": getAs(f.srv, f.admin, "/api/v1/operations/v2/"+id).Body.String(),
			"log download":   download(f.admin, id),
		}
		for route, body := range admin {
			require.Contains(t, body, ownerApplyMarker, "%s %s: the admin reads the owner-apply line", id, route)
			require.Contains(t, body, "Year One", "%s %s", id, route)
			for _, m := range attrMarkers {
				require.Contains(t, body, m, "%s %s: the admin reads the attributes whole", id, route)
			}
		}

		stream := streamOpLogEvents(t, f, mgr, id, engineOwnerApplyLine, ordinaryLogLine)
		require.Contains(t, stream, "Year One", "%s: the ordinary op.log event arrives", id)
		require.NotContains(t, stream, ownerApplyMarker, "%s: the owner-apply op.log event is withheld", id)
		stream = streamOpLogEvents(t, f, f.admin, id, engineOwnerApplyLine, ordinaryLogLine)
		require.Contains(t, stream, ownerApplyMarker, "%s: the admin's stream carries it", id)
	}

	w := getAs(f.srv, mgr, "/api/v1/system/activity-log?limit=50")
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	require.Contains(t, w.Body.String(), "scan complete")
	require.NotContains(t, w.Body.String(), "seek to Year One", "a user-data subsystem's line is withheld")
	w = getAs(f.srv, f.admin, "/api/v1/system/activity-log?limit=50")
	require.Contains(t, w.Body.String(), "seek to Year One", "the admin reads it")
}
