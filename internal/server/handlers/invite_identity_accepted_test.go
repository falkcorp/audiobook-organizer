// file: internal/server/handlers/invite_identity_accepted_test.go
// version: 0.1.0
// guid: b6d2e094-1c57-4a38-9f70-3e8a5d1c6b24
// last-edited: 2026-11-22

package handlers_test

import (
	"context"
	"encoding/json"
	"fmt"
	"iter"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/books/store"
	coreauth "github.com/falkcorp/mediacore/auth"
	"github.com/falkcorp/mediacore/rbac"
)

// F4-08 B10x: the accepted half of GET /users/invites under core identity is
// books' guest-policy outcome family, over the same 30 days and order as HEAD's.

// acceptedWindow is HEAD's window for the accepted list (acceptedInviteWindow).
const acceptedWindow = 30 * 24 * time.Hour

// usersView is core's identity view over a map: only User is read here.
type usersView struct {
	coreauth.Identities
	users map[coreauth.UserID]*coreauth.User
	err   error
}

func (u usersView) User(_ context.Context, id coreauth.UserID) (*coreauth.User, error) {
	if u.err != nil {
		return nil, u.err
	}
	if v, ok := u.users[id]; ok {
		return v, nil
	}
	return nil, coreauth.ErrNotFound
}

// acceptedFixture is the identity fixture with a view of core's accounts.
func acceptedFixture(t *testing.T, view coreauth.Identities, scopes ...rbac.Permission) *identityInviteFixture {
	t.Helper()
	f := newIdentityInviteFixture(t, scopes...)
	id := bridgeIdentity(t)
	id.Admin = f.acc
	id.Users = view
	f.useIdentity(id, "bruce", scopes...)
	return f
}

type acceptedOut struct {
	Username          string     `json:"username"`
	AcceptedAt        *time.Time `json:"accepted_at"`
	AcceptedUserID    string     `json:"accepted_user_id"`
	GuestPolicyStatus string     `json:"guest_policy_status"`
	GuestPolicyDetail string     `json:"guest_policy_detail"`
	GuestPolicyAt     *time.Time `json:"guest_policy_at"`
}

