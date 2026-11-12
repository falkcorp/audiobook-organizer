// file: core/integrations/providerhttp/forget_test.go
// version: 1.1.0
// guid: ae91cda8-42d5-49c4-8dd7-b672dca0337f
// last-edited: 2026-11-11

package providerhttp_test

import (
	"context"
	"sync/atomic"
	"testing"

	"github.com/falkcorp/mediacore/integrations"
	"github.com/falkcorp/mediacore/integrations/providerhttp"
)

const day = "2039-04-01"

// seed puts a quota row for scope in gs.
func seed(t *testing.T, gs providerhttp.GateStore, scope string) {
	t.Helper()
	if err := gs.PutQuota(context.Background(), scope, "query", day, 4); err != nil {
		t.Fatal(err)
	}
}

func quotaOf(t *testing.T, gs providerhttp.GateStore, scope string) int {
	t.Helper()
	_, used, err := gs.Quota(context.Background(), scope, "query")
	if err != nil {
		t.Fatal(err)
	}
	return used
}

func holdScopes(t *testing.T, gs providerhttp.GateStore) map[string]bool {
	t.Helper()
	hs, err := gs.Holds(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]bool{}
	for _, h := range hs {
		out[h.Scope] = true
	}
	return out
}

// TestForget_DropsAnInstancesStateAndOnlyItsOwn (round-2 MINOR 3 / N13): a
// deleted instance's scope mapping goes, and when no other instance shares its
// scope so do its hold, quota rows and in-memory state; a sibling keeps all of
// it. A ref created again starts clean.
func TestForget_DropsAnInstancesStateAndOnlyItsOwn(t *testing.T) {
	ctx := context.Background()
	clk := newClock()
	gs := providerhttp.NativeGateStore(store(t))
	r := newRegistry(t, clk, func(o *providerhttp.Options) { o.Store = gs })
	const shared = "google-books:0123456789ab"
	alfred, barbara, selina := ref("alfred"), ref("barbara"), ref("selina")
	custom := providerhttp.Policy{Limits: limits(100, 100), Scope: shared}
	plain := providerhttp.Policy{Limits: limits(100, 100)}
	holdOn(t, r, alfred, custom, integrations.ClassAuth)
	r.GateFor(barbara, custom)
	holdOn(t, r, selina, plain, integrations.ClassAuth)
	seed(t, gs, shared)
	seed(t, gs, selina.String())

	if err := r.Forget(ctx, alfred); err != nil { // barbara still shares the scope
		t.Fatal(err)
	}
	if sc, _ := gs.Scopes(ctx); len(sc) != 1 || sc[barbara] != shared {
		t.Fatalf("scope records after forgetting alfred = %v, want only barbara's", sc)
	}
	if !holdScopes(t, gs)[shared] || quotaOf(t, gs, shared) != 4 {
		t.Fatal("a sibling's hold or quota was dropped with a ref that shared its scope")
	}
	if h, ok := r.HoldFor(barbara); !ok || h.Scope != shared {
		t.Fatalf("barbara lost the shared hold: %+v, %v", h, ok)
	}

	if err := r.Forget(ctx, barbara); err != nil { // the last ref on the scope
		t.Fatal(err)
	}
	if sc, _ := gs.Scopes(ctx); len(sc) != 0 {
		t.Fatalf("scope records left: %v", sc)
	}
	if holdScopes(t, gs)[shared] || quotaOf(t, gs, shared) != 0 {
		t.Fatal("the scope's hold or quota outlived its last ref")
	}

	if err := r.Forget(ctx, selina); err != nil { // default scope
		t.Fatal(err)
	}
	if len(holdScopes(t, gs)) != 0 || quotaOf(t, gs, selina.String()) != 0 {
		t.Fatalf("a default-scope instance's rows were kept: %v", holdScopes(t, gs))
	}
	if _, ok := r.HoldFor(selina); ok {
		t.Fatal("the in-memory hold outlived Forget")
	}

	// Created again: clean, and admitted.
	g := r.GateFor(selina, plain)
	tk, err := g.Acquire(ctx, "")
	if err != nil {
		t.Fatalf("a re-created instance is refused by its predecessor's hold: %v", err)
	}
	tk.Done(nil)
	if err := r.Forget(ctx, ref("nobody")); err != nil {
		t.Fatalf("forgetting an unknown ref: %v", err)
	}
}

