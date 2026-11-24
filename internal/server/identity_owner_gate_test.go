// file: internal/server/identity_owner_gate_test.go
// version: 0.1.0
// guid: 72ee70a3-9f93-4b91-ba64-a6d4625eb749
// last-edited: 2026-11-21

package server

import (
	"net/http"
	"net/http/httptest"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"

	coreauth "github.com/falkcorp/mediacore/auth"
	"github.com/falkcorp/mediacore/rbac"

	"github.com/falkcorp/audiobook-organizer/internal/auth"
	database "github.com/falkcorp/audiobook-organizer/internal/books/store"
	"github.com/falkcorp/audiobook-organizer/internal/cfaccess"
	"github.com/falkcorp/audiobook-organizer/internal/config"
)

// F4-08 B10b2: the owner gate under core identity. It is the Access
// extension's principal carrying AssuranceOwner (cfaccess.RequireOwnerCore),
// not core's Guards.Owner fallback (the bridge's RequireOwner), which with
// core.auth.owner.required=false admits an interactive core:admin principal
// where HEAD refuses everyone but the Access owner while owner_email is set.

var ownerPathParam = regexp.MustCompile(`:[A-Za-z]+`)

// ownerGateWorld is a core-identity server over one store with an admin-role
// user who holds every scope, and the owner e-mail set.
type ownerGateWorld struct {
	core   *Server
	alfred *database.User
	scopes []rbac.Permission
}

func newOwnerGateWorld(t *testing.T, ownerRequired func() bool, ownerEmail string) *ownerGateWorld {
	t.Helper()
	gin.SetMode(gin.TestMode)
	orig := config.AppConfig
	t.Cleanup(func() { config.AppConfig = orig })
	config.AppConfig.EnableAuth = true
	config.AppConfig.OwnerEmail = ownerEmail
	store, err := database.NewPebbleStoreInMemory(t.TempDir() + "/owner-gate.pebble")
	require.NoError(t, err)
	t.Cleanup(func() { store.Close() })
	w := &ownerGateWorld{alfred: &database.User{ID: "u-alfred", Username: "alfred", Roles: []string{auth.SeedRoleAdmin}, Status: "active"}}
	var atoms []string
	for _, p := range auth.All() {
		atoms = append(atoms, string(p))
		w.scopes = append(w.scopes, rbac.Permission("test:"+string(p)))
	}
	w.scopes = append(w.scopes, rbac.CoreAdmin, "books:admin.view", "books:admin.debug", "core:users.manage")
	id := adoptIdentity(t, atoms, ownerRequired, nil, w.alfred)
	w.core = newTestServerWith(t, store, WithIdentity(id))
	return w
}

