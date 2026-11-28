// file: internal/books/identity_legacy_logout_boot_test.go
// version: 1.0.0
// guid: d25472dc-869e-4811-994a-11b1c5f76066
// last-edited: 2026-11-28

package books

import (
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	database "github.com/falkcorp/audiobook-organizer/internal/books/store"
)

// HEAD's POST /api/v1/auth/logout under core identity (F4-08 fix-forward). The
// session is core's: HEAD's handler revoked a books session from the family M3
// drained, cleared the cookie and answered 200 "logged out" while core's session
// stayed valid. The route now answers 410, changes nothing and clears no cookie;
// core's POST /api/core/v1/auth/logout is what ends the session.
func TestIdentityBoot_LegacyLogoutIsGoneAndNeverAFalseLogout(t *testing.T) {
	seed := &fpSeed{}
	fx := newFlipFixture(t, func(s *database.PebbleStore) { seedFootprint(t, s, seed) }, "")
	r := bootOverHEADStore(t, fx)
	h := r.app.Handler()

	ck := loginAs(t, r, "alfred.pennyworth", fpPass)
	me := doJSON(t, h, http.MethodGet, "/api/v1/auth/me", nil, ck)
	require.Equal(t, http.StatusOK, me.Code, "precondition: the session works: %s", me.Body.String())

	out := doJSON(t, h, http.MethodPost, "/api/v1/auth/logout", nil, ck)
	require.Equal(t, http.StatusGone, out.Code, "the legacy logout is gone under core identity: %s", out.Body.String())
	require.Contains(t, out.Body.String(), "/api/core/v1/auth/logout", "it names core's logout")
	require.NotContains(t, strings.ToLower(out.Body.String()), "logged out", "it never claims a logout")
	for _, c := range out.Result().Cookies() {
		require.False(t, strings.HasSuffix(c.Name, "session_id"), "it touches no session cookie: %v", c)
	}

	still := doJSON(t, h, http.MethodGet, "/api/v1/auth/me", nil, ck)
	require.Equal(t, http.StatusOK, still.Code, "nothing was half-revoked: the session still works: %s", still.Body.String())

	coreOut := doJSON(t, h, http.MethodPost, "/api/core/v1/auth/logout", nil, ck)
	require.Less(t, coreOut.Code, 300, coreOut.Body.String())
	dead := doJSON(t, h, http.MethodGet, "/api/v1/auth/me", nil, ck)
	require.Equal(t, http.StatusUnauthorized, dead.Code, "core's logout ends the session: %s", dead.Body.String())
}
