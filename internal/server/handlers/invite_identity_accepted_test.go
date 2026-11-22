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
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/books/store"
	coreauth "github.com/falkcorp/mediacore/auth"
	"github.com/falkcorp/mediacore/rbac"
)

// F4-08 B10x2: the accepted half of GET /users/invites under core identity is
// books' accept rows (database.InviteAccept) joined, by invite id, to the guest-policy
// outcome, over the same 30 days and order as HEAD's.

// acceptedWindow is HEAD's window for the accepted list (acceptedInviteWindow).
const acceptedWindow = 30 * 24 * time.Hour

// acceptedFixture is the identity fixture for the accepted list.
func acceptedFixture(t *testing.T, scopes ...rbac.Permission) *identityInviteFixture {
	t.Helper()
	f := newIdentityInviteFixture(t, scopes...)
	id := bridgeIdentity(t)
	id.Admin = f.acc
	f.useIdentity(id, "bruce", scopes...)
	return f
}

type acceptedOut struct {
	Username          string     `json:"username"`
	Email             string     `json:"email"`
	AcceptedAt        *time.Time `json:"accepted_at"`
	AcceptedUserID    string     `json:"accepted_user_id"`
	EmailBinding      string     `json:"email_binding"`
	SSOGrant          string     `json:"sso_grant"`
	GuestPolicyStatus string     `json:"guest_policy_status"`
	GuestPolicyDetail string     `json:"guest_policy_detail"`
	GuestPolicyAt     *time.Time `json:"guest_policy_at"`
	DisplacedUserIDs  []string   `json:"displaced_user_ids"`
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

func acceptRows(t *testing.T, f *inviteFixture) store.InviteAcceptStore {
	t.Helper()
	ia, ok := store.AsCapability[store.InviteAcceptStore](f.store)
	if !ok {
		t.Fatal("the store has no accept-row capability")
	}
	return ia
}

func recordAccept(t *testing.T, f *inviteFixture, a store.InviteAccept) {
	t.Helper()
	if err := acceptRows(t, f).RecordInviteAccept(a); err != nil {
		t.Fatal(err)
	}
}

// The list is the accept family's last 30 days on the ACCEPT time, newest first, each
// row naming its account from its own row (no read of core's accounts, so a deleted
// account is still listed under the name it accepted with); a row older than the
// window is not shown (HEAD's window).
func TestIdentityAcceptedInvites_ListsTheWindowNewestFirst(t *testing.T) {
	f := acceptedFixture(t, rbac.CoreAdmin)
	f.asUser(t, "bruce", "admin")
	now := f.clk.now()
	rec := func(inv, user, name string, at time.Time) {
		t.Helper()
		recordAccept(t, f.inviteFixture, store.InviteAccept{InviteID: inv, UserID: user, Username: name, AcceptedAt: at})
	}
	rec("inv-old", "u-old", "oswald.cobblepot", now.Add(-acceptedWindow-time.Hour))
	rec("inv-b", "u-barbara", "barbara.gordon", now.Add(-48*time.Hour))
	rec("inv-d", "u-dick", "dick.grayson", now.Add(-time.Hour))
	rec("inv-gone", "u-deleted", "harvey.dent", now.Add(-72*time.Hour))
	rec("inv-edge", "u-barbara", "barbara.gordon", now.Add(-acceptedWindow+time.Minute))

	rows, body := listAccepted(t, f.inviteFixture)
	var got []string
	for _, r := range rows {
		got = append(got, r.Username)
	}
	want := []string{"dick.grayson", "barbara.gordon", "harvey.dent", "barbara.gordon"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("accepted = %v, want %v (newest first, 30 days on the accept time): %s", got, want, body)
	}
	d := rows[0]
	if d.AcceptedUserID != "u-dick" || d.AcceptedAt == nil || !d.AcceptedAt.Equal(now.Add(-time.Hour)) {
		t.Fatalf("row = %+v", d)
	}
	if strings.Contains(body, `"token"`) {
		t.Fatalf("a token is in the body: %s", body)
	}
}

// accepted_at is the accept row's time and guest_policy_at the outcome's, and the
// window is judged on the accept time: an accept 29 days old whose sync reported
// yesterday is listed under its accept time, and one whose accept is outside the
// window is not listed though its sync reported inside it.
func TestIdentityAcceptedInvites_AcceptedAtIsTheAcceptNotTheSync(t *testing.T) {
	f := acceptedFixture(t, rbac.CoreAdmin)
	f.asUser(t, "bruce", "admin")
	now := f.clk.now()
	accepted, synced := now.Add(-29*24*time.Hour), now.Add(-24*time.Hour)
	recordAccept(t, f.inviteFixture, store.InviteAccept{InviteID: "inv-a", UserID: "u-a", Username: "alfred.pennyworth", AcceptedAt: accepted})
	if err := gpOutcomes(t, f.inviteFixture).RecordGuestPolicyOutcome("inv-a", "u-a", store.InviteGuestPolicyAdded, "", synced); err != nil {
		t.Fatal(err)
	}
	recordAccept(t, f.inviteFixture, store.InviteAccept{InviteID: "inv-o", UserID: "u-o", Username: "oswald.cobblepot", AcceptedAt: now.Add(-acceptedWindow - time.Hour)})
	if err := gpOutcomes(t, f.inviteFixture).RecordGuestPolicyOutcome("inv-o", "u-o", store.InviteGuestPolicyAdded, "", now); err != nil {
		t.Fatal(err)
	}
	rows, body := listAccepted(t, f.inviteFixture)
	if len(rows) != 1 || rows[0].Username != "alfred.pennyworth" {
		t.Fatalf("accepted = %s", body)
	}
	r := rows[0]
	if r.AcceptedAt == nil || !r.AcceptedAt.Equal(accepted) || r.GuestPolicyAt == nil || !r.GuestPolicyAt.Equal(synced) || r.GuestPolicyStatus != "added" {
		t.Fatalf("row = %+v: accepted_at must be %v and guest_policy_at %v", r, accepted, synced)
	}
}

// Every member HEAD's view carries is served when it is known, and the member set of
// a row is exact: a row that holds everything has all ten, one that holds only an
// identity has three (HEAD's omitempty).
func TestIdentityAcceptedInvites_EveryKnownMemberIsServedAndNoOther(t *testing.T) {
	f := acceptedFixture(t, rbac.CoreAdmin)
	f.asUser(t, "bruce", "admin")
	now := f.clk.now()
	gpAt := now.Add(-time.Hour)
	recordAccept(t, f.inviteFixture, store.InviteAccept{
		InviteID: "inv-full", UserID: "u-barbara", Username: "barbara.gordon", Email: "barbara.gordon@example.test",
		AcceptedAt: now.Add(-2 * time.Hour), EmailBinding: store.InviteEmailBound, SSOGrant: store.InviteSSOGranted,
		DisplacedUserIDs:  []string{"u-selina"},
		GuestPolicyStatus: store.InviteGuestPolicyFailed, GuestPolicyDetail: "HTTP 403", GuestPolicyAt: &gpAt,
	})
	recordAccept(t, f.inviteFixture, store.InviteAccept{InviteID: "inv-min", UserID: "u-dick", Username: "dick.grayson", AcceptedAt: now.Add(-3 * time.Hour)})

	w, _ := f.do(t, http.MethodGet, "/users/invites", nil)
	var body struct {
		Data struct {
			Accepted []map[string]any `json:"accepted"`
		} `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	keys := func(m map[string]any) []string {
		var ks []string
		for k := range m {
			ks = append(ks, k)
		}
		slices.Sort(ks)
		return ks
	}
	if len(body.Data.Accepted) != 2 {
		t.Fatalf("accepted = %v", body.Data.Accepted)
	}
	wantFull := []string{"accepted_at", "accepted_user_id", "displaced_user_ids", "email", "email_binding", "guest_policy_at", "guest_policy_detail",
		"guest_policy_status", "sso_grant", "username"}
	if got := keys(body.Data.Accepted[0]); !slices.Equal(got, wantFull) {
		t.Fatalf("full row members = %v, want %v", got, wantFull)
	}
	if got, want := keys(body.Data.Accepted[1]), []string{"accepted_at", "accepted_user_id", "username"}; !slices.Equal(got, want) {
		t.Fatalf("minimal row members = %v, want %v", got, want)
	}
	if f := body.Data.Accepted[0]; f["email"] != "barbara.gordon@example.test" || f["email_binding"] != "bound" || f["sso_grant"] != "granted" ||
		f["guest_policy_status"] != "failed" || f["guest_policy_detail"] != "HTTP 403" || fmt.Sprint(f["displaced_user_ids"]) != "[u-selina]" {
		t.Fatalf("full row = %v", f)
	}
}

// The outcome row, when there is one, is the sync's last word and wins over the guest
// policy members a carried row holds; with no outcome the row's own (HEAD's, for an
// accept M3 carried) are served.
func TestIdentityAcceptedInvites_OutcomeBeatsTheRowsOwnSyncMembers(t *testing.T) {
	f := acceptedFixture(t, rbac.CoreAdmin)
	f.asUser(t, "bruce", "admin")
	now := f.clk.now()
	carriedAt := now.Add(-5 * time.Hour)
	recordAccept(t, f.inviteFixture, store.InviteAccept{InviteID: "inv-1", UserID: "u-1", Username: "barbara.gordon", AcceptedAt: now.Add(-6 * time.Hour),
		GuestPolicyStatus: store.InviteGuestPolicyFailed, GuestPolicyDetail: "the carried failure", GuestPolicyAt: &carriedAt})
	recordAccept(t, f.inviteFixture, store.InviteAccept{InviteID: "inv-2", UserID: "u-2", Username: "dick.grayson", AcceptedAt: now.Add(-7 * time.Hour),
		GuestPolicyStatus: store.InviteGuestPolicyFailed, GuestPolicyDetail: "the carried failure", GuestPolicyAt: &carriedAt})
	if err := gpOutcomes(t, f.inviteFixture).RecordGuestPolicyOutcome("inv-1", "u-1", store.InviteGuestPolicyAdded, "retried", now.Add(-time.Hour)); err != nil {
		t.Fatal(err)
	}
	rows, body := listAccepted(t, f.inviteFixture)
	if len(rows) != 2 {
		t.Fatalf("accepted = %s", body)
	}
	if r := rows[0]; r.Username != "barbara.gordon" || r.GuestPolicyStatus != "added" || r.GuestPolicyDetail != "retried" || r.GuestPolicyAt == nil || !r.GuestPolicyAt.Equal(now.Add(-time.Hour)) {
		t.Fatalf("a row with an outcome = %+v", r)
	}
	if r := rows[1]; r.Username != "dick.grayson" || r.GuestPolicyStatus != "failed" || r.GuestPolicyDetail != "the carried failure" || r.GuestPolicyAt == nil || !r.GuestPolicyAt.Equal(carriedAt) {
		t.Fatalf("a carried row with no outcome = %+v", r)
	}
}

// An empty family answers [] and never fails the pending half of the body.
func TestIdentityAcceptedInvites_EmptyFamilyAnswersAnEmptyList(t *testing.T) {
	f := acceptedFixture(t, rbac.CoreAdmin)
	f.asUser(t, "bruce", "admin")
	if rows, body := listAccepted(t, f.inviteFixture); len(rows) != 0 || !strings.Contains(body, `"accepted":[]`) {
		t.Fatalf("empty family: %s", body)
	}
}

// The list reads and never writes: the families are byte-for-byte what they were.
func TestIdentityAcceptedInvites_ListNeverWrites(t *testing.T) {
	f := acceptedFixture(t, rbac.CoreAdmin)
	f.asUser(t, "bruce", "admin")
	at := f.clk.now().Add(-time.Hour)
	recordAccept(t, f.inviteFixture, store.InviteAccept{InviteID: "inv-1", UserID: "u-alfred", Username: "alfred.pennyworth", AcceptedAt: at})
	if err := gpOutcomes(t, f.inviteFixture).RecordGuestPolicyOutcome("inv-1", "u-alfred", store.InviteGuestPolicyPending, "", at); err != nil {
		t.Fatal(err)
	}
	snap := func() string {
		a, _ := acceptRows(t, f.inviteFixture).ListInviteAcceptsSince(time.Time{})
		o, _ := gpOutcomes(t, f.inviteFixture).GetGuestPolicyOutcome("inv-1")
		return fmt.Sprintf("%v|%v", a, o)
	}
	before := snap()
	for range 3 {
		listAccepted(t, f.inviteFixture)
	}
	if after := snap(); before != after || !strings.Contains(after, "alfred.pennyworth") {
		t.Fatalf("the list changed the families: %s -> %s", before, after)
	}
}

// Before the flip nothing changes: HEAD's own accepted rows (from its invite
// records) are listed, with HEAD's members, and the accept and outcome families are
// not read.
func TestAcceptedInvites_PreFlipListIsHEADsAndIgnoresTheAcceptFamily(t *testing.T) {
	f := newInviteFixture(t)
	_, data := f.do(t, http.MethodPost, "/users/invite", map[string]any{"username": "acc", "role_id": "viewer", "email": "acc@example.com"})
	if _, err := f.store.ConsumeInvite(fmt.Sprint(data["token"]), "bcrypt", "h"); err != nil {
		t.Fatal(err)
	}
	// Rows in the new families, which a pre-flip server never writes.
	recordAccept(t, f, store.InviteAccept{InviteID: "inv-core", UserID: "u-selina", Username: "selina.kyle", AcceptedAt: time.Now()})
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
	if strings.Contains(w.Body.String(), "u-selina") || strings.Contains(w.Body.String(), "selina.kyle") || strings.Contains(w.Body.String(), "inv-core") {
		t.Fatalf("the accept family leaked into the pre-flip list: %s", w.Body.String())
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
	f := newIdentityInviteFixture(t, rbac.CoreAdmin)
	id := bridgeIdentity(t)
	id.Admin = refusingAdmin{f.acc}
	f.useIdentity(id, "bruce", rbac.CoreAdmin)
	f.asUser(t, "bruce", "admin")
	recordAccept(t, f.inviteFixture, store.InviteAccept{InviteID: "inv-b", UserID: "u-barbara", Username: "barbara.gordon", AcceptedAt: f.clk.now()})
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
