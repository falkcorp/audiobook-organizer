// file: internal/search/register.go
// version: 3.0.2
// last-edited: 2026-06-23
// guid: 7b4e2c1a-9f3d-4a82-b6e5-1d0c8f5a3e72
//
// Registers the BleveIndex as the "searchindex" service. IndexService
// satisfies Starter and Stopper — Container.Start opens the index,
// Container.Stop closes it. server_lifecycle.go pulls Index() into
// s.searchIndex right after Container.Start returns.
//
// Path convention:
//
//	{dirname(config.DatabasePath)}/library.bleve

package search

import (
	"context"
	"log/slog"
	"path/filepath"

	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/serviceregistry"
)

// IndexService wraps a *BleveIndex with deferred-open semantics so
// test code that never calls Start doesn't leak Bleve file handles.
type IndexService struct {
	cfg  *config.Config
	idx  *BleveIndex
	path string
}

// Start opens the on-disk Bleve index. Idempotent — if already open,
// Start is a no-op and returns nil.
//
// Open failures are downgraded to warnings rather than errors so the
// server can run without search. Returning an error here would abort
// Container.Start and roll back already-started services. Callers
// check Index() != nil before using the result.
func (b *IndexService) Start(_ context.Context) error {
	if b == nil || b.idx != nil {
		return nil
	}
	if b.cfg == nil || b.cfg.DatabasePath == "" {
		slog.Info("searchindex DatabasePath not configured, running without search")
		return nil
	}
	indexPath := filepath.Join(filepath.Dir(b.cfg.DatabasePath), "library.bleve")
	idx, err := Open(indexPath)
	if err != nil {
		slog.Warn("Failed to open search index", "err", err)
		return nil
	}
	b.idx = idx
	b.path = indexPath
	slog.Info("Search index opened", "path", indexPath)
	return nil
}

// Stop closes the underlying Bleve index. Safe to call multiple times
// or before Start has been called. Idempotent: clears b.idx so a
// follow-up Stop (e.g. from the inline s.searchIndex.Close() in
// Server.Shutdown) is a no-op.
func (b *IndexService) Stop(_ context.Context) error {
	if b == nil || b.idx == nil {
		return nil
	}
	err := b.idx.Close()
	b.idx = nil
	if err != nil {
		slog.Warn("Failed to close search index", "err", err)
		return err
	}
	slog.Info("Search index closed")
	return nil
}

// Index returns the live *BleveIndex, or nil if Start has not been
// called or the service was stopped.
func (b *IndexService) Index() *BleveIndex {
	if b == nil {
		return nil
	}
	return b.idx
}

// Path returns the on-disk path the index was opened from, or "" if
// not yet started.
func (b *IndexService) Path() string {
	if b == nil {
		return ""
	}
	return b.path
}

func init() {
	serviceregistry.Register(serviceregistry.ServiceDef{
		Name:   "searchindex",
		Needs:  []string{serviceregistry.KeyConfig},
		Groups: []string{"scheduler"},
		Build: func(c *serviceregistry.Container) (any, error) {
			cfg := serviceregistry.Get[*config.Config](c, serviceregistry.KeyConfig)
			return &IndexService{cfg: cfg}, nil
		},
	})
}
