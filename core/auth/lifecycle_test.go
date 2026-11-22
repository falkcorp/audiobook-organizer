// file: core/auth/lifecycle_test.go
// version: 0.1.0
// guid: 8a6c1e47-2d03-4b95-9f7e-c15d0a38e264
// last-edited: 2026-11-22

package auth_test

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/falkcorp/mediacore/audit"
	"github.com/falkcorp/mediacore/auth"
	"github.com/falkcorp/mediacore/rbac"
	"github.com/falkcorp/mediacore/storage"
)

// Lifecycle hook (F4-08 B10r). Batman names only; addresses on example.test.

// lcRec records the calls of one registered Lifecycle. Each callback also
// reads the store from inside the call: a callback that runs before the
// write committed (or inside its batch) sees the old record and fails here.
type lcRec struct {
	f       *fx
	mu      sync.Mutex
	invites []auth.InviteAccepted
	status  []auth.StatusChanged
	emails  []auth.EmailChanged
}

// lifecycle registers the recording Lifecycle. The fixture rebuilds the
// service when the first user is stored, which would drop a registration made
// before it, so one bystander is stored first when the store is empty.
func (f *fx) lifecycle() *lcRec {
	f.t.Helper()
	if !f.svc.HasUsers() {
		f.user("alfred-pennyworth", rbac.RoleViewer)
	}
	r := &lcRec{f: f}
	if err := f.svc.RegisterLifecycle(auth.Lifecycle{
		InviteAccepted: func(ctx context.Context, e auth.InviteAccepted) {
			r.mu.Lock()
			r.invites = append(r.invites, e)
			r.mu.Unlock()
			if _, err := f.st.GetUser(ctx, e.UserID); err != nil {
				f.t.Errorf("InviteAccepted ran before the accept committed: user %s: %v", e.UserID, err)
			}
		},
		StatusChanged: func(ctx context.Context, e auth.StatusChanged) {
			r.mu.Lock()
			r.status = append(r.status, e)
			r.mu.Unlock()
			if u, err := f.st.GetUser(ctx, e.UserID); err != nil || u.Status != e.To {
				f.t.Errorf("StatusChanged ran before the write committed: stored %+v, %v; event %+v", u, err, e)
			}
		},
		EmailChanged: func(ctx context.Context, e auth.EmailChanged) {
			r.mu.Lock()
			r.emails = append(r.emails, e)
			r.mu.Unlock()
			if u, err := f.st.GetUser(ctx, e.UserID); err != nil || u.Email != e.To {
				f.t.Errorf("EmailChanged ran before the write committed: stored %+v, %v; event %+v", u, err, e)
			}
		},
	}); err != nil {
		f.t.Fatalf("RegisterLifecycle: %v", err)
	}
	return r
}

func (r *lcRec) counts() (invites, status, emails int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.invites), len(r.status), len(r.emails)
}

// wantNone fails unless no callback ran.
func (r *lcRec) wantNone(t *testing.T, what string) {
	t.Helper()
	if i, s, e := r.counts(); i+s+e != 0 {
		t.Fatalf("%s: callbacks ran (invite=%d status=%d email=%d), want none", what, i, s, e)
	}
}

// userWithEmail stores an active user holding email, verified or not.
func (f *fx) userWithEmail(name, email string, verified bool, roles ...rbac.RoleName) *auth.User {
	f.t.Helper()
	u := &auth.User{
		ID: auth.UserID("u-" + name), Username: name, Email: email, EmailVerified: verified, Roles: roles,
		Status: auth.StatusActive, CreatedAt: f.clock(), UpdatedAt: f.clock(), Version: 1,
	}
	f.commit(func(b *storage.Batch) error { return f.st.PutUser(b, u) })
	if !f.svc.HasUsers() {
		f.rebuild()
	}
	return f.reload(u.ID)
}

