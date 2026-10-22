// file: internal/batch/register.go
// version: 1.2.0
// last-edited: 2026-10-22

package batch

import (
	"github.com/falkcorp/audiobook-organizer/internal/serviceregistry"
)

func init() {
	serviceregistry.Register(serviceregistry.ServiceDef{
		Name:   serviceregistry.KeyBatch,
		Needs:  []string{serviceregistry.KeyStore, serviceregistry.KeyAudiobook},
		Groups: []string{"core"},
		Build: func(c *serviceregistry.Container) (any, error) {
			store := serviceregistry.Get[batchBookStore](c, serviceregistry.KeyStore)
			// The batch hard delete of a trashed book takes the purge's own path.
			purger := serviceregistry.Get[TrashPurger](c, serviceregistry.KeyAudiobook)
			return NewBatchService(store, WithTrashPurger(purger)), nil
		},
	})
}
