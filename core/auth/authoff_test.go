// file: core/auth/authoff_test.go
// version: 1.0.0
// guid: 8e2f4a67-1b3c-4d95-a0e8-5f6c7d9b1a42
// last-edited: 2026-11-11

package auth_test

import (
	"context"
	"errors"
	"testing"

	"github.com/falkcorp/mediacore/auth"
	"github.com/falkcorp/mediacore/rbac"
)

// Auth-off mode (05-authn §4.11).

func boolp(b bool) *bool { return &b }

// offService builds a service over f's store in mode with the given reach and
// settings; New's error is returned, not fatal.
func (f *fx) offService(opts auth.Options, st auth.Settings, reach func() auth.Reach) (*auth.Service, error) {
	f.t.Helper()
	return auth.New(context.Background(), opts, auth.Deps{
		Store: f.st, NewBatch: f.st.NewBatch, RBAC: f.rb, Audit: f.audit, Revocations: f.jrnl, Clock: f.clock,
		Hasher: f.hasher, Settings: func() auth.Settings { return st }, Reach: reach,
	})
}

var loopbackOnly = func() auth.Reach { return auth.Reach{} }

// offFx is a fixture whose service runs auth off over a loopback-only install.
func offFx(t *testing.T) (*fx, *auth.Service) {
	t.Helper()
	f := newFx(t)
	svc, err := f.offService(auth.Options{}, auth.Settings{Mode: auth.ModeDisabled}, loopbackOnly)
	if err != nil {
		t.Fatalf("New in auth-off mode: %v", err)
	}
	return f, svc
}

// TestAuthDisabled_AuthRequiredScopesStill401: the auth-off principal holds
// the open scopes and nothing AuthRequired, Sensitive or admin, has no user
// and no admin flag, and binds even before any user exists.
func TestAuthDisabled_AuthRequiredScopesStill401(t *testing.T) {
	_, svc := offFx(t)
	w, got := serve(t, svc, request("", ""))
	if w.Code != 200 || !got.called || got.p == nil {
		t.Fatalf("no principal bound: %d %+v", w.Code, got)
	}
	p := got.p
	if p.Method != auth.MethodAuthDisabled || p.User != nil || p.CredentialID != "" {
		t.Fatalf("principal = %+v", p)
	}
	if !p.Perms.Has("gotham:patrol") || !p.Perms.Has("gotham:cases.read") {
		t.Fatal("the open scopes are not held")
	}
	if p.Perms.Has(rbac.CoreAdmin) || p.Perms.Satisfies(rbac.Need(rbac.CoreAdmin)) {
		t.Fatal("auth-off holds core:admin")
	}
	if p.Perms.Has("gotham:cave.manage") {
		t.Fatal("auth-off holds a Sensitive scope")
	}
	if p.Perms.Has(rbac.Permission("core:audit.view")) {
		t.Fatal("auth-off holds an AuthRequired scope")
	}
	if p.Perms.Confined() {
		t.Fatal("auth-off principal is confined")
	}
	if got.checker == nil || got.checker.Has("gotham:patrol") != true {
		t.Fatal("the rbac checker was not bound")
	}
	if got.actor.Method != string(auth.MethodAuthDisabled) || got.actor.ClientAddr != "192.0.2.10" || got.actor.UserID != "" {
		t.Fatalf("audit actor = %+v", got.actor)
	}
}

// TestAuthDisabled_PresentedKeyIgnored (§4.2 step 0): a valid key, a revoked
// key, a garbage bearer and a session cookie all end as the same synthetic
// principal, with none of the presented credential's user or scopes.
func TestAuthDisabled_PresentedKeyIgnored(t *testing.T) {
	f, svc := offFx(t)
	dick := f.user("dick-grayson", "gotham.detective")
	sess := f.session(dick)
	raw, k := f.mint(sess, dick.ID, "gotham:patrol")
	for name, r := range map[string]func() *auth.Principal{
		"a valid key":   func() *auth.Principal { _, g := serve(t, svc, request(raw, "")); return g.p },
		"a garbage key": func() *auth.Principal { _, g := serve(t, svc, request("mck_garbage", "")); return g.p },
		"a cookie":      func() *auth.Principal { _, g := serve(t, svc, request("", "mcs_garbage")); return g.p },
	} {
		p := r()
		if p == nil || p.Method != auth.MethodAuthDisabled || p.User != nil || p.CredentialID != "" {
			t.Fatalf("%s: principal %+v", name, p)
		}
	}
	// The valid key was not even looked up: no use recorded against it.
	if got := f.key(k.ID); !got.LastUsedAt.IsZero() {
		t.Fatalf("auth-off touched a presented key: %+v", got)
	}
}

