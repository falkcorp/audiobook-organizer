// file: internal/metafetch/register.go
// version: 1.3.2
// last-edited: 2026-11-08

package metafetch

import (
	"github.com/falkcorp/audiobook-organizer/internal/serviceregistry"
)

// ServiceDefs returns this package's service definitions for the composition
// root's one explicit list (06 §6.11.1 step 3).
func ServiceDefs() []serviceregistry.ServiceDef {
	return []serviceregistry.ServiceDef{
		{
			Name:   serviceregistry.KeyMetadataState,
			Needs:  []string{serviceregistry.KeyStore},
			Groups: []string{"core"},
			Build: func(c *serviceregistry.Container) (any, error) {
				store := serviceregistry.Get[metadataStateStore](c, serviceregistry.KeyStore)
				return NewMetadataStateService(store), nil
			},
		},

		{
			Name:   serviceregistry.KeyMetaFetch,
			Needs:  []string{serviceregistry.KeyStore},
			Groups: []string{"core"},
			Build: func(c *serviceregistry.Container) (any, error) {
				store := serviceregistry.Get[Store](c, serviceregistry.KeyStore)
				return NewService(store), nil
			},
		},

		// olservice — Open Library data-dump lifecycle wrapper. No build-time
		// deps; the underlying OL store is opened lazily on first EnsureStore.
		// metafetch.Service.PostInit pulls this to wire SetOLStoreSource.
		{
			Name:   "olservice",
			Needs:  []string{},
			Groups: []string{"core"},
			Build: func(c *serviceregistry.Container) (any, error) {
				return NewOpenLibraryService(), nil
			},
		},
	}
}