func TestLifecycle_RegisterTwiceIsRefused(t *testing.T) {
	f := newFx(t)
	bruce := f.user("bruce", rbac.RoleAdmin) // the first user rebuilds the service: register after it
	dick := f.user("dick-grayson", "gotham.detective")
	var first, second int
	if err := f.svc.RegisterLifecycle(auth.Lifecycle{StatusChanged: func(context.Context, auth.StatusChanged) { first++ }}); err != nil {
		t.Fatal(err)
	}
	err := f.svc.RegisterLifecycle(auth.Lifecycle{StatusChanged: func(context.Context, auth.StatusChanged) { second++ }})
	if !errors.Is(err, auth.ErrLifecycleRegistered) {
		t.Fatalf("second RegisterLifecycle = %v, want ErrLifecycleRegistered", err)
	}
	if err := f.svc.SetStatus(context.Background(), f.session(bruce), dick.ID, auth.StatusDisabled); err != nil {
		t.Fatal(err)
	}
	if first != 1 || second != 0 {
		t.Fatalf("callbacks: first=%d second=%d, want 1 and 0: the refused registration must change nothing", first, second)
	}
}

// A zero Lifecycle registers (every callback may be nil) and still counts as
// the one registration; a service with no registration writes as before.
func TestLifecycle_NilCallbacksAndNoRegistration(t *testing.T) {
	f := newFx(t)
	bruce := f.user("bruce", rbac.RoleAdmin)
	dick := f.user("dick-grayson", "gotham.detective")
	ctx := context.Background()
	if err := f.svc.SetStatus(ctx, f.session(bruce), dick.ID, auth.StatusDisabled); err != nil {
		t.Fatalf("no registration: %v", err)
	}
	if err := f.svc.RegisterLifecycle(auth.Lifecycle{}); err != nil {
		t.Fatal(err)
	}
	if err := f.svc.SetStatus(ctx, f.session(bruce), dick.ID, auth.StatusActive); err != nil {
		t.Fatalf("all-nil lifecycle: %v", err)
	}
	if err := f.svc.RegisterLifecycle(auth.Lifecycle{}); !errors.Is(err, auth.ErrLifecycleRegistered) {
		t.Fatalf("an all-nil registration must still count: %v", err)
	}
}

func TestLifecycle_InviteAccepted(t *testing.T) {
	f := newFx(t)
	rec := f.lifecycle()
	bruce := f.user("bruce", rbac.RoleAdmin)
	gordon := f.user("jim-gordon", roleCommissioner)

	t.Run("admin creator", func(t *testing.T) {
		tok, view := f.invite(f.session(bruce), auth.InviteSpec{Username: "cassandra-cain", Email: "Cass@Example.test", Role: "gotham.detective"})
		u, _, err := f.accept(tok, "orphan-of-cain")
		if err != nil {
			t.Fatal(err)
		}
		want := auth.InviteAccepted{InviteID: view.ID, UserID: u.ID, CreatedBy: bruce.ID, Email: "cass@example.test", CreatorHeldAdmin: true}
		if n, _, _ := rec.counts(); n != 1 || !inviteEqual(rec.invites[0], want) {
			t.Fatalf("InviteAccepted calls = %+v, want exactly [%+v]", rec.invites, want)
		}
	})
	t.Run("creator below admin", func(t *testing.T) {
		tok, view := f.invite(f.session(gordon), auth.InviteSpec{Username: "stephanie-brown", Email: "steph@example.test", Role: "gotham.detective"})
		u, _, err := f.accept(tok, "spoiler-1234")
		if err != nil {
			t.Fatal(err)
		}
		want := auth.InviteAccepted{InviteID: view.ID, UserID: u.ID, CreatedBy: gordon.ID, Email: "steph@example.test", CreatorHeldAdmin: false}
		if n, _, _ := rec.counts(); n != 2 || !inviteEqual(rec.invites[1], want) {
			t.Fatalf("InviteAccepted calls = %+v, want a second %+v", rec.invites, want)
		}
	})
	t.Run("no address", func(t *testing.T) {
		tok, view := f.invite(f.session(bruce), auth.InviteSpec{Username: "barbara-gordon", Role: "gotham.detective"})
		u, _, err := f.accept(tok, "oracle-1234")
		if err != nil {
			t.Fatal(err)
		}
		want := auth.InviteAccepted{InviteID: view.ID, UserID: u.ID, CreatedBy: bruce.ID, CreatorHeldAdmin: true}
		if n, _, _ := rec.counts(); n != 3 || !inviteEqual(rec.invites[2], want) {
			t.Fatalf("InviteAccepted calls = %+v, want a third %+v", rec.invites, want)
		}
	})
}

