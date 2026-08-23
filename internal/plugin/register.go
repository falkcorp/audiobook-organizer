// file: internal/plugin/register.go
// version: 1.1.1
// last-edited: 2026-08-23

package plugin

import (
	"github.com/falkcorp/audiobook-organizer/internal/serviceregistry"
)

func init() {
	// eventbus: shared plugin event bus. Plugins and services publish here;
	// the bus has no external dependencies, so it's a leaf.
	serviceregistry.Register(serviceregistry.ServiceDef{
		Name:   serviceregistry.KeyEventBus,
		Needs:  []string{},
		Groups: []string{"core"},
		Build: func(c *serviceregistry.Container) (any, error) {
			return NewEventBus(), nil
		},
	})
}

// GetEventBus returns the shared *EventBus from the container's "eventbus"
// key (serviceregistry.KeyEventBus). eventbus is a "core"-group leaf
// service with no Needs of its own (see the Register call above), so it
// is always present once the container's core group is Included and Built.
//
// This wraps serviceregistry.Get[*EventBus](c, serviceregistry.KeyEventBus)
// (ARCH-8): the wrapped call and its panic-on-missing/undeclared-Needs
// behavior are unchanged, but the key and the return type are now fixed
// TOGETHER by this function's signature instead of chosen independently
// at each call site, so a call site can no longer pair KeyEventBus with
// the wrong type (or vice versa) — that pairing mismatch is now
// impossible to express, where before it type-checked and panicked at the
// type assertion inside Get. (A misspelled key was already a compile
// error before this change, via the KeyEventBus constant — every call
// site already used it, not a bare string literal.) GetEventBus resolves
// lazily at call time, same as the wrapped Get[T] did, so no
// registration-order behavior changed.
//
// This accessor cannot live in internal/serviceregistry itself: that
// package is a dependency of this one (see the Register call above), so
// serviceregistry importing plugin back would be an import cycle.
func GetEventBus(c *serviceregistry.Container) *EventBus {
	return serviceregistry.Get[*EventBus](c, serviceregistry.KeyEventBus)
}
