// file: internal/server/identity_sites_test.go
// version: 0.1.0
// guid: 5e8b3a17-0c92-4d64-a7f1-2b6d9c4e8a05
// last-edited: 2026-11-21

package server

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"

	coreauth "github.com/falkcorp/mediacore/auth"
	"github.com/falkcorp/mediacore/rbac"

	"github.com/falkcorp/audiobook-organizer/internal/auth"
	database "github.com/falkcorp/audiobook-organizer/internal/books/store"
	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/server/legacyauth"
	servermiddleware "github.com/falkcorp/audiobook-organizer/internal/server/middleware"
)

// siteWorld is one Server per mode over a store: books' own, as books mounts
// it today (no identity, LegacyAuthGin), and the same server built with
// WithIdentity (LegacyAuthCore). Each adopted site is asked the same question
// of both.
type siteWorld struct {
	head, core *Server
	id         *legacyauth.Identity
	bruce      *database.User
	store      database.Store
}

// newSiteWorld builds both servers over a store holding one user when
// withUser (so HEAD's first-run bypass is closed) and none otherwise (open).
// The table knows the atoms the sites ask for.
func newSiteWorld(t *testing.T, withUser, authOn bool, atoms ...string) *siteWorld {
	t.Helper()
	gin.SetMode(gin.TestMode)
	orig := config.AppConfig
	t.Cleanup(func() { config.AppConfig = orig })
	config.AppConfig.EnableAuth = authOn

	store, err := database.NewPebbleStoreInMemory(t.TempDir() + "/sites.pebble")
	require.NoError(t, err)
	t.Cleanup(func() { store.Close() })
	w := &siteWorld{store: store}
	if withUser {
		u, err := store.CreateUser("bruce", "bruce@example.test", "bcrypt", "x", []string{auth.SeedRoleViewer}, "active")
		require.NoError(t, err)
		w.bruce = u
	} else {
		w.bruce = &database.User{ID: "u-bruce", Username: "bruce", Status: "active"}
	}
	w.id = adoptIdentity(t, atoms, nil, nil, w.bruce)
	w.head = newTestServer(t, store)
	w.core = newTestServerWith(t, store, WithIdentity(w.id))
	return w
}

// ctxOf is a request context as a mode would hold it: HEAD's own permission
// set when headPerms is non-nil, the core principal and checker stage 12 binds
// when p is non-nil, and the Identity when bound.
func (w *siteWorld) ctxOf(bound bool, headPerms []auth.Permission, p *coreauth.Principal, c rbac.Checker) context.Context {
	ctx := context.Background()
	if bound {
		ctx = legacyauth.WithIdentity(ctx, w.id)
	}
	if headPerms != nil {
		ctx = auth.WithPermissions(auth.WithUser(ctx, w.bruce), headPerms)
	}
	if p != nil {
		ctx = coreauth.WithPrincipal(ctx, p)
		if c != nil {
			ctx = rbac.WithChecker(ctx, c)
		}
	}
	return ctx
}

func ginOf(ctx context.Context) *gin.Context {
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodGet, "/gotham", nil).WithContext(ctx)
	return c
}

