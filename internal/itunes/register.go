// file: internal/itunes/register.go
// version: 1.1.1
// last-edited: 2026-06-23

package itunes

import (
	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/serviceregistry"
)

func init() {
	serviceregistry.Register(serviceregistry.ServiceDef{
		Name:   "librarywatcher",
		Needs:  []string{serviceregistry.KeyConfig},
		Groups: []string{"scheduler"},
		Build: func(c *serviceregistry.Container) (any, error) {
			cfg := serviceregistry.Get[*config.Config](c, serviceregistry.KeyConfig)
			if cfg.ITunes.LibraryReadPath == "" {
				return nil, nil
			}
			// Create and start the fsnotify watcher for the iTunes Library.xml file.
			watcher, err := NewLibraryWatcher(cfg.ITunes.LibraryReadPath)
			if err != nil {
				return nil, err
			}
			return watcher, nil
		},
	})
}
