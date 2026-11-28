// file: internal/server/identity_access_binder_test.go
// version: 1.0.0
// guid: d3291f8b-8c54-40db-bca2-240dd7e9e0c5
// last-edited: 2026-11-28

package server

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"

	database "github.com/falkcorp/audiobook-organizer/internal/books/store"
	"github.com/falkcorp/audiobook-organizer/internal/cfaccess"
	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/legacyoauth"
	"github.com/falkcorp/audiobook-organizer/internal/server/legacyauth"
)

// Under core identity books' gin Access stage (cfMW, on /api/v1, /api/v3,
// /metrics and /api/events) verifies the assertion and marks the request, and
// never resolves, links or creates a books user: core's Access extension
// authenticated the request at stage 12, and M3 retired every family HEAD's
// resolver (legacyoauth.ResolveUser) reads and writes. These cells run a real
// Server built WithIdentity with the Access verifier overridden; the principal
// is bound as stage 12 binds it (identityServe).

const (
	binderAlfredJWT = "jwt-alfred-from-the-batcave"
	binderForgedJWT = "jwt-forged-by-the-riddler"
	binderEmail     = "alfred@example.test"
)

// errRetiredClaimWrite is the refusal a migrated store answers a write to a
// retired claim with (core/storage/db.go: a retired claim admits Delete and
// refuses Set).
var errRetiredClaimWrite = errors.New("binder test: a write to a retired claim is refused")

// accessBinderStore answers the reads and writes legacyoauth.ResolveUser makes
// (legacyoauth.UserStore) and records each call. By default it answers as a
// migrated store does: a retired claim reads empty and refuses a write. With
// admit set it answers as a store with no retired claim would: alfred's
// verified e-mail names an active books admin and every write succeeds, so
// nothing in the store holds a refusal.
type accessBinderStore struct {
	database.Store
	admit bool
	mu    sync.Mutex
	calls []string
}

// booksAlfred is the books record an admitting store resolves alfred's e-mail to.
func booksAlfred() *database.User {
	return &database.User{ID: "u-books-alfred", Username: "alfred", Email: binderEmail, Status: "active", Roles: []string{"admin"}}
}

func (s *accessBinderStore) record(m string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls = append(s.calls, m)
}

func (s *accessBinderStore) seen() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.calls...)
}

func (s *accessBinderStore) GetOAuthIdentityByProviderSubject(string, string) (*database.OAuthIdentity, error) {
	s.record("GetOAuthIdentityByProviderSubject")
	return nil, nil
}

func (s *accessBinderStore) GetUserByVerifiedEmail(string) (*database.User, error) {
	s.record("GetUserByVerifiedEmail")
	if s.admit {
		return booksAlfred(), nil
	}
	return nil, nil
}

func (s *accessBinderStore) CreateUser(string, string, string, string, []string, string) (*database.User, error) {
	s.record("CreateUser")
	if s.admit {
		return booksAlfred(), nil
	}
	return nil, errRetiredClaimWrite
}

func (s *accessBinderStore) CreateOAuthIdentity(*database.OAuthIdentity) (*database.OAuthIdentity, error) {
	s.record("CreateOAuthIdentity")
	if s.admit {
		return &database.OAuthIdentity{}, nil
	}
	return nil, errRetiredClaimWrite
}

type accessBinderFixture struct {
	srv    *Server
	store  *accessBinderStore
	alfred *database.User
	// marked is whether the last /api/v1 or /api/v3 request left the verified
	// marker set, read after the whole chain ran.
	marked bool
}

// newAccessBinderFixture is an identity server with the Access verifier
// overridden to admit alfred's assertion, and alfred on books' allow-list, so
// HEAD's binder, were it mounted, would reach the store.
func newAccessBinderFixture(t *testing.T) *accessBinderFixture {
	t.Helper()
	return newAccessBinderFixtureWith(t, false)
}

// newAccessBinderFixtureWith is newAccessBinderFixture over a store that admits
// every resolve and write when admit is set.
func newAccessBinderFixtureWith(t *testing.T, admit bool) *accessBinderFixture {
	t.Helper()
	gin.SetMode(gin.TestMode)
	setTunnelConnectors(t, "")
	cfAccessVerifierOverride = fakeAccessVerifier{
		binderAlfredJWT: {Provider: legacyoauth.ProviderCFAccess, Subject: "cf|alfred", Email: binderEmail, EmailVerified: true},
	}
	t.Cleanup(func() { cfAccessVerifierOverride = nil })
	orig := config.AppConfig
	t.Cleanup(func() { config.AppConfig = orig })
	config.AppConfig = config.Config{
		DatabaseType:       "pebble",
		RootDir:            t.TempDir(),
		EnableAuth:         true,
		OAuthAllowedEmails: binderEmail,
	}
	pebble, err := database.NewPebbleStoreInMemory(t.TempDir() + "/access-binder.pebble")
	require.NoError(t, err)
	t.Cleanup(func() { pebble.Close() })
	fx := &accessBinderFixture{
		store:  &accessBinderStore{Store: pebble, admit: admit},
		alfred: &database.User{ID: "u-alfred", Username: "alfred", Email: binderEmail, Status: "active"},
	}
	// Read the marker after the rest of the chain: the hook runs first in the
	// /api/v1 and /api/v3 groups, so c.Next() here runs cfMW, the auth stage
	// and the handler before the read.
	apiGroupHookForTest = func(c *gin.Context) {
		c.Next()
		fx.marked = cfaccess.CFAccessVerified(c)
	}
	t.Cleanup(func() { apiGroupHookForTest = nil })
	id := adoptIdentity(t, []string{"library.view"}, nil, nil, fx.alfred)
	fx.srv = newTestServerWith(t, fx.store, WithIdentity(id))
	return fx
}

