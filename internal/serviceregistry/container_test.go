// file: internal/serviceregistry/container_test.go
// version: 1.2.0
// last-edited: 2026-11-08

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
	order := []string{}
	defs := []ServiceDef{
		{Name: "leaf", Build: func(c *Container) (any, error) {
			order = append(order, "leaf")
			return &fakeService{name: "leaf"}, nil
		}},
		{Name: "top", Needs: []string{"leaf"}, Build: func(c *Container) (any, error) {
			_ = Get[*fakeService](c, "leaf") // verify Get works mid-Build
			order = append(order, "top")
			return &fakeService{name: "top"}, nil
		}},
	}

	c := NewContainerFrom(defs...)
	if err := c.Build(t.Context()); err != nil {
		t.Fatalf("build: %v", err)
	}
	if len(order) != 2 || order[0] != "leaf" || order[1] != "top" {
		t.Fatalf("build order = %v, want [leaf top]", order)
	}
}

func TestContainer_Get_UndeclaredDepPanics(t *testing.T) {
	defs := []ServiceDef{
		{Name: "leaf", Build: func(c *Container) (any, error) {
			return "leaf-instance", nil
		}},
		// "naughty" does NOT declare "leaf" in Needs but tries to Get it.
		{Name: "naughty", Build: func(c *Container) (any, error) {
			_ = Get[string](c, "leaf") // should panic
			return nil, nil
		}},
	}

	c := NewContainerFrom(defs...)
	defer func() {
		r := recover()
		if r == nil {
			t.Fatal("expected panic for undeclared Get")
		}
	}()
	_ = c.Build(t.Context())
}

func TestContainer_PostInit_RunsAfterAllBuilds(t *testing.T) {
	defs := []ServiceDef{
		{Name: "a", Build: func(c *Container) (any, error) { return &fakeService{name: "a"}, nil }},
		{Name: "b", Needs: []string{"a"}, Build: func(c *Container) (any, error) { return &fakeService{name: "b"}, nil }},
	}

	c := NewContainerFrom(defs...)
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
	stopOrder := []string{}
	defs := []ServiceDef{
		{Name: "a", Build: func(c *Container) (any, error) {
			return &recordingService{name: "a", sink: &stopOrder}, nil
		}},
		{Name: "b", Needs: []string{"a"}, Build: func(c *Container) (any, error) {
			return &recordingService{name: "b", sink: &stopOrder}, nil
		}},
		{Name: "c", Needs: []string{"b"}, Build: func(c *Container) (any, error) {
			return &recordingService{name: "c", sink: &stopOrder}, nil
		}},
	}

	c := NewContainerFrom(defs...)
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
// both import serviceregistry (to declare their own ServiceDefs), so
// serviceregistry importing them back — even from a test file — would be
// an import cycle. This uses an in-package stand-in of the same shape
// instead.
func TestTypedAccessor_UndeclaredDepStillPanics(t *testing.T) {
	defs := []ServiceDef{
		{Name: "widget", Build: func(c *Container) (any, error) {
			return "widget-instance", nil
		}},
		// "naughty" does NOT declare "widget" in Needs but calls the typed
		// accessor for it.
		{Name: "naughty", Build: func(c *Container) (any, error) {
			_ = getWidget(c) // should panic, same as a bare Get[string](c, "widget")
			return nil, nil
		}},
	}

	c := NewContainerFrom(defs...)
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

// TestContainer_StepExtender_AsksBeforeEveryServiceStep: with an extender, the
// container asks for start time right before each service's Build, PostInit
// and Start, in resolved order, and the request comes before the step's own
// work (05-health §5H.3.4). A service added to the container is covered
// without its own code asking.
func TestContainer_StepExtender_AsksBeforeEveryServiceStep(t *testing.T) {
	var log []string
	svc := func(name string) ServiceDef {
		return ServiceDef{Name: name, Build: func(*Container) (any, error) {
			log = append(log, "work build "+name)
			return &stepService{name: name, log: &log}, nil
		}}
	}
	leaf, top := svc("leaf"), svc("top")
	top.Needs = []string{"leaf"}
	c := NewContainerFrom(top, leaf).WithStepExtender(func(step string) { log = append(log, "ask "+step) })
	ctx := context.Background()
	for _, run := range []func(context.Context) error{c.Build, c.PostInit, c.Start} {
		if err := run(ctx); err != nil {
			t.Fatal(err)
		}
	}
	want := []string{
		"ask service build leaf", "work build leaf", "ask service build top", "work build top",
		"ask service postinit leaf", "work postinit leaf", "ask service postinit top", "work postinit top",
		"ask service start leaf", "work start leaf", "ask service start top", "work start top",
	}
	if strings.Join(log, "|") != strings.Join(want, "|") {
		t.Fatalf("steps:\n got %q\nwant %q", log, want)
	}
}

// stepService records its PostInit and Start work in the shared log.
type stepService struct {
	name string
	log  *[]string
}

func (s *stepService) PostInit(context.Context, *Container) error {
	*s.log = append(*s.log, "work postinit "+s.name)
	return nil
}
func (s *stepService) Start(context.Context) error {
	*s.log = append(*s.log, "work start "+s.name)
	return nil
}

// A container with no extender runs exactly as before.
func TestContainer_NoStepExtender_RunsUnchanged(t *testing.T) {
	c := NewContainerFrom(ServiceDef{Name: "leaf", Build: func(*Container) (any, error) { return &fakeService{name: "leaf"}, nil }})
	ctx := context.Background()
	for _, run := range []func(context.Context) error{c.Build, c.PostInit, c.Start} {
		if err := run(ctx); err != nil {
			t.Fatal(err)
		}
	}
}
