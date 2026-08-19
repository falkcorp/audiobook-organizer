// file: internal/quarantine/register.go
// version: 1.1.0
// last-edited: 2026-08-19

package quarantine

import (
	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/plugin"
	"github.com/falkcorp/audiobook-organizer/internal/serviceregistry"
)

func init() {
	serviceregistry.Register(serviceregistry.ServiceDef{
		Name:   serviceregistry.KeyQuarantine,
		Needs:  []string{serviceregistry.KeyStore, serviceregistry.KeyConfig, serviceregistry.KeyEventBus},
		Groups: []string{"core"},
		Build: func(c *serviceregistry.Container) (any, error) {
			store := serviceregistry.Get[Store](c, serviceregistry.KeyStore)
			cfg := serviceregistry.Get[*config.Config](c, serviceregistry.KeyConfig)
			bus := serviceregistry.Get[*plugin.EventBus](c, serviceregistry.KeyEventBus)
			return NewQuarantineService(store, cfg, bus), nil
		},
	})
}
