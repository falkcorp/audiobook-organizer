// file: internal/server/operation_error_text_user_data_test.go
// version: 1.0.0
// guid: 6c1d9e48-3a75-4b02-8f6e-d4a0b7c25e13
// last-edited: 2026-10-26

// An operation's error text and progress message are free text its row serves
// to every library.view caller, and an error such as "merge failed: user_id=<id>
// not found" names an account. The v2 read and the timeline, the v3 run read,
// list and event stream, and the diagnostics export withhold such text from a
// caller without users.manage, and serve it whole to one who holds it.

package server

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/activity"
	"github.com/falkcorp/audiobook-organizer/internal/database"
)

const (
	opErrNamesUser   = "op-plain-failed-names-user"
	opErrClean       = "op-plain-failed-clean"
	errNamingUser    = "merge failed: user_id=01INVENTEDERRUSER000000A could not be moved"
	progressNamesUsr = "moving state for user 01INVENTEDERRUSER000000A"
	errOrdinary      = "scan of Year One failed: disk full"
	progressOrdinary = "scanning Knightfall"
	errUserMarker    = "01INVENTEDERRUSER000000A"
)

func seedErrorTextOps(t *testing.T, f *repairsOpAccessFixture) {
	t.Helper()
	store := f.srv.storeForWiring()
	now := time.Now().UTC()
	insert := func(id, errText, progress string) {
		t.Helper()
		msg := errText
		require.NoError(t, store.InsertOperationV2(database.OperationV2Row{
			ID: id, DefID: "library.scan", Plugin: "test", Status: "failed", Params: `{}`,
			ErrorMessage: &msg, ProgressMessage: progress,
			QueuedAt: now.Add(time.Second), CompletedAt: &now,
		}))
	}
	insert(opErrNamesUser, errNamingUser, progressNamesUsr)
	insert(opErrClean, errOrdinary, progressOrdinary)
}

// Not parallel: it swaps the process-wide config.
func TestOperationErrorText_NamingAUserNeedsUsersManage(t *testing.T) {
	f := newRepairsOpAccessFixture(t)
	mgr := opsManagerToken(t, f)
	seedErrorTextOps(t, f)

	type read struct {
		name string
		path string
	}
	reads := []read{
		{"v2 single read", "/api/v1/operations/v2/" + opErrNamesUser},
		{"v2 timeline", "/api/v1/operations/timeline?limit=200"},
		{"v3 run read", "/api/v3/ops/runs/" + opErrNamesUser},
		{"v3 run list", "/api/v3/ops/runs?limit=100"},
	}
	for _, r := range reads {
		w := getAs(f.srv, mgr, r.path)
		require.Equal(t, http.StatusOK, w.Code, "%s: %s", r.name, w.Body.String())
		body := w.Body.String()
		require.NotContains(t, body, errUserMarker, "%s: the error or progress text names an account", r.name)
		require.NotContains(t, body, "could not be moved", "%s", r.name)
		require.Contains(t, body, activity.WithheldUserText, "%s: the text is replaced, not dropped", r.name)

		w = getAs(f.srv, f.admin, r.path)
		require.Equal(t, http.StatusOK, w.Code, "%s: %s", r.name, w.Body.String())
		require.Contains(t, w.Body.String(), "could not be moved", "%s: the admin reads the error whole", r.name)
	}

	// A list or timeline serves the clean op's text unchanged to both.
	for _, r := range []read{reads[1], reads[3]} {
		for _, who := range []string{mgr, f.admin} {
			body := getAs(f.srv, who, r.path).Body.String()
			require.Contains(t, body, "disk full", "%s: an ordinary error stays", r.name)
		}
	}
	body := getAs(f.srv, mgr, "/api/v1/operations/v2/"+opErrClean).Body.String()
	require.Contains(t, body, "disk full")
	require.Contains(t, body, "scanning Knightfall", "an ordinary progress message stays")
	require.False(t, strings.Contains(body, activity.WithheldUserText))
}

// The v3 event stream sends the same run view the read does.
// Not parallel: it swaps the process-wide config.
func TestOperationErrorText_V3EventStreamWithholdsIt(t *testing.T) {
	f := newRepairsOpAccessFixture(t)
	mgr := opsManagerToken(t, f)
	seedErrorTextOps(t, f)

	for who, wantWhole := range map[string]bool{mgr: false, f.admin: true} {
		ctx, cancel := context.WithCancel(context.Background())
		body, wait := openStream(t, f, ctx, "/api/v3/ops/events", who)
		for _, id := range []string{opErrNamesUser, opErrClean} {
			require.NoError(t, f.srv.opHub.Publish(ctx, "op.updated", map[string]any{"op_id": id}))
		}
		require.Eventually(t, func() bool {
			return strings.Contains(body(), `"id":"`+opErrClean+`"`) && strings.Contains(body(), `"id":"`+opErrNamesUser+`"`)
		}, 5*time.Second, 5*time.Millisecond, "events never arrived: %s", body())
		got := body()
		require.Contains(t, got, "disk full", "an ordinary error stays on the stream")
		require.Equal(t, wantWhole, strings.Contains(got, errUserMarker), "stream for admin=%t: %s", wantWhole, got)
		if !wantWhole {
			require.Contains(t, got, activity.WithheldUserText)
		}
		cancel()
		wait()
	}
}

// Not parallel: it swaps the process-wide config.
func TestOperationErrorText_DiagnosticsExportWithholdsIt(t *testing.T) {
	f := newRepairsOpAccessFixture(t)
	_ = opsManagerToken(t, f)
	seedErrorTextOps(t, f)
	store := f.srv.storeForWiring()
	now := time.Now().UTC()
	mgr, err := store.GetUserByUsername("opsmgr")
	require.NoError(t, err)
	admin, err := store.GetUserByUsername("admin")
	require.NoError(t, err)
	for id, actor := range map[string]string{"op-export-err-mgr": mgr.ID, "op-export-err-admin": admin.ID} {
		actor := actor
		require.NoError(t, store.InsertOperationV2(database.OperationV2Row{
			ID: id, DefID: "diagnostics.export", Plugin: "diagnostics", Status: "running",
			Params: `{"category":"general"}`, ActorUserID: &actor, QueuedAt: now,
		}))
	}

	opsJSON, _ := exportFiles(t, f, "op-export-err-mgr")
	require.NotContains(t, opsJSON, errUserMarker, "the export names no account in an error or progress message")
	require.Contains(t, opsJSON, activity.WithheldUserText)
	require.Contains(t, opsJSON, "disk full")
	require.Contains(t, opsJSON, "scanning Knightfall")

	opsJSON, _ = exportFiles(t, f, "op-export-err-admin")
	require.Contains(t, opsJSON, errUserMarker, "the admin's export carries the error whole")
	require.Contains(t, opsJSON, "moving state for user")
}
