// file: core/auth/accounts_admin_test.go
// version: 0.1.0
// guid: 60f73e2e-4fac-4412-ac7c-4b0d78ea421e
// last-edited: 2026-11-22

package auth_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/falkcorp/mediacore/auth"
	"github.com/falkcorp/mediacore/rbac"
	"github.com/falkcorp/mediacore/storage"
)

// The account half of the actor-checked admin handle (F4-08 B10t): SetUserEmail,
// the two user reads, the invite reads, RotateInvite and the ctx-principal rule
// of CreateInvite and DeleteInvite. Batman names, addresses on example.test.

// SetUserEmail binds an address verified (source "admin"), clears it with "",
// refuses an address another account holds verified, and fires EmailChanged
// after the commit.
func TestSetUserEmail_Rules(t *testing.T) {
	f := newFx(t)
	bruce := f.user("bruce", rbac.RoleAdmin)
	rec := f.lifecycle()
	admin := f.session(bruce)
	robin := f.userWithEmail("tim-drake", "tim.old@example.test", true, "gotham.detective")
	set := func(id auth.UserID, email string) error { return f.svc.SetUserEmail(f.as(admin), id, email) }

	if err := set(robin.ID, " Tim.Drake@Example.test "); err != nil {
		t.Fatal(err)
	}
	got := f.reload(robin.ID)
	if got.Email != "tim.drake@example.test" || !got.EmailVerified || got.Version != robin.Version+1 {
		t.Fatalf("stored %+v, want the address normalised, verified, version %d", got, robin.Version+1)
	}
	if got.CredentialEpoch != robin.CredentialEpoch {
		t.Fatalf("CredentialEpoch %d -> %d: HEAD's SetUserEmailAsAdmin bumps none", robin.CredentialEpoch, got.CredentialEpoch)
	}
	if n := len(f.events("core.user.set_email")); n != 1 {
		t.Fatalf("set_email audit records = %d, want 1", n)
	}
	if _, _, e := rec.counts(); e != 1 || rec.emails[0].From != "tim.old@example.test" || rec.emails[0].To != "tim.drake@example.test" {
		t.Fatalf("EmailChanged = %+v, want one change from the old to the new address", rec.emails)
	}

	// Setting what the user already holds verified writes nothing.
	if err := set(robin.ID, "tim.drake@example.test"); err != nil {
		t.Fatal(err)
	}
	if again := f.reload(robin.ID); again.Version != got.Version {
		t.Fatalf("an unchanged address rewrote the row: version %d -> %d", got.Version, again.Version)
	}
	if n := len(f.events("core.user.set_email")); n != 1 {
		t.Fatalf("an unchanged address was audited (%d records)", n)
	}

	// Another account holds the address verified.
	holder := f.userWithEmail("selina-kyle", "selina.kyle@example.test", true, "gotham.detective")
	wantErr(t, "an address another account holds verified", set(robin.ID, "Selina.Kyle@example.test"), auth.ErrEmailInUse)
	if f.reload(robin.ID).Email != "tim.drake@example.test" || f.reload(holder.ID).Version != holder.Version {
		t.Fatal("a refused set changed a row")
	}
	// ... but not one that only typed it in, which keeps its copy.
	typed := f.userWithEmail("harvey-bullock", "harvey@example.test", false, "gotham.detective")
	if err := set(robin.ID, "harvey@example.test"); err != nil {
		t.Fatal(err)
	}
	if after := f.reload(typed.ID); after.Email != "harvey@example.test" || after.Version != typed.Version {
		t.Fatalf("an admin set displaced an unverified holder: %+v", after)
	}

	// An unverified copy of the user's own is verified by setting it.
	own := f.userWithEmail("renee-montoya", "renee@example.test", false, "gotham.detective")
	if err := set(own.ID, "renee@example.test"); err != nil {
		t.Fatal(err)
	}
	if u := f.reload(own.ID); !u.EmailVerified {
		t.Fatal("setting the user's own unverified address did not verify it")
	}

	// "" clears.
	if err := set(robin.ID, ""); err != nil {
		t.Fatal(err)
	}
	if u := f.reload(robin.ID); u.Email != "" || u.EmailVerified {
		t.Fatalf("cleared address left %q verified=%v", u.Email, u.EmailVerified)
	}
	wantErr(t, "a malformed address", set(robin.ID, "not an address"), auth.ErrBadInput)
	wantErr(t, "an unknown user", set("u-nobody", "x@example.test"), auth.ErrNotFound)
}

