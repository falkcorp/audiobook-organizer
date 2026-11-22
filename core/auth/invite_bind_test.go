// file: core/auth/invite_bind_test.go
// version: 0.1.0
// guid: d580f458-60a4-48b4-9be4-551747009511
// last-edited: 2026-11-22

package auth_test

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/falkcorp/mediacore/audit"
	"github.com/falkcorp/mediacore/auth"
	"github.com/falkcorp/mediacore/rbac"
)

// The verified invite binding (F4-08 B10t, C1 and C2): an invite an admin
// mailed to its address (MarkInviteEmailDelivered) binds the address VERIFIED
// when accepted, if its creator still holds core:admin and no other account
// holds the address verified. Batman names, addresses on example.test.

// as is ctx acting as p, the way the middleware binds a request's principal.
func (f *fx) as(p *auth.Principal) context.Context {
	return auth.WithPrincipal(context.Background(), p)
}

// emailInvite creates an invite for address through admin and returns its
// token and view; the invite is NOT marked delivered.
func (f *fx) emailInvite(admin *auth.Principal, name, address string) (string, auth.InviteView) {
	f.t.Helper()
	return f.invite(admin, auth.InviteSpec{Username: name, Email: address, Role: "gotham.detective", TTL: time.Hour})
}

// events are the audit events recorded for action, in order.
func (f *fx) events(action string) []audit.Event {
	f.audit.mu.Lock()
	defer f.audit.mu.Unlock()
	var out []audit.Event
	for _, e := range f.audit.recorded {
		if e.Action == action {
			out = append(out, e)
		}
	}
	return out
}

// storedInvite is the stored invite with public ID id.
func (f *fx) storedInvite(id string) *auth.OneTimeToken {
	f.t.Helper()
	for t, err := range f.st.ScanOneTime(context.Background(), auth.OneTimeInvite) {
		if err != nil {
			f.t.Fatal(err)
		}
		if t.ID == id {
			return t
		}
	}
	return nil
}

// A flagged invite from an admin binds the address verified, takes it from the
// accounts that only typed it in, and says so in the hook payload and the
// audit record.
func TestInviteBind_FlaggedAcceptBindsVerifiedAndDisplaces(t *testing.T) {
	f := newFx(t)
	bruce := f.user("bruce", rbac.RoleAdmin)
	rec := f.lifecycle()
	admin := f.session(bruce)
	typed := f.userWithEmail("selina-kyle", "selina.kyle@example.test", false, "gotham.detective")
	tok, v := f.emailInvite(admin, "irena-dubrovna", "Selina.Kyle@Example.test")
	if err := f.svc.MarkInviteEmailDelivered(f.as(admin), v.ID); err != nil {
		t.Fatal(err)
	}
	u, _, err := f.accept(tok, "catwoman-1234")
	if err != nil {
		t.Fatalf("accept: %v", err)
	}
	got := f.reload(u.ID)
	if got.Email != "selina.kyle@example.test" || !got.EmailVerified {
		t.Fatalf("the invitee holds %q verified=%v, want the address verified", got.Email, got.EmailVerified)
	}
	if after := f.reload(typed.ID); after.Email != "" || after.EmailVerified {
		t.Fatalf("the account that typed the address in still holds %q", after.Email)
	}
	if len(rec.invites) != 1 {
		t.Fatalf("InviteAccepted ran %d times, want 1", len(rec.invites))
	}
	e := rec.invites[0]
	if !e.BindEmailOnAccept || !e.CreatorHeldAdmin || len(e.DisplacedUserIDs) != 1 || e.DisplacedUserIDs[0] != typed.ID {
		t.Fatalf("InviteAccepted = %+v, want flagged, creator admin, displacing %s", e, typed.ID)
	}
	accepted := f.events("core.invite.accept")
	if d := accepted[len(accepted)-1].Details; d["email_bound"] != true || d["email_source"] != "invite" {
		t.Fatalf("the accept's audit details = %v, want the binding named", d)
	}
}

