// file: internal/batch/register.go
// version: 1.1.0
// last-edited: 2026-08-19

package batch

import (
	"github.com/falkcorp/audiobook-organizer/internal/serviceregistry"
)

func init() {
	serviceregistry.Register(serviceregistry.ServiceDef{
		Name:   serviceregistry.KeyBatch,
		Needs:  []string{serviceregistry.KeyStore},
		Groups: []string{"core"},
		Build: func(c *serviceregistry.Container) (any, error) {
			store := serviceregistry.Get[batchBookStore](c, serviceregistry.KeyStore)
			return NewBatchService(store), nil
		},
	})
}
