// file: internal/config/register.go
// version: 1.0.1
// last-edited: 2026-06-23

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
			store := serviceregistry.Get[database.Store](c, serviceregistry.KeyStore)
			return NewUpdateService(store), nil
		},
	})
}
