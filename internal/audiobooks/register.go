// file: internal/audiobooks/register.go
// version: 1.1.1
// last-edited: 2026-06-23

package audiobooks

import (
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/serviceregistry"
)

func init() {
	serviceregistry.Register(serviceregistry.ServiceDef{
		Name:   serviceregistry.KeyAudiobook,
		Needs:  []string{serviceregistry.KeyStore},
		Groups: []string{"core"},
		Build: func(c *serviceregistry.Container) (any, error) {
			store := serviceregistry.Get[database.Store](c, serviceregistry.KeyStore)
			return NewAudiobookService(store), nil
		},
	})
	serviceregistry.Register(serviceregistry.ServiceDef{
		Name:   serviceregistry.KeyOrganize,
		Needs:  []string{serviceregistry.KeyStore},
		Groups: []string{"core"},
		Build: func(c *serviceregistry.Container) (any, error) {
			store := serviceregistry.Get[database.Store](c, serviceregistry.KeyStore)
			return NewOrganizeService(store), nil
		},
	})
}