func inviteEqual(a, b auth.InviteAccepted) bool {
	return a.InviteID == b.InviteID && a.UserID == b.UserID && a.CreatedBy == b.CreatedBy && a.Email == b.Email &&
		a.CreatorHeldAdmin == b.CreatorHeldAdmin && slices.Equal(a.DisplacedUserIDs, b.DisplacedUserIDs)
}

// The creator is judged at the accept, not at the invite: an admin demoted
// since is not an admin creator (and an invite of one that lost
// core:users.manage is refused outright, below).
func TestLifecycle_CreatorHeldAdminIsJudgedAtAccept(t *testing.T) {
	f := newFx(t)
	rec := f.lifecycle()
	bruce := f.user("bruce", rbac.RoleAdmin)
	f.user("alfred", rbac.RoleAdmin) // keeps an admin once bruce is demoted
	tok, _ := f.invite(f.session(bruce), auth.InviteSpec{Username: "tim-drake", Email: "tim@example.test", Role: "gotham.detective"})
	f.setRoles(bruce.ID, roleCommissioner) // still manages users, no longer admin
	if _, _, err := f.accept(tok, "red-robin-1"); err != nil {
		t.Fatal(err)
	}
	if n, _, _ := rec.counts(); n != 1 || rec.invites[0].CreatorHeldAdmin {
		t.Fatalf("calls = %+v, want one with CreatorHeldAdmin=false", rec.invites)
	}
}