// Who may call it: core:admin through an interactive method, and only over a
// user the caller dominates.
func TestSetUserEmail_Authority(t *testing.T) {
	f := newFx(t)
	bruce := f.user("bruce", rbac.RoleAdmin)
	gordon := f.user("jim-gordon", roleCommissioner)
	other := f.user("lucius-fox", rbac.RoleAdmin)
	target := f.user("tim-drake", "gotham.detective")
	_, key := f.mint(f.session(bruce), bruce.ID, "core:users.manage")
	call := func(ctx context.Context, id auth.UserID) error {
		return f.svc.SetUserEmail(ctx, id, "tim@example.test")
	}

	wantErr(t, "no principal", call(context.Background(), target.ID), auth.ErrDenied)
	wantErr(t, "users.manage without core:admin", call(f.as(f.session(gordon)), target.ID), auth.ErrDenied)
	wantErr(t, "an admin's API key", call(f.as(f.keyPrincipal(bruce, key)), target.ID), auth.ErrDenied)
	if f.reload(target.ID).Email != "" {
		t.Fatal("a refused call wrote the address")
	}
	// Dominance (rbac §4.7 rule 5): a role the actor does not hold every scope of.
	// A principal that holds core:admin and users.manage but lacks a scope the
	// target's role grants does not dominate the target.
	weak := f.session(bruce)
	weak.Perms = scopedChecker{rbac.CoreAdmin: true, "core:users.manage": true}
	f.setRoles(target.ID, "gotham.detective", rbac.RoleViewer)
	_ = other
	err := call(f.as(weak), target.ID)
	if err == nil {
		t.Fatal("a principal that does not cover the target's roles set its address")
	}
	if !errors.Is(err, rbac.ErrDominance) && !errors.Is(err, auth.ErrScopeEscalation) && !errors.Is(err, auth.ErrDenied) {
		t.Fatalf("dominance refusal = %v", err)
	}
	if f.reload(target.ID).Email != "" {
		t.Fatal("a refused dominance check wrote the address")
	}
	if err := call(f.as(f.session(bruce)), target.ID); err != nil {
		t.Fatalf("a dominating admin: %v", err)
	}
}

// scopedChecker is an rbac.Checker holding exactly the permissions it lists.
type scopedChecker map[rbac.Permission]bool

func (c scopedChecker) Has(p rbac.Permission) bool      { return c[p] }
func (c scopedChecker) Satisfies(rbac.Requirement) bool { return false }
func (c scopedChecker) Confined() bool                  { return false }
func (c scopedChecker) Concrete() []rbac.Permission {
	var out []rbac.Permission
	for p := range c {
		out = append(out, p)
	}
	return out
}

