// file: core/auth/users_test.go
// version: 1.0.0
// guid: 8d00694b-a22c-438a-82d5-31749fe5b244
// last-edited: 2026-11-10

package auth_test

import (
	"context"
	"errors"
	"iter"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/falkcorp/mediacore/audit"
	"github.com/falkcorp/mediacore/auth"
	"github.com/falkcorp/mediacore/backup/journal"
	"github.com/falkcorp/mediacore/rbac"
	"github.com/falkcorp/mediacore/storage"
)

// User management (05-authn §3.2; 05-rbac §4.6, §4.7 rules 2 and 5).

// countSeq counts what seq yields, failing on any error.
func countSeq[T any](t *testing.T, seq iter.Seq2[T, error]) int {
	t.Helper()
	n := 0
	for _, err := range seq {
		if err != nil {
			t.Fatal(err)
		}
		n++
	}
	return n
}

func wantErr(t *testing.T, what string, err, want error) {
	t.Helper()
	if !errors.Is(err, want) {
		t.Fatalf("%s: err = %v, want %v", what, err, want)
	}
}

// signedIn opens a real session for u and returns its principal, carrying
// the session's public ID as a request presenting it would.
func (f *fx) signedIn(u *auth.User) *auth.Principal {
	f.t.Helper()
	sess, err := f.svc.IssueSession(context.Background(), u, auth.OriginPassword, false, from("192.0.2.10"))
	if err != nil {
		f.t.Fatal(err)
	}
	p := f.session(u)
	p.CredentialID = sess.PublicID
	return p
}

func (f *fx) sessionEpoch(publicID string) int64 {
	f.t.Helper()
	rec, err := f.st.SessionByPublicID(context.Background(), publicID)
	if err != nil {
		f.t.Fatalf("SessionByPublicID %s: %v", publicID, err)
	}
	return rec.CredentialEpoch
}

func TestCreateUser_NeedsManageThroughAnInteractiveMethod(t *testing.T) {
	f := newFx(t)
	ctx := context.Background()
	gordon := f.user("jim-gordon", roleCommissioner)
	viewer := f.user("vicki-vale", rbac.RoleViewer)

	_, err := f.svc.CreateUser(ctx, f.session(viewer), auth.NewUser{Username: "dick-grayson"})
	wantErr(t, "a viewer creating a user", err, auth.ErrDenied)
	_, key := f.mint(f.session(gordon), gordon.ID, "core:users.manage")
	_, err = f.svc.CreateUser(ctx, f.keyPrincipal(gordon, key), auth.NewUser{Username: "dick-grayson"})
	wantErr(t, "an API key creating a user", err, auth.ErrDenied)
	_, err = f.svc.CreateUser(ctx, nil, auth.NewUser{Username: "dick-grayson"})
	wantErr(t, "no actor", err, auth.ErrDenied)

	u, err := f.svc.CreateUser(ctx, f.session(gordon), auth.NewUser{
		Username: "  Dick-Grayson ", Email: " robin@example.com ", EmailVerified: true, DisplayName: " Robin ",
		Roles: []rbac.RoleName{"gotham.detective", "gotham.detective", " "},
	})
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	stored := f.reload(u.ID)
	if stored.Username != "Dick-Grayson" || stored.Email != "robin@example.com" || !stored.EmailVerified ||
		stored.DisplayName != "Robin" || stored.Status != auth.StatusActive || stored.HasPassword ||
		!slices.Equal(stored.Roles, []rbac.RoleName{"gotham.detective"}) || stored.Version != 1 || len(stored.ID) != 26 {
		t.Fatalf("stored user = %+v", stored)
	}
	if got := f.audit.recordedActions(); !slices.Contains(got, "core.user.create") {
		t.Fatalf("recorded %v, want core.user.create", got)
	}
}

func TestCreateUser_CannotHandOutARoleTheActorLacks(t *testing.T) {
	f := newFx(t)
	gordon := f.user("jim-gordon", roleCommissioner)
	_, err := f.svc.CreateUser(context.Background(), f.session(gordon), auth.NewUser{Username: "jason-todd", Roles: []rbac.RoleName{rbac.RoleAdmin}})
	wantErr(t, "a commissioner creating an admin", err, auth.ErrScopeEscalation)
	wantErr(t, "a commissioner creating an admin", err, rbac.ErrEscalation)
	if _, err := f.svc.UserByUsername(context.Background(), "jason-todd"); !errors.Is(err, auth.ErrNotFound) {
		t.Fatalf("a refused creation stored a user: %v", err)
	}
}