// TestForget_SiblingNotBuiltSinceBoot: with the boot read of the store failed,
// this process knows no mappings but the store does; Forget reads them there. A
// sibling known only from the store keeps the shared scope's state; the last ref
// found only in the store takes it with it.
func TestForget_SiblingNotBuiltSinceBoot(t *testing.T) {
	ctx := context.Background()
	const shared = "google-books:feedfacecafe"
	alfred, barbara := ref("alfred"), ref("barbara")
	custom := providerhttp.Policy{Limits: limits(100, 100), Scope: shared}
	setup := func(t *testing.T) (providerhttp.GateStore, *providerhttp.Registry) {
		clk := newClock()
		gs := providerhttp.NativeGateStore(store(t))
		r1 := newRegistry(t, clk, func(o *providerhttp.Options) { o.Store = gs })
		holdOn(t, r1, alfred, custom, integrations.ClassAuth)
		r1.GateFor(barbara, custom)
		seed(t, gs, shared)
		fs := &failingStore{GateStore: gs}
		fs.failHolds.Store(true) // the restart cannot read its holds: nothing is in memory
		return gs, newRegistry(t, clk, func(o *providerhttp.Options) { o.Store = fs })
	}

	t.Run("a sibling in the store", func(t *testing.T) {
		gs, r2 := setup(t)
		if err := r2.Forget(ctx, alfred); err != nil {
			t.Fatal(err)
		}
		if !holdScopes(t, gs)[shared] || quotaOf(t, gs, shared) != 4 {
			t.Fatal("a scope shared with a ref known only from the store was dropped")
		}
		if sc, _ := gs.Scopes(ctx); len(sc) != 1 || sc[barbara] != shared {
			t.Fatalf("scope records = %v, want only barbara's", sc)
		}
	})

	t.Run("the last ref, known only from the store", func(t *testing.T) {
		gs, r2 := setup(t)
		if err := gs.PutScope(ctx, barbara, ""); err != nil { // alfred is the only ref left on it
			t.Fatal(err)
		}
		if err := r2.Forget(ctx, alfred); err != nil {
			t.Fatal(err)
		}
		if holdScopes(t, gs)[shared] || quotaOf(t, gs, shared) != 0 {
			t.Fatal("the scope of a ref known only from the store outlived Forget")
		}
	})
}

// brokenScopes fails Scopes on demand.
type brokenScopes struct {
	providerhttp.GateStore
	fail atomic.Bool
}

func (b *brokenScopes) Scopes(ctx context.Context) (map[integrations.Ref]string, error) {
	if b.fail.Load() {
		return nil, errStore
	}
	return b.GateStore.Scopes(ctx)
}

// TestForget_UnreadableStoreDropsNothing: when the siblings cannot be read,
// nothing is deleted (a shared scope must not be lost to a guess) and the error
// is returned.
func TestForget_UnreadableStoreDropsNothing(t *testing.T) {
	ctx := context.Background()
	clk := newClock()
	bs := &brokenScopes{GateStore: providerhttp.NativeGateStore(store(t))}
	r := newRegistry(t, clk, func(o *providerhttp.Options) { o.Store = bs })
	selina := ref("selina")
	holdOn(t, r, selina, providerhttp.Policy{Limits: limits(100, 100)}, integrations.ClassAuth)
	seed(t, bs, selina.String())
	bs.fail.Store(true)
	if err := r.Forget(ctx, selina); err == nil {
		t.Fatal("Forget reported success though the store could not be read")
	}
	bs.fail.Store(false)
	if !holdScopes(t, bs)[selina.String()] || quotaOf(t, bs, selina.String()) != 4 {
		t.Fatal("rows were deleted although the siblings could not be read")
	}
}