// AdminUser and AdminUsers need users.manage through an interactive method, the
// check runs inside the sequence, and a record carries no credential.
func TestAdminUsers_CheckAndNoCredential(t *testing.T) {
	f := newFx(t)
	bruce := f.user("bruce", rbac.RoleAdmin)
	viewer := f.user("alfred-pennyworth", rbac.RoleViewer)
	admin := f.session(bruce)
	_, key := f.mint(admin, bruce.ID, "core:users.manage")
	hash := "$2a$04$thisHashMustNeverBeReturnedByAnAdminRead0000000000000000"
	f.withHash(f.userWithEmail("selina-kyle", "selina.kyle@example.test", true, "gotham.detective"), hash)

	for name, ctx := range map[string]context.Context{
		"no principal": context.Background(),
		"a viewer":     f.as(f.session(viewer)),
		"an api key":   f.as(f.keyPrincipal(bruce, key)),
	} {
		if _, err := f.svc.AdminUser(ctx, bruce.ID); !errors.Is(err, auth.ErrDenied) {
			t.Fatalf("AdminUser as %s = %v, want ErrDenied", name, err)
		}
		n := 0
		for u, err := range f.svc.AdminUsers(ctx, auth.UserFilter{}) {
			n++
			if u != nil || !errors.Is(err, auth.ErrDenied) {
				t.Fatalf("AdminUsers as %s yielded (%v, %v), want only ErrDenied", name, u, err)
			}
		}
		if n != 1 {
			t.Fatalf("AdminUsers as %s yielded %d elements, want 1 (the refusal)", name, n)
		}
	}

	ctx := f.as(admin)
	if _, err := f.svc.AdminUser(ctx, "u-nobody"); !errors.Is(err, auth.ErrNotFound) {
		t.Fatalf("AdminUser of an absent user = %v, want ErrNotFound", err)
	}
	u, err := f.svc.AdminUser(ctx, "u-selina-kyle")
	if err != nil {
		t.Fatal(err)
	}
	if u.Email != "selina.kyle@example.test" || !u.EmailVerified || !u.HasPassword {
		t.Fatalf("AdminUser = %+v, want the address, verified, with the password flag", u)
	}
	u.Roles[0] = "tampered"
	if f.reload("u-selina-kyle").Roles[0] == "tampered" {
		t.Fatal("AdminUser shares its role slice with the store's copy")
	}
	var ids []auth.UserID
	for u, err := range f.svc.AdminUsers(ctx, auth.UserFilter{}) {
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, u.ID)
		if raw, _ := json.Marshal(u); strings.Contains(string(raw), "thisHashMustNever") {
			t.Fatalf("a listed record carries the password hash: %s", raw)
		}
	}
	slices.Sort(ids)
	if want := []auth.UserID{"u-alfred-pennyworth", "u-bruce", "u-selina-kyle"}; !slices.Equal(ids, want) {
		t.Fatalf("AdminUsers listed %v, want %v", ids, want)
	}
	raw, _ := json.Marshal(u)
	if strings.Contains(string(raw), "thisHashMustNever") {
		t.Fatalf("AdminUser carries the password hash: %s", raw)
	}

	// The record type holds no credential field: pin the whole field set, so a
	// credential added to User fails here and gets a decision.
	var fields []string
	for i := range reflect.TypeFor[auth.User]().NumField() {
		fields = append(fields, reflect.TypeFor[auth.User]().Field(i).Name)
	}
	want := []string{
		"ID", "Username", "Email", "EmailVerified", "HasPassword", "DisplayName", "Roles", "Status", "CredentialEpoch",
		"PasswordResetRequired", "SignInRestricted", "MergedInto", "MergedAt", "CreatedAt", "UpdatedAt", "Version",
	}
	if !slices.Equal(fields, want) {
		t.Fatalf("auth.User fields = %v\nwant %v: a new field must be judged for AdminUser(s) before it ships", fields, want)
	}
}

// Invite and AdminInvites carry the same check, and never a token.
func TestAdminInvites_CheckAndView(t *testing.T) {
	f := newFx(t)
	bruce := f.user("bruce", rbac.RoleAdmin)
	viewer := f.user("alfred-pennyworth", rbac.RoleViewer)
	admin := f.session(bruce)
	tok, v := f.emailInvite(admin, "irena-dubrovna", "irena@example.test")

	for name, ctx := range map[string]context.Context{"no principal": context.Background(), "a viewer": f.as(f.session(viewer))} {
		if _, err := f.svc.Invite(ctx, v.ID); !errors.Is(err, auth.ErrDenied) {
			t.Fatalf("Invite as %s = %v, want ErrDenied", name, err)
		}
		n := 0
		for _, err := range f.svc.AdminInvites(ctx) {
			n++
			if !errors.Is(err, auth.ErrDenied) {
				t.Fatalf("AdminInvites as %s yielded %v, want ErrDenied", name, err)
			}
		}
		if n != 1 {
			t.Fatalf("AdminInvites as %s yielded %d elements, want 1", name, n)
		}
	}
	got, err := f.svc.Invite(f.as(admin), v.ID)
	if err != nil || got.ID != v.ID || got.Email != "irena@example.test" || got.BindEmailOnAccept {
		t.Fatalf("Invite = %+v, %v", got, err)
	}
	if raw, _ := json.Marshal(got); strings.Contains(string(raw), tok) {
		t.Fatal("an invite view carries its token")
	}
	var listed []string
	for iv, err := range f.svc.AdminInvites(f.as(admin)) {
		if err != nil {
			t.Fatal(err)
		}
		listed = append(listed, iv.ID)
	}
	if !slices.Equal(listed, []string{v.ID}) {
		t.Fatalf("AdminInvites = %v, want [%s]", listed, v.ID)
	}
	if _, err := f.svc.Invite(f.as(admin), "01NOSUCH"); !errors.Is(err, auth.ErrNotFound) {
		t.Fatalf("Invite of an absent id = %v, want ErrNotFound", err)
	}
	f.advance(2 * time.Hour)
	if _, err := f.svc.Invite(f.as(admin), v.ID); !errors.Is(err, auth.ErrNotFound) {
		t.Fatalf("Invite of an expired invite = %v, want ErrNotFound", err)
	}
}

