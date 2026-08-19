// file: internal/merge/register.go
// version: 1.1.0
// last-edited: 2026-08-19

package merge

import (
	"github.com/falkcorp/audiobook-organizer/internal/serviceregistry"
)

func init() {
	serviceregistry.Register(serviceregistry.ServiceDef{
		Name:   serviceregistry.KeyMerge,
		Needs:  []string{serviceregistry.KeyStore},
		Groups: []string{"core"},
		Build: func(c *serviceregistry.Container) (any, error) {
			store := serviceregistry.Get[Store](c, serviceregistry.KeyStore)
			return NewService(store), nil
		},
	})
}
