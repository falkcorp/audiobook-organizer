// file: internal/server/handlers/opaccess/served_result_test.go
// version: 1.1.0
// guid: 5a0c83e7-29d1-4f6b-b4e8-0d7f1a9c3b52
// last-edited: 2026-10-26

package opaccess

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/activity"
	"github.com/falkcorp/audiobook-organizer/internal/auth"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/repairs"
)

// ServedResult removes zip_path from a diagnostics export's result for a caller
// without settings.manage and serves every other result unchanged.
func TestReader_ServedResult(t *testing.T) {
	const export = `{"zip_path":"/lib/Gotham/exports/hush.zip","narrowed_to":["library.view"]}`
	row := func(def string, data *string) *database.OperationV2Row {
		return &database.OperationV2Row{ID: "op", DefID: def, ResultData: data}
	}
	view := []auth.Permission{auth.PermLibraryView}
	settings := []auth.Permission{auth.PermLibraryView, auth.PermSettingsManage}

	c, _ := ctxHolding()
	got := NewRepairs(fixers{}, noOps, holdsOnly(view...)).For(c).ServedResult(row("diagnostics.export", ptr(export)))
	require.NotNil(t, got)
	require.NotContains(t, *got, "zip_path")
	require.NotContains(t, *got, "hush.zip")
	require.Contains(t, *got, `"narrowed_to":["library.view"]`, "the recorded scope stays")

	c, _ = ctxHolding()
	got = NewRepairs(fixers{}, noOps, holdsOnly(settings...)).For(c).ServedResult(row("diagnostics.export", ptr(export)))
	require.Equal(t, export, *got, "settings.manage reads it whole, as it may download it")

	c, _ = ctxHolding()
	scan := ptr(`{"zip_path":"kept","scanned":3}`)
	require.Equal(t, scan, NewRepairs(fixers{}, noOps, holdsOnly(view...)).For(c).ServedResult(row("library.scan", scan)),
		"another operation's result is not touched")

	c, _ = ctxHolding()
	require.Nil(t, NewRepairs(fixers{}, noOps, holdsOnly(view...)).For(c).ServedResult(row("diagnostics.export", ptr("{not json"))), "unparseable: withheld")
	require.Nil(t, NewRepairs(fixers{}, noOps, holdsOnly(view...)).For(c).ServedResult(row("diagnostics.export", nil)))
	require.Nil(t, NewRepairs(fixers{}, noOps, holdsOnly(view...)).For(c).ServedResult(nil))
	unwired := NewRepairs(fixers{}, noOps, nil).For(c).ServedResult(row("diagnostics.export", ptr(export)))
	require.NotNil(t, unwired)
	require.NotContains(t, *unwired, "zip_path", "an unwired check holds nothing")
}

// applyResultNamingUsers is a repairs.apply result whose owner rows ran under
// one user's grant and whose free-text fields carry a user id.
const applyResultNamingUsers = `{"fixer_id":"hush.fix","plan_op_id":"01JPLANKNIGHTFALL00000000A","requested":2,` +
	`"owner_rows":1,"owner_user_id":"01INVENTEDOWNER0000000000A",` +
	`"follow_up_error":"could not queue for user_id=01INVENTEDOWNER0000000000A",` +
	`"aborted":"stopped: scan of Year One failed",` +
	`"rows":[{"row_id":"b1","outcome":"applied","owner_user_id":"01INVENTEDOWNER0000000000A"},` +
	`{"row_id":"b2","outcome":"failed","error":"merge failed: user_id=01INVENTEDOWNER0000000000A could not be moved"},` +
	`{"row_id":"b3","outcome":"failed","error":"disk full while writing Knightfall"}]}`

