// file: core/auth/invite_session_ttl_test.go
// version: 0.1.0
// guid: 5d0b7e84-39c2-4f6a-8a17-c4e2b96f1d03
// last-edited: 2026-11-22

package auth_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/falkcorp/mediacore/auth"
)

// F4-08 B10ttl: the session an accepted invite opens lives
// Options.InviteSessionTTL when the app set one (ABO: HEAD's 30 days), else
// core.auth.session.remember_ttl; and setting it moves neither a remember-me
// sign-in nor a plain one. The settings here are far from the defaults (3h
// plain, 9h remember, 720h invite), so no lifetime can pass by matching a default.

const (
	ttlPlain    = 3 * time.Hour
	ttlRemember = 9 * time.Hour
	ttlInvite   = 720 * time.Hour
)

// ttlFx is a fixture whose live settings are ttlPlain and ttlRemember and
// whose service carries inviteTTL.
func ttlFx(t *testing.T, inviteTTL time.Duration) *fx {
	t.Helper()
	f := newFx(t)
	f.setSettings(auth.Settings{SessionTTL: ttlPlain, RememberTTL: ttlRemember})
	f.inviteSessionTTL = inviteTTL
	f.rebuild()
	return f
}

// inviteSessionExpiry accepts a fresh invite and returns when the session it
// opened expires, as the issued session and the stored record say (both must
// agree: the cookie is cut from one and the check reads the other).
func inviteSessionExpiry(t *testing.T, f *fx) time.Time {
	t.Helper()
	gordon := f.user("jim-gordon", roleCommissioner)
	tok, _ := f.invite(f.session(gordon), detectiveInvite("cassandra-cain"))
	_, sess, err := f.accept(tok, "orphan-of-cain")
	if err != nil {
		t.Fatalf("AcceptInvite: %v", err)
	}
	rec, err := f.st.SessionByPublicID(context.Background(), sess.PublicID)
	if err != nil {
		t.Fatal(err)
	}
	if rec.Origin != auth.OriginInvite || !rec.ExpiresAt.Equal(sess.ExpiresAt) {
		t.Fatalf("stored session %+v, issued to expire %v", rec, sess.ExpiresAt)
	}
	return sess.ExpiresAt
}

// Cell 1: an invite-accept session expires at now+InviteSessionTTL.
func TestInviteSessionTTL_AcceptUsesTheOption(t *testing.T) {
	f := ttlFx(t, ttlInvite)
	if got, want := inviteSessionExpiry(t, f), f.clock().Add(ttlInvite); !got.Equal(want) {
		t.Fatalf("invite session expires %v, want now+%v = %v", got, ttlInvite, want)
	}
}

// Cell 2: the option moves no other session. A remember-me sign-in is
// remember_ttl, a plain one session.ttl, and a temp-login consume session.ttl,
// with InviteSessionTTL set.
func TestInviteSessionTTL_LeavesEveryOtherSessionAlone(t *testing.T) {
	f, _ := loginFx(t)
	f.setSettings(auth.Settings{SessionTTL: ttlPlain, RememberTTL: ttlRemember})
	f.inviteSessionTTL = ttlInvite
	f.rebuild()

	u := f.withPassword(f.user("brucewayne"), "i-am-the-night")
	c := from("192.0.2.10")
	ctx := context.Background()

	plain, err := f.login("brucewayne", "i-am-the-night", c)
	if err != nil {
		t.Fatal(err)
	}
	if want := f.clock().Add(ttlPlain); !plain.ExpiresAt.Equal(want) {
		t.Fatalf("plain sign-in expires %v, want session.ttl = %v", plain.ExpiresAt, want)
	}
	remembered, err := f.svc.Login(ctx, auth.LoginInput{Username: "brucewayne", Password: "i-am-the-night", Remember: true, Client: c})
	if err != nil {
		t.Fatal(err)
	}
	if want := f.clock().Add(ttlRemember); !remembered.ExpiresAt.Equal(want) {
		t.Fatalf("remember-me sign-in expires %v, want remember_ttl = %v", remembered.ExpiresAt, want)
	}
	// The exported IssueSession: remember is remember_ttl, and the invite
	// origin does not turn it into the invite lifetime (only AcceptInvite does).
	issued, err := f.svc.IssueSession(ctx, u, auth.OriginInvite, true, c)
	if err != nil {
		t.Fatal(err)
	}
	if want := f.clock().Add(ttlRemember); !issued.ExpiresAt.Equal(want) {
		t.Fatalf("IssueSession(remember) expires %v, want remember_ttl = %v", issued.ExpiresAt, want)
	}

	gordon := f.user("jim-gordon", roleCommissioner)
	link := f.mintTemp(f.session(gordon), u.ID, false)
	temp, err := f.svc.ConsumeTempLogin(ctx, link, "", c)
	if err != nil {
		t.Fatal(err)
	}
	if want := f.clock().Add(ttlPlain); !temp.ExpiresAt.Equal(want) {
		t.Fatalf("temp-login session expires %v, want session.ttl = %v", temp.ExpiresAt, want)
	}
}

// Cell 3: with InviteSessionTTL zero (hello, a new app) an invite-accept
// session is remember_ttl, and follows it when an operator changes it.
func TestInviteSessionTTL_ZeroFallsBackToRememberTTL(t *testing.T) {
	f := ttlFx(t, 0)
	if got, want := inviteSessionExpiry(t, f), f.clock().Add(ttlRemember); !got.Equal(want) {
		t.Fatalf("invite session expires %v, want remember_ttl = %v", got, want)
	}
}

// With the option set, remember_ttl does not move the invite session: the
// option is compile-time and has no operator key.
func TestInviteSessionTTL_OperatorRememberTTLDoesNotShortenIt(t *testing.T) {
	f := ttlFx(t, ttlInvite)
	f.setSettings(auth.Settings{SessionTTL: ttlPlain, RememberTTL: time.Hour})
	if got, want := inviteSessionExpiry(t, f), f.clock().Add(ttlInvite); !got.Equal(want) {
		t.Fatalf("invite session expires %v, want %v whatever remember_ttl is", got, want)
	}
}

// A negative value is a build mistake, refused at New rather than issuing
// sessions already expired.
func TestInviteSessionTTL_NegativeIsRefused(t *testing.T) {
	f := newFx(t)
	f.inviteSessionTTL = -time.Hour
	_, err := auth.New(context.Background(), auth.Options{InviteSessionTTL: f.inviteSessionTTL}, auth.Deps{
		Store: f.st, NewBatch: f.st.NewBatch, RBAC: f.rb, Audit: f.audit, Revocations: f.jrnl, Clock: f.clock,
		Hasher: f.hasher, Settings: f.liveSettings,
	})
	if !errors.Is(err, auth.ErrBadInput) {
		t.Fatalf("New with a negative InviteSessionTTL = %v, want ErrBadInput", err)
	}
}
