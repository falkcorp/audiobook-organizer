// file: internal/audiobooks/register.go
// version: 1.2.1
// last-edited: 2026-11-08

package audiobooks

import (
	"github.com/falkcorp/audiobook-organizer/internal/serviceregistry"
)

// ServiceDefs returns this package's service definitions for the composition
// root's one explicit list (06 §6.11.1 step 3).
func ServiceDefs() []serviceregistry.ServiceDef {
	return []serviceregistry.ServiceDef{
		{
			Name:   serviceregistry.KeyAudiobook,
			Needs:  []string{serviceregistry.KeyStore},
			Groups: []string{"core"},
			Build: func(c *serviceregistry.Container) (any, error) {
				store := serviceregistry.Get[audiobookStore](c, serviceregistry.KeyStore)
				return NewAudiobookService(store), nil
			},
		},
		{
			Name:   serviceregistry.KeyOrganize,
			Needs:  []string{serviceregistry.KeyStore},
			Groups: []string{"core"},
			Build: func(c *serviceregistry.Container) (any, error) {
				store := serviceregistry.Get[organizeServiceStore](c, serviceregistry.KeyStore)
				return NewOrganizeService(store), nil
			},
		},
	}
}