// Never on a refused or rolled-back write: every refusal below leaves the
// callbacks untouched.
func TestLifecycle_NothingOnARefusedWrite(t *testing.T) {
	f := newFx(t)
	rec := f.lifecycle()
	ctx := context.Background()
	bruce := f.user("bruce", rbac.RoleAdmin)
	gordon := f.user("jim-gordon", roleCommissioner)

	t.Run("accept: short password", func(t *testing.T) {
		tok, _ := f.invite(f.session(bruce), auth.InviteSpec{Username: "jason-todd", Email: "jason@example.test", Role: "gotham.detective"})
		if _, _, err := f.accept(tok, "short"); err == nil {
			t.Fatal("a short password was accepted")
		}
		rec.wantNone(t, "short password")
	})
	t.Run("accept: unknown token", func(t *testing.T) {
		if _, _, err := f.accept("mci_"+strings.Repeat("A", 43), "long-enough-pw"); err == nil {
			t.Fatal("an unknown token was accepted")
		}
		rec.wantNone(t, "unknown token")
	})
	t.Run("accept: expired", func(t *testing.T) {
		tok, _ := f.invite(f.session(bruce), auth.InviteSpec{Username: "harper-row", Role: "gotham.detective"})
		f.advance(auth.DefaultInviteTTL + time.Hour)
		if _, _, err := f.accept(tok, "long-enough-pw"); !errors.Is(err, auth.ErrInviteInvalid) {
			t.Fatalf("expired accept = %v, want ErrInviteInvalid", err)
		}
		rec.wantNone(t, "expired invite")
	})
	t.Run("accept: username taken since", func(t *testing.T) {
		tok, _ := f.invite(f.session(bruce), auth.InviteSpec{Username: "duke-thomas", Role: "gotham.detective"})
		f.user("duke-thomas", "gotham.detective")
		if _, _, err := f.accept(tok, "long-enough-pw"); !errors.Is(err, auth.ErrInviteInvalid) || !errors.Is(err, auth.ErrUserExists) {
			t.Fatalf("taken accept = %v, want ErrInviteInvalid wrapping ErrUserExists", err)
		}
		rec.wantNone(t, "username taken")
	})
	t.Run("accept: creator demoted", func(t *testing.T) {
		tok, _ := f.invite(f.session(gordon), auth.InviteSpec{Username: "kate-kane", Role: "gotham.detective"})
		f.setRoles(gordon.ID, "gotham.detective")
		if _, _, err := f.accept(tok, "long-enough-pw"); !errors.Is(err, auth.ErrInviteInvalid) {
			t.Fatalf("accept of a demoted creator's invite = %v, want ErrInviteInvalid", err)
		}
		rec.wantNone(t, "demoted creator")
	})
	t.Run("status: no-op, dominance, unknown", func(t *testing.T) {
		dick := f.user("dick-grayson", "gotham.detective")
		if err := f.svc.SetStatus(ctx, f.session(bruce), dick.ID, auth.StatusActive); err != nil {
			t.Fatalf("setting the status a user has: %v", err)
		}
		rec.wantNone(t, "same status")
		if err := f.svc.SetStatus(ctx, f.session(f.reload(dick.ID)), bruce.ID, auth.StatusDisabled); err == nil {
			t.Fatal("a detective disabled an admin")
		}
		if err := f.svc.SetStatus(ctx, f.session(bruce), "u-nobody", auth.StatusDisabled); err == nil {
			t.Fatal("disabled a user that does not exist")
		}
		rec.wantNone(t, "refused disables")
	})
	t.Run("profile: nothing to change, same spelling, name only, address taken", func(t *testing.T) {
		selina := f.userWithEmail("selina-kyle", "selina.kyle@example.test", true, "gotham.detective")
		f.userWithEmail("harvey-bullock", "harvey@example.test", true, "gotham.detective")
		p := f.session(selina)
		same := "SELINA.Kyle@example.test"
		name := "Catwoman"
		if _, err := f.svc.UpdateProfile(ctx, p, auth.ProfileUpdate{Email: &same}); err != nil {
			t.Fatalf("spelling-only change: %v", err)
		}
		if _, err := f.svc.UpdateProfile(ctx, p, auth.ProfileUpdate{DisplayName: &name}); err != nil {
			t.Fatalf("name-only change: %v", err)
		}
		if _, err := f.svc.UpdateProfile(ctx, p, auth.ProfileUpdate{}); err == nil {
			t.Fatal("an empty patch was accepted")
		}
		taken := "harvey@example.test"
		if _, err := f.svc.UpdateProfile(ctx, p, auth.ProfileUpdate{Email: &taken}); !errors.Is(err, auth.ErrEmailInUse) {
			t.Fatalf("a verified holder's address = %v, want ErrEmailInUse", err)
		}
		rec.wantNone(t, "profile writes that changed no address")
	})
}

func TestLifecycle_StatusChanged(t *testing.T) {
	f := newFx(t)
	rec := f.lifecycle()
	ctx := context.Background()
	bruce := f.user("bruce", rbac.RoleAdmin)
	selina := f.userWithEmail("selina-kyle", "selina.kyle@example.test", true, "gotham.detective")

	if err := f.svc.SetStatus(ctx, f.session(bruce), selina.ID, auth.StatusDisabled); err != nil {
		t.Fatal(err)
	}
	if err := f.svc.SetStatus(ctx, f.session(bruce), selina.ID, auth.StatusActive); err != nil {
		t.Fatal(err)
	}
	want := []auth.StatusChanged{
		{UserID: selina.ID, Email: "selina.kyle@example.test", From: auth.StatusActive, To: auth.StatusDisabled},
		{UserID: selina.ID, Email: "selina.kyle@example.test", From: auth.StatusDisabled, To: auth.StatusActive},
	}
	if _, n, _ := rec.counts(); n != 2 || !slices.Equal(rec.status, want) {
		t.Fatalf("StatusChanged calls = %+v, want %+v", rec.status, want)
	}
}

