// file: internal/server/handlers/repairs/op_error_user_data_test.go
// version: 1.0.0
// guid: d41a7c93-6e02-4b58-a9f7-1c85e3b0d26f
// last-edited: 2026-10-26

package repairs

import (
	"net/http"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/auth"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/repairs"
	"github.com/falkcorp/audiobook-organizer/internal/server/handlers/opaccess"
)

// The last-run pointer GET /repairs serves carries the op's error text, which can
// name a user ("merge failed: user_id=..."). It is served whole to a caller who
// holds users.manage and replaced by the withheld notice for everyone else,
// including a handler whose access check was never wired (it holds nothing).
func TestListFixers_LastRunErrorNamingAUserNeedsUsersManage(t *testing.T) {
	const userError = "apply failed: could not move state for user 01INVENTEDERRORUSER0000A"
	const marker = "01INVENTEDERRORUSER0000A"
	const plainError = "apply failed: Knightfall is missing a file"

	for name, tc := range map[string]struct {
		errText string
		access  func(holdsUsersManage bool) *opaccess.Repairs
		manage  bool
		want    string
		notWant string
	}{
		"without users.manage": {userError, wired, false, "withheld", marker},
		"with users.manage":    {userError, wired, true, marker, "withheld"},
		"not wired":            {userError, func(bool) *opaccess.Repairs { return nil }, true, "withheld", marker},
		"an ordinary error":    {plainError, wired, false, "Knightfall is missing a file", "withheld"},
	} {
		t.Run(name, func(t *testing.T) {
			reg := repairs.NewRegistry()
			require.NoError(t, reg.Register(stubFixer{id: "plain"}))
			ops := newFakeOps(map[string]string{"repairs_last_apply_op:plain": "op-failed"})
			msg := tc.errText
			ops.rows["op-failed"] = &database.OperationV2Row{
				ID: "op-failed", DefID: repairs.ApplyOpID, Status: "failed",
				QueuedAt: time.Date(2026, 9, 27, 11, 0, 0, 0, time.UTC), ErrorMessage: &msg,
			}
			h := New(reg, &fakeEnqueuer{}, ops).WithOpAccess(tc.access(tc.manage))
			r := gin.New()
			r.Use(callerWith(&database.User{ID: "u-editor", Username: "editor"}, auth.PermLibraryView, auth.PermLibraryEditMetadata))
			r.GET("/repairs", h.ListFixers)

			w := do(r, http.MethodGet, "/repairs", "")
			require.Equal(t, http.StatusOK, w.Code, w.Body.String())
			require.Contains(t, w.Body.String(), "op-failed", "the pointer is still served")
			require.Contains(t, w.Body.String(), tc.want)
			require.NotContains(t, w.Body.String(), tc.notWant)
		})
	}
}

// wired is an access check whose caller holds every permission except, unless
// holdsUsersManage, users.manage.
func wired(holdsUsersManage bool) *opaccess.Repairs {
	return opaccess.NewRepairs(nil, nil, func(_ *gin.Context, p auth.Permission) bool {
		return p != auth.PermUsersManage || holdsUsersManage
	})
}
