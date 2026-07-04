// file: internal/itunes/service/service.go
// version: 2.2.0
// guid: 81ccaec6-42b0-4828-83c8-7a96680112d9
// last-edited: 2026-07-03

package itunesservice

import (
	"context"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/itunes"
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
	// AudiobookRoot is the on-disk audiobook tree the path-repair
	// operation walks for tier B (embedded tag scan) and tier C
	// (fuzzy match). Empty disables those tiers.
	AudiobookRoot string
	// ReportDir is where the path-repair operation drops its JSON
	// report file. Empty means inline-only (no file).
	ReportDir string
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
	Importer    *Importer
	Batcher     *WriteBackBatcher
	Positions   *PositionSync
	Paths       *PathReconciler
	Repair      *PathRepairer
	Playlists   *PlaylistSync
	Provisioner *TrackProvisioner
	Transfer    *TransferService
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

	// M1 step 2: Batcher lives here now. Built first so sub-components
	// that need it (Provisioner today, Positions/Playlists/etc. in later
	// M1 steps) can be wired with the real handle at construction time
	// instead of via post-hoc setters.
	svc.Batcher = NewWriteBackBatcher(5*time.Second, WriteBackBatcherConfig{
		AutoWriteBack:       deps.Config.AutoWriteBack,
		ITLWriteBackEnabled: deps.Config.ITLWriteBackEnabled,
		LibraryWritePath:    deps.Config.LibraryWritePath,
	}, deps.Store)

	// SPEC 3 §4: wire the iTunes-in-use precondition. File-activity on the
	// library and its journal siblings is the only signal visible from this
	// side of the share; a hit defers the flush one debounce cycle (work is
	// re-enqueued, never lost). Before this, SetLibraryNotInUse had no
	// production caller and every write raced a potentially-open iTunes.
	if p := deps.Config.LibraryWritePath; p != "" {
		svc.Batcher.SetLibraryNotInUse(itunes.FileActivityLibraryCheck(p, 2*time.Minute))
	}

	// M1 step 1: Provisioner. Gets the real batcher directly — no
	// SetEnqueuer hop needed now that Batcher is wired above.
	svc.Provisioner = newTrackProvisioner(deps.Store, svc.Batcher, deps.Config)

	// M1 step 3: PositionSync. Reads/writes admin user positions and
	// pushes bookmark updates via the batcher.
	svc.Positions = newPositionSync(deps.Store, svc.Batcher)

	// M1 step 4: PlaylistSync. Imports smart playlists from the ITL
	// and pushes dirty playlists back out. Pushes use the batcher.
	svc.Playlists = newPlaylistSync(deps.Store, svc.Batcher)

	// M1 step 5: PathReconciler. Backfill operation that fixes up
	// iTunes paths after library reorganizations.
	svc.Paths = newPathReconciler(deps.Store, svc.Batcher)

	// PathRepairer. Recovers cases where iTunes still references a
	// stale on-disk path after organize: dumps the iTunes XML, finds
	// missing locations, and re-discovers them via PID lookup,
	// embedded tag scan, or fuzzy match. Apply mode enqueues fixes
	// through the same Batcher.
	svc.Repair = newPathRepairer(deps.Store, svc.Batcher, PathRepairConfig{
		XMLPath:       deps.Config.LibraryReadPath,
		AudiobookRoot: deps.AudiobookRoot,
		ReportDir:     deps.ReportDir,
	})

	// M1 step 6: TransferService. ITL download/upload/backup/restore
	// handlers. No deps — keyed off config.AppConfig.
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

// Start launches any long-lived sub-component goroutines (currently just
// the WriteBackBatcher, wired in PR 2's step 2f). No-op when disabled.
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
