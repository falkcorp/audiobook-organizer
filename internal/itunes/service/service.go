// file: internal/itunes/service/service.go
// version: 2.3.0
// guid: 81ccaec6-42b0-4828-83c8-7a96680112d9
// last-edited: 2026-10-07

package itunesservice

import (
	"context"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/logger"
	"github.com/falkcorp/audiobook-organizer/internal/metafetch"
	"github.com/falkcorp/audiobook-organizer/internal/plugin"
	"github.com/falkcorp/audiobook-organizer/internal/realtime"
)

// Deps is the explicit dependency set for Service. No globals, no Server,
// no closure captures of server-owned state.
//
// PLUGIN-DECOUPLE (May 13, 2026): replaced two server-bound closure fields
// with event-bus + organizer-factory deps that don't capture *Server.
// This unblocks the container's ability to construct itunesservice.
type Deps struct {
	Store      Store
	ActivityFn func(database.ActivityEntry)
	Realtime   *realtime.EventHub // may be nil; means no SSE push
	Config     Config
	Logger     logger.Logger
	// EventBus is where the service publishes lifecycle events. The
	// dedup engine subscribes to plugin.EventBookImported and runs its
	// dedup-on-import check there. May be nil (no events published).
	//
	// Replaces the pre-PLUGIN-DECOUPLE `OnBookCreated func(bookID string)`
	// callback, which captured server.fireDedupOnImport and prevented
	// container-based construction.
	EventBus plugin.EventPublisher
	// Metafetch is a pre-constructed metafetch service used during
	// metadata enrichment. May be nil (enrichment becomes a no-op).
	Metafetch *metafetch.Service
	// OrganizerFactory builds a BookOrganizer on demand. Required when
	// ImportMode is organize/organized. May be nil (organize phase skipped).
	//
	// Post PLUGIN-DECOUPLE the factory must be a pure-data closure (no
	// server captures); callers pass e.g.
	//   OrganizerFactory: func() BookOrganizer {
	//       return organizer.NewOrganizer(&config.AppConfig)
	//   }
	// which only captures the package-level config pointer.
	OrganizerFactory func() BookOrganizer
}

// All sub-components are now real (M1 steps 1–7 complete).

// Service owns the iTunes integration. Prefer a single *Service on the
// Server struct — it composes the seven sub-components below with shared
// lifecycle (Start / Shutdown).
type Service struct {
	deps Deps

	// Sub-components. Nil when the service is disabled; populated by New.
	Importer  *Importer
	Positions *PositionSync
	Playlists *PlaylistSync
	Transfer  *TransferService
}

// New constructs a fully-wired iTunes service. Returns ErrITunesDisabled
// equivalent (cfg.Enabled == false) routes through NewDisabled instead —
// callers should branch on cfg.Enabled at the construction site.
func New(deps Deps) (*Service, error) {
	if !deps.Config.Enabled {
		return NewDisabled(), nil
	}
	if deps.Logger == nil {
		deps.Logger = logger.New("itunes")
	}
	svc := &Service{
		deps: deps,
	}

	// PositionSync: pulls iTunes bookmarks into the admin user's positions.
	svc.Positions = newPositionSync(deps.Store)

	// PlaylistSync: imports smart playlists from the ITL.
	svc.Playlists = newPlaylistSync(deps.Store)

	// TransferService: the ITL download handler. No deps — keyed off
	// config.AppConfig.
	svc.Transfer = newTransferService()

	// M1 step 7: Importer. Owns the full import + sync pipeline.
	svc.Importer = newImporter(deps)

	return svc, nil
}

// NewDisabled constructs a Service whose methods all return
// ErrITunesDisabled. Use when cfg.Enabled == false so the rest of the
// server can still wire a non-nil *Service and avoid nil guards at every
// call site.
func NewDisabled() *Service {
	return &Service{}
}

// Enabled reports whether the service has active sub-components wired.
// A disabled service returns false; a real service returns true once
// Start has run (or immediately — PR 2 decides per component).
func (s *Service) Enabled() bool {
	// PR 2 will refine: "enabled and started" vs "enabled but not yet
	// started". For now, Enabled == cfg.Enabled.
	return s.deps.Config.Enabled
}

// Start launches any long-lived sub-component goroutines (none since iTunes
// write-back was removed on 2026-10-07). No-op when disabled.
func (s *Service) Start(ctx context.Context) error {
	if !s.Enabled() {
		return nil
	}
	// Sub-component Start calls added in PR 2. This skeleton is a no-op
	// so PR 1 can ship without behavior change.
	return nil
}

// Shutdown flushes any long-lived sub-components and waits up to timeout
// for graceful completion. No-op when disabled.
func (s *Service) Shutdown(timeout time.Duration) error {
	if !s.Enabled() {
		return nil
	}
	// Sub-component Shutdown calls added in PR 2.
	return nil
}
