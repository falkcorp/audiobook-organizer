// file: internal/importer/register.go
// version: 1.1.1
// last-edited: 2026-11-08

package importer

import (
	"github.com/falkcorp/audiobook-organizer/internal/serviceregistry"
)

// ServiceDefs returns this package's service definitions for the composition
// root's one explicit list (06 §6.11.1 step 3).
func ServiceDefs() []serviceregistry.ServiceDef {
	return []serviceregistry.ServiceDef{
		{
			Name:   serviceregistry.KeyImportPath,
			Needs:  []string{serviceregistry.KeyStore},
			Groups: []string{"core"},
			Build: func(c *serviceregistry.Container) (any, error) {
				store := serviceregistry.Get[Store](c, serviceregistry.KeyStore)
				return NewImportPathService(store), nil
			},
		},
	}
}
