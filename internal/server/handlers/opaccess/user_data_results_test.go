// file: internal/server/handlers/opaccess/user_data_results_test.go
// version: 1.0.0
// guid: 5a1d8e37-2c96-4b04-8f7a-d03e6b19c452
// last-edited: 2026-10-26

package opaccess

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/auth"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/merge"
	"github.com/falkcorp/audiobook-organizer/internal/usermerge"
)

// The result scope: an op whose result names users is readable with users.manage
// only, an ordinary op by anyone, and an unwired check refuses the user-data one.
func TestReader_MayReadResult(t *testing.T) {
	withUsers := []auth.Permission{auth.PermLibraryView, auth.PermUsersManage}
	settings := []auth.Permission{auth.PermLibraryView, auth.PermSettingsManage}
	row := func(def string) *database.OperationV2Row {
		return &database.OperationV2Row{ID: "op", DefID: def, ResultData: ptr(`{"items":[{"user_id":"01INVENTEDLISTENER000000A"}]}`)}
	}
	for name, tc := range map[string]struct {
		def  string
		held []auth.Permission
		want bool
	}{
		"repair, viewer":           {merge.UserStateRepairOpID, []auth.Permission{auth.PermLibraryView}, false},
		"repair, settings.manage":  {merge.UserStateRepairOpID, settings, false},
		"repair, users.manage":     {merge.UserStateRepairOpID, withUsers, true},
		"user merge, viewer":       {usermerge.OpDefID, []auth.Permission{auth.PermLibraryView}, false},
		"user merge, users.manage": {usermerge.OpDefID, withUsers, true},
		"ordinary op, viewer":      {"library.scan", []auth.Permission{auth.PermLibraryView}, true},
	} {
		c, _ := ctxHolding()
		r := NewRepairs(fixers{}, noOps, holdsOnly(tc.held...)).For(c)
		require.Equal(t, tc.want, r.MayReadResult(row(tc.def)), name)
	}
	c, _ := ctxHolding()
	require.True(t, NewRepairs(fixers{}, noOps, holdsOnly()).For(c).MayReadResult(nil), "no row")

	// Unwired (nil *Repairs): fail closed for a user-data result, open for the rest.
	var unwired *Repairs
	c, _ = ctxHolding()
	require.False(t, unwired.For(c).MayReadResult(row(merge.UserStateRepairOpID)))
	require.True(t, unwired.For(c).MayReadResult(row("library.scan")))
}

// RequireResult answers 403 itself, and WithoutResult strips the result and
// params of a refused row without touching the input.
func TestReader_RequireResultAndWithoutResult(t *testing.T) {
	row := database.OperationV2Row{ID: "op", DefID: merge.UserStateRepairOpID, Params: `{"apply":true}`, ResultData: ptr(`{"items":[1]}`)}

	c, w := ctxHolding()
	r := NewRepairs(fixers{}, noOps, holdsOnly(auth.PermLibraryView)).For(c)
	require.False(t, r.RequireResult(&row))
	require.Contains(t, []int{401, 403}, w.Code, "refused (RespondPermissionDenied: 401 with no user on the context, 403 for a signed-in caller)")
	got := r.WithoutResult(row)
	require.Nil(t, got.ResultData)
	require.Empty(t, got.Params)
	require.Equal(t, "op", got.ID, "the rest of the row stays")
	require.NotNil(t, row.ResultData, "the input is not modified")
	require.Equal(t, `{"apply":true}`, row.Params)

	c, w = ctxHolding()
	r = NewRepairs(fixers{}, noOps, holdsOnly(auth.PermLibraryView, auth.PermUsersManage)).For(c)
	require.True(t, r.RequireResult(&row))
	require.Equal(t, 200, w.Code)
	require.Equal(t, row, r.WithoutResult(row))
}