// An accept that binds nothing touches no other account: no flag (an invite
// whose mail was never confirmed), or a creator who lost core:admin.
func TestInviteBind_UnboundAcceptDisplacesNobody(t *testing.T) {
	t.Run("no flag", func(t *testing.T) {
		f := newFx(t)
		bruce := f.user("bruce", rbac.RoleAdmin)
		rec := f.lifecycle()
		typed := f.userWithEmail("selina-kyle", "selina.kyle@example.test", false, "gotham.detective")
		tok, _ := f.emailInvite(f.session(bruce), "irena-dubrovna", "selina.kyle@example.test")
		u, _, err := f.accept(tok, "catwoman-1234")
		if err != nil {
			t.Fatal(err)
		}
		if got := f.reload(u.ID); got.Email != "selina.kyle@example.test" || got.EmailVerified {
			t.Fatalf("the invitee holds %q verified=%v, want it unverified", got.Email, got.EmailVerified)
		}
		if after := f.reload(typed.ID); after.Email != "selina.kyle@example.test" || after.Version != typed.Version {
			t.Fatalf("an unbound accept touched another account: %+v", after)
		}
		if e := rec.invites[0]; e.BindEmailOnAccept || len(e.DisplacedUserIDs) != 0 {
			t.Fatalf("InviteAccepted = %+v, want unflagged and nobody displaced", e)
		}
	})
	t.Run("creator demoted after the mail", func(t *testing.T) {
		f := newFx(t)
		bruce := f.user("bruce", rbac.RoleAdmin)
		rec := f.lifecycle()
		admin := f.session(bruce)
		typed := f.userWithEmail("selina-kyle", "selina.kyle@example.test", false, "gotham.detective")
		tok, v := f.emailInvite(admin, "irena-dubrovna", "selina.kyle@example.test")
		if err := f.svc.MarkInviteEmailDelivered(f.as(admin), v.ID); err != nil {
			t.Fatal(err)
		}
		// Still able to invite (users.manage, and the role it assigns), no longer an admin.
		f.setRoles(bruce.ID, roleCommissioner, "gotham.detective")
		u, _, err := f.accept(tok, "catwoman-1234")
		if err != nil {
			t.Fatalf("accept: %v", err)
		}
		if got := f.reload(u.ID); got.EmailVerified {
			t.Fatalf("the invitee holds %q verified after the creator was demoted", got.Email)
		}
		if after := f.reload(typed.ID); after.Email == "" {
			t.Fatal("a demoted creator's invite took an address from another account")
		}
		if e := rec.invites[0]; !e.BindEmailOnAccept || e.CreatorHeldAdmin || len(e.DisplacedUserIDs) != 0 {
			t.Fatalf("InviteAccepted = %+v, want flagged, creator not admin, nobody displaced", e)
		}
	})
}

// A verified holder that appears after the mail refuses the accept, as at
// HEAD: the invite stays unspent, nothing is displaced and nothing is bound.
func TestInviteBind_VerifiedHolderRefusesTheAcceptAndSpendsNothing(t *testing.T) {
	f := newFx(t)
	bruce := f.user("bruce", rbac.RoleAdmin)
	admin := f.session(bruce)
	typed := f.userWithEmail("selina-kyle", "selina.kyle@example.test", false, "gotham.detective")
	tok, v := f.emailInvite(admin, "irena-dubrovna", "selina.kyle@example.test")
	if err := f.svc.MarkInviteEmailDelivered(f.as(admin), v.ID); err != nil {
		t.Fatal(err)
	}
	holder := f.userWithEmail("harvey-bullock", "selina.kyle@example.test", true, "gotham.detective")
	_, _, err := f.accept(tok, "catwoman-1234")
	if !errors.Is(err, auth.ErrEmailInUse) || !errors.Is(err, auth.ErrInviteInvalid) {
		t.Fatalf("accept = %v, want ErrInviteInvalid wrapping ErrEmailInUse", err)
	}
	if f.storedInvite(v.ID) == nil {
		t.Fatal("the refused accept spent the invite")
	}
	if after := f.reload(typed.ID); after.Email == "" {
		t.Fatal("a refused bind still displaced an unverified holder")
	}
	if after := f.reload(holder.ID); after.Version != holder.Version {
		t.Fatal("a refused bind touched the verified holder")
	}
	if _, err := f.svc.UserByUsername(context.Background(), "irena-dubrovna"); !errors.Is(err, auth.ErrNotFound) {
		t.Fatalf("the refused accept created the account: %v", err)
	}
}

