// file: core/kernel/integrations_downloads.go
// version: 0.1.0
// guid: 41bffadd-d9b4-471f-a9a4-7791446e4505
// last-edited: 2026-11-22

package kernel

import (
	"context"

	"github.com/falkcorp/mediacore/integrations/download"
	"github.com/falkcorp/mediacore/library"
)

// liveMapper is a library.PathMapper that asks the Manager for its current
// mapper on every call: Manager.Mapper() returns a new mapper after each
// Reload, so the mapper itself must not be captured at build time or a
// mapping change would never reach the Pool.
type liveMapper struct{ m library.Manager }

var _ library.PathMapper = liveMapper{}

func (l liveMapper) ToLocal(source, remote string) (library.Path, error) {
	return l.m.Mapper().ToLocal(source, remote)
}

func (l liveMapper) ToRemote(source string, p library.Path) (string, error) {
	return l.m.Mapper().ToRemote(source, p)
}

func (l liveMapper) Mappings(source string) []library.Mapping { return l.m.Mapper().Mappings(source) }

// buildDownloads builds the download Pool over the Registry, with the
// library's mapper as its path mapper (05-download 4.4: one mapper, one set of
// matching rules) and core.download.caps_ttl.
func (s *integrationsServices) buildDownloads(_ context.Context) error {
	sc, err := s.scope("download", "adapter", "instance")
	if err != nil {
		return err
	}
	var d downloadSettings
	if s.in.Download != nil {
		d = s.in.Download()
	}
	p, err := download.NewPool(s.registry, download.PoolOptions{
		Paths:   liveMapper{m: s.library},
		CapsTTL: d.CapsTTL.Std(),
		Metrics: sc,
		Now:     s.in.now(),
		Logger:  s.in.logger("download"),
	})
	if err != nil {
		return err
	}
	s.downloads = p
	return nil
}