func TestCreateUser_RefusesBadAndTakenInput(t *testing.T) {
	f := newFx(t)
	ctx := context.Background()
	bruce := f.user("bruce-wayne", rbac.RoleAdmin)
	f.edit(f.user("selina-kyle"), func(u *auth.User) { u.Email, u.EmailVerified = "Cat@Example.com", true })
	f.edit(f.user("harvey-dent"), func(u *auth.User) { u.Email = "twoface@example.com" })
	actor := f.session(bruce)

	for _, c := range []struct {
		name string
		in   auth.NewUser
		want error
	}{
		{"blank username", auth.NewUser{Username: "   "}, auth.ErrBadInput},
		{"short password", auth.NewUser{Username: "tim-drake", Password: "robin3"}, auth.ErrBadInput},
		{"username in another case", auth.NewUser{Username: "BRUCE-WAYNE"}, auth.ErrUserExists},
		{"e-mail verified elsewhere", auth.NewUser{Username: "holly-robinson", Email: " cat@example.com"}, auth.ErrUserExists},
	} {
		_, err := f.svc.CreateUser(ctx, actor, c.in)
		wantErr(t, c.name, err, c.want)
	}
	// An address another account holds unverified does not block (HEAD).
	if _, err := f.svc.CreateUser(ctx, actor, auth.NewUser{Username: "gilda-dent", Email: "twoface@example.com"}); err != nil {
		t.Fatalf("an unverified holder blocked a creation: %v", err)
	}
}

func TestCreateUser_PasswordSignsIn(t *testing.T) {
	f, _ := loginFx(t)
	bruce := f.user("bruce-wayne", rbac.RoleAdmin)
	u, err := f.svc.CreateUser(context.Background(), f.session(bruce), auth.NewUser{Username: "barbara-gordon", Password: "oracle-online"})
	if err != nil {
		t.Fatal(err)
	}
	if !f.reload(u.ID).HasPassword {
		t.Fatal("the stored user has no password")
	}
	if _, err := f.login("barbara-gordon", "oracle-online", from("192.0.2.20")); err != nil {
		t.Fatalf("Login with the created password: %v", err)
	}
}

func TestCreateUser_SetsHasUsers(t *testing.T) {
	f := newFx(t)
	if f.svc.HasUsers() {
		t.Fatal("a fresh store reports users")
	}
	// An admin principal over a user the service has not seen.
	admin := &auth.User{ID: "u-bruce", Username: "bruce", Roles: []rbac.RoleName{rbac.RoleAdmin}, Status: auth.StatusActive}
	p := &auth.Principal{User: admin, Method: auth.MethodSession,
		Perms: f.rb.Compile(rbac.CheckerInput{Roles: admin.Roles, Interactive: true})}
	if _, err := f.svc.CreateUser(context.Background(), p, auth.NewUser{Username: "alfred"}); err != nil {
		t.Fatal(err)
	}
	if !f.svc.HasUsers() {
		t.Fatal("HasUsers is false after a user was created")
	}
}

func TestUsers_LookupsAndFilter(t *testing.T) {
	f := newFx(t)
	ctx := context.Background()
	bruce := f.user("Bruce-Wayne", rbac.RoleAdmin)
	f.user("lucius-fox", "gotham.detective")
	f.edit(f.user("jervis-tetch", "gotham.detective"), func(u *auth.User) { u.Status = auth.StatusDisabled })

	if u, err := f.svc.User(ctx, bruce.ID); err != nil || u.ID != bruce.ID {
		t.Fatalf("User = %v, %v", u, err)
	}
	if u, err := f.svc.UserByUsername(ctx, "  bruce-WAYNE "); err != nil || u.ID != bruce.ID {
		t.Fatalf("UserByUsername = %v, %v", u, err)
	}
	if _, err := f.svc.UserByUsername(ctx, "the-joker"); !errors.Is(err, auth.ErrNotFound) {
		t.Fatalf("UserByUsername of no one: %v", err)
	}
	names := func(flt auth.UserFilter) []string {
		var out []string
		for u, err := range f.svc.Users(ctx, flt) {
			if err != nil {
				t.Fatal(err)
			}
			out = append(out, u.Username)
		}
		slices.Sort(out)
		return out
	}
	if got := names(auth.UserFilter{}); len(got) != 3 {
		t.Fatalf("Users() = %v", got)
	}
	if got := names(auth.UserFilter{Role: "gotham.detective"}); !slices.Equal(got, []string{"jervis-tetch", "lucius-fox"}) {
		t.Fatalf("Users(role) = %v", got)
	}
	if got := names(auth.UserFilter{Role: "gotham.detective", Status: auth.StatusActive}); !slices.Equal(got, []string{"lucius-fox"}) {
		t.Fatalf("Users(role, active) = %v", got)
	}
	for range f.svc.Users(ctx, auth.UserFilter{}) {
		break // stopping early must not panic
	}
}

