// file: internal/serviceregistry/container_test.go
// version: 1.1.0
// last-edited: 2026-08-23

package serviceregistry

import (
	"context"
	"strings"
	"testing"
)

type fakeService struct {
	name     string
	postInit bool
	started  bool
	stopped  bool
}

func (f *fakeService) PostInit(ctx context.Context, c *Container) error {
	f.postInit = true
	return nil
}
func (f *fakeService) Start(ctx context.Context) error { f.started = true; return nil }
func (f *fakeService) Stop(ctx context.Context) error  { f.stopped = true; return nil }

func TestContainer_Build_RunsInDepOrder(t *testing.T) {
	ResetForTest()
	t.Cleanup(ResetForTest)

	order := []string{}
	Register(ServiceDef{Name: "leaf", Build: func(c *Container) (any, error) {
		order = append(order, "leaf")
		return &fakeService{name: "leaf"}, nil
	}})
	Register(ServiceDef{Name: "top", Needs: []string{"leaf"}, Build: func(c *Container) (any, error) {
		_ = Get[*fakeService](c, "leaf") // verify Get works mid-Build
		order = append(order, "top")
		return &fakeService{name: "top"}, nil
	}})

	c := NewContainer().Include("top")
	if err := c.Build(t.Context()); err != nil {
		t.Fatalf("build: %v", err)
	}
	if len(order) != 2 || order[0] != "leaf" || order[1] != "top" {
		t.Fatalf("build order = %v, want [leaf top]", order)
	}
}

func TestContainer_Get_UndeclaredDepPanics(t *testing.T) {
	ResetForTest()
	t.Cleanup(ResetForTest)

	Register(ServiceDef{Name: "leaf", Build: func(c *Container) (any, error) {
		return "leaf-instance", nil
	}})
	// "naughty" does NOT declare "leaf" in Needs but tries to Get it.
	Register(ServiceDef{Name: "naughty", Build: func(c *Container) (any, error) {
		_ = Get[string](c, "leaf") // should panic
		return nil, nil
	}})

	c := NewContainer().Include("leaf", "naughty")
	defer func() {
		r := recover()
		if r == nil {
			t.Fatal("expected panic for undeclared Get")
		}
	}()
	_ = c.Build(t.Context())
}

func TestContainer_PostInit_RunsAfterAllBuilds(t *testing.T) {
	ResetForTest()
	t.Cleanup(ResetForTest)

	Register(ServiceDef{Name: "a", Build: func(c *Container) (any, error) { return &fakeService{name: "a"}, nil }})
	Register(ServiceDef{Name: "b", Needs: []string{"a"}, Build: func(c *Container) (any, error) { return &fakeService{name: "b"}, nil }})

	c := NewContainer().Include("a", "b")
	if err := c.Build(t.Context()); err != nil {
		t.Fatalf("build: %v", err)
	}
	if err := c.PostInit(t.Context()); err != nil {
		t.Fatalf("postinit: %v", err)
	}

	a := Get[*fakeService](c, "a")
	b := Get[*fakeService](c, "b")
	if !a.postInit || !b.postInit {
		t.Fatalf("postinit not called: a=%v b=%v", a.postInit, b.postInit)
	}
}

func TestContainer_Stop_ReverseOrder(t *testing.T) {
	ResetForTest()
	t.Cleanup(ResetForTest)

	stopOrder := []string{}
	Register(ServiceDef{Name: "a", Build: func(c *Container) (any, error) {
		return &recordingService{name: "a", sink: &stopOrder}, nil
	}})
	Register(ServiceDef{Name: "b", Needs: []string{"a"}, Build: func(c *Container) (any, error) {
		return &recordingService{name: "b", sink: &stopOrder}, nil
	}})
	Register(ServiceDef{Name: "c", Needs: []string{"b"}, Build: func(c *Container) (any, error) {
		return &recordingService{name: "c", sink: &stopOrder}, nil
	}})

	c := NewContainer().Include("c")
	if err := c.Build(t.Context()); err != nil {
		t.Fatalf("build: %v", err)
	}
	if err := c.PostInit(t.Context()); err != nil {
		t.Fatalf("postinit: %v", err)
	}
	if err := c.Start(t.Context()); err != nil {
		t.Fatalf("start: %v", err)
	}
	if err := c.Stop(t.Context()); err != nil {
		t.Fatalf("stop: %v", err)
	}

	want := []string{"c", "b", "a"}
	if len(stopOrder) != 3 {
		t.Fatalf("stop order = %v, want %v", stopOrder, want)
	}
	for i := range want {
		if stopOrder[i] != want[i] {
			t.Errorf("stopOrder[%d] = %q, want %q", i, stopOrder[i], want[i])
		}
	}
}

type recordingService struct {
	name string
	sink *[]string
}

func (r *recordingService) Start(ctx context.Context) error { return nil }
func (r *recordingService) Stop(ctx context.Context) error {
	*r.sink = append(*r.sink, r.name)
	return nil
}

// TestTypedAccessor_UndeclaredDepStillPanics pins the invariant the real
// typed accessors (config.GetConfig, plugin.GetEventBus — ARCH-8) depend
// on: a named wrapper function that just calls Get[T](c, name) internally
// preserves Get's panic-on-undeclared-Needs behavior byte for byte,
// because it's still the same call, just spelled once instead of at every
// call site.
//
// The real accessors can't be exercised from this file: config and plugin
// both import serviceregistry (to Register their own ServiceDefs), so
// serviceregistry importing them back — even from a test file — would be
// an import cycle. This uses an in-package stand-in of the same shape
// instead.
func TestTypedAccessor_UndeclaredDepStillPanics(t *testing.T) {
	ResetForTest()
	t.Cleanup(ResetForTest)

	Register(ServiceDef{Name: "widget", Build: func(c *Container) (any, error) {
		return "widget-instance", nil
	}})
	// "naughty" does NOT declare "widget" in Needs but calls the typed
	// accessor for it.
	Register(ServiceDef{Name: "naughty", Build: func(c *Container) (any, error) {
		_ = getWidget(c) // should panic, same as a bare Get[string](c, "widget")
		return nil, nil
	}})

	c := NewContainer().Include("widget", "naughty")
	defer func() {
		r := recover()
		if r == nil {
			t.Fatal("expected panic for undeclared Get via typed accessor")
		}
		msg, ok := r.(string)
		if !ok {
			t.Fatalf("panic value = %v (%T), want string", r, r)
		}
		if !strings.Contains(msg, "not in its Needs") {
			t.Fatalf("panic message = %q, want it to mention Needs (same message as a bare Get[T] panic)", msg)
		}
	}()
	_ = c.Build(t.Context())
}

// getWidget stands in for a real owning-package typed accessor like
// config.GetConfig or plugin.GetEventBus: a named function with a fixed
// key and fixed return type that wraps Get[T].
func getWidget(c *Container) string {
	return Get[string](c, "widget")
}
