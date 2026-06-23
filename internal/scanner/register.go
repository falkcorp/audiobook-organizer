// file: internal/scanner/register.go
// version: 1.0.1
// last-edited: 2026-06-23

package scanner

import (
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/serviceregistry"
)

func init() {
	serviceregistry.Register(serviceregistry.ServiceDef{
		Name:   serviceregistry.KeyScan,
		Needs:  []string{serviceregistry.KeyStore, serviceregistry.KeyEmbeddingStore},
		Groups: []string{"core"},
		Build: func(c *serviceregistry.Container) (any, error) {
			store := serviceregistry.Get[database.Store](c, serviceregistry.KeyStore)
			scanSvc := NewScanService(store)
			// Wire in EmbeddingStore for metadata hash dedup detection
			if es := serviceregistry.Get[*database.EmbeddingStore](c, serviceregistry.KeyEmbeddingStore); es != nil {
				scanSvc.SetEmbeddingStore(es)
			}
			return scanSvc, nil
		},
	})
}
