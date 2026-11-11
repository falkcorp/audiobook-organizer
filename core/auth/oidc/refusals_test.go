// file: core/auth/oidc/refusals_test.go
// version: 1.0.0
// guid: aa0b397a-03fa-4c88-a319-edbc891c1b23
// last-edited: 2026-11-11

package oidc

import (
	"context"
	"errors"
	"iter"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/falkcorp/mediacore/auth"
	"github.com/falkcorp/mediacore/rbac"
)

// countingStore counts the full user scans a resolver makes.
type countingStore struct {
	auth.Store
	scans atomic.Int64
}

func (c *countingStore) ScanUsers(ctx context.Context) iter.Seq2[*auth.User, error] {
	c.scans.Add(1)
	return c.Store.ScanUsers(ctx)
}

func (f *fx) countingResolver(extra func(context.Context, string) (bool, error)) (*Resolver, *countingStore) {
	f.t.Helper()
	cs := &countingStore{Store: f.st}
	r, err := NewResolver(ResolverDeps{
		Store: cs, NewBatch: f.st.NewBatch, Audit: f.audit, Roles: f.rb, Accounts: f.svc,
		ExtraAllowed: extra, Clock: f.clock,
	})
	if err != nil {
		f.t.Fatal(err)
	}
	return r, cs
}

// An admitted identity that is refused (the owner's address, held by an admin
// with a password) is decided once: 25 requests cost one scan and one audit
// event, and the entry lapses after the TTL.
func TestOIDC_RefusedIdentityCostsOneScanAndOneAuditPerTTL(t *testing.T) {
	f := newFx(t)
	f.withPassword(&auth.User{ID: "01JBATCAVE0000000000000004", Username: "bruce", Email: "bruce@wayne.example.test", EmailVerified: true, Roles: []rbac.RoleName{rbac.RoleAdmin}})
	r, cs := f.countingResolver(nil)
	c := claims("sub-owner", "bruce@wayne.example.test")
	var first error
	for i := range 25 {
		_, err := r.Resolve(context.Background(), trusting(), c)
		mustIs(t, err, auth.ErrEmailInUse)
		if i == 0 {
			first = err
		} else if err != first {
			t.Fatalf("request %d: a remembered refusal must be the same refusal: %v vs %v", i, err, first)
		}
	}
	if n := cs.scans.Load(); n != 1 {
		t.Fatalf("scans = %d for 25 refused requests, want 1", n)
	}
	if n := f.audit.emittedWith("core.identity.link", "email_in_use"); n != 1 {
		t.Fatalf("denied audit events = %d for 25 refused requests, want 1", n)
	}
	f.advance(RefusalTTL - time.Second)
	if _, err := r.Resolve(context.Background(), trusting(), c); !errors.Is(err, auth.ErrEmailInUse) || cs.scans.Load() != 1 {
		t.Fatalf("inside the TTL: %v, scans %d", err, cs.scans.Load())
	}
	f.advance(2 * time.Second)
	if _, err := r.Resolve(context.Background(), trusting(), c); !errors.Is(err, auth.ErrEmailInUse) {
		t.Fatal(err)
	}
	if cs.scans.Load() != 2 || f.audit.emittedWith("core.identity.link", "email_in_use") != 2 {
		t.Fatalf("after the TTL: scans %d, audit %d, want 2 and 2", cs.scans.Load(), f.audit.emittedWith("core.identity.link", "email_in_use"))
	}
}

// Requests that arrive together queue on the e-mail lock; the first decides and
// the others take its refusal instead of scanning again.
func TestOIDC_ConcurrentRefusedRequestsShareOneDecision(t *testing.T) {
	f := newFx(t)
	f.withPassword(&auth.User{ID: "01JBATCAVE0000000000000004", Username: "bruce", Email: "bruce@wayne.example.test", EmailVerified: true, Roles: []rbac.RoleName{rbac.RoleAdmin}})
	r, cs := f.countingResolver(nil)
	var wg sync.WaitGroup
	for range 25 {
		wg.Go(func() {
			if _, err := r.Resolve(context.Background(), trusting(), claims("sub-owner", "bruce@wayne.example.test")); !errors.Is(err, auth.ErrEmailInUse) {
				t.Errorf("err = %v", err)
			}
		})
	}
	wg.Wait()
	if n := cs.scans.Load(); n != 1 {
		t.Fatalf("scans = %d for 25 concurrent refused requests, want 1", n)
	}
	if n := f.audit.emittedWith("core.identity.link", "email_in_use"); n != 1 {
		t.Fatalf("audit events = %d, want 1", n)
	}
}

// A refusal only ever delays: when the cause is fixed (the holder loses its
// admin role) the sign-in is still refused until the entry lapses and then
// succeeds. The cache never turns a refusal into an admission early, and an
// admission is never cached at all.
func TestOIDC_RefusalLapsesAfterTheCauseIsFixed(t *testing.T) {
	f := newFx(t)
	holder := f.put(&auth.User{ID: "01JBATCAVE0000000000000004", Username: "bruce", Email: "bruce@wayne.example.test", EmailVerified: true, Roles: []rbac.RoleName{rbac.RoleAdmin}})
	r, _ := f.countingResolver(nil)
	c := claims("sub-owner", "bruce@wayne.example.test")
	if _, err := r.Resolve(context.Background(), trusting(), c); !errors.Is(err, auth.ErrEmailInUse) {
		t.Fatal(err)
	}
	// The holder is demoted: linkable now, but the remembered refusal stands.
	demoted := *holder
	demoted.Roles, demoted.Version = []rbac.RoleName{rbac.RoleViewer}, holder.Version+1
	f.put(&demoted)
	if _, err := r.Resolve(context.Background(), trusting(), c); !errors.Is(err, auth.ErrEmailInUse) {
		t.Fatalf("inside the TTL the refusal must stand: %v", err)
	}
	f.advance(RefusalTTL + time.Second)
	u, err := r.Resolve(context.Background(), trusting(), c)
	if err != nil {
		t.Fatalf("after the TTL the sign-in must link: %v", err)
	}
	if u.ID != holder.ID {
		t.Fatalf("linked %s, want the holder %s", u.ID, holder.ID)
	}
}

