// file: core/kernel/integrations_library.go
// version: 0.1.0
// guid: 27398f58-f29e-4b19-bbac-ef443c9e7d71
// last-edited: 2026-11-22

package kernel

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"reflect"
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

// errLibraryZeroSection refuses a core.library reader that returns the zero
// value. librarySource marshals the section, so a zero struct becomes explicit
// zeros for free_space.warn_percent and free_space.default_min_bytes, which
// library accepts (>= 0) and so turns its free-space floor off, overriding the
// 10 percent and 5 GiB defaults.
var errLibraryZeroSection = errors.New("core.library section is the zero value: the reader must return the effective section with the config defaults applied (a zero free_space.warn_percent and default_min_bytes disable library's free-space floor)")

// sectionIsZero reports whether every field of v is unset. A nil slice or map
// and an empty one both count as unset: the defaults declare empty lists
// (roots, mappings, protected_paths), which are not a choice an operator made.
func sectionIsZero(v any) bool { return valueIsZero(reflect.ValueOf(v)) }

func valueIsZero(v reflect.Value) bool {
	switch v.Kind() {
	case reflect.Struct:
		for f := range v.NumField() {
			if !valueIsZero(v.Field(f)) {
				return false
			}
		}
		return true
	case reflect.Slice, reflect.Map:
		return v.Len() == 0
	default:
		return v.IsZero()
	}
}

// libraryHandles is the Close and Reload handles of the Manager library.New
// returned: both live on the concrete manager, not on the interface. A
// Manager that lacks either is refused, and one that can be closed is closed
// first, so a refused build does not leak it.
func libraryHandles(m library.Manager) (io.Closer, library.Reloader, error) {
	closer, canClose := m.(io.Closer)
	reloader, canReload := m.(library.Reloader)
	if canClose && canReload {
		return closer, reloader, nil
	}
	err := fmt.Errorf("library.New returned a %T that is not an io.Closer and a library.Reloader", m)
	if canClose {
		if cerr := closer.Close(); cerr != nil {
			err = errors.Join(err, fmt.Errorf("close it: %w", cerr))
		}
	}
	return nil, nil, err
}

// buildLibrary builds the library Manager over c/library/ and loads its roots
// (Reload): a root that fails is registered disabled and retried at the next
// Reload, and a decode error in one entry is returned after the others load,
// so neither stops boot (05-library 5L.4 steps 1 and 3, 5L.5). The install ID,
// the module-enabled and capability predicates, the seeding oracles and the
// excluded paths are inputs. A reader that returns the zero section is refused
// (errLibraryZeroSection).
//
// A Reload error is logged at Error, not returned: a bad root must not stop
// boot. It is a real finding, since a mapping or root that was skipped can
// change where a download maps, but the kernel has no setup-finding or health
// mechanism a constructor can raise (core/health checks are registered by their
// owners at boot, and library has none), so the log line is the report.
func (s *integrationsServices) buildLibrary(ctx context.Context) error {
	if s.in.Library != nil && sectionIsZero(s.in.Library()) {
		return errLibraryZeroSection
	}
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
	closer, reloader, err := libraryHandles(m)
	if err != nil {
		return err
	}
	s.library = m
	s.built("library", func(context.Context) error { return closer.Close() })
	if err := reloader.Reload(ctx); err != nil {
		s.log.Error("integrations: library roots loaded with errors", slog.Any("err", err))
	}
	return nil
}
