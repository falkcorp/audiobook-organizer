// file: internal/server/handlers/status_users_identity_test.go
// version: 0.1.0
// guid: 3e9b6f12-8c47-4d05-a1e3-5f20c7d84b96
// last-edited: 2026-11-27

package handlers_test

import (
	"context"
	"encoding/json"
	"errors"
	"iter"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	coreauth "github.com/falkcorp/mediacore/auth"
	"github.com/falkcorp/mediacore/rbac"

	"github.com/falkcorp/audiobook-organizer/internal/server/handlers"
	handlersmocks "github.com/falkcorp/audiobook-organizer/internal/server/handlers/mocks"
	"github.com/falkcorp/audiobook-organizer/internal/server/legacyauth"
)

// Under core identity GET /api/v1/auth/status and GET /api/v1/users read core's
// accounts (after M3 books' store holds none). The 2026-11-24 prod-copy
// rehearsal (F4-08 at 69260476d) saw both read books' drained store: status
// answered a fresh install and the list was empty. Every cell here builds the
// handler over a mock store with NO expectations, so a handler that read books'
// store again fails the cell (the mock reports the unexpected call).

// coreUsers is core's read-only identity view over a fixed list, in order. err,
// when set, is yielded after the list.
type coreUsers struct {
	list []*coreauth.User
	err  error
}

func (f coreUsers) User(context.Context, coreauth.UserID) (*coreauth.User, error) {
	return nil, errors.New("not used")
}
func (f coreUsers) UserByUsername(context.Context, string) (*coreauth.User, error) {
	return nil, errors.New("not used")
}
func (f coreUsers) Users(context.Context, coreauth.UserFilter) iter.Seq2[*coreauth.User, error] {
	return func(yield func(*coreauth.User, error) bool) {
		for _, u := range f.list {
			if !yield(u, nil) {
				return
			}
		}
		if f.err != nil {
			yield(nil, f.err)
		}
	}
}
func (f coreUsers) PrincipalFrom(ctx context.Context) (*coreauth.Principal, bool) {
	return coreauth.PrincipalFrom(ctx)
}

var (
	gothamT0 = time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)
	gothamT1 = time.Date(2026, 4, 2, 9, 30, 0, 0, time.UTC)
)

// systemAccount is core's _system, as every core boot seeds it.
func systemAccount() *coreauth.User {
	return &coreauth.User{ID: coreauth.SystemUserID, Username: "_system", Email: "_system@local", Status: coreauth.StatusSystem,
		CreatedAt: gothamT0, UpdatedAt: gothamT0}
}

// migratedAccounts is what M3 leaves of a HEAD install: its system user under
// core's ID, an admin, an editor and a viewer HEAD had deactivated, one of them
// merged into the admin.
func migratedAccounts() []*coreauth.User {
	return []*coreauth.User{
		systemAccount(),
		{ID: "01JBRUCE00000000000000000A", Username: "bruce.wayne", Email: "bruce@example.test", Roles: []rbac.RoleName{"admin"},
			Status: coreauth.StatusActive, CreatedAt: gothamT0, UpdatedAt: gothamT1},
		{ID: "01JLUCIUS0000000000000000B", Username: "lucius.fox", Email: "lucius@example.test", Roles: []rbac.RoleName{"editor"},
			Status: coreauth.StatusActive, CreatedAt: gothamT0, UpdatedAt: gothamT0},
		{ID: "01JHARVEY0000000000000000C", Username: "harvey.dent", Email: "harvey@example.test", Roles: []rbac.RoleName{"viewer"},
			Status: coreauth.StatusDisabled, MergedInto: "01JBRUCE00000000000000000A", CreatedAt: gothamT0, UpdatedAt: gothamT1},
	}
}

// identityWithUsers is a bridged Identity whose core account view is users.
func identityWithUsers(t *testing.T, users coreauth.Identities) *legacyauth.Identity {
	t.Helper()
	id := bridgeIdentity(t, "users.manage")
	id.Users = users
	return id
}

