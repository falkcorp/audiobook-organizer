// file: internal/serviceregistry/graph_test.go
// version: 1.1.0
// last-edited: 2026-11-08

package serviceregistry

import (
	"errors"
	"testing"
)

func TestResolve_LexStableOrder(t *testing.T) {
	var defs []ServiceDef

	// Four defs with no Needs, given in no particular order. Resolve orders them lexically.
	for _, name := range []string{"zebra", "alpha", "mike", "delta"} {
		n := name
		defs = append(defs, ServiceDef{
			Name:  n,
			Build: func(c *Container) (any, error) { return n, nil },
		})
	}

	c := NewContainerFrom(defs...)
	if err := c.Resolve(); err != nil {
		t.Fatalf("resolve: %v", err)
	}
	got := c.Names()
	want := []string{"alpha", "delta", "mike", "zebra"}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range got {
		if got[i] != want[i] {
			t.Errorf("order[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

func TestResolve_CycleDetected(t *testing.T) {
	defs := []ServiceDef{
		{Name: "a", Needs: []string{"b"}, Build: func(c *Container) (any, error) { return nil, nil }},
		{Name: "b", Needs: []string{"a"}, Build: func(c *Container) (any, error) { return nil, nil }},
	}

	c := NewContainerFrom(defs...)
	err := c.Resolve()
	if !errors.Is(err, ErrCycle) {
		t.Fatalf("expected ErrCycle, got %v", err)
	}
}

func TestResolve_UnknownDep(t *testing.T) {
	defs := []ServiceDef{
		{Name: "a", Needs: []string{"nonexistent"}, Build: func(c *Container) (any, error) { return nil, nil }},
	}

	c := NewContainerFrom(defs...)
	err := c.Resolve()
	if !errors.Is(err, ErrUnknownService) {
		t.Fatalf("expected ErrUnknownService, got %v", err)
	}
}

// Every def NewContainerFrom is given is in the build set, so the closure of
// Needs is no longer what pulls a service in. The order still follows it.
func TestResolve_NeedsChainOrder(t *testing.T) {
	defs := []ServiceDef{
		{Name: "leaf", Build: func(c *Container) (any, error) { return "leaf", nil }},
		{Name: "mid", Needs: []string{"leaf"}, Build: func(c *Container) (any, error) { return "mid", nil }},
		{Name: "top", Needs: []string{"mid"}, Build: func(c *Container) (any, error) { return "top", nil }},
	}

	c := NewContainerFrom(defs...)
	if err := c.Resolve(); err != nil {
		t.Fatalf("resolve: %v", err)
	}
	got := c.Names()
	want := []string{"leaf", "mid", "top"}
	if len(got) != 3 || got[0] != want[0] || got[1] != want[1] || got[2] != want[2] {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestResolve_OverrideIsLeaf(t *testing.T) {
	defs := []ServiceDef{
		// "real" needs "store"; "store" has no def, only an override.
		{Name: "real", Needs: []string{"store"}, Build: func(c *Container) (any, error) {
			return Get[string](c, "store"), nil
		}},
	}

	c := NewContainerFrom(defs...).Override("store", "mock-store")
	if err := c.Resolve(); err != nil {
		t.Fatalf("resolve: %v", err)
	}
	got := c.Names()
	// store appears first (no deps), then real.
	if len(got) != 2 || got[0] != "store" || got[1] != "real" {
		t.Fatalf("got %v, want [store real]", got)
	}
}
