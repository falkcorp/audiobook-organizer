// file: internal/server/handlers/opsv3/bridge_sites_test.go
// version: 0.1.0
// guid: d470ae77-c768-4c9a-8ff2-89b4e33e97a9
// last-edited: 2026-11-21

package opsv3

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"

	coreauth "github.com/falkcorp/mediacore/auth"
	"github.com/falkcorp/mediacore/rbac"

	"github.com/falkcorp/audiobook-organizer/internal/auth"
	"github.com/falkcorp/audiobook-organizer/internal/server/legacyauth"
)

// F4-08 B7: the default StrictAuthz().Holds asks the bridge under core identity
// and HEAD's auth.Can otherwise (four worlds, as in the sibling packages), and
// Require, which goes through middleware.HoldsPermission, answers the same.
func TestBridgeSites_StrictAuthz(t *testing.T) {
	scope := bridgeScope(string(auth.PermUsersManage))
	for _, tc := range []struct {
		name      string
		bound     bool
		headPerms []auth.Permission
		scopes    []rbac.Permission
		held      bool
	}{
		{"legacy: HEAD's permission set holds it", false, []auth.Permission{auth.PermUsersManage}, nil, true},
		{"legacy: a core principal holding the scope changes nothing", false, nil, []rbac.Permission{scope}, false},
		{"core identity: the principal holds the scope", true, nil, []rbac.Permission{scope}, true},
		{"core identity: HEAD's permission set is not read", true, []auth.Permission{auth.PermUsersManage}, nil, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			gin.SetMode(gin.TestMode)
			rec := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(rec)
			c.Request = httptest.NewRequest(http.MethodGet, "/ops", nil)
			if len(tc.headPerms) > 0 {
				c.Request = c.Request.WithContext(auth.WithPermissions(c.Request.Context(), tc.headPerms))
			}
			var id *legacyauth.Identity
			if tc.bound {
				id = bridgeIdentity(t, string(auth.PermUsersManage))
			}
			if tc.bound || len(tc.scopes) > 0 {
				bindCore(c, id, coreauth.MethodSession, tc.scopes...)
			}
			z := StrictAuthz()
			require.Equal(t, tc.held, z.Holds(c, auth.PermUsersManage))
			require.Equal(t, tc.held, z.Require(c, auth.PermUsersManage))
			if tc.held {
				require.Equal(t, http.StatusOK, rec.Code, "a held permission writes no refusal")
			} else {
				require.GreaterOrEqual(t, rec.Code, http.StatusUnauthorized)
			}
		})
	}
}
