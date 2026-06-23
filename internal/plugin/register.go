// file: internal/plugin/register.go
// version: 1.0.1
// last-edited: 2026-06-23

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
