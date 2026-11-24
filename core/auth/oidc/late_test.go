// file: core/auth/oidc/late_test.go
// version: 0.1.0
// guid: 2d7f9a13-6c48-4be0-85a1-d0f3e8b24c76
// last-edited: 2026-11-21

package oidc

import (
	"context"
	"errors"
	"sync"
	"testing"
)

// Batman names only.

// TestLateResolver_UnboundRefusesEveryClaim: before a Resolver is bound the
// handle answers ErrResolverUnbound and no user, whatever the claim and policy
// would have admitted. It is the one error a caller can match, and it admits
// nothing, links nothing and creates nothing.
func TestLateResolver_UnboundRefusesEveryClaim(t *testing.T) {
	f := newFx(t)
	l := NewLateResolver()
	for name, pol := range map[string]Policy{"trusting": trusting(), "untrusting": untrusting()} {
		u, err := l.Resolve(context.Background(), pol, claims("sub-robin", "dick@wayne.example.test"))
		if !errors.Is(err, ErrResolverUnbound) || u != nil {
			t.Errorf("%s policy, unbound: user %+v, error %v; want nil and ErrResolverUnbound", name, u, err)
		}
	}
	if n := f.countUsers(); n != 0 {
		t.Errorf("an unbound resolution created %d users", n)
	}
}

// TestLateResolver_BindLateAdmitsLikeTheResolver: a Bind after the handle was
// already used makes the very next Resolve answer from the bound Resolver
// (the extension holds the handle from before the kernel builds the Resolver),
// and the answer is the Resolver's own.
func TestLateResolver_BindLateAdmitsLikeTheResolver(t *testing.T) {
	f := newFx(t)
	l := NewLateResolver()
	if _, err := l.Resolve(context.Background(), untrusting(), claims("sub-robin", "dick@wayne.example.test")); !errors.Is(err, ErrResolverUnbound) {
		t.Fatalf("before Bind: %v", err)
	}
	if err := l.Bind(f.resolver(nil)); err != nil {
		t.Fatal(err)
	}
	u, err := l.Resolve(context.Background(), untrusting(), claims("sub-robin", "dick@wayne.example.test"))
	if err != nil || u == nil || u.Email != "dick@wayne.example.test" {
		t.Fatalf("after Bind: %+v, %v", u, err)
	}
	// An address outside the policy is refused by the Resolver, not admitted
	// by the handle.
	if u, err := l.Resolve(context.Background(), untrusting(), claims("sub-joker", "joker@arkham.example.test")); err == nil || u != nil {
		t.Fatalf("a claim outside the allow-list resolved to %+v", u)
	}
}

// TestLateResolver_BindRules: nil reads as bound, so it is refused; a second
// Bind over a bound Resolver is refused with ErrResolverBound and leaves the
// first in place.
func TestLateResolver_BindRules(t *testing.T) {
	f := newFx(t)
	l := NewLateResolver()
	if err := l.Bind(nil); err == nil {
		t.Fatal("Bind(nil) was accepted")
	}
	if _, err := l.Resolve(context.Background(), untrusting(), claims("sub-robin", "dick@wayne.example.test")); !errors.Is(err, ErrResolverUnbound) {
		t.Fatalf("a refused Bind(nil) left the handle bound: %v", err)
	}
	first := f.resolver(nil)
	if err := l.Bind(first); err != nil {
		t.Fatal(err)
	}
	if err := l.Bind(f.resolver(nil)); !errors.Is(err, ErrResolverBound) {
		t.Fatalf("a second Bind = %v, want ErrResolverBound", err)
	}
	if l.cur.Load() != first {
		t.Fatal("a refused second Bind replaced the bound resolver")
	}
}

// TestLateResolver_UnbindOnlyByTheOwner: Unbind returns the handle to refusing,
// but only for the Resolver that is bound: a stale owner's Unbind (a previous
// boot's) leaves the current binding alone. After Unbind a new Resolver binds.
func TestLateResolver_UnbindOnlyByTheOwner(t *testing.T) {
	f := newFx(t)
	l := NewLateResolver()
	stale, current := f.resolver(nil), f.resolver(nil)
	if err := l.Bind(current); err != nil {
		t.Fatal(err)
	}
	l.Unbind(stale)
	if _, err := l.Resolve(context.Background(), untrusting(), claims("sub-robin", "dick@wayne.example.test")); err != nil {
		t.Fatalf("a stale Unbind unbound the current resolver: %v", err)
	}
	l.Unbind(current)
	if _, err := l.Resolve(context.Background(), untrusting(), claims("sub-tim", "tim@wayne.example.test")); !errors.Is(err, ErrResolverUnbound) {
		t.Fatalf("after the owner's Unbind: %v, want ErrResolverUnbound", err)
	}
	if err := l.Bind(f.resolver(nil)); err != nil {
		t.Fatalf("Bind after Unbind: %v", err)
	}
}

// TestLateResolver_ConcurrentBindAndResolve: Resolve racing Bind and Unbind
// answers either the Resolver's result or ErrResolverUnbound, never anything
// else (run under -race).
func TestLateResolver_ConcurrentBindAndResolve(t *testing.T) {
	f := newFx(t)
	l := NewLateResolver()
	r := f.resolver(nil)
	var wg sync.WaitGroup
	for i := range 16 {
		wg.Go(func() {
			if i%4 == 0 {
				_ = l.Bind(r)
				l.Unbind(r)
				return
			}
			_, err := l.Resolve(context.Background(), untrusting(), claims("sub-robin", "dick@wayne.example.test"))
			if err != nil && !errors.Is(err, ErrResolverUnbound) {
				t.Errorf("Resolve: %v", err)
			}
		})
	}
	wg.Wait()
}
