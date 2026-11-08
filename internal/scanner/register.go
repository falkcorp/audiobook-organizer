// file: internal/scanner/register.go
// version: 1.1.1
// last-edited: 2026-11-08

package scanner

import (
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/serviceregistry"
)

// ServiceDefs returns this package's service definitions for the composition
// root's one explicit list (06 §6.11.1 step 3).
func ServiceDefs() []serviceregistry.ServiceDef {
	return []serviceregistry.ServiceDef{
		{
			Name:   serviceregistry.KeyScan,
			Needs:  []string{serviceregistry.KeyStore, serviceregistry.KeyEmbeddingStore},
			Groups: []string{"core"},
			Build: func(c *serviceregistry.Container) (any, error) {
				store := serviceregistry.Get[scannerStore](c, serviceregistry.KeyStore)
				scanSvc := NewScanService(store)
				// Wire in EmbeddingStore for metadata hash dedup detection
				if es := serviceregistry.Get[*database.EmbeddingStore](c, serviceregistry.KeyEmbeddingStore); es != nil {
					scanSvc.SetEmbeddingStore(es)
				}
				return scanSvc, nil
			},
		},
	}
}
