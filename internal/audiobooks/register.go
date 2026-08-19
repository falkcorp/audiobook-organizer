// file: internal/audiobooks/register.go
// version: 1.2.0
// last-edited: 2026-08-19

package audiobooks

import (
	"github.com/falkcorp/audiobook-organizer/internal/serviceregistry"
)

func init() {
	serviceregistry.Register(serviceregistry.ServiceDef{
		Name:   serviceregistry.KeyAudiobook,
		Needs:  []string{serviceregistry.KeyStore},
		Groups: []string{"core"},
		Build: func(c *serviceregistry.Container) (any, error) {
			store := serviceregistry.Get[audiobookStore](c, serviceregistry.KeyStore)
			return NewAudiobookService(store), nil
		},
	})
	serviceregistry.Register(serviceregistry.ServiceDef{
		Name:   serviceregistry.KeyOrganize,
		Needs:  []string{serviceregistry.KeyStore},
		Groups: []string{"core"},
		Build: func(c *serviceregistry.Container) (any, error) {
			store := serviceregistry.Get[organizeServiceStore](c, serviceregistry.KeyStore)
			return NewOrganizeService(store), nil
		},
	})
}
