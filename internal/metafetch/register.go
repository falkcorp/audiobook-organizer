// file: internal/metafetch/register.go
// version: 1.2.1
// last-edited: 2026-06-23

package metafetch

import (
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/serviceregistry"
)

func init() {
	serviceregistry.Register(serviceregistry.ServiceDef{
		Name:   serviceregistry.KeyMetadataState,
		Needs:  []string{serviceregistry.KeyStore},
		Groups: []string{"core"},
		Build: func(c *serviceregistry.Container) (any, error) {
			store := serviceregistry.Get[database.Store](c, serviceregistry.KeyStore)
			return NewMetadataStateService(store), nil
		},
	})

	serviceregistry.Register(serviceregistry.ServiceDef{
		Name:   serviceregistry.KeyMetaFetch,
		Needs:  []string{serviceregistry.KeyStore},
		Groups: []string{"core"},
		Build: func(c *serviceregistry.Container) (any, error) {
			store := serviceregistry.Get[database.Store](c, serviceregistry.KeyStore)
			return NewService(store), nil
		},
	})

	// olservice — Open Library data-dump lifecycle wrapper. No build-time
	// deps; the underlying OL store is opened lazily on first EnsureStore.
	// metafetch.Service.PostInit pulls this to wire SetOLStore.
	serviceregistry.Register(serviceregistry.ServiceDef{
		Name:   "olservice",
		Needs:  []string{},
		Groups: []string{"core"},
		Build: func(c *serviceregistry.Container) (any, error) {
			return NewOpenLibraryService(), nil
		},
	})
}