// Refusals are per (provider, subject, address): another subject, or the same
// subject under another address, is its own decision, and a success is never
// remembered.
func TestOIDC_RefusalIsPerSubjectAndAddress(t *testing.T) {
	f := newFx(t)
	f.withPassword(&auth.User{ID: "01JBATCAVE0000000000000004", Username: "bruce", Email: "bruce@wayne.example.test", EmailVerified: true, Roles: []rbac.RoleName{rbac.RoleAdmin}})
	r, cs := f.countingResolver(nil)
	for range 3 {
		_, err := r.Resolve(context.Background(), trusting(), claims("sub-a", "bruce@wayne.example.test"))
		mustIs(t, err, auth.ErrEmailInUse)
	}
	if cs.scans.Load() != 1 {
		t.Fatalf("scans = %d", cs.scans.Load())
	}
	_, err := r.Resolve(context.Background(), trusting(), claims("sub-b", "bruce@wayne.example.test"))
	mustIs(t, err, auth.ErrEmailInUse)
	if cs.scans.Load() != 2 {
		t.Fatalf("another subject shared a refusal: scans = %d", cs.scans.Load())
	}
	// A success is not remembered: an unused address creates, then resolves by link.
	for range 3 {
		if _, err := r.Resolve(context.Background(), trusting(), claims("sub-c", "jason@wayne.example.test")); err != nil {
			t.Fatal(err)
		}
	}
	if len(r.refused.m) != 2 {
		t.Fatalf("remembered %d refusals, want 2 (a success must not be remembered)", len(r.refused.m))
	}
}

// A refusal for "not admitted" is remembered too: the allowlist hook is not
// asked again for the TTL.
func TestOIDC_NotAdmittedIsRememberedToo(t *testing.T) {
	f := newFx(t)
	var asked atomic.Int64
	r, _ := f.countingResolver(func(context.Context, string) (bool, error) { asked.Add(1); return false, nil })
	pol := Policy{Provider: "gotham"}
	for range 25 {
		_, err := r.Resolve(context.Background(), pol, claims("sub-x", "joker@arkham.example.test"))
		mustIs(t, err, ErrEmailNotAllowed)
	}
	if asked.Load() != 1 {
		t.Fatalf("the admission hook ran %d times, want 1", asked.Load())
	}
	f.advance(RefusalTTL + time.Second)
	_, _ = r.Resolve(context.Background(), pol, claims("sub-x", "joker@arkham.example.test"))
	if asked.Load() != 2 {
		t.Fatalf("after the TTL the hook ran %d times, want 2", asked.Load())
	}
	// An admission failure that is not a refusal (the hook errors) is not remembered.
	r2, _ := f.countingResolver(func(context.Context, string) (bool, error) {
		asked.Add(1)
		return false, errors.New("invite store down")
	})
	before := asked.Load()
	for range 3 {
		_, err := r2.Resolve(context.Background(), pol, claims("sub-y", "joker@arkham.example.test"))
		mustIs(t, err, auth.ErrStoreUnavailable)
	}
	if asked.Load()-before != 3 {
		t.Fatalf("a store failure was remembered as a refusal (%d hook calls for 3 requests)", asked.Load()-before)
	}
}

func TestRefusalsAreBoundedAndExpire(t *testing.T) {
	now := time.Date(2026, 11, 11, 9, 0, 0, 0, time.UTC)
	c := newRefusals()
	c.max = 3
	e := errors.New("refused")
	c.put("a", e, now)
	c.put("b", e, now.Add(time.Second))
	c.put("c", e, now.Add(2*time.Second))
	c.put("d", e, now.Add(3*time.Second)) // full of live entries: the soonest-expiring goes
	if len(c.m) != 3 || c.get("a", now.Add(3*time.Second)) != nil || c.get("d", now.Add(3*time.Second)) == nil {
		t.Fatalf("set = %v", c.m)
	}
	// Past the TTL every entry is gone and is dropped on read.
	if c.get("d", now.Add(3*time.Second+RefusalTTL)) != nil || len(c.m) != 2 {
		t.Fatalf("expired entry served or kept: %v", c.m)
	}
	// Expired entries are evicted before live ones when the set is full.
	c2 := newRefusals()
	c2.max = 2
	c2.put("old", e, now)
	c2.put("live", e, now.Add(RefusalTTL))
	c2.put("new", e, now.Add(RefusalTTL+time.Second))
	if _, ok := c2.m["live"]; !ok || len(c2.m) != 2 {
		t.Fatalf("a live entry was evicted before an expired one: %v", c2.m)
	}
	// Every expired entry goes at once when the set is full, not just one.
	c3 := newRefusals()
	c3.max = 3
	c3.put("e1", e, now)
	c3.put("e2", e, now.Add(time.Second))
	c3.put("live", e, now.Add(RefusalTTL))
	c3.put("new", e, now.Add(RefusalTTL+2*time.Second))
	if len(c3.m) != 2 {
		t.Fatalf("expired entries were not swept before a live one was evicted: %v", c3.m)
	}
	// An overwrite of a present key at the bound does not evict.
	c2.put("live", e, now.Add(RefusalTTL+2*time.Second))
	if len(c2.m) != 2 {
		t.Fatalf("overwrite changed the size: %v", c2.m)
	}
}