func serveUnderIdentity(t *testing.T, id *legacyauth.Identity, path string, h gin.HandlerFunc) *httptest.ResponseRecorder {
	t.Helper()
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodGet, path, nil)
	if id != nil {
		c.Request = c.Request.WithContext(legacyauth.WithIdentity(c.Request.Context(), id))
	}
	h(c)
	return w
}

func statusBody(t *testing.T, w *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	var resp struct {
		Data map[string]any `json:"data"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp), w.Body.String())
	return resp.Data
}

// TestGetStatusUnderIdentity_MigratedStoreAnswersAsHEAD: on a migrated store
// (_system and three people in core, none in books' store) the status is the
// one HEAD answered over the same users before the flip, the rehearsal's
// baseline: auth on, users present, sign-in required, no setup offered.
func TestGetStatusUnderIdentity_MigratedStoreAnswersAsHEAD(t *testing.T) {
	h := handlers.NewAuthHandler(handlersmocks.NewMockAuthStore(t), true)
	got := statusBody(t, serveUnderIdentity(t, identityWithUsers(t, coreUsers{list: migratedAccounts()}), "/api/v1/auth/status", h.GetStatus))
	assert.Equal(t, map[string]any{"auth_enabled": true, "bootstrap_ready": false, "has_users": true, "requires_auth": true}, got)
}

// TestGetStatusUnderIdentity_SystemAccountIsNotAUser: _system is seeded by every
// core boot and is never a person, so a store holding only _system has no user
// and offers setup (core's has_users rule, 05-authn §4.2 item 1); one person
// beside it is a user. HEAD's CountUsers counted every u: row, its system user
// included; this is the rule the lead set for the flip (F4-08 fix-forward).
func TestGetStatusUnderIdentity_SystemAccountIsNotAUser(t *testing.T) {
	h := handlers.NewAuthHandler(handlersmocks.NewMockAuthStore(t), true)
	only := statusBody(t, serveUnderIdentity(t, identityWithUsers(t, coreUsers{list: []*coreauth.User{systemAccount()}}), "/api/v1/auth/status", h.GetStatus))
	assert.Equal(t, map[string]any{"auth_enabled": true, "bootstrap_ready": true, "has_users": false, "requires_auth": false}, only)

	none := statusBody(t, serveUnderIdentity(t, identityWithUsers(t, coreUsers{}), "/api/v1/auth/status", h.GetStatus))
	assert.Equal(t, only, none, "an empty account list and a _system-only one are the same install")

	person := &coreauth.User{ID: "01JALFRED0000000000000000D", Username: "alfred", Roles: []rbac.RoleName{"admin"}, Status: coreauth.StatusActive}
	one := statusBody(t, serveUnderIdentity(t, identityWithUsers(t, coreUsers{list: []*coreauth.User{systemAccount(), person}}), "/api/v1/auth/status", h.GetStatus))
	assert.Equal(t, true, one["has_users"])
	assert.Equal(t, false, one["bootstrap_ready"])
	assert.Equal(t, true, one["requires_auth"])
}

// TestGetStatusUnderIdentity_AuthOffAndRefusals: auth off keeps HEAD's
// formulas (no sign-in, no setup); an unreadable account list, or a server given
// no account view, answers 500 rather than a guess, and never reads books' store.
// A record core cannot decode counts as an account (the side that requires
// sign-in).
func TestGetStatusUnderIdentity_AuthOffAndRefusals(t *testing.T) {
	off := handlers.NewAuthHandler(handlersmocks.NewMockAuthStore(t), false)
	got := statusBody(t, serveUnderIdentity(t, identityWithUsers(t, coreUsers{list: migratedAccounts()}), "/api/v1/auth/status", off.GetStatus))
	assert.Equal(t, map[string]any{"auth_enabled": false, "bootstrap_ready": false, "has_users": true, "requires_auth": false}, got)

	on := handlers.NewAuthHandler(handlersmocks.NewMockAuthStore(t), true)
	w := serveUnderIdentity(t, identityWithUsers(t, coreUsers{list: []*coreauth.User{systemAccount()}, err: errors.New("the batcomputer is offline")}), "/api/v1/auth/status", on.GetStatus)
	assert.Equal(t, http.StatusInternalServerError, w.Code, w.Body.String())
	w = serveUnderIdentity(t, identityWithUsers(t, nil), "/api/v1/auth/status", on.GetStatus)
	assert.Equal(t, http.StatusInternalServerError, w.Code, w.Body.String())

	undecodable := statusBody(t, serveUnderIdentity(t, identityWithUsers(t, coreUsers{list: []*coreauth.User{systemAccount()}, err: coreauth.ErrUndecodableRecord}), "/api/v1/auth/status", on.GetStatus))
	assert.Equal(t, true, undecodable["has_users"])
	assert.Equal(t, true, undecodable["requires_auth"])
}

// TestListUsersUnderIdentity_ListsCoresAccountsInHEADsShape: the list is core's
// every account, _system included as HEAD's list included its system user, so
// count and length equal HEAD's on the same users (the rehearsal's baseline: 4
// with system_listed true for three people). Each row carries exactly HEAD's
// fields: _system's roles stay null, core's "disabled" is HEAD's "inactive",
// and a merged account names the account it went to.
func TestListUsersUnderIdentity_ListsCoresAccountsInHEADsShape(t *testing.T) {
	h := handlers.NewUserHandler(handlersmocks.NewMockUserStore(t))
	w := serveUnderIdentity(t, identityWithUsers(t, coreUsers{list: migratedAccounts()}), "/api/v1/users", h.ListUsers)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	var resp struct {
		Data struct {
			Users []map[string]any `json:"users"`
			Count int              `json:"count"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp), w.Body.String())
	require.Equal(t, 4, resp.Data.Count)
	require.Len(t, resp.Data.Users, 4)

	byName := map[string]map[string]any{}
	for _, u := range resp.Data.Users {
		assert.ElementsMatch(t, []string{"id", "username", "email", "roles", "status", "merged_into_user_id", "created_at", "updated_at"}, keysOf(u),
			"a row's fields are not HEAD's: %v", u)
		byName[u["username"].(string)] = u
	}
	sys, ok := byName["_system"]
	require.True(t, ok, "the list leaves out _system, which HEAD's list showed")
	assert.Equal(t, "system", sys["status"])
	assert.Equal(t, "_system", sys["id"])
	assert.Nil(t, sys["roles"], "_system's roles: HEAD showed null")

	bruce := byName["bruce.wayne"]
	assert.Equal(t, "01JBRUCE00000000000000000A", bruce["id"])
	assert.Equal(t, "bruce@example.test", bruce["email"])
	assert.Equal(t, []any{"admin"}, bruce["roles"])
	assert.Equal(t, "active", bruce["status"])
	assert.Equal(t, "", bruce["merged_into_user_id"])
	assert.Equal(t, gothamT0.Format(time.RFC3339Nano), bruce["created_at"])
	assert.Equal(t, gothamT1.Format(time.RFC3339Nano), bruce["updated_at"])

	harvey := byName["harvey.dent"]
	assert.Equal(t, "inactive", harvey["status"])
	assert.Equal(t, "01JBRUCE00000000000000000A", harvey["merged_into_user_id"])
}

// TestListUsersUnderIdentity_Refusals: a read error fails the whole list (a
// list that left an account out would read as that account gone), and a server
// given no account view answers 500; neither reads books' store.
func TestListUsersUnderIdentity_Refusals(t *testing.T) {
	h := handlers.NewUserHandler(handlersmocks.NewMockUserStore(t))
	w := serveUnderIdentity(t, identityWithUsers(t, coreUsers{list: migratedAccounts(), err: coreauth.ErrUndecodableRecord}), "/api/v1/users", h.ListUsers)
	assert.Equal(t, http.StatusInternalServerError, w.Code, w.Body.String())
	w = serveUnderIdentity(t, identityWithUsers(t, nil), "/api/v1/users", h.ListUsers)
	assert.Equal(t, http.StatusInternalServerError, w.Code, w.Body.String())
}

func keysOf(m map[string]any) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