// CreateInvite and DeleteInvite decide on the principal bound on ctx: none, or
// another one than the actor passed, is refused, whatever the actor's own Perms.
func TestInvite_WritesNeedTheActorBoundOnTheContext(t *testing.T) {
	f := newFx(t)
	bruce := f.user("bruce", rbac.RoleAdmin)
	viewer := f.user("alfred-pennyworth", rbac.RoleViewer)
	admin := f.session(bruce)
	spec := auth.InviteSpec{Username: "irena-dubrovna", Role: "gotham.detective"}

	_, _, err := f.svc.CreateInvite(context.Background(), admin, spec)
	wantErr(t, "no principal on the ctx", err, auth.ErrDenied)
	_, _, err = f.svc.CreateInvite(f.as(f.session(viewer)), admin, spec)
	wantErr(t, "another principal on the ctx", err, auth.ErrDenied)
	// A weak principal on the ctx with an admin's actor value passed.
	_, _, err = f.svc.CreateInvite(f.as(f.session(viewer)), f.session(viewer), spec)
	wantErr(t, "a viewer's own principal", err, auth.ErrDenied)
	if n := f.invites(); n != 0 {
		t.Fatalf("%d invites stored after refused creates", n)
	}
	_, v := f.invite(admin, spec)
	wantErr(t, "delete with no principal", f.svc.DeleteInvite(context.Background(), admin, v.ID), auth.ErrDenied)
	wantErr(t, "delete as another principal", f.svc.DeleteInvite(f.as(f.session(viewer)), admin, v.ID), auth.ErrDenied)
	if f.storedInvite(v.ID) == nil {
		t.Fatal("a refused delete removed the invite")
	}
}

// invites counts the stored invites.
func (f *fx) invites() int {
	f.t.Helper()
	n := 0
	for _, err := range f.st.ScanOneTime(context.Background(), auth.OneTimeInvite) {
		if err != nil {
			f.t.Fatal(err)
		}
		n++
	}
	return n
}