// A commit that conflicts and is retried calls the callback once, for the
// attempt that committed.
func TestLifecycle_ARetriedCommitCallsOnce(t *testing.T) {
	f := newFx(t)
	rec := f.lifecycle()
	bruce := f.user("bruce", rbac.RoleAdmin)
	dick := f.user("dick-grayson", "gotham.detective")
	interfered := false
	f.audit.hold = func(e audit.Event) {
		if interfered || e.Action != "core.user.disable" {
			return
		}
		interfered = true
		// A concurrent write to the same user lands after the first attempt
		// staged its write and before it commits: the commit conflicts.
		u := f.reload(dick.ID)
		u.DisplayName, u.Version = "Nightwing", u.Version+1
		f.commit(func(b *storage.Batch) error { return f.st.PutUser(b, u) })
	}
	if err := f.svc.SetStatus(context.Background(), f.session(bruce), dick.ID, auth.StatusDisabled); err != nil {
		t.Fatal(err)
	}
	if !interfered {
		t.Fatal("the interfering write never ran: the test did not force a retry")
	}
	if _, n, _ := rec.counts(); n != 1 {
		t.Fatalf("StatusChanged ran %d times over a retried commit, want 1", n)
	}
}

func TestLifecycle_EmailChanged(t *testing.T) {
	f := newFx(t)
	rec := f.lifecycle()
	ctx := context.Background()
	selina := f.userWithEmail("selina-kyle", "selina.kyle@example.test", true, "gotham.detective")
	oracle := f.user("barbara-gordon", "gotham.detective") // no address yet

	to := "irena.dubrovna@example.test"
	if _, err := f.svc.UpdateProfile(ctx, f.session(selina), auth.ProfileUpdate{Email: &to}); err != nil {
		t.Fatal(err)
	}
	first := "oracle@example.test"
	if _, err := f.svc.UpdateProfile(ctx, f.session(oracle), auth.ProfileUpdate{Email: &first}); err != nil {
		t.Fatal(err)
	}
	want := []auth.EmailChanged{
		{UserID: selina.ID, From: "selina.kyle@example.test", To: to},
		{UserID: oracle.ID, From: "", To: first},
	}
	if _, _, n := rec.counts(); n != 2 || !slices.Equal(rec.emails, want) {
		t.Fatalf("EmailChanged calls = %+v, want %+v", rec.emails, want)
	}
}

// A panicking callback does not fail the write, is logged with its type and
// stack and never its value, and the next callback still runs.
func TestLifecycle_APanickingCallbackFailsNothing(t *testing.T) {
	f := newFx(t)
	bruce := f.user("bruce", rbac.RoleAdmin)
	dick := f.user("dick-grayson", "gotham.detective")
	var logs bytes.Buffer
	f.rebuildLogging(slog.New(slog.NewTextHandler(&logs, nil)))
	ran := 0
	if err := f.svc.RegisterLifecycle(auth.Lifecycle{
		StatusChanged: func(context.Context, auth.StatusChanged) {
			ran++
			panic("the-joker-was-here")
		},
	}); err != nil {
		t.Fatal(err)
	}
	if err := f.svc.SetStatus(context.Background(), f.session(bruce), dick.ID, auth.StatusDisabled); err != nil {
		t.Fatalf("SetStatus failed over a panicking callback: %v", err)
	}
	if got := f.reload(dick.ID).Status; got != auth.StatusDisabled {
		t.Fatalf("stored status = %s, want disabled", got)
	}
	out := logs.String()
	if ran != 1 || !strings.Contains(out, "lifecycle hook panicked") || !strings.Contains(out, "hook=status_changed") ||
		!strings.Contains(out, "panic_type=string") || !strings.Contains(out, "stack=") {
		t.Fatalf("ran=%d, log:\n%s", ran, out)
	}
	if strings.Contains(out, "the-joker-was-here") {
		t.Fatalf("the panic value reached the log:\n%s", out)
	}
	// The service is still usable: a second write calls the callback again.
	if err := f.svc.SetStatus(context.Background(), f.session(bruce), dick.ID, auth.StatusActive); err != nil || ran != 2 {
		t.Fatalf("after the panic: err=%v ran=%d, want nil and 2", err, ran)
	}
}