// A Repairs apply's result is served whole to a caller with users.manage and,
// to every other caller who may read it, without the owner's user id (top level
// and per row) and with the fixer's free text read through UserText.
func TestReader_ServedResult_RepairsApplyWithholdsUserData(t *testing.T) {
	row := &database.OperationV2Row{ID: "op", DefID: repairs.ApplyOpID, ResultData: ptr(applyResultNamingUsers)}
	view := []auth.Permission{auth.PermLibraryView}
	users := []auth.Permission{auth.PermLibraryView, auth.PermUsersManage}

	c, _ := ctxHolding()
	got := NewRepairs(fixers{}, noOps, holdsOnly(users...)).For(c).ServedResult(row)
	require.Equal(t, applyResultNamingUsers, *got, "users.manage reads it whole")

	c, _ = ctxHolding()
	got = NewRepairs(fixers{}, noOps, holdsOnly(view...)).For(c).ServedResult(row)
	require.NotNil(t, got)
	require.NotContains(t, *got, "01INVENTEDOWNER0000000000A", "no user id survives anywhere in the result")
	require.NotContains(t, *got, "owner_user_id")

	var res struct {
		FollowUpError string `json:"follow_up_error"`
		Aborted       string `json:"aborted"`
		PlanOpID      string `json:"plan_op_id"`
		Requested     int    `json:"requested"`
		OwnerRows     int    `json:"owner_rows"`
		Rows          []struct {
			RowID   string `json:"row_id"`
			Outcome string `json:"outcome"`
			Error   string `json:"error"`
		} `json:"rows"`
	}
	require.NoError(t, json.Unmarshal([]byte(*got), &res))
	require.Equal(t, activity.WithheldUserText, res.FollowUpError, "a follow-up error naming a user is withheld")
	require.Equal(t, "stopped: scan of Year One failed", res.Aborted, "text that names no user stays")
	require.Equal(t, "01JPLANKNIGHTFALL00000000A", res.PlanOpID)
	require.Equal(t, 2, res.Requested)
	require.Equal(t, 1, res.OwnerRows)
	require.Len(t, res.Rows, 3)
	require.Equal(t, "applied", res.Rows[0].Outcome)
	require.Equal(t, activity.WithheldUserText, res.Rows[1].Error, "a row error naming a user is withheld")
	require.Equal(t, "disk full while writing Knightfall", res.Rows[2].Error)
	require.Equal(t, applyResultNamingUsers, *row.ResultData, "the stored row is not modified")

	c, _ = ctxHolding()
	unwired := NewRepairs(fixers{}, noOps, nil).For(c).ServedResult(row)
	require.NotNil(t, unwired)
	require.NotContains(t, *unwired, "01INVENTEDOWNER0000000000A", "an unwired check holds nothing")

	for name, bad := range map[string]string{
		"unparseable":   "{not json",
		"null":          "null",
		"rows not list": `{"rows":{"a":1}}`,
		"error not str": `{"rows":[{"row_id":"b1","error":7}]}`,
		"aborted not":   `{"aborted":["x"]}`,
	} {
		c, _ = ctxHolding()
		require.Nil(t, NewRepairs(fixers{}, noOps, holdsOnly(view...)).For(c).ServedResult(
			&database.OperationV2Row{ID: "op", DefID: repairs.ApplyOpID, ResultData: ptr(bad)}), name+": withheld whole")
	}
}

// WithoutResult, which the diagnostics export runs every operation row through,
// serves a readable result as ServedResult does.
func TestReader_WithoutResult_ServesTheRedactedResult(t *testing.T) {
	row := database.OperationV2Row{ID: "op", DefID: repairs.ApplyOpID, ResultData: ptr(applyResultNamingUsers)}
	c, _ := ctxHolding()
	got := NewRepairs(fixers{}, noOps, holdsOnly(auth.PermLibraryView)).For(c).WithoutResult(row)
	require.NotNil(t, got.ResultData)
	require.NotContains(t, *got.ResultData, "01INVENTEDOWNER0000000000A")
	require.Equal(t, applyResultNamingUsers, *row.ResultData, "the caller's row is not modified")

	exp := database.OperationV2Row{ID: "op2", DefID: "diagnostics.export", ResultData: ptr(`{"zip_path":"/lib/Gotham/exports/hush.zip"}`)}
	c, _ = ctxHolding()
	got = NewRepairs(fixers{}, noOps, holdsOnly(auth.PermLibraryView)).For(c).WithoutResult(exp)
	require.NotNil(t, got.ResultData)
	require.NotContains(t, *got.ResultData, "zip_path", "an export's archive path is removed in the bulk path too")
}
