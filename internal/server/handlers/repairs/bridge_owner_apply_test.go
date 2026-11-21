// file: internal/server/handlers/repairs/bridge_owner_apply_test.go
// version: 0.2.0
// guid: aeb707ef-c59d-4aaa-aea4-bb81a9a182db
// last-edited: 2026-11-21

package repairs

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"

	coreauth "github.com/falkcorp/mediacore/auth"
	"github.com/falkcorp/mediacore/rbac"

	"github.com/falkcorp/audiobook-organizer/internal/auth"
	"github.com/falkcorp/audiobook-organizer/internal/cfaccess"
	"github.com/falkcorp/audiobook-organizer/internal/repairs"
	"github.com/falkcorp/audiobook-organizer/internal/server/legacyauth"
)

// bindCoreOwner is bindCore for a principal that is the Access extension's,
// carrying assurance a (AssuranceOwner for a verified owner token), the form in
// which stage 12 hands the owner proof to a handler under core identity.
func bindCoreOwner(c *gin.Context, id *legacyauth.Identity, m coreauth.Method, a coreauth.Assurance, scopes ...rbac.Permission) {
	chk := bridgeChecker(scopes)
	p := &coreauth.Principal{Method: m, Perms: chk, Assurance: a}
	ctx := coreauth.WithPrincipal(c.Request.Context(), p)
	ctx = rbac.WithChecker(ctx, chk)
	if id != nil {
		ctx = legacyauth.WithIdentity(ctx, id)
	}
	c.Request = c.Request.WithContext(ctx)
}

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
				// Under identity the owner proof is the extension principal's.
				bindCoreOwner(c, id, cfaccess.CoreMethod, coreauth.AssuranceOwner, tc.scopes...)
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

// F4-08 B10b2: under core identity the owner proof is core's principal, the
// Access extension's with AssuranceOwner, and nothing the legacy context says.
// HEAD's proof (cf_access method plus an owner JWT e-mail in the legacy
// context) must not carry over: the legacy stand-in below claims it in every
// case and only the principal differs.
func TestOwnerApply_CoreIdentityProofIsThePrincipals(t *testing.T) {
	// The legacy context always claims HEAD's whole proof.
	caller := ownerCaller{auth.MethodCFAccess, ownerUser, testOwnerEmail}
	edit := rbac.Permission("test:" + string(auth.PermLibraryEditMetadata))
	scopes := []rbac.Permission{rbac.CoreAdmin, edit}
	const body = `{"plan_op_id":"op-owner-plan","row_id":"own"}`
	for _, tc := range []struct {
		name      string
		method    coreauth.Method
		assurance coreauth.Assurance
		ownerMail string
		want      int
	}{
		{"the Access owner", cfaccess.CoreMethod, coreauth.AssuranceOwner, testOwnerEmail, http.StatusAccepted},
		{"an Access user who is not the owner", cfaccess.CoreMethod, 0, testOwnerEmail, http.StatusForbidden},
		{"a session whatever its assurance", coreauth.MethodSession, coreauth.AssuranceOwner, testOwnerEmail, http.StatusForbidden},
		{"a password session, no assurance", coreauth.MethodSession, 0, testOwnerEmail, http.StatusForbidden},
		{"auth disabled", coreauth.MethodAuthDisabled, 0, testOwnerEmail, http.StatusForbidden},
		{"the owner's principal with owner_email now unset", cfaccess.CoreMethod, coreauth.AssuranceOwner, "", http.StatusForbidden},
	} {
		t.Run(tc.name, func(t *testing.T) {
			inner, enq, grants := ownerSetupWithEmail(t, caller, tc.ownerMail)
			id := bridgeIdentity(t, string(auth.PermLibraryEditMetadata))
			outer := gin.New()
			outer.Use(func(c *gin.Context) {
				bindCoreOwner(c, id, tc.method, tc.assurance, scopes...)
				c.Next()
			})
			outer.Any("/*any", gin.WrapH(inner))
			w := ownerPost(outer, ownerPath, body, browserHdrs)
			require.Equal(t, tc.want, w.Code, w.Body.String())
			if tc.want != http.StatusAccepted {
				require.Empty(t, enq.calls, "a refused owner apply enqueues nothing")
				return
			}
			require.Len(t, enq.calls, 1)
			// The grant and the op record HEAD's method name and the configured
			// owner e-mail, and the apply's own redemption accepts them once.
			require.Equal(t, string(auth.MethodCFAccess), enq.calls[0].actorMethod)
			require.True(t, enq.calls[0].ownerApply)
			var p repairs.ApplyParams
			require.NoError(t, json.Unmarshal(enq.calls[0].params, &p))
			who := repairs.OwnerRedeemer{ActorUserID: enq.calls[0].actor,
				IsOwner: func(e string) bool { return auth.IsOwnerEmail(e, testOwnerEmail) }}
			a := repairs.ResolveOwnerApproval(grants, p, who)
			require.Empty(t, a.Refused)
			require.Equal(t, string(auth.MethodCFAccess), a.AuthMethod)
		})
	}
}

// The owner status the Repairs page reads agrees with the apply, under
// identity too: it asks the same proof.
func TestOwnerStatus_CoreIdentityAgreesWithApply(t *testing.T) {
	caller := ownerCaller{auth.MethodCFAccess, ownerUser, testOwnerEmail}
	edit := rbac.Permission("test:" + string(auth.PermLibraryEditMetadata))
	for _, tc := range []struct {
		name      string
		method    coreauth.Method
		assurance coreauth.Assurance
		want      bool
	}{
		{"the Access owner", cfaccess.CoreMethod, coreauth.AssuranceOwner, true},
		{"an Access user who is not the owner", cfaccess.CoreMethod, 0, false},
		{"a session", coreauth.MethodSession, 0, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			inner, _, _ := ownerSetup(t, caller)
			id := bridgeIdentity(t, string(auth.PermLibraryEditMetadata))
			outer := gin.New()
			outer.Use(func(c *gin.Context) {
				bindCoreOwner(c, id, tc.method, tc.assurance, rbac.CoreAdmin, edit)
				c.Next()
			})
			outer.Any("/*any", gin.WrapH(inner))
			req := httptest.NewRequest(http.MethodGet, "/api/v1/repairs/owner-status", nil)
			req.Host = "books.example.com"
			w := httptest.NewRecorder()
			outer.ServeHTTP(w, req)
			require.Equal(t, http.StatusOK, w.Code, w.Body.String())
			if tc.want {
				require.Contains(t, w.Body.String(), `"allowed":true`)
			} else {
				require.Contains(t, w.Body.String(), `"allowed":false`)
			}
		})
	}
}