// The callback's ctx keeps the request's values but not its cancellation.
func TestLifecycle_CallbackContextOutlivesTheRequest(t *testing.T) {
	f := newFx(t)
	bruce := f.user("bruce", rbac.RoleAdmin)
	dick := f.user("dick-grayson", "gotham.detective")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var got error = errors.New("callback never ran")
	if err := f.svc.RegisterLifecycle(auth.Lifecycle{StatusChanged: func(c context.Context, _ auth.StatusChanged) {
		cancel() // the client hangs up while the callback is called
		got = c.Err()
	}}); err != nil {
		t.Fatal(err)
	}
	if err := f.svc.SetStatus(ctx, f.session(bruce), dick.ID, auth.StatusDisabled); err != nil {
		t.Fatal(err)
	}
	if got != nil {
		t.Fatalf("callback ctx err = %v, want nil (the write is durable)", got)
	}
}

// rebuildLogging is rebuild with the service logging to log.
func (f *fx) rebuildLogging(log *slog.Logger) {
	f.t.Helper()
	svc, err := auth.New(context.Background(), auth.Options{ImportedKeyPrefixes: []string{"abk_"}}, auth.Deps{
		Store: f.st, NewBatch: f.st.NewBatch, RBAC: f.rb, Audit: f.audit, Revocations: f.jrnl, Clock: f.clock,
		Hasher: f.hasher, Settings: f.liveSettings, OnInvalidated: f.inval.callback(), Log: log,
	})
	if err != nil {
		f.t.Fatal(err)
	}
	f.svc = svc
}

// STEP 0 (F4-08 B10r, ruling 5), as B10t left it: what core's AcceptInvite does
// when another account holds the invite's address and the invite does NOT bind
// it (never flagged by MarkInviteEmailDelivered). It claims nothing: the
// invitee's copy is stored unverified, the existing holder is untouched, and a
// verified holder does not block it. The holder appears after the invite was
// made, because CreateInvite refuses a verified holder (HEAD's rule at
// create). The bound accept is in invite_bind_test.go.
func TestAcceptInvite_AnotherHolderOfTheInviteAddress(t *testing.T) {
	for _, tc := range []struct {
		name     string
		verified bool
	}{{"unverified holder", false}, {"verified holder", true}} {
		t.Run(tc.name, func(t *testing.T) {
			f := newFx(t)
			bruce := f.user("bruce", rbac.RoleAdmin)
			tok, _ := f.invite(f.session(bruce), auth.InviteSpec{Username: "irena-dubrovna", Email: "Selina.Kyle@Example.test", Role: "gotham.detective"})
			holder := f.userWithEmail("selina-kyle", "selina.kyle@example.test", tc.verified, "gotham.detective")
			u, _, err := f.accept(tok, "catwoman-1234")
			if err != nil {
				t.Fatalf("accept: %v", err)
			}
			after := f.reload(holder.ID)
			if after.Email != "selina.kyle@example.test" || after.EmailVerified != tc.verified || after.Version != holder.Version {
				t.Fatalf("core's accept touched the existing holder: before %+v after %+v", holder, after)
			}
			got := f.reload(u.ID)
			if got.Email != "selina.kyle@example.test" || got.EmailVerified {
				t.Fatalf("the invitee holds %q verified=%v, want the address unverified", got.Email, got.EmailVerified)
			}
		})
	}
}