func TestChangeOwnPassword(t *testing.T) {
	f, _ := loginFx(t)
	ctx := context.Background()
	dick := f.withPassword(f.user("dick-grayson", "gotham.detective"), "flying-graysons")
	me := f.signedIn(dick)
	other := f.signedIn(dick)

	wantErr(t, "wrong current", f.svc.ChangeOwnPassword(ctx, me, "bludhaven", "nightwing-1984"), auth.ErrInvalidCredentials)
	wantErr(t, "empty current", f.svc.ChangeOwnPassword(ctx, me, "", "nightwing-1984"), auth.ErrBadInput)
	wantErr(t, "short next", f.svc.ChangeOwnPassword(ctx, me, "flying-graysons", "short"), auth.ErrBadInput)
	_, key := f.mint(f.session(dick), dick.ID, "gotham:patrol")
	wantErr(t, "an API key", f.svc.ChangeOwnPassword(ctx, f.keyPrincipal(dick, key), "flying-graysons", "nightwing-1984"), auth.ErrDenied)
	if got := f.reload(dick.ID).CredentialEpoch; got != dick.CredentialEpoch {
		t.Fatalf("a refused change moved the epoch to %d", got)
	}

	if err := f.svc.ChangeOwnPassword(ctx, me, "flying-graysons", "nightwing-1984"); err != nil {
		t.Fatalf("ChangeOwnPassword: %v", err)
	}
	after := f.reload(dick.ID)
	if after.CredentialEpoch != dick.CredentialEpoch+1 {
		t.Fatalf("epoch %d, want %d", after.CredentialEpoch, dick.CredentialEpoch+1)
	}
	if got := f.sessionEpoch(me.CredentialID); got != after.CredentialEpoch {
		t.Fatalf("the caller's own session is at epoch %d, want the new %d", got, after.CredentialEpoch)
	}
	if got := f.sessionEpoch(other.CredentialID); got != dick.CredentialEpoch {
		t.Fatalf("another session was re-stamped to %d", got)
	}
	if _, err := f.svc.PrincipalFor(ctx, after, auth.MethodSession, nil, dick.CredentialEpoch); !errors.Is(err, auth.ErrStaleEpoch) {
		t.Fatalf("a session at the old epoch still resolves: %v", err)
	}
	if f.jrnl.count(journal.PasswordChanged) != 1 || f.jrnl.count(journal.CredentialEpochBumped) != 1 {
		t.Fatalf("journal lines %+v", f.jrnl.lines)
	}
	for _, l := range f.jrnl.lines {
		if l.Epoch != after.CredentialEpoch || l.Subject.ID != string(dick.ID) {
			t.Fatalf("journal line %+v, want subject %s at epoch %d", l, dick.ID, after.CredentialEpoch)
		}
	}
	if _, err := f.login("dick-grayson", "flying-graysons", from("192.0.2.21")); !errors.Is(err, auth.ErrInvalidCredentials) {
		t.Fatalf("the old password still signs in: %v", err)
	}
	if _, err := f.login("dick-grayson", "nightwing-1984", from("192.0.2.21")); err != nil {
		t.Fatalf("the new password does not sign in: %v", err)
	}
}

