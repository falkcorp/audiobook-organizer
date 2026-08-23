// file: internal/config/register.go
// version: 1.2.1
// last-edited: 2026-08-23

package config

import (
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/serviceregistry"
)

func init() {
	serviceregistry.Register(serviceregistry.ServiceDef{
		Name:   serviceregistry.KeyConfigUpdate,
		Needs:  []string{serviceregistry.KeyStore},
		Groups: []string{"core"},
		Build: func(c *serviceregistry.Container) (any, error) {
			store := serviceregistry.Get[database.SettingsStore](c, serviceregistry.KeyStore)
			return NewUpdateService(store), nil
		},
	})
}

// GetConfig returns the shared *Config from the container's "config" key
// (serviceregistry.KeyConfig). Config is Override'd unconditionally in
// production wiring (internal/server/server.go, before Container.Build) —
// it is not built by a factory and has no Needs entry of its own — so
// every registered service may treat it as always present.
//
// This wraps serviceregistry.Get[*Config](c, serviceregistry.KeyConfig)
// (ARCH-8): the wrapped call and its panic-on-missing/undeclared-Needs
// behavior are unchanged, but the key and the return type are now fixed
// TOGETHER by this function's signature instead of chosen independently
// at each call site, so a call site can no longer pair KeyConfig with the
// wrong type (or vice versa) — that pairing mismatch is now impossible to
// express, where before it type-checked and panicked at the type assertion
// inside Get. (A misspelled key was already a compile error before this
// change, via the KeyConfig constant — every call site already used it,
// not a bare string literal.) It does NOT change what happens when
// "config" truly isn't present (still panics) — see
// internal/serviceregistry/container_test.go for that invariant. Both
// GetConfig here and the wrapped Get[T] resolve lazily at call time, at
// the same point in the same Build closures the old bare Get[T](c,
// KeyConfig) calls ran at, so no registration-order behavior changed.
//
// This accessor cannot live in internal/serviceregistry itself: that
// package is a dependency of this one (see the Register call above), so
// serviceregistry importing config back would be an import cycle.
func GetConfig(c *serviceregistry.Container) *Config {
	return serviceregistry.Get[*Config](c, serviceregistry.KeyConfig)
}
