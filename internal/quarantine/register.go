// file: internal/quarantine/register.go
// version: 1.2.1
// last-edited: 2026-11-08

package quarantine

import (
	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/plugin"
	"github.com/falkcorp/audiobook-organizer/internal/serviceregistry"
)

// ServiceDefs returns this package's service definitions for the composition
// root's one explicit list (06 §6.11.1 step 3).
func ServiceDefs() []serviceregistry.ServiceDef {
	return []serviceregistry.ServiceDef{
		{
			Name:   serviceregistry.KeyQuarantine,
			Needs:  []string{serviceregistry.KeyStore, serviceregistry.KeyConfig, serviceregistry.KeyEventBus},
			Groups: []string{"core"},
			Build: func(c *serviceregistry.Container) (any, error) {
				store := serviceregistry.Get[Store](c, serviceregistry.KeyStore)
				cfg := config.GetConfig(c)
				bus := plugin.GetEventBus(c)
				return NewQuarantineService(store, cfg, bus), nil
			},
		},
	}
}