func listAccepted(t *testing.T, f *inviteFixture) ([]acceptedOut, string) {
	t.Helper()
	w, _ := f.do(t, http.MethodGet, "/users/invites", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("list: %d %s", w.Code, w.Body.String())
	}
	var body struct {
		Data struct {
			Accepted json.RawMessage `json:"accepted"`
		} `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if string(body.Data.Accepted) == "null" || len(body.Data.Accepted) == 0 {
		t.Fatalf("accepted must be an array, got %q: %s", body.Data.Accepted, w.Body.String())
	}
	var rows []acceptedOut
	if err := json.Unmarshal(body.Data.Accepted, &rows); err != nil {
		t.Fatal(err)
	}
	return rows, w.Body.String()
}

func gpOutcomes(t *testing.T, f *inviteFixture) store.GuestPolicyStore {
	t.Helper()
	gp, ok := store.AsCapability[store.GuestPolicyStore](f.store)
	if !ok {
		t.Fatal("the store has no guest-policy capability")
	}
	return gp
}

// The list is the family's last 30 days, newest first, each row naming the
// account from core's view; a row older than the window is not shown (HEAD's
// window), and an account core no longer holds is listed with no name.
func TestIdentityAcceptedInvites_ListsTheWindowNewestFirst(t *testing.T) {
	view := usersView{users: map[coreauth.UserID]*coreauth.User{
		"u-barbara": {ID: "u-barbara", Username: "barbara.gordon"},
		"u-dick":    {ID: "u-dick", Username: "dick.grayson"},
	}}
	f := acceptedFixture(t, view, rbac.CoreAdmin)
	f.asUser(t, "bruce", "admin")
	gp := gpOutcomes(t, f.inviteFixture)
	now := f.clk.now()
	rec := func(inv, user, status, detail string, at time.Time) {
		t.Helper()
		if err := gp.RecordGuestPolicyOutcome(inv, user, status, detail, at); err != nil {
			t.Fatal(err)
		}
	}
	rec("inv-old", "u-old", store.InviteGuestPolicyAdded, "", now.Add(-acceptedWindow-time.Hour))
	rec("inv-b", "u-barbara", store.InviteGuestPolicyAdded, "", now.Add(-48*time.Hour))
	rec("inv-d", "u-dick", store.InviteGuestPolicySkipped, "not emailed", now.Add(-time.Hour))
	rec("inv-gone", "u-deleted", store.InviteGuestPolicyFailed, "HTTP 403", now.Add(-72*time.Hour))
	rec("inv-edge", "u-barbara", store.InviteGuestPolicyAdded, "", now.Add(-acceptedWindow+time.Minute))

	rows, body := listAccepted(t, f.inviteFixture)
	var got []string
	for _, r := range rows {
		got = append(got, r.Username+"/"+r.GuestPolicyStatus)
	}
	want := []string{"dick.grayson/skipped", "barbara.gordon/added", "/failed", "barbara.gordon/added"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("accepted = %v, want %v (newest first, 30 days, a deleted account unnamed): %s", got, want, body)
	}
	d := rows[0]
	if d.AcceptedUserID != "u-dick" || d.GuestPolicyDetail != "not emailed" || d.AcceptedAt == nil || !d.AcceptedAt.Equal(now.Add(-time.Hour)) || d.GuestPolicyAt == nil {
		t.Fatalf("row = %+v", d)
	}
	for _, banned := range []string{`"token"`, `"email"`, "email_binding", "sso_grant", "displaced_user_ids"} {
		if strings.Contains(body, banned) {
			t.Fatalf("a member the family does not record (%s) is in the body: %s", banned, body)
		}
	}
}

// An empty family, a server with no view of core's accounts and a read of the
// view that fails all list rows or [] and never fail the pending half.
func TestIdentityAcceptedInvites_EmptyAndUnreadableViewsAnswerCleanly(t *testing.T) {
	f := acceptedFixture(t, usersView{}, rbac.CoreAdmin)
	f.asUser(t, "bruce", "admin")
	if rows, body := listAccepted(t, f.inviteFixture); len(rows) != 0 || !strings.Contains(body, `"accepted":[]`) {
		t.Fatalf("empty family: %s", body)
	}
	now := f.clk.now()
	if err := gpOutcomes(t, f.inviteFixture).RecordGuestPolicyOutcome("inv-1", "u-alfred", store.InviteGuestPolicyAdded, "", now); err != nil {
		t.Fatal(err)
	}
	for name, view := range map[string]coreauth.Identities{
		"no view of accounts":  nil,
		"a view that errors":   usersView{err: fmt.Errorf("core is unreachable")},
		"an account not found": usersView{},
	} {
		g := acceptedFixture(t, view, rbac.CoreAdmin)
		g.asUser(t, "bruce", "admin")
		if err := gpOutcomes(t, g.inviteFixture).RecordGuestPolicyOutcome("inv-1", "u-alfred", store.InviteGuestPolicyAdded, "", now); err != nil {
			t.Fatal(err)
		}
		rows, body := listAccepted(t, g.inviteFixture)
		if len(rows) != 1 || rows[0].Username != "" || rows[0].AcceptedUserID != "u-alfred" || rows[0].GuestPolicyStatus != "added" {
			t.Fatalf("%s: %s", name, body)
		}
	}
}

// The list reads and never writes: the family is byte-for-byte what it was.
func TestIdentityAcceptedInvites_ListNeverWrites(t *testing.T) {
	f := acceptedFixture(t, usersView{}, rbac.CoreAdmin)
	f.asUser(t, "bruce", "admin")
	gp := gpOutcomes(t, f.inviteFixture)
	at := f.clk.now().Add(-time.Hour)
	if err := gp.RecordGuestPolicyOutcome("inv-1", "u-alfred", store.InviteGuestPolicyPending, "", at); err != nil {
		t.Fatal(err)
	}
	before, _ := gp.ListGuestPolicyOutcomesSince(time.Time{})
	for range 3 {
		listAccepted(t, f.inviteFixture)
	}
	after, _ := gp.ListGuestPolicyOutcomesSince(time.Time{})
	if fmt.Sprint(before) != fmt.Sprint(after) || len(after) != 1 {
		t.Fatalf("the list changed the family: %v -> %v", before, after)
	}
}

// Before the flip nothing changes: HEAD's own accepted rows (from its invite
// records) are listed, with HEAD's members, and the outcome family is not read.
func TestAcceptedInvites_PreFlipListIsHEADsAndIgnoresTheOutcomeFamily(t *testing.T) {
	f := newInviteFixture(t)
	_, data := f.do(t, http.MethodPost, "/users/invite", map[string]any{"username": "acc", "role_id": "viewer", "email": "acc@example.com"})
	if _, err := f.store.ConsumeInvite(fmt.Sprint(data["token"]), "bcrypt", "h"); err != nil {
		t.Fatal(err)
	}
	// A row in the new family, which a pre-flip server never writes.
	if err := gpOutcomes(t, f).RecordGuestPolicyOutcome("inv-core", "u-selina", store.InviteGuestPolicyAdded, "", time.Now()); err != nil {
		t.Fatal(err)
	}
	w, list := f.do(t, http.MethodGet, "/users/invites", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("status %d", w.Code)
	}
	acc, _ := list["accepted"].([]any)
	if len(acc) != 1 {
		t.Fatalf("accepted = %v, want HEAD's one row", list["accepted"])
	}
	row := acc[0].(map[string]any)
	if row["username"] != "acc" || row["email"] != "acc@example.com" || row["email_binding"] != "not_emailed" {
		t.Fatalf("row = %v", row)
	}
	if strings.Contains(w.Body.String(), "u-selina") || strings.Contains(w.Body.String(), "inv-core") {
		t.Fatalf("the outcome family leaked into the pre-flip list: %s", w.Body.String())
	}
}

// refusingAdmin is core's admin handle for a caller whose principal may not list
// invites: AdminInvites yields the refusal as its only element.
type refusingAdmin struct{ *fakeAccounts }

func (refusingAdmin) AdminInvites(context.Context) iter.Seq2[coreauth.InviteView, error] {
	return func(yield func(coreauth.InviteView, error) bool) {
		yield(coreauth.InviteView{}, fmt.Errorf("%w: core:users.manage is needed to list invites", coreauth.ErrDenied))
	}
}

// The accepted rows come after core's gate, never around it: a caller core
// refuses is answered 403 and the body carries no guest, though the family holds
// one. The route's own users.manage check is the first layer; this is the second.
func TestIdentityAcceptedInvites_ACallerCoreRefusesSeesNoGuest(t *testing.T) {
	view := usersView{users: map[coreauth.UserID]*coreauth.User{"u-barbara": {ID: "u-barbara", Username: "barbara.gordon"}}}
	f := newIdentityInviteFixture(t, rbac.CoreAdmin)
	id := bridgeIdentity(t)
	id.Admin = refusingAdmin{f.acc}
	id.Users = view
	f.useIdentity(id, "bruce", rbac.CoreAdmin)
	f.asUser(t, "bruce", "admin")
	if err := gpOutcomes(t, f.inviteFixture).RecordGuestPolicyOutcome("inv-b", "u-barbara", store.InviteGuestPolicyAdded, "", f.clk.now()); err != nil {
		t.Fatal(err)
	}
	w, _ := f.do(t, http.MethodGet, "/users/invites", nil)
	if w.Code != http.StatusForbidden {
		t.Fatalf("a refused caller got %d: %s", w.Code, w.Body.String())
	}
	if b := w.Body.String(); strings.Contains(b, "barbara.gordon") || strings.Contains(b, "u-barbara") || strings.Contains(b, "guest_policy") {
		t.Fatalf("a refused caller was shown a guest: %s", b)
	}
}
