// file: core/auth/account_footprint_test.go
// version: 0.1.0
// guid: 0b6a7c52-3f1d-4e89-a4c7-9d2e5f81b630
// last-edited: 2026-11-22

package auth_test

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/falkcorp/mediacore/auth"
	"github.com/falkcorp/mediacore/rbac"
	"github.com/falkcorp/mediacore/storage"
)

// The masked account footprint (F4-08 B10fp): AdminAccountFootprint. Batman
// names, addresses on example.test.

// footprintFx is a fixture with an admin, a target account that owns two keys
// (one revoked), two linked sign-ins, two invites and sessions, and a bystander
// who owns a key, an invite and a session the target's counts must not include.
type footprintFx struct {
	*fx
	admin, target, bystander *auth.User
}

func newFootprintFx(t *testing.T) *footprintFx {
	t.Helper()
	f := newFx(t)
	bruce := f.user("bruce", rbac.RoleAdmin)
	selina := f.userWithEmail("selina-kyle", "selina.kyle@example.test", true, rbac.RoleAdmin)
	harley := f.user("harley-quinn", "gotham.detective")
	return &footprintFx{fx: f, admin: bruce, target: selina, bystander: harley}
}

func (f *footprintFx) read(ctx context.Context) (auth.AccountFootprint, error) {
	return f.svc.AdminAccountFootprint(ctx, f.target.ID)
}

// The rule: a user holding core:users.manage and core:admin, through any
// method. A viewer, a commissioner (users.manage without core:admin), an admin's
// key scoped to users.manage alone, a request with no principal and the
// auth-disabled principal are refused with ErrDenied; an admin's session and an
// admin's key that holds both are admitted, as HEAD's mergeActor admitted them.
func TestAccountFootprint_CheckIsUsersManageAndAdminThroughAnyMethod(t *testing.T) {
	f := newFootprintFx(t)
	gordon := f.user("jim-gordon", roleCommissioner)
	alfred := f.user("alfred-pennyworth", rbac.RoleViewer)
	adminSession := f.session(f.admin)
	_, manageOnly := f.mint(adminSession, f.admin.ID, "core:users.manage")
	_, both := f.mint(adminSession, f.admin.ID, "core:users.manage", rbac.CoreAdmin)
	noUser := &auth.Principal{Method: auth.MethodAuthDisabled, Perms: scopedChecker{rbac.CoreAdmin: true, "core:users.manage": true}}

	for name, ctx := range map[string]context.Context{
		"no principal":                 context.Background(),
		"a viewer":                     f.as(f.session(alfred)),
		"users.manage without admin":   f.as(f.session(gordon)),
		"a key scoped to users.manage": f.as(f.keyPrincipal(f.admin, manageOnly)),
		"the auth-disabled principal":  f.as(noUser),
	} {
		got, err := f.read(ctx)
		if !errors.Is(err, auth.ErrDenied) {
			t.Fatalf("AdminAccountFootprint as %s = (%+v, %v), want ErrDenied", name, got, err)
		}
		if got.APIKeys != 0 || len(got.Identities) != 0 {
			t.Fatalf("AdminAccountFootprint as %s returned data with its refusal: %+v", name, got)
		}
	}
	for name, ctx := range map[string]context.Context{
		"an admin's session":             f.as(adminSession),
		"an admin's key that holds both": f.as(f.keyPrincipal(f.admin, both)),
	} {
		if _, err := f.read(ctx); err != nil {
			t.Fatalf("AdminAccountFootprint as %s: %v", name, err)
		}
	}
	if _, err := f.svc.AdminAccountFootprint(f.as(adminSession), "u-nobody"); !errors.Is(err, auth.ErrNotFound) {
		t.Fatalf("AdminAccountFootprint of an absent account = %v, want ErrNotFound", err)
	}
}

// The sign-ins come back masked, ordered by provider then subject, and the
// result never carries a raw subject.
func TestAccountFootprint_IdentitiesAreMaskedAndOrdered(t *testing.T) {
	f := newFootprintFx(t)
	f.linkIdentity(f.target, "gotham-sso", "subject-of-selina-0042")
	f.linkIdentity(f.target, "arkham-sso", "gotham-guest-subject-99")
	f.linkIdentity(f.target, "arkham-sso", "abcd")
	f.linkIdentity(f.bystander, "gotham-sso", "subject-of-harley-0077")

	fp, err := f.read(f.as(f.session(f.admin)))
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, id := range fp.Identities {
		got = append(got, id.Provider+"|"+id.SubjectMask)
		if !id.LinkedAt.Equal(f.clock()) || id.Email != f.target.Email {
			t.Errorf("link %+v: linked_at or e-mail not carried", id)
		}
	}
	want := []string{"arkham-sso|••••", "arkham-sso|go…99", "gotham-sso|su…42"}
	if !slices.Equal(got, want) {
		t.Fatalf("identities = %v, want %v", got, want)
	}
	raw, _ := json.Marshal(fp)
	for _, secret := range []string{"subject-of-selina", "gotham-guest", "abcd", "subject-of-harley"} {
		if strings.Contains(string(raw), secret) {
			t.Fatalf("the footprint carries the subject %q: %s", secret, raw)
		}
	}
}