func binderRequest(path, assertion string) *http.Request {
	req := httptest.NewRequest(http.MethodGet, path, nil)
	req.RemoteAddr = "127.0.0.1:4100"
	if assertion != "" {
		req.Header.Set(cfaccess.CFAccessHeader, assertion)
	}
	return req
}

// A valid Access assertion for an allow-listed person authenticates through
// core's principal alone: the request is alfred's, the verified marker is set
// (client_trust.go reads it), and books' store is never asked to resolve, link
// or create anyone. HEAD's binder mounted under identity reads oauthid: and
// attempts the refused u: write on this request, and fails the store check.
func TestIdentityAccessBinder_AValidAssertionBindsNoBooksUser(t *testing.T) {
	fx := newAccessBinderFixture(t)
	p, c := adoptPrincipal(legacyauth.MethodExtCFAccess, fx.alfred)
	rec := identityServe(fx.srv.router, binderRequest("/api/v1/auth/me", binderAlfredJWT), p, c)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.Contains(t, rec.Body.String(), `"u-alfred"`, "the request is the principal's user")
	require.True(t, fx.marked, "the assertion is still verified and the request marked")
	require.Empty(t, fx.store.seen(), "books' store resolved, linked or created no one (no retired-claim write)")
}

// Without a principal the request is refused whatever assertion it carries, on
// an /api/v1 route and on an ABS route, as before: the Access stage never
// admits anyone, and it consults no store either way.
func TestIdentityAccessBinder_NoPrincipalIsStillRefused(t *testing.T) {
	fx := newAccessBinderFixture(t)
	for _, tc := range []struct {
		name      string
		assertion string
		marked    bool
	}{
		{name: "no assertion"},
		{name: "an assertion that does not verify", assertion: binderForgedJWT},
		{name: "a valid assertion core admitted no one for", assertion: binderAlfredJWT, marked: true},
	} {
		fx.marked = false
		rec := identityServe(fx.srv.router, binderRequest("/api/v1/auth/me", tc.assertion), nil, nil)
		require.Equal(t, http.StatusUnauthorized, rec.Code, "/api/v1, %s: %s", tc.name, rec.Body.String())
		require.Equal(t, tc.marked, fx.marked, "/api/v1, %s: marker", tc.name)
	}
	require.Empty(t, fx.store.seen(), "books' store resolved, linked or created no one")
	requireABSRefusesAccessOnly(t)
}

// requireABSRefusesAccessOnly pins the ABS surface's answer to an Access-only
// request under core identity: 401 whatever assertion it carries. The surface
// is not behind books' gin Access stage (it has its own fail-closed resolver,
// wireABSRoutes), so this is the "as before" leg for it. Its server has a plain
// Pebble store, which the surface requires.
func requireABSRefusesAccessOnly(t *testing.T) {
	t.Helper()
	s := identityABSServer(t, WithAccessVerifier(fakeAccessVerifier{
		binderAlfredJWT: {Provider: legacyoauth.ProviderCFAccess, Subject: "cf|alfred", Email: binderEmail, EmailVerified: true},
	}))
	for _, assertion := range []string{"", binderForgedJWT, binderAlfredJWT} {
		rec := identityServe(s.router, binderRequest("/api/me", assertion), nil, nil)
		require.Equal(t, http.StatusUnauthorized, rec.Code, "ABS /api/me, assertion %q: %s", assertion, rec.Body.String())
	}
}

// The refusal of an Access-only request (a valid, allow-listed assertion and no
// core principal) is an explicit check, not the storage claim: over a store
// that would resolve alfred to an active books admin and admit every write,
// each route family books' Access stage covers (/api/v1, /api/v3, /metrics,
// /api/events) and the ABS surface still answer 401, and books' store is never
// asked to resolve, link or create anyone. The checks holding it are the
// identity branch of accessMiddleware (no books resolve under core identity)
// and the bridge's principal requirement (requireAuth). HEAD's binder mounted
// under identity resolves alfred here and fails the store check; an auth stage
// that admits a request without a principal fails the status check.
func TestIdentityAccessBinder_AccessOnlyIsRefusedByCheckNotByStorage(t *testing.T) {
	fx := newAccessBinderFixtureWith(t, true)
	// /metrics before /api/events: an auth stage that let a request through
	// with no principal would hold the event stream open, so the route that
	// answers at once goes first and fails first.
	for _, path := range []string{"/api/v1/auth/me", "/api/v3/ops/defs", "/metrics", "/api/events"} {
		rec := identityServe(fx.srv.router, binderRequest(path, binderAlfredJWT), nil, nil)
		require.Equal(t, http.StatusUnauthorized, rec.Code, "%s: an Access-only request with no core principal: %s", path, rec.Body.String())
	}
	require.Empty(t, fx.store.seen(), "books' store resolved, linked or created no one, though it would have admitted it")
	requireABSRefusesAccessOnly(t)
}
