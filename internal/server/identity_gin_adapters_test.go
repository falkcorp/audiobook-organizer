// file: internal/server/identity_gin_adapters_test.go
// version: 0.1.0
// guid: 0c4f7d92-6b1a-4e38-9d05-a2e8b7c3f614
// last-edited: 2026-11-21

package server

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"

	coreauth "github.com/falkcorp/mediacore/auth"
	"github.com/falkcorp/mediacore/rbac"

	"github.com/falkcorp/audiobook-organizer/internal/auth"
	database "github.com/falkcorp/audiobook-organizer/internal/books/store"
	servermiddleware "github.com/falkcorp/audiobook-organizer/internal/server/middleware"
)

// adoptUnknownUser is a user the bridge's lookup does not know.
var adoptUnknownUser = database.User{ID: "u-joker", Username: "joker", Status: "active"}

// adapterServe mounts mw then an OK handler on a fresh engine and serves one
// GET. bound puts the Identity on the request first, as NewServer's Bind
// middleware does on a bridged server; p and c are the principal and checker
// stage 12 binds (nil binds none). The handler records the user the chain left
// on the gin context.
func (w *siteWorld) adapterServe(bound bool, mw gin.HandlerFunc, p *coreauth.Principal, c rbac.Checker) (code int, user string) {
	r := gin.New()
	if bound {
		r.Use(w.id.Bind())
	}
	r.GET("/x", mw, func(g *gin.Context) {
		if u, ok := servermiddleware.CurrentUser(g); ok && u != nil {
			user = u.Username
		}
		g.Status(http.StatusOK)
	})
	rec := identityServe(r, httptest.NewRequest(http.MethodGet, "/x", nil), p, c)
	return rec.Code, user
}

// TestGinAdapters_Perm: s.perm asks the atom's table entry through the
// principal under core identity and books' EnableAuth otherwise.
func TestGinAdapters_Perm(t *testing.T) {
	const atom = "library.view"

	t.Run("legacy: HEAD's guard decides and a core principal changes nothing", func(t *testing.T) {
		off := newSiteWorld(t, true, false, atom)
		other, otherC := adoptPrincipal(coreauth.MethodSession, off.bruce, "test:other")
		for name, p := range map[string]*coreauth.Principal{"no principal": nil, "a core principal without the atom": other} {
			code, _ := off.adapterServe(false, off.head.perm(auth.Permission(atom)), p, otherC)
			require.Equal(t, http.StatusOK, code, "auth off at HEAD is a pass-through: "+name)
		}
		on := newSiteWorld(t, true, true, atom)
		yes, yesC := adoptPrincipal(coreauth.MethodSession, on.bruce, "test:"+atom)
		code, _ := on.adapterServe(false, on.head.perm(auth.Permission(atom)), yes, yesC)
		require.Equal(t, http.StatusUnauthorized, code, "HEAD reads its own user, not the core principal")
	})

	t.Run("core identity: the principal's scopes decide, books' EnableAuth is not read", func(t *testing.T) {
		for _, authOn := range []bool{true, false} {
			w := newSiteWorld(t, true, authOn, atom)
			mw := w.core.perm(auth.Permission(atom))
			code, _ := w.adapterServe(true, mw, nil, nil)
			require.Equal(t, http.StatusUnauthorized, code, "no principal")
			no, noC := adoptPrincipal(coreauth.MethodSession, w.bruce, "test:other")
			code, _ = w.adapterServe(true, mw, no, noC)
			require.Equal(t, http.StatusForbidden, code, "a principal without the atom's scope, EnableAuth=%v", authOn)
			yes, yesC := adoptPrincipal(coreauth.MethodSession, w.bruce, "test:"+atom)
			code, _ = w.adapterServe(true, mw, yes, yesC)
			require.Equal(t, http.StatusOK, code, "a principal with the scope")
			// core's auth-disabled checker holds every requirement that is
			// not all-AuthRequired; the fixture's checker stands for it by
			// holding the atom's scope.
			off, offC := adoptPrincipal(coreauth.MethodAuthDisabled, nil, "test:"+atom)
			code, _ = w.adapterServe(true, mw, off, offC)
			require.Equal(t, http.StatusOK, code, "the auth-disabled principal is judged by its own checker")
			offNone, offNoneC := adoptPrincipal(coreauth.MethodAuthDisabled, nil)
			code, _ = w.adapterServe(true, mw, offNone, offNoneC)
			require.Equal(t, http.StatusUnauthorized, code, "and refused with 401 (no user) where its checker does not hold")
			code, _ = w.adapterServe(true, w.core.perm(auth.Permission("not.in.the.table")), yes, yesC)
			require.Equal(t, http.StatusForbidden, code, "an atom the table does not know is refused")
		}
	})
}

