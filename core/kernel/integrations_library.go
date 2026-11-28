// file: core/kernel/integrations_library.go
// version: 0.1.0
// guid: 27398f58-f29e-4b19-bbac-ef443c9e7d71
// last-edited: 2026-11-22

package kernel

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"strings"

	"github.com/falkcorp/mediacore/config"
	"github.com/falkcorp/mediacore/library"
)

// librarySource is library.ConfigSource over the live core.library section:
// Lookup answers a core.library.<a>.<b> key with the JSON of the field the
// section's json tags name, which is what the config store's Effective holds.
// The section is marshalled on each Lookup, so a hot reload is read on the next
// Reload; it is marshalled generically, so a field B3-a later types (roots and
// mappings are raw JSON today) keeps working without a change here. The real
// *config.Store satisfies library.ConfigSource directly and may replace this.
type librarySource struct{ get func() librarySettings }

var _ library.ConfigSource = librarySource{}

const libraryKeyPrefix = "core.library."

func (c librarySource) Lookup(p config.Path) (config.KeyState, bool) {
	rest, ok := strings.CutPrefix(string(p), libraryKeyPrefix)
	if !ok || c.get == nil {
		return config.KeyState{}, false
	}
	raw, err := json.Marshal(c.get())
	if err != nil {
		return config.KeyState{}, false
	}
	for seg := range strings.SplitSeq(rest, ".") {
		var m map[string]json.RawMessage
		if json.Unmarshal(raw, &m) != nil {
			return config.KeyState{}, false
		}
		if raw, ok = m[seg]; !ok {
			return config.KeyState{}, false
		}
	}
	return config.KeyState{Path: p, Effective: raw}, true
}

// buildLibrary builds the library Manager over c/library/ and loads its roots
// (Reload): a root that fails is registered disabled and retried at the next
// Reload, and a decode error in one entry is returned after the others load,
// so neither stops boot (05-library 5L.4 steps 1 and 3, 5L.5). The install ID,
// the module-enabled and capability predicates, the seeding oracles and the
// excluded paths are inputs.
func (s *integrationsServices) buildLibrary(ctx context.Context) error {
	ks, err := s.keyspace(library.Owner)
	if err != nil {
		return err
	}
	sc, err := s.scope("library", library.MetricLabels()...)
	if err != nil {
		return err
	}
	m, err := library.New(library.Options{
		Keyspace:      ks,
		Config:        librarySource{get: s.in.Library},
		Oracles:       s.in.LibraryOracles,
		Logger:        s.in.logger("library"),
		Metrics:       sc,
		InstallID:     s.in.InstallID,
		Enabled:       s.in.ModuleEnabled,
		HasCapability: s.in.HasCapability,
		ExcludePaths:  s.in.ExcludePaths,
	})
	if err != nil {
		return err
	}
	// library.New returns the Manager interface; Close and Reload are on the
	// concrete manager (io.Closer, library.Reloader).
	closer, ok := m.(io.Closer)
	reloader, ok2 := m.(library.Reloader)
	if !ok || !ok2 {
		return fmt.Errorf("library.New returned a %T that is not an io.Closer and a library.Reloader", m)
	}
	s.library = m
	s.built("library", func(context.Context) error { return closer.Close() })
	if err := reloader.Reload(ctx); err != nil {
		s.log.Warn("integrations: library roots loaded with errors", slog.Any("err", err))
	}
	return nil
}
