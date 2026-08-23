// file: internal/config/register_test.go
// version: 1.1.0
// last-edited: 2026-08-23

package config_test

import (
	"strings"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/database/mocks"
	"github.com/falkcorp/audiobook-organizer/internal/serviceregistry"
)

func TestConfigUpdateRegistration(t *testing.T) {
	c := serviceregistry.NewContainer().
		Override("store", mocks.NewMockStore(t)).
		Include("configupdate")
	if err := c.Build(t.Context()); err != nil {
		t.Fatalf("build: %v", err)
	}
	svc := serviceregistry.Get[*config.UpdateService](c, "configupdate")
	if svc == nil {
		t.Fatal("UpdateService is nil")
	}
}

// TestGetConfig_ReturnsOverriddenConfig mirrors production wiring
// (internal/server/server.go Override("config", &config.AppConfig)):
// config.GetConfig(c) must return exactly the *Config instance the
// container was given, with no Build/Needs involvement, since config is
// Override'd rather than built by a factory.
func TestGetConfig_ReturnsOverriddenConfig(t *testing.T) {
	want := &config.Config{RootDir: "/rootdir-marker"}
	c := serviceregistry.NewContainer().Override(serviceregistry.KeyConfig, want)
	if err := c.Build(t.Context()); err != nil {
		t.Fatalf("build: %v", err)
	}
	got := config.GetConfig(c)
	if got != want {
		t.Fatalf("GetConfig() = %p, want %p (same instance)", got, want)
	}
	if got.RootDir != "/rootdir-marker" {
		t.Fatalf("GetConfig().RootDir = %q, want %q", got.RootDir, "/rootdir-marker")
	}
}

// TestGetConfig_UndeclaredDepPanics: ARCH-8 acceptance criterion — a typed
// accessor for a service not in the calling builder's Needs still panics
// with the same message a bare Get[*Config](c, "config") would, since
// GetConfig is just Get[*Config] under a fixed name. This is a
// compile-time-safety improvement (the key string and T can no longer be
// mistyped independently at the call site), not a change in runtime
// behavior.
func TestGetConfig_UndeclaredDepPanics(t *testing.T) {
	// No serviceregistry.ResetForTest here: this test only adds a new,
	// uniquely-named ServiceDef ("naughty-config-consumer") rather than
	// re-registering anything that collides with — or needs to survive
	// past — the package's real "configupdate" registration, so it
	// doesn't disturb other tests in this binary that depend on it.
	//
	// "naughty" does NOT declare serviceregistry.KeyConfig in Needs but
	// calls config.GetConfig during Build.
	serviceregistry.Register(serviceregistry.ServiceDef{
		Name: "naughty-config-consumer",
		Build: func(c *serviceregistry.Container) (any, error) {
			_ = config.GetConfig(c)
			return nil, nil
		},
	})

	c := serviceregistry.NewContainer().
		Override(serviceregistry.KeyConfig, &config.Config{}).
		Include("naughty-config-consumer")
	defer func() {
		r := recover()
		if r == nil {
			t.Fatal("expected panic for undeclared GetConfig")
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
