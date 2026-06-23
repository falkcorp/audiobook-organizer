// file: internal/importer/register.go
// version: 1.0.1
// last-edited: 2026-06-23

package importer

import (
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/serviceregistry"
)

func init() {
	serviceregistry.Register(serviceregistry.ServiceDef{
		Name:   serviceregistry.KeyImportPath,
		Needs:  []string{serviceregistry.KeyStore},
		Groups: []string{"core"},
		Build: func(c *serviceregistry.Container) (any, error) {
			store := serviceregistry.Get[database.Store](c, serviceregistry.KeyStore)
			return NewImportPathService(store), nil
		},
	})
}
