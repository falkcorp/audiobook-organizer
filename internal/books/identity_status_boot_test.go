// file: internal/books/identity_status_boot_test.go
// version: 0.2.0
// guid: 8a4d1c73-2f95-4b60-9e18-c6b07d3a52e4
// last-edited: 2026-11-28

package books

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/auth"
	database "github.com/falkcorp/audiobook-organizer/internal/books/store"
)

// The three checks the 2026-11-24 prod-copy rehearsal (F4-08 at 69260476d)
// failed, through a real boot over a HEAD store that M3 migrates:
// auth_status_same, counts_same:/api/v1/users and f408_c2_no_p4d_failure.

// headUsersAt is what HEAD's own store calls answered over the seeded rows,
// before the flip: the u: row count GetStatus counted and ListUsers' length.
type headUsersAt struct {
	count, listed int
}

// seedHEADWithSystemUser is seedHEAD plus HEAD's system user, which every HEAD
// start seeded (seedRolesAndTokens), and records HEAD's answers over the rows.
func seedHEADWithSystemUser(t *testing.T, keys *headKeys, head *headUsersAt) func(*database.PebbleStore) {
	t.Helper()
	inner := seedHEAD(t, keys)
	return func(s *database.PebbleStore) {
		t.Helper()
		inner(s)
		require.NoError(t, auth.SeedSystemUser(s))
		n, err := s.CountUsers()
		require.NoError(t, err)
		users, err := s.ListUsers()
		require.NoError(t, err)
		require.True(t, slices.ContainsFunc(users, func(u database.User) bool { return u.Username == auth.SystemUserID }),
			"the HEAD store holds no system user: the cell would not compare like with like")
		head.count, head.listed = n, len(users)
	}
}

// legacyGet is one GET through the root handler, with raw as the bearer
// credential when it is not empty.
func legacyGet(r *runningApp, path, raw string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodGet, path, nil)
	req.RemoteAddr = "192.0.2.10:41000"
	if raw != "" {
		req.Header.Set("Authorization", "Bearer "+raw)
	}
	rec := httptest.NewRecorder()
	r.app.Handler().ServeHTTP(rec, req)
	return rec
}

// TestIdentityBoot_LegacyStatusAndUserListAreCores: after M3 has moved HEAD's
// accounts into core, GET /api/v1/auth/status answers what HEAD answered over
// the same users (has_users, sign-in required, no setup offered), and GET
// /api/v1/users, with a key holding users.manage as the rehearsal's bootstrap
// key did, lists every account core holds, _system included, so its count and
// length are HEAD's. Both read books' store at 69260476d and answered a fresh
// install and an empty list.
func TestIdentityBoot_LegacyStatusAndUserListAreCores(t *testing.T) {
	var keys headKeys
	var head headUsersAt
	fx := newFlipFixture(t, seedHEADWithSystemUser(t, &keys, &head), "")
	r := bootOverHEADStore(t, fx)

	rec := legacyGet(r, "/api/v1/auth/status", "")
	require.Equal(t, http.StatusOK, rec.Code, bodyOf(rec))
	var status struct {
		Data map[string]any `json:"data"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &status), bodyOf(rec))
	// HEAD's GetStatus over the same rows (enable_auth on): count > 0.
	require.Positive(t, head.count)
	assert.Equal(t, map[string]any{"auth_enabled": true, "bootstrap_ready": false, "has_users": true, "requires_auth": true}, status.Data,
		"the status is not HEAD's over the same users")

	admin := loginCoreAdmin(t, r, wayne, keys.password)
	raw, _ := admin.mintKey("oracle-users", []string{"core:users.manage", "core:roles.view"})
	rec = legacyGet(r, "/api/v1/users", raw)
	require.Equal(t, http.StatusOK, rec.Code, bodyOf(rec))
	var list struct {
		Data struct {
			Users []struct {
				ID, Username, Status string
			} `json:"users"`
			Count int `json:"count"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &list), bodyOf(rec))
	assert.Equal(t, head.listed, list.Data.Count, "count is not HEAD's over the same users")
	assert.Len(t, list.Data.Users, head.listed, "the list's length is not HEAD's over the same users")
	var names []string
	for _, u := range list.Data.Users {
		names = append(names, u.Username)
	}
	assert.ElementsMatch(t, []string{auth.SystemUserID, wayne, "lucius.fox", "harvey.dent"}, names)
	for _, u := range list.Data.Users {
		if u.Username == "harvey.dent" {
			assert.Equal(t, "inactive", u.Status, "HEAD's deactivated viewer")
		}
	}
}

