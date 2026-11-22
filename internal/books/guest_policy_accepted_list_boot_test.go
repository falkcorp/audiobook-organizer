// file: internal/books/guest_policy_accepted_list_boot_test.go
// version: 0.1.0
// guid: 3a91c5e8-7d24-4f60-b8a3-5e1d90c4f7b2
// last-edited: 2026-11-22

package books

import (
	"encoding/json"
	"net/http"
	"strconv"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	database "github.com/falkcorp/audiobook-organizer/internal/books/store"
	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/mediacore/app"
	"github.com/falkcorp/mediacore/module"
)

// The accepted-invites list under core identity, at full boot (F4-08 B10x).
//
// GET /api/v1/users/invites answers HEAD's body, whose "accepted" member is the
// last 30 days of accepted invites with their Cloudflare guest-policy outcome:
// how the owner sees whether a guest (a Lissen phone user) got in. Under core
// identity the invite is core's, so the list is read from books' own outcome
// family (guestpolicyout:), keyed by the invite core reported. A real flipped
// boot: core's invites, accounts and sessions, books' registered lifecycle, and a
// fake only of Cloudflare's API.

// acceptedListBody is the part of the list's body these cells read. Accepted
// stays raw so a cell can tell [] from null.
type acceptedListBody struct {
	Data struct {
		Invites  []json.RawMessage `json:"invites"`
		Accepted json.RawMessage   `json:"accepted"`
	} `json:"data"`
}

type acceptedRow struct {
	Username          string     `json:"username"`
	AcceptedAt        *time.Time `json:"accepted_at"`
	AcceptedUserID    string     `json:"accepted_user_id"`
	GuestPolicyStatus string     `json:"guest_policy_status"`
	GuestPolicyDetail string     `json:"guest_policy_detail"`
	GuestPolicyAt     *time.Time `json:"guest_policy_at"`
}

// acceptedList is the list as a caller sees it: the status, the raw accepted
// member and the body.
func acceptedList(t *testing.T, r *runningApp, bearer string, ck *http.Cookie, addr string) (int, json.RawMessage, string) {
	t.Helper()
	rec := invitesReq(t, r, http.MethodGet, "/api/v1/users/invites", "", bearer, ck, addr)
	var b acceptedListBody
	if rec.Code == http.StatusOK {
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &b), rec.Body.String())
	}
	return rec.Code, b.Data.Accepted, rec.Body.String()
}

func acceptedRows(t *testing.T, raw json.RawMessage) []acceptedRow {
	t.Helper()
	var rows []acceptedRow
	require.NoError(t, json.Unmarshal(raw, &rows), string(raw))
	return rows
}