// TestSites_PermHolds covers permHolds, HoldsPermission (through it) and the
// ops v3 Holds: three sites that share one decision.
func TestSites_PermHolds(t *testing.T) {
	const atom = "library.view"
	scoped := func(w *siteWorld, scopes ...rbac.Permission) (*coreauth.Principal, rbac.Checker) {
		return adoptPrincipal(coreauth.MethodSession, w.bruce, scopes...)
	}
	holders := map[string]func(*siteWorld, bool) func(*gin.Context, auth.Permission) bool{
		"permHolds": func(w *siteWorld, bridged bool) func(*gin.Context, auth.Permission) bool {
			if bridged {
				return w.core.permHolds()
			}
			return w.head.permHolds()
		},
		"opsV3Authz.Holds": func(w *siteWorld, bridged bool) func(*gin.Context, auth.Permission) bool {
			if bridged {
				return w.core.opsV3Authz().Holds
			}
			return w.head.opsV3Authz().Holds
		},
	}
	for site, holds := range holders {
		t.Run(site+"/LegacyAuthGin leaves HEAD deciding", func(t *testing.T) {
			w := newSiteWorld(t, true, true, atom)
			lax, laxC := scoped(w, "test:"+atom)
			// A core principal that holds the atom is invisible: HEAD reads its own set.
			require.False(t, holds(w, false)(ginOf(w.ctxOf(false, nil, lax, laxC)), auth.Permission(atom)))
			require.True(t, holds(w, false)(ginOf(w.ctxOf(false, []auth.Permission{atom}, nil, nil)), auth.Permission(atom)))
			require.False(t, holds(w, false)(ginOf(w.ctxOf(false, []auth.Permission{"library.delete"}, nil, nil)), auth.Permission(atom)))
		})
		t.Run(site+"/HEAD's first-run bypass is HEAD's", func(t *testing.T) {
			w := newSiteWorld(t, false, true, atom)
			require.True(t, holds(w, false)(ginOf(w.ctxOf(false, nil, nil, nil)), auth.Permission(atom)),
				"precondition: an empty store holds every permission under HEAD")
		})
		t.Run(site+"/core identity judges the principal", func(t *testing.T) {
			w := newSiteWorld(t, true, true, atom)
			yes, yesC := scoped(w, "test:"+atom)
			no, noC := scoped(w, "test:other")
			require.True(t, holds(w, true)(ginOf(w.ctxOf(true, nil, yes, yesC)), auth.Permission(atom)))
			require.False(t, holds(w, true)(ginOf(w.ctxOf(true, nil, no, noC)), auth.Permission(atom)), "a principal without the scope")
			require.False(t, holds(w, true)(ginOf(w.ctxOf(true, nil, yes, nil)), auth.Permission(atom)), "no checker bound")
			require.False(t, holds(w, true)(ginOf(w.ctxOf(true, nil, nil, nil)), auth.Permission(atom)), "no principal")
			require.False(t, holds(w, true)(ginOf(w.ctxOf(true, nil, yes, yesC)), "batcave.flood"), "an atom the table does not know")
		})
		t.Run(site+"/core identity has no first-run bypass", func(t *testing.T) {
			w := newSiteWorld(t, false, true, atom)
			require.False(t, holds(w, true)(ginOf(w.ctxOf(true, nil, nil, nil)), auth.Permission(atom)),
				"an anonymous request on an empty store must not hold the atom under core identity")
		})
		t.Run(site+"/core identity: auth off is the auth-disabled principal, not EnableAuth", func(t *testing.T) {
			// books' EnableAuth is true, as after a flip where core's mode is off.
			w := newSiteWorld(t, true, true, atom)
			off, offC := adoptPrincipal(coreauth.MethodAuthDisabled, nil, "test:nothing")
			require.True(t, holds(w, true)(ginOf(w.ctxOf(true, nil, off, offC)), auth.Permission(atom)))
			// and books' EnableAuth=false no longer opens anything.
			w2 := newSiteWorld(t, true, false, atom)
			no, noC := scoped(w2, "test:other")
			require.False(t, holds(w2, true)(ginOf(w2.ctxOf(true, nil, no, noC)), auth.Permission(atom)))
		})
	}

	t.Run("HoldsPermission is strict under core identity: the auth-disabled principal does not hold", func(t *testing.T) {
		w := newSiteWorld(t, true, true, atom)
		off, offC := adoptPrincipal(coreauth.MethodAuthDisabled, nil)
		require.False(t, servermiddleware.HoldsPermission(w.store, ginOf(w.ctxOf(true, nil, off, offC)), auth.Permission(atom)))
		yes, yesC := scoped(w, "test:"+atom)
		require.True(t, servermiddleware.HoldsPermission(w.store, ginOf(w.ctxOf(true, nil, yes, yesC)), auth.Permission(atom)))
		// HEAD: the same call with HEAD's set.
		require.True(t, servermiddleware.HoldsPermission(w.store, ginOf(w.ctxOf(false, []auth.Permission{atom}, nil, nil)), auth.Permission(atom)))
		require.False(t, servermiddleware.HoldsPermission(w.store, ginOf(w.ctxOf(false, nil, yes, yesC)), auth.Permission(atom)))
	})
}