func TestChangeOwnPassword_RefusesAChangedAccountAndAnAccountWithoutAPassword(t *testing.T) {
	f, _ := loginFx(t)
	ctx := context.Background()
	oidcOnly := f.user("kate-kane")
	wantErr(t, "no password", f.svc.ChangeOwnPassword(ctx, f.signedIn(oidcOnly), "anything-at-all", "batwoman-2006"), auth.ErrInvalidCredentials)

	// The epoch moves after the verification, while the write is staged: its
	// commit conflicts, and the retry finds the credentials changed and
	// refuses rather than applying the new password over the newer state.
	cass := f.withPassword(f.user("cassandra-cain"), "silent-one")
	me := f.signedIn(cass)
	var once sync.Once
	f.audit.hold = func(audit.Event) {
		once.Do(func() { f.edit(cass, func(u *auth.User) { u.CredentialEpoch++ }) })
	}
	err := f.svc.ChangeOwnPassword(ctx, me, "silent-one", "black-bat-2009")
	wantErr(t, "epoch moved mid-change", err, auth.ErrStaleEpoch)
	f.audit.hold = nil
	if _, err := f.login("cassandra-cain", "silent-one", from("192.0.2.22")); err != nil {
		t.Fatalf("the refused change replaced the password: %v", err)
	}
}

func TestSetStatus(t *testing.T) {
	f := newFx(t)
	ctx := context.Background()
	gordon := f.user("jim-gordon", roleCommissioner)
	bullock := f.user("harvey-bullock", "gotham.detective")
	actor := f.session(gordon)

	wantErr(t, "an unknown status", f.svc.SetStatus(ctx, actor, bullock.ID, "suspended"), auth.ErrBadInput)
	wantErr(t, "pending", f.svc.SetStatus(ctx, actor, bullock.ID, auth.StatusPending), auth.ErrBadInput)
	if err := f.svc.SetStatus(ctx, actor, bullock.ID, auth.StatusDisabled); err != nil {
		t.Fatalf("disable: %v", err)
	}
	off := f.reload(bullock.ID)
	if off.Status != auth.StatusDisabled || off.CredentialEpoch != bullock.CredentialEpoch+1 {
		t.Fatalf("after disable %+v", off)
	}
	if f.jrnl.count(journal.UserDisabled) != 1 || f.jrnl.count(journal.CredentialEpochBumped) != 1 {
		t.Fatalf("journal after disable %+v", f.jrnl.lines)
	}
	// The same status again writes nothing.
	if err := f.svc.SetStatus(ctx, actor, bullock.ID, auth.StatusDisabled); err != nil {
		t.Fatal(err)
	}
	if got := f.reload(bullock.ID); got.Version != off.Version {
		t.Fatalf("a no-op status change wrote version %d", got.Version)
	}
	if err := f.svc.SetStatus(ctx, actor, bullock.ID, auth.StatusActive); err != nil {
		t.Fatalf("enable: %v", err)
	}
	on := f.reload(bullock.ID)
	if on.Status != auth.StatusActive || on.CredentialEpoch != off.CredentialEpoch+1 {
		t.Fatalf("after enable %+v", on)
	}
	if f.jrnl.count(journal.UserDisabled) != 1 || f.jrnl.count(journal.CredentialEpochBumped) != 2 {
		t.Fatalf("journal after enable %+v", f.jrnl.lines)
	}
	if got := f.audit.recordedActions(); !slices.Contains(got, "core.user.disable") || !slices.Contains(got, "core.user.enable") {
		t.Fatalf("recorded %v", got)
	}

	pending := f.edit(f.user("stephanie-brown"), func(u *auth.User) { u.Status = auth.StatusPending })
	wantErr(t, "a pending invitee", f.svc.SetStatus(ctx, actor, pending.ID, auth.StatusActive), auth.ErrBadInput)
	system := f.edit(f.user("_system"), func(u *auth.User) { u.Status = auth.StatusSystem })
	wantErr(t, "the system user", f.svc.SetStatus(ctx, actor, system.ID, auth.StatusDisabled), auth.ErrBadInput)
	wantErr(t, "a missing user", f.svc.SetStatus(ctx, actor, "u-nobody", auth.StatusDisabled), auth.ErrNotFound)
}

