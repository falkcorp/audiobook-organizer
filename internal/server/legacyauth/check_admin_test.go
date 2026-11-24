// file: internal/server/legacyauth/check_admin_test.go
// version: 0.1.0
// guid: 7d2c4e91-5a08-4b3f-9e16-c0a8f3d52b74
// last-edited: 2026-11-21

package legacyauth

import (
	"net/http"
	"testing"

	"github.com/gin-gonic/gin"

	coreauth "github.com/falkcorp/mediacore/auth"
	"github.com/falkcorp/mediacore/rbac"
)

// TestCheckAdmin: CheckAdmin is RequireAdmin's decision, returned instead of
// continued. Its result must agree with the response it wrote, because a gate
// that adds a check of its own after it acts on the result alone.
func TestCheckAdmin(t *testing.T) {
	b := newBridge(t, nil)
	debug := rbac.Need("books:admin.debug")
	for name, tc := range map[string]struct {
		perm   rbac.Requirement
		ctx    ginCase
		passes bool
		status int
	}{
		"no principal":    {debug, ginCase{ctx: bound(nil, laxChecker{})}, false, http.StatusUnauthorized},
		"a lacking scope": {debug, ginCase{ctx: as(coreauth.MethodSession, setChecker{"books:admin.view"})}, false, http.StatusForbidden},
		"the scope":       {debug, ginCase{ctx: as(coreauth.MethodSession, setChecker{"books:admin.debug"})}, true, http.StatusOK},
		// 403, not HEAD's 401: a 401 would end a no-auth install's SPA session
		// (F4-08 ruling G5(a)).
		"auth-disabled, checker refuses": {debug, ginCase{ctx: as(coreauth.MethodAuthDisabled, setChecker{})}, false,
			http.StatusForbidden},
		"a Public requirement": {rbac.Public, ginCase{ctx: as(coreauth.MethodSession, laxChecker{})}, false, http.StatusForbidden},
		"a zero requirement":   {rbac.Requirement{}, ginCase{ctx: as(coreauth.MethodSession, laxChecker{})}, false, http.StatusForbidden},
	} {
		t.Run(name, func(t *testing.T) {
			var got, ran bool
			rec := serve(tc.ctx.ctx, func(c *gin.Context) {
				got = b.CheckAdmin(c, tc.perm)
				ran = true
				if got {
					c.Next()
				}
			})
			if !ran || got != tc.passes {
				t.Fatalf("CheckAdmin = %v, want %v", got, tc.passes)
			}
			if rec.Code != tc.status {
				t.Fatalf("status = %d, want %d", rec.Code, tc.status)
			}
		})
	}
}
