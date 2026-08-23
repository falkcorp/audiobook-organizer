// file: internal/plugin/register_test.go
// version: 1.0.1
// guid: 8f4a2c17-3e6b-4d5a-9c1f-2b7e4a5d6f8a
// last-edited: 2026-08-23

package plugin_test

import (
	"strings"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/plugin"
	"github.com/falkcorp/audiobook-organizer/internal/serviceregistry"
)

// registerEventBusDef re-registers the same "eventbus" ServiceDef that
// internal/plugin/register.go's init() registers at process start, so
// each test here is self-contained regardless of whether an earlier test
// in this binary already called serviceregistry.ResetForTest.
func registerEventBusDef() {
	serviceregistry.Register(serviceregistry.ServiceDef{
		Name:   serviceregistry.KeyEventBus,
		Needs:  []string{},
		Groups: []string{"core"},
		Build: func(c *serviceregistry.Container) (any, error) {
			return plugin.NewEventBus(), nil
		},
	})
}

// TestGetEventBus_ReturnsBuiltInstance exercises the "eventbus" ServiceDef
// (same shape as internal/plugin/register.go's init) end to end: Build it
// via the container's normal Needs-checked path and confirm
// plugin.GetEventBus(c) returns the same built instance.
func TestGetEventBus_ReturnsBuiltInstance(t *testing.T) {
	serviceregistry.ResetForTest()
	t.Cleanup(serviceregistry.ResetForTest)
	registerEventBusDef()

	c := serviceregistry.NewContainer().Include(serviceregistry.KeyEventBus)
	if err := c.Build(t.Context()); err != nil {
		t.Fatalf("build: %v", err)
	}
	bus := plugin.GetEventBus(c)
	if bus == nil {
		t.Fatal("GetEventBus() returned nil")
	}
	// Same instance as a direct Get[T] call under the same key. See
	// internal/serviceregistry/typed_accessor_regression_test.go for why
	// the trailing marker below is required on this exact line.
	want := serviceregistry.Get[*plugin.EventBus](c, serviceregistry.KeyEventBus) // serviceregistry-guard:allow-raw-get -- deliberate comparison, not a regression
	if bus != want {
		t.Fatalf("GetEventBus() = %p, want %p (same built instance)", bus, want)
	}
}

// TestGetEventBus_UndeclaredDepPanics — ARCH-8 acceptance criterion: a
// typed accessor for a service not in the calling builder's Needs still
// panics with the same message a bare Get[*EventBus](c, "eventbus")
// would, since GetEventBus is just Get[*EventBus] under a fixed name.
func TestGetEventBus_UndeclaredDepPanics(t *testing.T) {
	serviceregistry.ResetForTest()
	t.Cleanup(serviceregistry.ResetForTest)
	registerEventBusDef()

	// "naughty" does NOT declare serviceregistry.KeyEventBus in Needs but
	// calls plugin.GetEventBus during Build.
	serviceregistry.Register(serviceregistry.ServiceDef{
		Name: "naughty-eventbus-consumer",
		Build: func(c *serviceregistry.Container) (any, error) {
			_ = plugin.GetEventBus(c)
			return nil, nil
		},
	})

	c := serviceregistry.NewContainer().Include(serviceregistry.KeyEventBus, "naughty-eventbus-consumer")
	defer func() {
		r := recover()
		if r == nil {
			t.Fatal("expected panic for undeclared GetEventBus")
		}
		msg, ok := r.(string)
		if !ok {
			t.Fatalf("panic value = %v (%T), want string", r, r)
		}
		if !strings.Contains(msg, "not in its Needs") {
			t.Fatalf("panic message = %q, want it to mention Needs", msg)
		}
	}()
	_ = c.Build(t.Context())
}