// TestAuthDisabled_InteractiveGuardRefuses (SEC5-8): the auth-off method is
// neither interactive nor allowed to change credentials, and with no user the
// credential surfaces refuse its principal.
func TestAuthDisabled_InteractiveGuardRefuses(t *testing.T) {
	f, svc := offFx(t)
	if auth.MethodAuthDisabled.Interactive() || auth.MethodAuthDisabled.MayChangeCredentials() {
		t.Fatal("auth_disabled is interactive or may change credentials")
	}
	_, got := serve(t, svc, request("", ""))
	p := got.p
	ctx := context.Background()
	dick := f.user("dick-grayson", "gotham.detective")
	_, _, err := svc.CreateAPIKey(ctx, p, auth.KeySpec{Owner: dick.ID, Name: "x", Scopes: []rbac.Permission{"gotham:patrol"}})
	wantErr(t, "minting a key", err, auth.ErrDenied)
	wantErr(t, "revoking a key", svc.RevokeAPIKey(ctx, p, "any"), auth.ErrDenied)
	_, err = svc.MintTempLogin(ctx, p, dick.ID)
	wantErr(t, "a temp-login link", err, auth.ErrDenied)
	wantErr(t, "a revalidation", svc.Revalidate(ctx, p), auth.ErrInvalidCredentials)
}

// TestAuthDisabled_RefusesNonLoopbackOrProxyWithoutOptIn (§4.11, SEC-30,
// SEC4-8): auth off needs loopback-only reach unless
// core.auth.disabled_allow_remote, or the app's default for it, says
// otherwise; unknown reach counts as remote; and the posture says so.
func TestAuthDisabled_RefusesNonLoopbackOrProxyWithoutOptIn(t *testing.T) {
	f := newFx(t)
	off := auth.Settings{Mode: auth.ModeDisabled}
	allow := auth.Settings{Mode: auth.ModeDisabled, DisabledAllowRemote: boolp(true)}
	deny := auth.Settings{Mode: auth.ModeDisabled, DisabledAllowRemote: boolp(false)}
	listener := func() auth.Reach { return auth.Reach{NonLoopbackListener: true} }
	proxy := func() auth.Reach { return auth.Reach{TrustedProxy: true} }
	for _, tc := range []struct {
		name    string
		opts    auth.Options
		st      auth.Settings
		reach   func() auth.Reach
		refused bool
		remote  bool
	}{
		{"loopback only", auth.Options{}, off, loopbackOnly, false, false},
		{"a non-loopback listener", auth.Options{}, off, listener, true, false},
		{"a trusted proxy", auth.Options{}, off, proxy, true, false},
		{"unknown reach", auth.Options{}, off, nil, true, false},
		{"a listener, allowed", auth.Options{}, allow, listener, false, true},
		{"a proxy, allowed", auth.Options{}, allow, proxy, false, true},
		{"unknown reach, allowed", auth.Options{}, allow, nil, false, true},
		{"the app default allows", auth.Options{DefaultDisabledAllowRemote: true}, off, listener, false, true},
		{"the operator value beats the app default", auth.Options{DefaultDisabledAllowRemote: true}, deny, listener, true, false},
		{"loopback with the app default", auth.Options{DefaultDisabledAllowRemote: true}, off, loopbackOnly, false, false},
	} {
		svc, err := f.offService(tc.opts, tc.st, tc.reach)
		if tc.refused {
			if !errors.Is(err, auth.ErrAuthDisabledRemote) || svc != nil {
				t.Errorf("%s: svc %v, err %v, want ErrAuthDisabledRemote", tc.name, svc, err)
			}
			continue
		}
		if err != nil {
			t.Errorf("%s: %v", tc.name, err)
			continue
		}
		if got := svc.Posture(); got != (auth.Posture{Disabled: true, DisabledRemote: tc.remote}) {
			t.Errorf("%s: posture %+v, want remote=%v", tc.name, got, tc.remote)
		}
	}

	// A required service never reports auth off and never needs Reach.
	svc, err := f.offService(auth.Options{}, auth.Settings{Mode: auth.ModeRequired}, listener)
	if err != nil || svc.Posture() != (auth.Posture{}) {
		t.Fatalf("required mode: %+v, %v", svc, err)
	}
	if _, err := f.offService(auth.Options{}, auth.Settings{Mode: "optional"}, loopbackOnly); !errors.Is(err, auth.ErrBadInput) {
		t.Fatalf("an unknown mode: %v", err)
	}
}

// TestAuthRequired_BindsNoSyntheticPrincipal: the default mode never binds
// the auth-off principal, whatever the request, and mode is fixed at New: a
// settings change afterwards does not turn it on.
func TestAuthRequired_BindsNoSyntheticPrincipal(t *testing.T) {
	f := newFx(t)
	f.user("dick-grayson", "gotham.detective")
	_, got := serve(t, f.svc, request("", ""))
	if got.p != nil || !got.called {
		t.Fatalf("required mode bound %+v", got.p)
	}
	f.setSettings(auth.Settings{Mode: auth.ModeDisabled})
	_, got = serve(t, f.svc, request("", ""))
	if got.p != nil {
		t.Fatalf("a live flip to disabled bound %+v", got.p)
	}
	if f.svc.Posture().Disabled {
		t.Fatal("posture changed after New")
	}
}