// displaceUnverifiedHolders is the helper the invite's bind calls in the
// accept's own commit: HEAD's clearEmailFromHolders over the unproven holders.
func TestDisplaceUnverifiedHolders(t *testing.T) {
	ctx := context.Background()
	displace := func(f *fx, email string, newUser auth.UserID) ([]auth.UserID, error) {
		b := f.st.NewBatch()
		defer func() { _ = b.Close() }()
		ids, err := f.svc.DisplaceUnverifiedHolders(ctx, b, email, newUser)
		if err != nil {
			return nil, err
		}
		if err := b.Commit(ctx, storage.Sync); err != nil {
			t.Fatal(err)
		}
		return ids, nil
	}

	t.Run("unverified holders lose it, in the batch", func(t *testing.T) {
		f := newFx(t)
		a := f.userWithEmail("selina-kyle", "selina.kyle@example.test", false, "gotham.detective")
		b := f.userWithEmail("harvey-bullock", "Selina.KYLE@example.test", false, "gotham.detective") // another spelling
		other := f.userWithEmail("renee-montoya", "renee@example.test", false, "gotham.detective")
		newUser := f.userWithEmail("irena-dubrovna", "selina.kyle@example.test", false, "gotham.detective")
		ids, err := displace(f, "selina.kyle@example.test", newUser.ID)
		if err != nil {
			t.Fatal(err)
		}
		slices.Sort(ids)
		if want := []auth.UserID{b.ID, a.ID}; !slices.Equal(ids, want) { // sorted by id: harvey before selina
			t.Fatalf("displaced %v, want %v", ids, want)
		}
		for _, id := range ids {
			if u := f.reload(id); u.Email != "" || u.EmailVerified {
				t.Fatalf("%s still holds %q (verified=%v)", id, u.Email, u.EmailVerified)
			}
		}
		if f.reload(other.ID).Email != "renee@example.test" {
			t.Fatal("an unrelated account lost its address")
		}
		if f.reload(newUser.ID).Email != "selina.kyle@example.test" {
			t.Fatal("the new account lost its own address")
		}
	})
	t.Run("a verified holder refuses it and nothing is staged", func(t *testing.T) {
		f := newFx(t)
		loose := f.userWithEmail("selina-kyle", "selina.kyle@example.test", false, "gotham.detective")
		f.userWithEmail("harvey-bullock", "selina.kyle@example.test", true, "gotham.detective")
		newUser := f.user("irena-dubrovna", "gotham.detective")
		if _, err := displace(f, "selina.kyle@example.test", newUser.ID); !errors.Is(err, auth.ErrEmailInUse) {
			t.Fatalf("a verified holder = %v, want ErrEmailInUse", err)
		}
		if f.reload(loose.ID).Email == "" {
			t.Fatal("an unverified holder lost its address although the bind was refused")
		}
	})
	t.Run("no holder displaces nobody", func(t *testing.T) {
		f := newFx(t)
		newUser := f.user("irena-dubrovna", "gotham.detective")
		if ids, err := displace(f, "selina.kyle@example.test", newUser.ID); err != nil || len(ids) != 0 {
			t.Fatalf("displaced %v, %v", ids, err)
		}
	})
}

// UpdateProfile holds the account e-mail lock across its commit; the
// callback runs after the lock is released, so one that takes it (as a
// sign-up, an OIDC resolve or another profile write does) does not wait on
// the call that is calling it.
func TestLifecycle_EmailChangedRunsWithTheEmailLockReleased(t *testing.T) {
	f := newFx(t)
	selina := f.userWithEmail("selina-kyle", "selina.kyle@example.test", true, "gotham.detective")
	took := make(chan struct{})
	if err := f.svc.RegisterLifecycle(auth.Lifecycle{EmailChanged: func(context.Context, auth.EmailChanged) {
		unlock := f.svc.LockEmails() // blocks forever while UpdateProfile still holds the lock
		unlock()
		close(took)
	}}); err != nil {
		t.Fatal(err)
	}
	to := "irena.dubrovna@example.test"
	done := make(chan error, 1)
	go func() {
		_, err := f.svc.UpdateProfile(context.Background(), f.session(f.reload(selina.ID)), auth.ProfileUpdate{Email: &to})
		done <- err
	}()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("UpdateProfile never returned: the callback waited on the e-mail lock the call still held")
	}
	select {
	case <-took:
	default:
		t.Fatal("the callback never took the e-mail lock")
	}
}
