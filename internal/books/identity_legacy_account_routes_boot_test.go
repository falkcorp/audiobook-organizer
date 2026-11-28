// file: internal/books/identity_legacy_account_routes_boot_test.go
// version: 1.0.0
// guid: 02d03265-45b4-49cc-8f59-3a4bd73dc4b8
// last-edited: 2026-11-28

package books

import (
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"

	database "github.com/falkcorp/audiobook-organizer/internal/books/store"
)

// HEAD's account routes that the flip left mounted, on a migrated store (F4-08
// fix-forward sweep). Each reads books' user, session or role rows, which M3
// drained, so under core identity each one REFUSES or changes nothing, whatever
// the caller holds. None of them may admit, sign anyone in or change an account:
// wiring them to core would move refusals toward admission, which is out of
// scope, and their deletion is filed (todo.d). This pins the refusals, and
// shows each account is untouched afterwards: the passwords still work at core's
// login and the accounts stay active.
func TestIdentityBoot_LegacyAccountRoutesRefuseOnAMigratedStore(t *testing.T) {
	seed := &fpSeed{}
	fx := newFlipFixture(t, func(s *database.PebbleStore) { seedFootprint(t, s, seed) }, "")
	r := bootOverHEADStore(t, fx)
	h := r.app.Handler()

	admin := loginAs(t, r, "lucius.fox", fpPass)
	viewer := loginAs(t, r, "alfred.pennyworth", fpPass)

	refused := func(t *testing.T, method, path string, body any, ck *http.Cookie) {
		t.Helper()
		rec := doJSON(t, h, method, path, body, ck)
		require.GreaterOrEqual(t, rec.Code, 400, "%s %s must refuse on a migrated store: %d %s", method, path, rec.Code, rec.Body.String())
	}

	// HEAD's password sign-in finds no books user: the real password is refused
	// and no session cookie is set.
	login := doJSON(t, h, http.MethodPost, "/api/v1/auth/login", map[string]string{"username": "lucius.fox", "password": fpPass}, nil)
	require.Equal(t, http.StatusUnauthorized, login.Code, login.Body.String())
	for _, c := range login.Result().Cookies() {
		require.Empty(t, c.Value, "HEAD's login sets no session: %v", c)
	}

	refused(t, http.MethodPatch, "/api/v1/auth/me", map[string]string{"email": "oracle@example.test"}, viewer)
	refused(t, http.MethodPut, "/api/v1/auth/me/password", map[string]string{"current_password": fpPass, "new_password": "a-different-batarang-77"}, viewer)
	refused(t, http.MethodPut, "/api/v1/auth/me/password", map[string]string{"user_id": seed.target, "new_password": "a-different-batarang-77"}, admin)
	refused(t, http.MethodDelete, "/api/v1/auth/sessions/0123456789abcdef", nil, viewer)
	refused(t, http.MethodPost, "/api/v1/users/"+seed.target+"/reset-password", nil, admin)
	refused(t, http.MethodPost, "/api/v1/users/"+seed.target+"/deactivate", nil, admin)
	refused(t, http.MethodPost, "/api/v1/users/"+seed.viewer+"/reactivate", nil, admin)

	// The sessions list reads books' drained session rows: it lists nothing, so it
	// discloses no session of anyone.
	list := doJSON(t, h, http.MethodGet, "/api/v1/auth/sessions", nil, viewer)
	require.Equal(t, http.StatusOK, list.Code, list.Body.String())
	require.JSONEq(t, `{"data":{"sessions":[]}}`, list.Body.String())

	// Nothing changed: every password still signs in at core, and every account
	// is active (core's login refuses a disabled one).
	loginAs(t, r, "lucius.fox", fpPass)
	loginAs(t, r, "alfred.pennyworth", fpPass)
	loginAs(t, r, "selina.kyle", fpPass)
	me := doJSON(t, h, http.MethodGet, "/api/v1/auth/me", nil, viewer)
	require.Equal(t, http.StatusOK, me.Code, "the caller's session still works: %s", me.Body.String())
}
