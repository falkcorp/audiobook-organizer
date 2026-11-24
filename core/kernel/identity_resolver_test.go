// file: core/kernel/identity_resolver_test.go
// version: 0.1.0
// guid: 6a1e8c35-d2f7-4093-9b46-7c0e5f1a8d24
// last-edited: 2026-11-21

package kernel

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/falkcorp/mediacore/auth/oidc"
)

// Batman names only.

func gothamPolicy() oidc.Policy {
	return oidc.Policy{Provider: "gotham", AllowedDomains: []string{"wayne.example.test"}}
}

func gothamClaims(sub, email string) oidc.Claims {
	return oidc.Claims{Provider: "gotham", Subject: sub, Email: email, EmailVerified: true, Name: "Dick Grayson"}
}

// resolverBoot boots the identity switch on with an AccountResolver handle.
func resolverBoot(t *testing.T) (*oidc.LateResolver, *Kernel, *swapHost) {
	t.Helper()
	h, c, _ := probeHost(t, true)
	late := oidc.NewLateResolver()
	c.AccountResolver = late
	k := h.mustBoot(c)
	t.Cleanup(func() { h.close(k) })
	return late, k, h
}

// TestIdentityBoot_AccountResolverFailsClosedUntilBound: the app's handle
// refuses with ErrResolverUnbound until the identity boot's P5 binds it, and
// the boot is what binds it: a claim the policy allows then resolves to a
// stored user.
func TestIdentityBoot_AccountResolverFailsClosedUntilBound(t *testing.T) {
	late := oidc.NewLateResolver()
	if _, err := late.Resolve(t.Context(), gothamPolicy(), gothamClaims("sub-robin", "dick@wayne.example.test")); !errors.Is(err, oidc.ErrResolverUnbound) {
		t.Fatalf("before any boot: %v, want ErrResolverUnbound", err)
	}
	h, c, _ := probeHost(t, true)
	c.AccountResolver = late
	k := h.mustBoot(c)
	defer h.close(k)
	u, err := late.Resolve(t.Context(), gothamPolicy(), gothamClaims("sub-robin", "dick@wayne.example.test"))
	if err != nil || u == nil || u.Email != "dick@wayne.example.test" {
		t.Fatalf("after the boot: %+v, %v", u, err)
	}
	stored, err := k.ident.users.GetUser(t.Context(), u.ID)
	if err != nil || stored.Email != "dick@wayne.example.test" {
		t.Fatalf("the resolved user is not in the identity store the kernel built: %+v, %v", stored, err)
	}
}

// TestIdentityBoot_AccountResolverUsesTheAuthServiceAndItsLock: the resolver's
// Accounts is the *auth.Service the kernel built, not a second one. Two
// consequences are observable. A sign-in that creates an account waits for
// the e-mail lock every other creator holds, and the first account it creates
// is reported to the service that decides whether first-user setup is open.
func TestIdentityBoot_AccountResolverUsesTheAuthServiceAndItsLock(t *testing.T) {
	late, k, _ := resolverBoot(t)
	svc := k.ident.svc.Auth
	if svc.HasUsers() {
		t.Fatal("fixture: the identity boot already has a user, so HasUsers proves nothing")
	}

	unlock := svc.LockEmails()
	done := make(chan error, 1)
	go func() {
		_, err := late.Resolve(context.Background(), gothamPolicy(), gothamClaims("sub-tim", "tim@wayne.example.test"))
		done <- err
	}()
	select {
	case err := <-done:
		unlock()
		t.Fatalf("a sign-in that creates an account finished (%v) while the auth service's e-mail lock was held: the resolver does not share it", err)
	case <-time.After(300 * time.Millisecond):
	}
	if svc.HasUsers() {
		t.Error("an account was created while the e-mail lock was held")
	}
	unlock()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("the sign-in after the lock was released: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the sign-in never finished after the lock was released")
	}
	if !svc.HasUsers() {
		t.Error("the resolver created the first account without telling the auth service (NoteUserCreated), so first-user setup stays open")
	}
}

// TestIdentityBoot_AccountResolverUnboundOnClose: closeCore returns the handle
// to refusing before it stops anything, so a late sign-in is refused rather
// than resolved against stores that are closing; no handle, no resolver.
func TestIdentityBoot_AccountResolverUnboundOnClose(t *testing.T) {
	late, k, h := resolverBoot(t)
	if k.ident.resolver == nil {
		t.Fatal("the kernel kept no resolver")
	}
	if _, err := late.Resolve(t.Context(), gothamPolicy(), gothamClaims("sub-robin", "dick@wayne.example.test")); err != nil {
		t.Fatal(err)
	}
	h.close(k)
	if _, err := late.Resolve(t.Context(), gothamPolicy(), gothamClaims("sub-tim", "tim@wayne.example.test")); !errors.Is(err, oidc.ErrResolverUnbound) {
		t.Fatalf("after close: %v, want ErrResolverUnbound", err)
	}
}

func TestIdentityBoot_NoAccountResolverHandleBuildsNone(t *testing.T) {
	h, c, _ := probeHost(t, true)
	k := h.mustBoot(c)
	defer h.close(k)
	if k.ident.resolver != nil {
		t.Error("the kernel built an account resolver for an app that handed no handle")
	}
}

// A switch-off boot builds no identity services, so a handle stays unbound.
func TestIdentityBoot_SwitchOffLeavesTheAccountResolverUnbound(t *testing.T) {
	h, c, _ := probeHost(t, false)
	late := oidc.NewLateResolver()
	c.AccountResolver = late
	k := h.mustBoot(c)
	defer h.close(k)
	if _, err := late.Resolve(t.Context(), gothamPolicy(), gothamClaims("sub-robin", "dick@wayne.example.test")); !errors.Is(err, oidc.ErrResolverUnbound) {
		t.Fatalf("a switch-off boot bound the resolver: %v", err)
	}
}