// TestGinAdapters_CredGuard: the credential-change guard, both shapes, asks
// the principal's method under core identity.
func TestGinAdapters_CredGuard(t *testing.T) {
	t.Run("legacy: auth off is a pass-through whatever a core principal says", func(t *testing.T) {
		w := newSiteWorld(t, true, false)
		key, keyC := adoptPrincipal(coreauth.MethodAPIKey, w.bruce, "test:x")
		for name, mw := range map[string]gin.HandlerFunc{
			"always":      w.head.credGuardMiddleware(nil),
			"conditional": w.head.credGuardMiddleware(func(*gin.Context) bool { return true }),
		} {
			code, _ := w.adapterServe(false, mw, key, keyC)
			require.Equal(t, http.StatusOK, code, name)
		}
	})

	t.Run("core identity: always-guarded", func(t *testing.T) {
		w := newSiteWorld(t, true, false)
		mw := w.core.credGuardMiddleware(nil)
		code, _ := w.adapterServe(true, mw, nil, nil)
		require.Equal(t, http.StatusUnauthorized, code, "no principal")
		for _, m := range []coreauth.Method{coreauth.MethodSession, coreauth.MethodSessionDelegated} {
			p, c := adoptPrincipal(m, w.bruce, "test:x")
			code, _ = w.adapterServe(true, mw, p, c)
			require.Equal(t, http.StatusOK, code, string(m))
		}
		for _, m := range []coreauth.Method{coreauth.MethodAPIKey, coreauth.MethodAuthDisabled, "ext:cfaccess"} {
			p, c := adoptPrincipal(m, w.bruce, "test:x")
			code, _ = w.adapterServe(true, mw, p, c)
			require.Equal(t, http.StatusForbidden, code, string(m)+": HEAD passes the auth-disabled case, core refuses it")
		}
	})

	t.Run("core identity: conditional", func(t *testing.T) {
		w := newSiteWorld(t, true, true)
		var asked int
		guard := func(answer bool) gin.HandlerFunc {
			asked = 0
			return w.core.credGuardMiddleware(func(*gin.Context) bool { asked++; return answer })
		}
		sess, sessC := adoptPrincipal(coreauth.MethodSession, w.bruce, "test:x")
		key, keyC := adoptPrincipal(coreauth.MethodAPIKey, w.bruce, "test:x")

		code, _ := w.adapterServe(true, guard(true), sess, sessC)
		require.Equal(t, http.StatusOK, code, "an interactive method passes")
		require.Zero(t, asked, "and guarded is not consulted for it")

		code, _ = w.adapterServe(true, guard(true), key, keyC)
		require.Equal(t, http.StatusForbidden, code, "a key when guarded reports true")
		require.Equal(t, 1, asked)
		code, _ = w.adapterServe(true, guard(false), key, keyC)
		require.Equal(t, http.StatusOK, code, "a key when guarded reports false")
		code, _ = w.adapterServe(true, guard(true), nil, nil)
		require.Equal(t, http.StatusUnauthorized, code, "no principal when guarded reports true")
	})
}

// TestGinAdapters_RequireAuth: s.requireAuth hands HEAD's handlers the user
// stage 12 resolved under core identity and authenticates through books'
// session otherwise.
func TestGinAdapters_RequireAuth(t *testing.T) {
	t.Run("legacy: HEAD's RequireAuth, a core principal is not read", func(t *testing.T) {
		off := newSiteWorld(t, true, false)
		sess, sessC := adoptPrincipal(coreauth.MethodSession, off.bruce)
		code, user := off.adapterServe(false, off.head.requireAuth(), sess, sessC)
		require.Equal(t, http.StatusOK, code, "auth off at HEAD passes")
		require.Empty(t, user, "and binds no user")

		on := newSiteWorld(t, true, true)
		sess, sessC = adoptPrincipal(coreauth.MethodSession, on.bruce)
		code, user = on.adapterServe(false, on.head.requireAuth(), sess, sessC)
		require.Equal(t, http.StatusUnauthorized, code, "HEAD asks for its own session")
		require.Empty(t, user)
	})

	t.Run("core identity: the principal's user is bound, books' EnableAuth is not read", func(t *testing.T) {
		for _, authOn := range []bool{true, false} {
			w := newSiteWorld(t, true, authOn)
			mw := w.core.requireAuth()
			code, _ := w.adapterServe(true, mw, nil, nil)
			require.Equal(t, http.StatusUnauthorized, code, "no principal, EnableAuth=%v", authOn)

			sess, sessC := adoptPrincipal(coreauth.MethodSession, w.bruce)
			code, user := w.adapterServe(true, mw, sess, sessC)
			require.Equal(t, http.StatusOK, code)
			require.Equal(t, "bruce", user, "HEAD's handlers see the user core resolved")

			gone, goneC := adoptPrincipal(coreauth.MethodSession, &adoptUnknownUser, "test:x")
			code, user = w.adapterServe(true, mw, gone, goneC)
			require.Equal(t, http.StatusUnauthorized, code, "a principal whose user cannot be mapped")
			require.Empty(t, user)

			off, offC := adoptPrincipal(coreauth.MethodAuthDisabled, nil)
			code, user = w.adapterServe(true, mw, off, offC)
			require.Equal(t, http.StatusOK, code, "the auth-disabled principal passes")
			require.Empty(t, user, "and binds no user")
		}
	})
}