func TestManagingAnotherUser_NeedsDominance(t *testing.T) {
	f := newFx(t)
	ctx := context.Background()
	gordon := f.user("jim-gordon", roleCommissioner)
	bruce := f.user("bruce-wayne", rbac.RoleAdmin)
	f.user("alfred-pennyworth", rbac.RoleAdmin)
	actor := f.session(gordon)

	wantErr(t, "disable an admin", f.svc.SetStatus(ctx, actor, bruce.ID, auth.StatusDisabled), auth.ErrDominance)
	wantErr(t, "delete an admin", f.svc.DeleteUser(ctx, actor, bruce.ID), auth.ErrDominance)
	wantErr(t, "demote an admin", f.svc.SetRoles(ctx, actor, bruce.ID, []rbac.RoleName{rbac.RoleViewer}), auth.ErrDominance)
	if got := f.reload(bruce.ID); got.Version != bruce.Version {
		t.Fatalf("a refused change wrote version %d", got.Version)
	}
	viewer := f.user("vicki-vale", rbac.RoleViewer)
	wantErr(t, "a viewer disabling anyone", f.svc.SetStatus(ctx, f.session(viewer), gordon.ID, auth.StatusDisabled), auth.ErrDenied)
}

func TestSetRoles(t *testing.T) {
	f := newFx(t)
	ctx := context.Background()
	gordon := f.user("jim-gordon", roleCommissioner)
	bruce := f.user("bruce-wayne", rbac.RoleAdmin)
	montoya := f.user("renee-montoya", "gotham.detective")

	wantErr(t, "handing out admin", f.svc.SetRoles(ctx, f.session(gordon), montoya.ID, []rbac.RoleName{rbac.RoleAdmin, "gotham.detective"}), auth.ErrScopeEscalation)
	actor := f.session(bruce)
	if err := f.svc.SetRoles(ctx, actor, montoya.ID, []rbac.RoleName{rbac.RoleViewer, "gotham.detective"}); err != nil {
		t.Fatal(err)
	}
	montoya = f.reload(montoya.ID)
	if err := f.svc.SetRoles(ctx, actor, montoya.ID, []rbac.RoleName{"gotham.detective", rbac.RoleViewer, rbac.RoleViewer}); err != nil {
		t.Fatal(err)
	}
	if got := f.reload(montoya.ID); got.Version != montoya.Version {
		t.Fatalf("the same role set (reordered) wrote version %d", got.Version)
	}
	if err := f.svc.SetRoles(ctx, actor, montoya.ID, []rbac.RoleName{"gotham.detective"}); err != nil {
		t.Fatalf("SetRoles: %v", err)
	}
	after := f.reload(montoya.ID)
	if !slices.Equal(after.Roles, []rbac.RoleName{"gotham.detective"}) || after.CredentialEpoch != montoya.CredentialEpoch {
		t.Fatalf("after SetRoles %+v (a role change bumps no epoch)", after)
	}
	if f.jrnl.count(journal.RoleUnassigned) != 1 {
		t.Fatalf("journal %+v", f.jrnl.lines)
	}
	l := f.jrnl.lines[len(f.jrnl.lines)-1]
	if l.Subject.ID != string(montoya.ID) || l.Object.Kind != "role" || l.Object.ID != string(rbac.RoleViewer) {
		t.Fatalf("role.unassigned line %+v", l)
	}
	if got := f.audit.recordedActions(); !slices.Contains(got, "core.user.set_roles") {
		t.Fatalf("recorded %v", got)
	}
}

// TestLastAdmin_CannotBeDisabledOrDemoted (05-authn §10): the last active
// core:admin holder keeps it through every user write, its own included.
func TestLastAdmin_CannotBeDisabledOrDemoted(t *testing.T) {
	f := newFx(t)
	ctx := context.Background()
	bruce := f.user("bruce-wayne", rbac.RoleAdmin)
	f.edit(f.user("alfred-pennyworth", rbac.RoleAdmin), func(u *auth.User) { u.Status = auth.StatusDisabled })
	f.user("lucius-fox", "custom.no-such-role")
	me := f.session(bruce)

	wantErr(t, "disable", f.svc.SetStatus(ctx, me, bruce.ID, auth.StatusDisabled), auth.ErrLastAdmin)
	wantErr(t, "demote", f.svc.SetRoles(ctx, me, bruce.ID, []rbac.RoleName{rbac.RoleViewer}), auth.ErrLastAdmin)
	wantErr(t, "delete", f.svc.DeleteUser(ctx, me, bruce.ID), auth.ErrLastAdmin)
	if got := f.reload(bruce.ID); got.Version != bruce.Version {
		t.Fatalf("a refused change wrote version %d", got.Version)
	}
	// Keeping core:admin through another role is not losing it.
	if err := f.svc.SetRoles(ctx, me, bruce.ID, []rbac.RoleName{rbac.RoleAdmin, rbac.RoleViewer}); err != nil {
		t.Fatalf("adding a role to the last admin: %v", err)
	}

	// With a second active holder, the first may step down.
	dick := f.user("dick-grayson", rbac.RoleAdmin)
	if err := f.svc.SetRoles(ctx, f.session(f.reload(bruce.ID)), bruce.ID, []rbac.RoleName{rbac.RoleViewer}); err != nil {
		t.Fatalf("demotion with another admin active: %v", err)
	}
	// Now dick is the last one.
	wantErr(t, "disable the new last admin", f.svc.SetStatus(ctx, f.session(dick), dick.ID, auth.StatusDisabled), auth.ErrLastAdmin)
}

