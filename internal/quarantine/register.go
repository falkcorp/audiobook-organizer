// file: internal/quarantine/register.go
// version: 1.2.0
// last-edited: 2026-08-23

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
			cfg := config.GetConfig(c)
			bus := plugin.GetEventBus(c)
			return NewQuarantineService(store, cfg, bus), nil
		},
	})
}
