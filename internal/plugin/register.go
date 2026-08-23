// file: internal/plugin/register.go
// version: 1.1.0
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
// behavior are unchanged, but the key string and the return type are now
// fixed by this function's signature rather than chosen ad hoc at each
// call site, so a typo'd key or a mismatched type argument is a compile
// error here instead of a runtime panic there.
//
// This accessor cannot live in internal/serviceregistry itself: that
// package is a dependency of this one (see the Register call above), so
// serviceregistry importing plugin back would be an import cycle.
func GetEventBus(c *serviceregistry.Container) *EventBus {
	return serviceregistry.Get[*EventBus](c, serviceregistry.KeyEventBus)
}
