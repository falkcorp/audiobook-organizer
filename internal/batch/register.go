// file: internal/batch/register.go
// version: 1.2.1
// guid: fa495c96-a75b-485a-add0-2c369aae3e9e
// last-edited: 2026-11-08

package batch

import (
	"github.com/falkcorp/audiobook-organizer/internal/serviceregistry"
)

// ServiceDefs returns this package's service definitions for the composition
// root's one explicit list (06 §6.11.1 step 3).
func ServiceDefs() []serviceregistry.ServiceDef {
	return []serviceregistry.ServiceDef{
		{
			Name:   serviceregistry.KeyBatch,
			Needs:  []string{serviceregistry.KeyStore, serviceregistry.KeyAudiobook},
			Groups: []string{"core"},
			Build: func(c *serviceregistry.Container) (any, error) {
				store := serviceregistry.Get[batchBookStore](c, serviceregistry.KeyStore)
				// The batch hard delete of a trashed book takes the purge's own path.
				purger := serviceregistry.Get[TrashPurger](c, serviceregistry.KeyAudiobook)
				return NewBatchService(store, WithTrashPurger(purger)), nil
			},
		},
	}
}