// MarkInviteEmailDelivered's rules: who may, which invites, idempotence.
func TestMarkInviteEmailDelivered_Rules(t *testing.T) {
	f := newFx(t)
	bruce := f.user("bruce", rbac.RoleAdmin)
	gordon := f.user("jim-gordon", roleCommissioner, "gotham.detective")
	admin := f.session(bruce)
	_, key := f.mint(admin, bruce.ID, "core:users.manage")
	mark := func(p *auth.Principal, id string) error { return f.svc.MarkInviteEmailDelivered(f.as(p), id) }

	_, v := f.emailInvite(admin, "irena-dubrovna", "irena@example.test")
	wantErr(t, "a users.manage holder who is not an admin", mark(f.session(gordon), v.ID), auth.ErrDenied)
	wantErr(t, "an admin's key", mark(f.keyPrincipal(bruce, key), v.ID), auth.ErrDenied)
	wantErr(t, "no principal on the ctx", f.svc.MarkInviteEmailDelivered(context.Background(), v.ID), auth.ErrDenied)
	if f.storedInvite(v.ID).BindEmailOnAccept {
		t.Fatal("a refused call flagged the invite")
	}

	if err := mark(admin, v.ID); err != nil {
		t.Fatal(err)
	}
	flagged := *f.storedInvite(v.ID)
	if !flagged.BindEmailOnAccept {
		t.Fatal("the invite is not flagged")
	}
	before := len(f.events("core.invite.mark_delivered"))
	if err := mark(admin, v.ID); err != nil {
		t.Fatalf("a second call = %v, want nil", err)
	}
	if again := *f.storedInvite(v.ID); !reflect.DeepEqual(again, flagged) {
		t.Fatalf("a second call rewrote the invite: %+v then %+v", flagged, again)
	}
	if n := len(f.events("core.invite.mark_delivered")); n != before || n != 1 {
		t.Fatalf("mark_delivered audit records = %d after the second call (was %d), want 1 and unchanged", n, before)
	}

	_, bare := f.invite(admin, auth.InviteSpec{Username: "duke-thomas", Role: "gotham.detective"})
	wantErr(t, "an invite with no address", mark(admin, bare.ID), auth.ErrBadInput)
	wantErr(t, "an unknown invite", mark(admin, "01NOSUCHINVITE"), auth.ErrNotFound)
	wantErr(t, "an empty id", mark(admin, ""), auth.ErrNotFound)

	tok, acc := f.emailInvite(admin, "tim-drake", "tim@example.test")
	if _, _, err := f.accept(tok, "red-robin-1234"); err != nil {
		t.Fatal(err)
	}
	wantErr(t, "an accepted invite", mark(admin, acc.ID), auth.ErrNotFound)

	_, gone := f.emailInvite(admin, "harper-row", "harper@example.test")
	if err := f.svc.DeleteInvite(f.as(admin), admin, gone.ID); err != nil {
		t.Fatal(err)
	}
	wantErr(t, "a deleted invite", mark(admin, gone.ID), auth.ErrNotFound)

	// Past its expiry, flagged or not.
	_, late := f.emailInvite(admin, "barbara-gordon", "barbara@example.test")
	f.advance(2 * time.Hour)
	wantErr(t, "an expired invite", mark(admin, late.ID), auth.ErrInviteInvalid)
	wantErr(t, "an expired invite that is already flagged", mark(admin, v.ID), auth.ErrInviteInvalid)
	if f.storedInvite(late.ID).BindEmailOnAccept {
		t.Fatal("an expired invite was flagged")
	}
}

// CreateInvite refuses an address another account holds verified (HEAD's rule
// at create, database.CreateInvite), and not one only typed in.
func TestCreateInvite_RefusesAVerifiedHolderOfTheAddress(t *testing.T) {
	f := newFx(t)
	bruce := f.user("bruce", rbac.RoleAdmin)
	admin := f.session(bruce)
	f.userWithEmail("selina-kyle", "selina.kyle@example.test", true, "gotham.detective")
	f.userWithEmail("harvey-bullock", "harvey@example.test", false, "gotham.detective")
	_, _, err := f.svc.CreateInvite(f.as(admin), admin, auth.InviteSpec{Username: "irena-dubrovna", Email: "Selina.Kyle@Example.test", Role: "gotham.detective"})
	wantErr(t, "a verified holder", err, auth.ErrEmailInUse)
	if _, _, err := f.svc.CreateInvite(f.as(admin), admin, auth.InviteSpec{Username: "renee-montoya", Email: "harvey@example.test", Role: "gotham.detective"}); err != nil {
		t.Fatalf("an address only typed in refused an invite: %v", err)
	}
}