// TestSites_PermHoldsLive: a stream's recheck goes to core's Refresh under
// core identity and can only narrow; HEAD's recheck against books' store is
// what runs without an identity.
func TestSites_PermHoldsLive(t *testing.T) {
	const atom = "library.view"
	w := newSiteWorld(t, true, true, atom)
	yes, yesC := adoptPrincipal(coreauth.MethodSession, w.bruce, "test:"+atom)

	holds := func(refresh func(context.Context, *coreauth.Principal) (*coreauth.Principal, error), ctx context.Context) bool {
		w.id.Refresh = refresh
		return w.core.permHoldsLive()(ginOf(ctx), auth.Permission(atom))
	}
	same := func(_ context.Context, p *coreauth.Principal) (*coreauth.Principal, error) { return p, nil }
	require.True(t, holds(same, w.ctxOf(true, nil, yes, yesC)), "a principal that still stands holds what it opened with")

	demoted := func(_ context.Context, p *coreauth.Principal) (*coreauth.Principal, error) {
		np := *p
		np.Perms = adoptChecker{"test:other"}
		return &np, nil
	}
	require.False(t, holds(demoted, w.ctxOf(true, nil, yes, yesC)), "a role lowered after the stream opened: the fresh checker no longer holds the atom")

	gone := func(context.Context, *coreauth.Principal) (*coreauth.Principal, error) {
		return nil, coreauth.ErrInactiveUser
	}
	require.False(t, holds(gone, w.ctxOf(true, nil, yes, yesC)), "a disabled account stops holding")
	require.False(t, holds(func(context.Context, *coreauth.Principal) (*coreauth.Principal, error) {
		return nil, errors.New("store down")
	}, w.ctxOf(true, nil, yes, yesC)), "a refresh that cannot answer fails closed")
	require.False(t, holds(func(context.Context, *coreauth.Principal) (*coreauth.Principal, error) {
		return &coreauth.Principal{Method: coreauth.MethodSession}, nil
	}, w.ctxOf(true, nil, yes, yesC)), "a refreshed principal with no checker holds nothing")
	require.False(t, holds(same, w.ctxOf(true, nil, nil, nil)), "no principal")

	off, offC := adoptPrincipal(coreauth.MethodAuthDisabled, nil)
	require.True(t, holds(same, w.ctxOf(true, nil, off, offC)), "auth off passes while core still answers it")
	require.False(t, holds(gone, w.ctxOf(true, nil, off, offC)), "and stops once core refuses it")

	// HEAD: the identity-free server never asks Refresh.
	asked := false
	w.id.Refresh = func(context.Context, *coreauth.Principal) (*coreauth.Principal, error) { asked = true; return yes, nil }
	got := w.head.permHoldsLive()(ginOf(w.ctxOf(false, []auth.Permission{atom}, yes, yesC)), auth.Permission(atom))
	require.False(t, asked, "HEAD's chain must not ask core")
	require.False(t, got, "HEAD's recheck reads books' store, which holds no such user")
}

// TestSites_RunMaintenanceJob: the per-job permission check.
func TestSites_RunMaintenanceJob(t *testing.T) {
	const atom = "settings.manage"
	do := func(s *Server, ctx context.Context) int {
		rec := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(rec)
		c.Params = gin.Params{{Key: "job_id", Value: "relink-report"}}
		req := httptest.NewRequest(http.MethodPost, "/maintenance/jobs/relink-report", http.NoBody).WithContext(ctx)
		req.Header.Set("Content-Type", "application/json")
		c.Request = req
		s.runMaintenanceJob(c)
		return rec.Code
	}
	t.Run("LegacyAuthGin leaves HEAD deciding", func(t *testing.T) {
		w := newSiteWorld(t, true, true, atom)
		yes, yesC := adoptPrincipal(coreauth.MethodSession, w.bruce, "test:"+atom)
		require.Equal(t, http.StatusForbidden, do(w.head, w.ctxOf(false, []auth.Permission{"library.view"}, yes, yesC)), "HEAD sees only its own set")
		require.Equal(t, http.StatusUnauthorized, do(w.head, w.ctxOf(false, nil, yes, yesC)), "HEAD: no user on the context")
		require.NotContains(t, []int{http.StatusUnauthorized, http.StatusForbidden}, do(w.head, w.ctxOf(false, []auth.Permission{atom}, nil, nil)))
	})
	t.Run("HEAD with EnableAuth off skips the check", func(t *testing.T) {
		w := newSiteWorld(t, true, false, atom)
		require.NotContains(t, []int{http.StatusUnauthorized, http.StatusForbidden}, do(w.head, w.ctxOf(false, nil, nil, nil)))
	})
	t.Run("core identity judges the principal", func(t *testing.T) {
		w := newSiteWorld(t, true, false, atom) // EnableAuth false must not open anything
		yes, yesC := adoptPrincipal(coreauth.MethodSession, w.bruce, "test:"+atom)
		no, noC := adoptPrincipal(coreauth.MethodSession, w.bruce, "test:other")
		require.Equal(t, http.StatusForbidden, do(w.core, legacyCtxWithUser(w, true, no, noC)), "a principal without the scope")
		require.Equal(t, http.StatusUnauthorized, do(w.core, w.ctxOf(true, nil, nil, nil)), "no principal")
		require.NotContains(t, []int{http.StatusUnauthorized, http.StatusForbidden}, do(w.core, legacyCtxWithUser(w, true, yes, yesC)))
		off, offC := adoptPrincipal(coreauth.MethodAuthDisabled, nil)
		require.NotContains(t, []int{http.StatusUnauthorized, http.StatusForbidden}, do(w.core, w.ctxOf(true, nil, off, offC)), "auth off is the auth-disabled principal")
	})
}

// legacyCtxWithUser is ctxOf with HEAD's user bound too, as Authenticate
// leaves the request: the 401/403 split reads it.
func legacyCtxWithUser(w *siteWorld, bound bool, p *coreauth.Principal, c rbac.Checker) context.Context {
	return auth.WithUser(w.ctxOf(bound, nil, p, c), w.bruce)
}