// routes is every route behind the owner gate, from the server's own record.
func (w *ownerGateWorld) routes(t *testing.T) []string {
	t.Helper()
	require.NotEmpty(t, w.core.ownerRoutes)
	keys := make([]string, 0, len(w.core.ownerRoutes))
	for k := range w.core.ownerRoutes {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func ownerRouteRequest(key string) *http.Request {
	method, full, _ := strings.Cut(key, " ")
	req := httptest.NewRequest(method, ownerPathParam.ReplaceAllString(full, "x"), strings.NewReader("{}"))
	req.Header.Set("Content-Type", "application/json")
	return req
}

// TestOwnerGate_IdentityRefusesEveryNonOwner: on every owner route, with an
// owner set, a signed-in admin who holds every scope including core:admin is
// refused by the owner gate, whether core.auth.owner.required is true or false
// (false is the setting under which core's own fallback would let them in).
func TestOwnerGate_IdentityRefusesEveryNonOwner(t *testing.T) {
	for name, required := range map[string]func() bool{
		"owner.required true":  func() bool { return true },
		"owner.required false": func() bool { return false },
		"owner.required unset": nil,
	} {
		t.Run(name, func(t *testing.T) {
			w := newOwnerGateWorld(t, required, ownerRouteEmail)
			for _, key := range w.routes(t) {
				p, c := adoptPrincipal(coreauth.MethodSession, w.alfred, w.scopes...)
				rec := identityServe(w.core.router, ownerRouteRequest(key), p, c)
				require.True(t, ownerRefused(rec), "%s: a session admin holding every scope got %d: %s", key, rec.Code, rec.Body.String())

				// The same account's e-mail is the owner's: an e-mail is not a proof.
				mail := *w.alfred
				mail.Email = ownerRouteEmail
				p, c = adoptPrincipal(coreauth.MethodSession, &mail, w.scopes...)
				rec = identityServe(w.core.router, ownerRouteRequest(key), p, c)
				require.True(t, ownerRefused(rec), "%s: an account with the owner's e-mail got %d: %s", key, rec.Code, rec.Body.String())

				// A key never reaches the gate: the credential guard refuses it first.
				p, c = adoptPrincipal(coreauth.MethodAPIKey, w.alfred, w.scopes...)
				rec = identityServe(w.core.router, ownerRouteRequest(key), p, c)
				require.Contains(t, []int{http.StatusUnauthorized, http.StatusForbidden}, rec.Code, "%s: a key reached %d: %s", key, rec.Code, rec.Body.String())

				rec = identityServe(w.core.router, ownerRouteRequest(key), nil, nil)
				require.Equal(t, http.StatusUnauthorized, rec.Code, "%s: no principal", key)
			}
		})
	}
}

// TestOwnerGate_IdentityOwnerUnsetLeavesTheOrdinaryGuards: with no owner set
// HEAD's gate does not apply (the first Access user may claim ownership, plan
// D15), so a signed-in admin reaches the route's own handler.
func TestOwnerGate_IdentityOwnerUnsetLeavesTheOrdinaryGuards(t *testing.T) {
	w := newOwnerGateWorld(t, func() bool { return false }, "")
	for _, key := range w.routes(t) {
		p, c := adoptPrincipal(coreauth.MethodSession, w.alfred, w.scopes...)
		rec := identityServe(w.core.router, ownerRouteRequest(key), p, c)
		require.False(t, ownerRefused(rec), "%s: refused with no owner set: %d %s", key, rec.Code, rec.Body.String())
	}
}

// TestOwnerGate_IdentityGateAdmitsOnlyTheAccessOwner runs the gate the routes
// carry (the wiring's choice between HEAD's and core's) over principals, so the
// allow side is proved without the bridge's method mapping, which B10b1 owns.
func TestOwnerGate_IdentityGateAdmitsOnlyTheAccessOwner(t *testing.T) {
	w := newOwnerGateWorld(t, func() bool { return false }, ownerRouteEmail)
	reach := func(p *coreauth.Principal) bool {
		r := gin.New()
		r.Use(func(c *gin.Context) {
			if p != nil {
				c.Request = c.Request.WithContext(coreauth.WithPrincipal(c.Request.Context(), p))
			}
		})
		r.POST("/x", w.core.ownerGateWhenOwnerSet(), func(c *gin.Context) { c.Status(http.StatusNoContent) })
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/x", nil))
		return rec.Code == http.StatusNoContent
	}
	user := &coreauth.User{ID: "u-alfred", Username: "alfred", Status: coreauth.StatusActive}
	require.True(t, reach(&coreauth.Principal{User: user, Method: cfaccess.CoreMethod, Assurance: coreauth.AssuranceOwner}), "the Access owner")
	require.False(t, reach(&coreauth.Principal{User: user, Method: cfaccess.CoreMethod}), "an Access user who is not the owner")
	require.False(t, reach(&coreauth.Principal{User: user, Method: coreauth.MethodSession, Assurance: coreauth.AssuranceOwner}), "assurance on a session is not the Access proof")
	require.False(t, reach(&coreauth.Principal{User: user, Method: coreauth.MethodSession}), "a session admin")
	require.False(t, reach(nil), "no principal")
}

// TestOwnerGate_ParityWithHead drives the owner routes on a HEAD server and
// on the core-identity server for the same callers and compares the owner
// gate's decision: the owner passes, another Access admin and a password admin
// are refused, and with no owner set nobody is.
func TestOwnerGate_ParityWithHead(t *testing.T) {
	head := setupOwnerRouteServer(t)
	w := newOwnerGateWorld(t, func() bool { return false }, ownerRouteEmail)
	headKeys := make([]string, 0, len(head.srv.ownerRoutes))
	for k := range head.srv.ownerRoutes {
		headKeys = append(headKeys, k)
	}
	sort.Strings(headKeys)
	require.Equal(t, headKeys, w.routes(t), "both chains register the same owner routes")

	do := func(key string, hdr map[string]string) *httptest.ResponseRecorder {
		req := ownerRouteRequest(key)
		for k, v := range hdr {
			req.Header.Set(k, v)
		}
		rec := httptest.NewRecorder()
		head.srv.router.ServeHTTP(rec, req)
		return rec
	}
	for _, key := range headKeys {
		// HEAD: a password session and another Access admin are refused by the owner gate; the owner is not.
		require.True(t, ownerRefused(do(key, map[string]string{"Authorization": "Bearer " + head.sessionToken})), "%s HEAD password admin", key)
		require.True(t, ownerRefused(do(key, map[string]string{cfaccess.CFAccessHeader: "jwt-other"})), "%s HEAD other Access admin", key)
		require.False(t, ownerRefused(do(key, map[string]string{cfaccess.CFAccessHeader: "jwt-owner"})), "%s HEAD owner", key)

		// Core identity: the same decisions for the same callers.
		p, c := adoptPrincipal(coreauth.MethodSession, w.alfred, w.scopes...)
		require.True(t, ownerRefused(identityServe(w.core.router, ownerRouteRequest(key), p, c)), "%s core password admin", key)
	}
	config.AppConfig.OwnerEmail = ""
	for _, key := range headKeys {
		require.False(t, ownerRefused(do(key, map[string]string{"Authorization": "Bearer " + head.sessionToken})), "%s HEAD, no owner", key)
		p, c := adoptPrincipal(coreauth.MethodSession, w.alfred, w.scopes...)
		require.False(t, ownerRefused(identityServe(w.core.router, ownerRouteRequest(key), p, c)), "%s core, no owner", key)
	}
}
