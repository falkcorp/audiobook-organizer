// file: internal/server/handlers/repairs/bridge_owner_apply_test.go
// version: 0.1.0
// guid: aeb707ef-c59d-4aaa-aea4-bb81a9a182db
// last-edited: 2026-11-21

package repairs

import (
	"net/http"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"

	coreauth "github.com/falkcorp/mediacore/auth"
	"github.com/falkcorp/mediacore/rbac"

	"github.com/falkcorp/audiobook-organizer/internal/auth"
	"github.com/falkcorp/audiobook-organizer/internal/server/legacyauth"
)

// F4-08 B7: OwnerApply keeps HEAD's role-name test and, under core identity,
// also needs core:admin. The caller is the owner signed in through Cloudflare
// Access with the admin role (HEAD's whole proof), so only the principal differs.
func TestOwnerApply_CoreAdminIsAFurtherRefusalUnderCoreIdentity(t *testing.T) {
	caller := ownerCaller{auth.MethodCFAccess, ownerUser, testOwnerEmail}
	// The fixer's own permission (HoldsPermission, adopted in this slice too) is
	// held in every core-identity case, so the only difference is core:admin.
	edit := rbac.Permission("test:" + string(auth.PermLibraryEditMetadata))
	const body = `{"plan_op_id":"op-owner-plan","row_id":"own"}`
	for _, tc := range []struct {
		name   string
		bound  bool
		scopes []rbac.Permission
		want   int
	}{
		{"legacy: a core principal without core:admin changes nothing", false, nil, http.StatusAccepted},
		{"core identity: the principal holds core:admin", true, []rbac.Permission{rbac.CoreAdmin, edit}, http.StatusAccepted},
		{"core identity: the role holds but the principal lacks core:admin", true, []rbac.Permission{edit}, http.StatusForbidden},
	} {
		t.Run(tc.name, func(t *testing.T) {
			inner, enq, _ := ownerSetup(t, caller)
			var id *legacyauth.Identity
			if tc.bound {
				id = bridgeIdentity(t, string(auth.PermLibraryEditMetadata))
			}
			outer := gin.New()
			outer.Use(func(c *gin.Context) {
				bindCore(c, id, coreauth.MethodSession, tc.scopes...)
				c.Next()
			})
			outer.Any("/*any", gin.WrapH(inner))
			w := ownerPost(outer, ownerPath, body, browserHdrs)
			require.Equal(t, tc.want, w.Code, w.Body.String())
			if tc.want == http.StatusAccepted {
				require.Len(t, enq.calls, 1)
			} else {
				require.Empty(t, enq.calls, "a refused owner apply enqueues nothing")
			}
		})
	}
}