// Keys are counted for the account alone, every status included, and no key
// field is returned.
func TestAccountFootprint_CountsTheAccountsKeysOfEveryStatus(t *testing.T) {
	f := newFootprintFx(t)
	admin := f.session(f.admin)
	f.mint(admin, f.target.ID, rbac.CoreAdmin)
	_, revoked := f.mint(admin, f.target.ID, "core:users.manage")
	f.mint(admin, f.bystander.ID, "gotham:patrol")
	if err := f.svc.SetAPIKeyStatus(context.Background(), admin, revoked.ID, auth.KeyRevoked); err != nil {
		t.Fatal(err)
	}
	fp, err := f.read(f.as(admin))
	if err != nil {
		t.Fatal(err)
	}
	if fp.APIKeys != 2 {
		t.Fatalf("APIKeys = %d, want 2 (a revoked key counts, the bystander's key does not)", fp.APIKeys)
	}
	raw, _ := json.Marshal(fp)
	for _, leak := range []string{revoked.ID, revoked.HashPrefix, "mck_"} {
		if leak != "" && strings.Contains(string(raw), leak) {
			t.Fatalf("the footprint carries key material %q: %s", leak, raw)
		}
	}
}

// Only sessions that stand count: an expired one, one an epoch bump outdated
// and another account's are left out.
func TestAccountFootprint_ActiveSessionsExcludeExpiredAndOutdated(t *testing.T) {
	f := newFootprintFx(t)
	ctx := context.Background()
	issue := func(u *auth.User) {
		t.Helper()
		if _, err := f.svc.IssueSession(ctx, u, auth.OriginPassword, false, from("192.0.2.10")); err != nil {
			t.Fatal(err)
		}
	}
	adminCtx := f.as(f.session(f.admin))
	count := func(step string, want int) {
		t.Helper()
		fp, err := f.read(adminCtx)
		if err != nil {
			t.Fatal(err)
		}
		if fp.ActiveSessions != want {
			t.Fatalf("%s: ActiveSessions = %d, want %d", step, fp.ActiveSessions, want)
		}
	}

	issue(f.target)
	issue(f.target)
	count("two open sessions", 2)
	f.advance(auth.DefaultSessionTTL + time.Minute)
	issue(f.target)
	issue(f.bystander)
	count("two expired, one open (the bystander's is not counted)", 1)

	// An epoch bump no session was re-stamped for outdates the open one.
	f.bumpEpoch(f.target.ID, f.reload(f.target.ID).CredentialEpoch+1)
	count("the open session outdated by the epoch bump", 0)
	issue(f.reload(f.target.ID))
	count("a session opened at the new epoch", 1)
}

// Invites count by creator, the allow-list row by the account's address.
func TestAccountFootprint_InvitesAndGrants(t *testing.T) {
	f := newFootprintFx(t)
	admin := f.session(f.admin)
	f.invite(f.session(f.target), detectiveInvite("tim-drake"))
	f.invite(f.session(f.target), detectiveInvite("dick-grayson"))
	f.invite(admin, detectiveInvite("jason-todd"))
	adminCtx := f.as(admin)

	fp, err := f.read(adminCtx)
	if err != nil {
		t.Fatal(err)
	}
	if fp.Invites != 2 || fp.InviteGrants != 0 {
		t.Fatalf("Invites, InviteGrants = %d, %d; want 2, 0", fp.Invites, fp.InviteGrants)
	}
	f.commit(func(b *storage.Batch) error { return f.st.ImportInviteAllow(b, " Selina.Kyle@Example.test ") })
	fp, err = f.read(adminCtx)
	if err != nil {
		t.Fatal(err)
	}
	if fp.InviteGrants != 1 {
		t.Fatalf("InviteGrants = %d, want 1 for the account's address on the allow-list", fp.InviteGrants)
	}
	if fp.Unreadable == nil || len(fp.Unreadable) != 0 {
		t.Fatalf("Unreadable = %#v, want a non-nil empty list", fp.Unreadable)
	}
}

// The view holds no credential field: pin both types' field sets, so a field
// added to either is judged before it ships.
func TestAccountFootprint_FieldSetIsPinned(t *testing.T) {
	names := func(typ reflect.Type) []string {
		var out []string
		for field := range typ.Fields() {
			out = append(out, field.Name)
		}
		return out
	}
	if got, want := names(reflect.TypeFor[auth.AccountFootprint]()),
		[]string{"Identities", "APIKeys", "Invites", "InviteGrants", "ActiveSessions", "Unreadable"}; !slices.Equal(got, want) {
		t.Fatalf("AccountFootprint fields = %v, want %v: a new field must be judged for a credential before it ships", got, want)
	}
	if got, want := names(reflect.TypeFor[auth.IdentityLink]()),
		[]string{"Provider", "SubjectMask", "Email", "LinkedAt"}; !slices.Equal(got, want) {
		t.Fatalf("IdentityLink fields = %v, want %v: a new field must be judged for a credential before it ships", got, want)
	}
}

// MaskSubject is HEAD's mask: one bullet per byte up to four bytes, else the
// first two and last two around an ellipsis.
func TestMaskSubject(t *testing.T) {
	for in, want := range map[string]string{
		"": "", "a": "•", "abcd": "••••", "abcde": "ab…de", "gotham-guest-9": "go…-9",
	} {
		if got := auth.MaskSubject(in); got != want {
			t.Errorf("MaskSubject(%q) = %q, want %q", in, got, want)
		}
	}
}