// RotateInvite: the old link dies, the new one works, the expiry is exactly the
// old one, the new invite is born unflagged, and WasEmailBound reports the old
// flag.
func TestRotateInvite(t *testing.T) {
	f := newFx(t)
	bruce := f.user("bruce", rbac.RoleAdmin)
	gordon := f.user("jim-gordon", roleCommissioner)
	admin := f.session(bruce)
	rotate := func(p *auth.Principal, id string) (string, auth.InviteRotation, error) {
		return f.svc.RotateInvite(f.as(p), id)
	}

	oldTok, old := f.emailInvite(admin, "irena-dubrovna", "irena@example.test")
	if err := f.svc.MarkInviteEmailDelivered(f.as(admin), old.ID); err != nil {
		t.Fatal(err)
	}
	f.advance(10 * time.Minute) // a rotation does not restart the clock
	link, rot, err := rotate(admin, old.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !rot.WasEmailBound {
		t.Fatal("WasEmailBound is false for a flagged invite")
	}
	if rot.Invite.BindEmailOnAccept || f.storedInvite(rot.Invite.ID).BindEmailOnAccept {
		t.Fatal("the rotated invite was born flagged")
	}
	if !rot.Invite.ExpiresAt.Equal(old.ExpiresAt) || !f.storedInvite(rot.Invite.ID).ExpiresAt.Equal(old.ExpiresAt) {
		t.Fatalf("expiry %v, want exactly the old %v", rot.Invite.ExpiresAt, old.ExpiresAt)
	}
	if rot.Invite.ID == old.ID || rot.Invite.Username != old.Username || rot.Invite.Email != old.Email || rot.Invite.Role != old.Role {
		t.Fatalf("rotated %+v from %+v", rot.Invite, old)
	}
	if f.storedInvite(old.ID) != nil || f.invites() != 1 {
		t.Fatalf("old invite still stored, or %d invites exist", f.invites())
	}
	if _, _, err := f.accept(oldTok, "catwoman-1234"); !errors.Is(err, auth.ErrInviteInvalid) {
		t.Fatalf("accept of the old link = %v, want ErrInviteInvalid", err)
	}
	newTok, ok := strings.CutPrefix(link, "/invite#token=")
	if !ok || newTok == oldTok {
		t.Fatalf("link %q", link)
	}
	if got := len(f.events("core.invite.rotate")); got != 1 {
		t.Fatalf("rotate audit records = %d, want 1", got)
	}
	if u, _, err := f.accept(newTok, "catwoman-1234"); err != nil || u.EmailVerified {
		t.Fatalf("accept of the new link = %v, %v; want an unverified account (born unflagged)", u, err)
	}

	// An unflagged invite reports it, and stays unflagged.
	_, plain := f.emailInvite(admin, "duke-thomas", "duke@example.test")
	_, rot2, err := rotate(admin, plain.ID)
	if err != nil || rot2.WasEmailBound || rot2.Invite.BindEmailOnAccept {
		t.Fatalf("rotate of an unflagged invite = %+v, %v", rot2, err)
	}

	// Refusals.
	_, _, err = rotate(f.session(f.user("alfred-pennyworth", rbac.RoleViewer)), rot2.Invite.ID)
	wantErr(t, "a viewer", err, auth.ErrDenied)
	_, _, err = f.svc.RotateInvite(context.Background(), rot2.Invite.ID)
	wantErr(t, "no principal", err, auth.ErrDenied)
	_, _, err = rotate(admin, "01NOSUCH")
	wantErr(t, "an absent invite", err, auth.ErrNotFound)
	// A users.manage holder that cannot assign the invite's role.
	_, adminInv := f.invite(admin, auth.InviteSpec{Username: "lucius-fox", Role: rbac.RoleAdmin})
	_, _, err = rotate(f.session(gordon), adminInv.ID)
	wantErr(t, "a manager rotating an admin invite", err, auth.ErrScopeEscalation)
	if f.storedInvite(adminInv.ID) == nil {
		t.Fatal("a refused rotation removed the invite")
	}
	if _, _, err := rotate(f.session(gordon), rot2.Invite.ID); err != nil {
		t.Fatalf("a manager rotating a role it may assign: %v", err)
	}

	// An expired invite is refused, and nothing changes.
	_, late := f.emailInvite(admin, "harper-row", "harper@example.test")
	f.advance(3 * time.Hour)
	_, _, err = rotate(admin, late.ID)
	wantErr(t, "an expired invite", err, auth.ErrInviteInvalid)
}

// failIssue makes IssueOneTime fail, to prove RotateInvite's delete and issue
// are one batch.
type failIssue struct{ auth.Store }

func (failIssue) IssueOneTime(*storage.Batch, *auth.OneTimeToken) (auth.Token, error) {
	return "", errors.New("the Batcave's disks are on fire")
}

func TestRotateInvite_AFailedIssueLeavesTheOldInviteInPlace(t *testing.T) {
	f := newFx(t)
	bruce := f.user("bruce", rbac.RoleAdmin)
	admin := f.session(bruce)
	tok, old := f.emailInvite(admin, "irena-dubrovna", "irena@example.test")
	svc := f.over(failIssue{f.st})
	if _, _, err := svc.RotateInvite(f.as(admin), old.ID); err == nil {
		t.Fatal("a failed issue reported success")
	}
	if f.storedInvite(old.ID) == nil {
		t.Fatal("the delete committed although the create failed: the old invite is gone")
	}
	if _, _, err := f.accept(tok, "catwoman-1234"); err != nil {
		t.Fatalf("the old link no longer works after a failed rotation: %v", err)
	}
}

// A rotation holds the invite to CreateInvite's rules again.
func TestRotateInvite_RechecksTheAddressAndUsername(t *testing.T) {
	f := newFx(t)
	bruce := f.user("bruce", rbac.RoleAdmin)
	admin := f.session(bruce)
	_, v := f.emailInvite(admin, "irena-dubrovna", "irena@example.test")
	f.userWithEmail("selina-kyle", "irena@example.test", true, "gotham.detective")
	_, _, err := f.svc.RotateInvite(f.as(admin), v.ID)
	wantErr(t, "a verified holder appeared", err, auth.ErrEmailInUse)
	if f.storedInvite(v.ID) == nil {
		t.Fatal("a refused rotation removed the invite")
	}
}

// The ctx principal the account writes decide on is the one the REAL middleware
// binds: a request presenting a session reaches its handler with the principal
// on r.Context(), and passing that same principal to CreateInvite and
// DeleteInvite is accepted (middleware.go: bind -> WithPrincipal).
func TestInvite_WritesWorkThroughTheRealMiddleware(t *testing.T) {
	f := newFx(t)
	bruce := f.user("bruce", rbac.RoleAdmin)
	sess, err := f.svc.IssueSession(context.Background(), bruce, auth.OriginPassword, false, from("192.0.2.10"))
	if err != nil {
		t.Fatal(err)
	}
	var created string
	var gotErr error
	h := f.svc.Middleware()(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p, ok := auth.PrincipalFrom(r.Context())
		if !ok {
			gotErr = errors.New("the middleware bound no principal")
			return
		}
		_, v, err := f.svc.CreateInvite(r.Context(), p, auth.InviteSpec{Username: "irena-dubrovna", Role: "gotham.detective"})
		if err != nil {
			gotErr = err
			return
		}
		created = v.ID
		gotErr = f.svc.MarkInviteEmailDelivered(r.Context(), "01NOSUCHINVITE") // reaches the lookup: the admin check passed
		if errors.Is(gotErr, auth.ErrNotFound) {
			gotErr = f.svc.DeleteInvite(r.Context(), p, v.ID)
		}
	}))
	h.ServeHTTP(httptest.NewRecorder(), request("", sess.Token))
	if gotErr != nil || created == "" {
		t.Fatalf("a signed-in request: invite %q, err %v", created, gotErr)
	}
	if f.storedInvite(created) != nil {
		t.Fatal("the delete through the same request did not remove the invite")
	}
}

