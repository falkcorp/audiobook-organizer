// file: core/auth/ext_expiry_test.go
// version: 0.1.0
// guid: 7bf08e7e-0963-4940-971f-dc98df3bfd3e
// last-edited: 2026-11-20

package auth_test

import (
	"context"
	"testing"
	"time"

	"github.com/falkcorp/mediacore/auth"
	"github.com/falkcorp/mediacore/rbac"
)

// bindExt runs stage 12 once over a service registering an ext:oracle
// extension that asserts a, and returns the service and what it bound or
// refused.
func bindExt(t *testing.T, f *fx, a *auth.Assertion) (*auth.Service, seen) {
	t.Helper()
	svc, err := f.withExtensions(t, auth.Extension{Authenticator: oracleExt{a: a}, Order: 150,
		Spec: auth.MethodSpec{Method: "ext:oracle", Interactive: true}})
	if err != nil {
		t.Fatal(err)
	}
	_, got := serve(t, svc, request("", ""))
	return svc, got
}

// mustBindExt is bindExt that fails the test unless a principal was bound.
func mustBindExt(t *testing.T, f *fx, a *auth.Assertion) (*auth.Service, *auth.Principal) {
	t.Helper()
	svc, got := bindExt(t, f, a)
	if got.p == nil || got.p.Method != "ext:oracle" {
		t.Fatalf("stage 12 bound %+v (refusal %+v), want an ext:oracle principal", got.p, got.fail)
	}
	return svc, got.p
}

// refusedExpired checks stage 12 refused the assertion as expired.
func refusedExpired(t *testing.T, what string, got seen) {
	t.Helper()
	if got.p != nil || !got.failed || got.fail.Code != auth.CodeExpired {
		t.Fatalf("%s: bound %+v, refusal %+v; want the expired refusal", what, got.p, got.fail)
	}
}

// expiredBoth checks Revalidate and Refresh both refuse p as expired.
func expiredBoth(t *testing.T, what string, svc *auth.Service, p *auth.Principal) {
	t.Helper()
	ctx := context.Background()
	wantErr(t, what+": Revalidate", svc.Revalidate(ctx, p), auth.ErrExpired)
	_, err := svc.Refresh(ctx, p)
	wantErr(t, what+": Refresh", err, auth.ErrExpired)
}

// TestExtensionExpiry_AssertedExpBoundsThePrincipal: an extension's asserted
// ExpiresAt (cfaccess: the JWT's exp) ends its principal. One already past is
// refused at stage 12; one in the future binds, stands and refreshes until
// the bound, and at it (not only after) Revalidate and Refresh refuse it with
// ErrExpired, including the principal an earlier Refresh returned, so a
// refresh never extends it.
func TestExtensionExpiry_AssertedExpBoundsThePrincipal(t *testing.T) {
	f := newFx(t)
	ctx := context.Background()
	barbara := f.user("barbara-gordon", rbac.RoleViewer)
	now := f.clock()

	_, got := bindExt(t, f, &auth.Assertion{User: barbara.ID, CredentialID: "cf-stale", AuthTime: now.Add(-2 * time.Hour), ExpiresAt: now.Add(-time.Second)})
	refusedExpired(t, "an assertion whose exp has passed", got)
	_, got = bindExt(t, f, &auth.Assertion{User: barbara.ID, CredentialID: "cf-edge", ExpiresAt: now})
	refusedExpired(t, "an assertion expiring this instant", got)

	// An asserted exp beyond the cap is the extension's own answer: it wins.
	svc, p := mustBindExt(t, f, &auth.Assertion{User: barbara.ID, CredentialID: "cf-belfry", AuthTime: now, ExpiresAt: now.Add(30 * time.Hour)})
	if err := svc.Revalidate(ctx, p); err != nil {
		t.Fatalf("Revalidate of a fresh extension principal: %v", err)
	}
	f.advance(25 * time.Hour) // past the 24 h default cap: exp, not the cap, decides
	np := refreshed(t, svc, p)
	if err := svc.Revalidate(ctx, np); err != nil {
		t.Fatalf("Revalidate of the refreshed principal before its exp: %v", err)
	}
	f.advance(5*time.Hour - time.Second)
	refreshed(t, svc, p)
	f.advance(time.Second) // exactly at exp
	expiredBoth(t, "the principal at its exp", svc, p)
	expiredBoth(t, "the principal Refresh returned earlier, at the original exp", svc, np)
}