// TestLastAdmin_ConcurrentDemotions (05-rbac §12): two admins demote each
// other at once; both decisions read the other as an active holder before
// either commits. One lands, the other retries and is refused.
func TestLastAdmin_ConcurrentDemotions(t *testing.T) {
	f := newFx(t)
	ctx := context.Background()
	bruce := f.user("bruce-wayne", rbac.RoleAdmin)
	dick := f.user("dick-grayson", rbac.RoleAdmin)
	pBruce, pDick := f.session(bruce), f.session(dick)

	// Both writes stage their batch (the last-admin read and its guard
	// included) and wait for each other before either commits; a retry
	// passes straight through.
	var mu sync.Mutex
	arrived := 0
	both := make(chan struct{})
	f.audit.hold = func(audit.Event) {
		mu.Lock()
		arrived++
		if arrived == 2 {
			close(both)
		}
		mu.Unlock()
		<-both
	}
	errs := make([]error, 2)
	var wg sync.WaitGroup
	wg.Go(func() { errs[0] = f.svc.SetRoles(ctx, pBruce, dick.ID, []rbac.RoleName{rbac.RoleViewer}) })
	wg.Go(func() { errs[1] = f.svc.SetRoles(ctx, pDick, bruce.ID, []rbac.RoleName{rbac.RoleViewer}) })
	wg.Wait()

	refused := 0
	for _, err := range errs {
		switch {
		case err == nil:
		case errors.Is(err, auth.ErrLastAdmin):
			refused++
		default:
			t.Fatalf("unexpected error %v", err)
		}
	}
	if refused != 1 {
		t.Fatalf("errors %v: want exactly one ErrLastAdmin", errs)
	}
	admins := 0
	for _, id := range []auth.UserID{bruce.ID, dick.ID} {
		if slices.Contains(f.reload(id).Roles, rbac.RoleAdmin) {
			admins++
		}
	}
	if admins != 1 {
		t.Fatalf("%d admins left, want 1", admins)
	}
}