func TestGuestPolicyAcceptedList_FullBoot(t *testing.T) {
	cf := newGPCF(gpJim)
	gpInstallCF(t, cf)
	t.Setenv("ABO_BOOKS_CFGUESTS_ACCOUNT_ID", gpCFAccount)
	t.Setenv("ABO_BOOKS_CFGUESTS_GUESTS_POLICY_ID", gpCFPolicy)
	t.Setenv("ABO_BOOKS_CFGUESTS_POLICY_API_TOKEN", gpCFToken)
	t.Cleanup(SetIdentityFlipForTest(true))
	tap := &gpTap{}
	r := runComposedWith(t, func(cfg *config.Config) { cfg.EnableAuth = true }, nil, nil, nil,
		func(m module.Module) module.Module { return gpTapModule{Module: m, tap: tap} })
	h := r.app.Handler()
	adminName, adminPass := redeemBootstrap(t, r.composed)
	ck := idLogin(t, h, adminName, adminPass)
	e := &gpEnv{runningApp: r, cf: cf, tap: tap, ids: map[string]string{}, admin: ck}
	e.ids[gpAdminName] = idUserID(t, h, ck, adminName)
	admin := &coreAdmin{t: t, r: r, cookie: ck}
	var addr int
	next := func() string { addr++; return "192.0.2." + strconv.Itoa(100+addr) + ":41000" }

	t.Run("01_an_empty_family_lists_an_empty_array_not_null", func(t *testing.T) {
		code, raw, body := acceptedList(t, r, "", ck, next())
		require.Equal(t, http.StatusOK, code, body)
		assert.JSONEq(t, `[]`, string(raw), "the accepted member of an empty list must be [], got: %s", body)
	})

	var barbaraID, dickID string
	t.Run("02_guests_who_accept_are_listed_for_the_admin_newest_first", func(t *testing.T) {
		var tok string
		barbaraID, tok = e.invite(t, ck, "barbara.gordon", gpBarbara)
		e.markDelivered(t, barbaraID)
		e.accept(t, tok)
		added := e.waitOutcome(t, barbaraID, database.InviteGuestPolicyAdded)
		// A second guest whose invite was never mailed: the sync skipped them.
		time.Sleep(10 * time.Millisecond)
		dickID, tok = e.invite(t, ck, "dick.grayson", gpDick)
		e.accept(t, tok)
		skipped := e.waitOutcome(t, dickID, database.InviteGuestPolicySkipped)

		code, raw, body := acceptedList(t, r, "", ck, next())
		require.Equal(t, http.StatusOK, code, body)
		rows := acceptedRows(t, raw)
		require.Len(t, rows, 2, body)
		assert.Equal(t, "dick.grayson", rows[0].Username, "newest first, as HEAD's list is: %s", body)
		assert.Equal(t, "barbara.gordon", rows[1].Username, body)
		assert.Equal(t, gpUser(t, e, "dick.grayson"), rows[0].AcceptedUserID)
		assert.Equal(t, gpUser(t, e, "barbara.gordon"), rows[1].AcceptedUserID)
		assert.Equal(t, database.InviteGuestPolicySkipped, rows[0].GuestPolicyStatus)
		assert.Equal(t, skipped.Detail, rows[0].GuestPolicyDetail)
		assert.Equal(t, database.InviteGuestPolicyAdded, rows[1].GuestPolicyStatus)
		assert.Equal(t, added.Detail, rows[1].GuestPolicyDetail)
		for _, row := range rows {
			require.NotNil(t, row.AcceptedAt, "accepted_at is what the Users page prints: %s", body)
			assert.WithinDuration(t, time.Now(), *row.AcceptedAt, time.Minute)
			require.NotNil(t, row.GuestPolicyAt, body)
		}
		// HEAD's shape: these members and no others, and never a token.
		var generic []map[string]any
		require.NoError(t, json.Unmarshal(raw, &generic))
		for _, m := range generic {
			for k := range m {
				assert.Contains(t, []string{"username", "accepted_at", "accepted_user_id", "guest_policy_status", "guest_policy_detail", "guest_policy_at"}, k,
					"a member HEAD's accepted view does not have: %s", body)
			}
		}
		assert.NotContains(t, body, `"token"`)
		// The pending half of the same body is still there.
		var b acceptedListBody
		require.NoError(t, json.Unmarshal([]byte(body), &b))
		assert.Empty(t, b.Data.Invites, "both invites were accepted, none is open: %s", body)
	})

	// The refusals are the pending list's (B10y): the accepted list is part of that
	// body, so a caller refused the list sees none of it. These cells hold rows in
	// the family, so a list served outside the gate would show a guest's name.
	t.Run("03_a_non_admin_session_is_refused_and_sees_no_guest", func(t *testing.T) {
		sess := idLogin(t, h, "dick.grayson", gpPass)
		code, _, body := acceptedList(t, r, "", sess, next())
		require.Equal(t, http.StatusForbidden, code, body)
		assert.NotContains(t, body, "barbara.gordon")
		assert.NotContains(t, body, "dick.grayson")
		assert.NotContains(t, body, "guest_policy")
	})

	t.Run("04_a_key_without_users_manage_is_refused_and_one_with_it_lists_as_at_HEAD", func(t *testing.T) {
		viewer, _ := admin.mintKey("batman-gotham-gazette", []string{"books:view"})
		code, _, body := acceptedList(t, r, viewer, nil, next())
		require.Equal(t, http.StatusForbidden, code, body)
		assert.NotContains(t, body, "barbara.gordon")
		assert.NotContains(t, body, "guest_policy")

		manager, _ := admin.mintKey("batman-oracle-link", []string{"core:users.manage", "core:roles.view"})
		code, raw, body := acceptedList(t, r, manager, nil, next())
		require.Equal(t, http.StatusOK, code, body)
		assert.Len(t, acceptedRows(t, raw), 2, "HEAD's users.manage key saw the accepted list: %s", body)
	})

	t.Run("05_the_list_is_read_only", func(t *testing.T) {
		before := e.outcome(t, barbaraID)
		for i := 0; i < 3; i++ {
			code, _, body := acceptedList(t, r, "", ck, next())
			require.Equal(t, http.StatusOK, code, body)
		}
		after := e.outcome(t, barbaraID)
		assert.Equal(t, before, after, "listing changed the outcome row")
	})
	require.NoError(t, e.stop())
}

// With authentication off the list is served to every request, as HEAD served
// it. Core's invites cannot be made with auth off (it refuses the auth-disabled
// principal), so the outcome rows are written the way the lifecycle writes them.
func TestGuestPolicyAcceptedList_AuthOffListsAsHEADDid(t *testing.T) {
	t.Cleanup(SetIdentityFlipForTest(true))
	r := runComposedWith(t, nil, nil, func(o *app.Options) { o.ConfigFlags["core.auth.mode"] = "disabled" }, nil)
	code, raw, body := acceptedList(t, r, "", nil, "127.0.0.31:41000")
	require.Equal(t, http.StatusOK, code, body)
	assert.JSONEq(t, `[]`, string(raw), body)

	gp, ok := database.AsCapability[database.GuestPolicyStore](r.store)
	require.True(t, ok)
	require.NoError(t, gp.RecordGuestPolicyOutcome("01BATMANGUESTINVITE000001", "u-alfred", database.InviteGuestPolicyFailed, "the Access policy refused the update", time.Now()))
	code, raw, body = acceptedList(t, r, "", nil, "127.0.0.32:41000")
	require.Equal(t, http.StatusOK, code, body)
	rows := acceptedRows(t, raw)
	require.Len(t, rows, 1, body)
	assert.Equal(t, "u-alfred", rows[0].AcceptedUserID)
	assert.Equal(t, database.InviteGuestPolicyFailed, rows[0].GuestPolicyStatus)
}
