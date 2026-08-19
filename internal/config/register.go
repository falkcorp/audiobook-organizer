// file: internal/config/register.go
// version: 1.1.0
// last-edited: 2026-08-19

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