// TestExtensionExpiry_NoExpTakesTheCap: an extension that asserts no expiry
// is bounded by core.auth.extension_max_age after its AuthTime, read live and
// capped at MaxExtensionMaxAge; an AuthTime from the future counts as now. A
// Refresh carries the bound and never restarts it from the refresh time.
func TestExtensionExpiry_NoExpTakesTheCap(t *testing.T) {
	f := newFx(t)
	ctx := context.Background()
	dick := f.user("dick-grayson", rbac.RoleViewer)

	// A configured two hours, signed in an hour ago: one hour left.
	f.setSettings(auth.Settings{ExtensionMaxAge: 2 * time.Hour})
	_, got := bindExt(t, f, &auth.Assertion{User: dick.ID, CredentialID: "cf-old", AuthTime: f.clock().Add(-2 * time.Hour)})
	refusedExpired(t, "an assertion signed in a configured cap ago", got)
	svc, p := mustBindExt(t, f, &auth.Assertion{User: dick.ID, CredentialID: "cf-perch", AuthTime: f.clock().Add(-time.Hour)})
	f.advance(30 * time.Minute)
	np := refreshed(t, svc, p)
	if err := svc.Revalidate(ctx, np); err != nil {
		t.Fatalf("Revalidate of the refreshed principal inside the cap: %v", err)
	}
	f.advance(30 * time.Minute) // AuthTime + 2 h
	expiredBoth(t, "a no-exp principal at its cap", svc, p)
	expiredBoth(t, "the refreshed no-exp principal at the original cap", svc, np)

	// The default, 24 h.
	f.setSettings(auth.Settings{})
	svc, p = mustBindExt(t, f, &auth.Assertion{User: dick.ID, CredentialID: "cf-default", AuthTime: f.clock().Add(-23 * time.Hour)})
	f.advance(time.Hour - time.Second)
	refreshed(t, svc, p)
	f.advance(time.Second)
	expiredBoth(t, "a no-exp principal at the default cap", svc, p)

	// A configured value above the ceiling is capped at it.
	f.setSettings(auth.Settings{ExtensionMaxAge: 1000 * time.Hour})
	_, got = bindExt(t, f, &auth.Assertion{User: dick.ID, CredentialID: "cf-week", AuthTime: f.clock().Add(-auth.MaxExtensionMaxAge)})
	refusedExpired(t, "an assertion signed in the ceiling ago under an over-ceiling setting", got)
	mustBindExt(t, f, &auth.Assertion{User: dick.ID, CredentialID: "cf-week", AuthTime: f.clock().Add(-auth.MaxExtensionMaxAge + time.Minute)})

	// No AuthTime, or one from the future: the cap runs from now.
	f.setSettings(auth.Settings{ExtensionMaxAge: time.Hour})
	svc, none := mustBindExt(t, f, &auth.Assertion{User: dick.ID, CredentialID: "cf-none"})
	asvc, ahead := mustBindExt(t, f, &auth.Assertion{User: dick.ID, CredentialID: "cf-ahead", AuthTime: f.clock().Add(100 * time.Hour)})
	f.advance(time.Hour)
	expiredBoth(t, "a no-exp principal with no AuthTime, a cap later", svc, none)
	expiredBoth(t, "a no-exp principal with a future AuthTime, a cap later", asvc, ahead)
}

// TestExtensionExpiry_PrincipalForWithoutABoundIsRefused: Service.PrincipalFor
// is exported and takes any registered ext: method, but only stage 12 sets an
// extension principal's bound. One built through PrincipalFor has none, and a
// principal with no end would hold a stream open past its credential, so
// Revalidate and Refresh both refuse it as expired, narrowed or not.
func TestExtensionExpiry_PrincipalForWithoutABoundIsRefused(t *testing.T) {
	f := newFx(t)
	ctx := context.Background()
	oliver := f.user("lucius-fox", rbac.RoleViewer)
	svc, err := f.withExtensions(t, auth.Extension{Authenticator: oracleExt{a: &auth.Assertion{User: oliver.ID}}, Order: 150,
		Spec: auth.MethodSpec{Method: "ext:oracle", Interactive: true}})
	if err != nil {
		t.Fatal(err)
	}
	for name, scopes := range map[string][]rbac.Permission{
		"the method's own rule": nil,
		"a narrowing":           {"gotham:patrol"},
	} {
		p, err := svc.PrincipalFor(ctx, oliver, "ext:oracle", scopes, oliver.CredentialEpoch)
		if err != nil {
			t.Fatalf("PrincipalFor ext:oracle over %s: %v", name, err)
		}
		expiredBoth(t, "an ext:oracle principal PrincipalFor built over "+name, svc, p)
	}
}

// TestExtensionExpiry_NegativeMaxAgeTakesTheDefault: a configured
// extension_max_age at or below zero is the default (24 h), not a bound that
// is already past (which would refuse every extension sign-in) nor none.
func TestExtensionExpiry_NegativeMaxAgeTakesTheDefault(t *testing.T) {
	f := newFx(t)
	jason := f.user("jason-todd", rbac.RoleViewer)

	f.setSettings(auth.Settings{ExtensionMaxAge: -time.Hour})
	svc, p := mustBindExt(t, f, &auth.Assertion{User: jason.ID, CredentialID: "cf-alley", AuthTime: f.clock().Add(-23 * time.Hour)})
	f.advance(time.Hour - time.Second)
	refreshed(t, svc, p)
	f.advance(time.Second) // AuthTime + 24 h
	expiredBoth(t, "a no-exp principal under a negative setting, at the default cap", svc, p)
}