// Auth-off mode's principal has no user: every account method refuses it
// (the writes need a creator or an actor row, and the reads need users.manage
// through a user's own sign-in), and none panics on the nil user.
func TestAccountAdmin_RefusesTheAuthOffPrincipal(t *testing.T) {
	f := newFx(t)
	bruce := f.user("bruce", rbac.RoleAdmin)
	off := &auth.Principal{Method: auth.MethodAuthDisabled, Perms: scopedChecker{rbac.CoreAdmin: true, "core:users.manage": true}}
	ctx := auth.WithPrincipal(context.Background(), off)
	_, _, err := f.svc.CreateInvite(ctx, off, auth.InviteSpec{Username: "irena-dubrovna", Role: "gotham.detective"})
	wantErr(t, "CreateInvite", err, auth.ErrDenied)
	wantErr(t, "DeleteInvite", f.svc.DeleteInvite(ctx, off, "x"), auth.ErrDenied)
	wantErr(t, "MarkInviteEmailDelivered", f.svc.MarkInviteEmailDelivered(ctx, "x"), auth.ErrDenied)
	_, _, err = f.svc.RotateInvite(ctx, "x")
	wantErr(t, "RotateInvite", err, auth.ErrDenied)
	wantErr(t, "SetUserEmail", f.svc.SetUserEmail(ctx, bruce.ID, "bruce@example.test"), auth.ErrDenied)
	_, err = f.svc.AdminUser(ctx, bruce.ID)
	wantErr(t, "AdminUser", err, auth.ErrDenied)
	_, err = f.svc.Invite(ctx, "x")
	wantErr(t, "Invite", err, auth.ErrDenied)
}