// TestIdentityBoot_NoLegacySeedWarnings (rehearsal checks f408_c2_no_p4d_failure
// and f408_no_retired_claim_write): a boot over a HEAD store, the one M3 runs
// in, and a second boot of the store M3 left, each log neither "seed system
// user" nor "seed roles" at WARN (both seeds wrote into claims M3 retired and
// were refused on every start), no P4d failure, and no line at all naming a
// write refused "in a retired claim of" books, from any start-up writer: the
// seeds, the API-key expiry stamp, or anything the second boot runs before M3
// is skipped. (HEAD's startup read-only key writer is pinned by the server
// cell TestSeedRolesAndTokens_WritesNoRetiredRowUnderCoreIdentity: the flag
// that drives it is core's after M6, so no composed boot turns it on.) Each capture is taken once the server logs "start-up credentials
// settled", the line after the last of those writers, so a writer the capture
// missed cannot pass the cell by running late.
//
// The same boots answer the two count-gated public routes as core identity
// does on a migrated install: POST /api/v1/auth/setup is gone (410, core's
// POST /api/core/v1/auth/setup creates the first account), and a protected route refuses an
// anonymous caller (401) rather than taking HEAD's no-users bypass.
func TestIdentityBoot_NoLegacySeedWarnings(t *testing.T) {
	var keys headKeys
	var head headUsersAt
	fx := newFlipFixture(t, seedHEADWithSystemUser(t, &keys, &head), "")
	r := bootOverHEADStore(t, fx)
	assertCleanStart(t, "the boot M3 ran in", r)
	assertCountGatesClosed(t, r)
	require.NoError(t, r.stop())

	r = bootOverHEADStore(t, fx)
	assertCleanStart(t, "the second boot of the migrated store", r)
	assertCountGatesClosed(t, r)
}

// assertCleanStart waits for the server's last start-up writer and asserts the
// captured start output holds no legacy seed WARN, no P4d failure and no write
// refused in a retired claim.
func assertCleanStart(t *testing.T, boot string, r *runningApp) {
	t.Helper()
	var logged string
	require.Eventuallyf(t, func() bool {
		logged = r.newLog.String()
		return strings.Contains(logged, "start-up credentials settled")
	}, time.Minute, 50*time.Millisecond, "%s: the capture never held the server's start output: the cell would be blind", boot)
	for _, line := range strings.Split(logged, "\n") {
		warn := strings.Contains(line, "level=WARN") || strings.Contains(line, "level=ERROR") ||
			strings.Contains(line, `"level":"WARN"`) || strings.Contains(line, `"level":"ERROR"`)
		for _, msg := range []string{"seed system user", "seed roles"} {
			assert.Falsef(t, warn && strings.Contains(line, msg), "%s: a legacy seed was attempted under core identity: %s", boot, line)
		}
		assert.NotContainsf(t, line, "kernel: P4d identity restore steps failed", "%s", boot)
		assert.NotContainsf(t, line, "in a retired claim of", "%s: a start-up write into a retired claim: %s", boot, line)
	}
}

// assertCountGatesClosed: the public setup route is gone and an anonymous
// caller is refused on a protected route.
func assertCountGatesClosed(t *testing.T, r *runningApp) {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/setup",
		strings.NewReader(`{"username":"joker","password":"hahahahaha","email":"joker@example.test"}`))
	req.Header.Set("Content-Type", "application/json")
	req.RemoteAddr = "192.0.2.10:41000"
	rec := httptest.NewRecorder()
	r.app.Handler().ServeHTTP(rec, req)
	assert.Equal(t, http.StatusGone, rec.Code, "POST /api/v1/auth/setup: %s", bodyOf(rec))

	rec = legacyGet(r, "/api/v1/users", "")
	assert.Equal(t, http.StatusUnauthorized, rec.Code, "anonymous GET /api/v1/users: %s", bodyOf(rec))
}