func TestDeleteUser_TakesEveryCredentialWithIt(t *testing.T) {
	f := newFx(t)
	ctx := context.Background()
	bruce := f.user("bruce-wayne", rbac.RoleAdmin)
	jason := f.user("jason-todd", "gotham.detective")
	keep := f.user("tim-drake", "gotham.detective")
	f.signedIn(jason)
	keepSess := f.signedIn(keep)
	f.mint(f.session(jason), jason.ID, "gotham:patrol")
	_, keepKey := f.mint(f.session(keep), keep.ID, "gotham:patrol")
	now := f.clock()
	f.commit(func(b *storage.Batch) error {
		if err := f.st.PutIdentity(b, &auth.ExternalIdentity{Provider: "wayne-sso", Subject: "red-hood", UserID: jason.ID, CreatedAt: now}); err != nil {
			return err
		}
		if err := f.st.PutIdentity(b, &auth.ExternalIdentity{Provider: "wayne-sso", Subject: "red-robin", UserID: keep.ID, CreatedAt: now}); err != nil {
			return err
		}
		for _, tk := range []*auth.OneTimeToken{
			{Kind: auth.OneTimeTempLogin, UserID: jason.ID, CreatedBy: bruce.ID, CreatedAt: now, ExpiresAt: now.Add(time.Hour)},
			{Kind: auth.OneTimeInvite, CreatedBy: jason.ID, Role: "gotham.detective", CreatedAt: now, ExpiresAt: now.Add(time.Hour)},
			{Kind: auth.OneTimeInvite, CreatedBy: bruce.ID, Role: "gotham.detective", CreatedAt: now, ExpiresAt: now.Add(time.Hour)},
		} {
			if _, err := f.st.IssueOneTime(b, tk); err != nil {
				return err
			}
		}
		return nil
	})

	if err := f.svc.DeleteUser(ctx, f.session(bruce), jason.ID); err != nil {
		t.Fatalf("DeleteUser: %v", err)
	}
	if _, err := f.st.GetUser(ctx, jason.ID); !errors.Is(err, auth.ErrNotFound) {
		t.Fatalf("the user is still stored: %v", err)
	}
	if _, err := f.svc.UserByUsername(ctx, "jason-todd"); !errors.Is(err, auth.ErrNotFound) {
		t.Fatalf("the username index survived: %v", err)
	}
	held := func(id auth.UserID) [3]int {
		return [3]int{countSeq(t, f.st.ScanUserSessions(ctx, id)), countSeq(t, f.st.ScanAPIKeys(ctx, id)), countSeq(t, f.st.ScanIdentities(ctx, id))}
	}
	if got := held(jason.ID); got != [3]int{} {
		t.Fatalf("sessions, keys, identities of the deleted user left: %v", got)
	}
	if got := held(keep.ID); got != [3]int{1, 1, 1} {
		t.Fatalf("another user's sessions, keys, identities: %v, want one each", got)
	}
	f.sessionEpoch(keepSess.CredentialID)
	f.key(keepKey.ID)
	var invites, temps int
	for tk, err := range f.st.ScanOneTime(ctx, auth.OneTimeInvite) {
		if err != nil {
			t.Fatal(err)
		}
		if tk.CreatedBy == jason.ID {
			t.Fatal("an invite the deleted user created survived")
		}
		invites++
	}
	for tk, err := range f.st.ScanOneTime(ctx, auth.OneTimeTempLogin) {
		if err != nil {
			t.Fatal(err)
		}
		if tk.UserID == jason.ID {
			t.Fatal("a temp-login for the deleted user survived")
		}
		temps++
	}
	if invites != 1 || temps != 0 {
		t.Fatalf("invites %d (want bruce's 1), temp-logins %d", invites, temps)
	}
	if f.jrnl.count(journal.UserDeleted) != 1 {
		t.Fatalf("journal %+v", f.jrnl.lines)
	}
	if got := f.audit.recordedActions(); !slices.Contains(got, "core.user.delete") {
		t.Fatalf("recorded %v", got)
	}

	system := f.edit(f.user("_system"), func(u *auth.User) { u.Status = auth.StatusSystem })
	wantErr(t, "delete the system user", f.svc.DeleteUser(ctx, f.session(bruce), system.ID), auth.ErrBadInput)
}

func TestLogin_IsAudited(t *testing.T) {
	f, _ := loginFx(t)
	u := f.withPassword(f.user("bruce-wayne"), "i-am-the-night")
	sess, err := f.login("bruce-wayne", "i-am-the-night", from("192.0.2.30"))
	if err != nil {
		t.Fatal(err)
	}
	f.audit.mu.Lock()
	rec := f.audit.recorded[len(f.audit.recorded)-1]
	f.audit.mu.Unlock()
	if rec.Action != "core.auth.login" || rec.Outcome != audit.Success || rec.Actor.UserID != string(u.ID) ||
		rec.Actor.CredentialID != sess.PublicID || rec.Actor.ClientAddr != "192.0.2.30" {
		t.Fatalf("success record %+v", rec)
	}

	if _, err := f.login("bruce-wayne", "wrong-password", from("192.0.2.31")); !errors.Is(err, auth.ErrInvalidCredentials) {
		t.Fatal(err)
	}
	if _, err := f.login("the-joker", "wrong-password", from("192.0.2.31")); !errors.Is(err, auth.ErrInvalidCredentials) {
		t.Fatal(err)
	}
	f.audit.mu.Lock()
	emitted := slices.Clone(f.audit.emitted)
	f.audit.mu.Unlock()
	if len(emitted) != 2 {
		t.Fatalf("emitted %+v, want two refusals", emitted)
	}
	for i, e := range emitted {
		if e.Action != "core.auth.login" || e.Outcome != audit.Failure || e.Reason != "invalid_credentials" {
			t.Fatalf("refusal %d = %+v", i, e)
		}
	}
	if emitted[0].Target.ID != string(u.ID) || emitted[1].Target.ID != "" {
		t.Fatalf("targets %q, %q: the account only when it exists", emitted[0].Target.ID, emitted[1].Target.ID)
	}
}
