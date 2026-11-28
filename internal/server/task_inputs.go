// file: internal/server/task_inputs.go
// version: 0.1.0
// guid: 1153bb7f-1c57-4218-941a-2c5541fd7d83
// last-edited: 2026-11-28

package server

import (
	"context"
	"errors"
	"fmt"

	"github.com/falkcorp/audiobook-organizer/internal/ai"
	database "github.com/falkcorp/audiobook-organizer/internal/books/store"
	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/deluge"
	"github.com/falkcorp/audiobook-organizer/internal/metabatch"
	"github.com/falkcorp/audiobook-organizer/internal/metadata"
	"github.com/falkcorp/audiobook-organizer/internal/operations"
	maintenanceplugin "github.com/falkcorp/audiobook-organizer/internal/plugins/maintenance"
	"github.com/falkcorp/audiobook-organizer/internal/repairs"
	"github.com/falkcorp/audiobook-organizer/internal/scheduler"
	"github.com/falkcorp/audiobook-organizer/internal/serviceregistry"

	acoustidplugin "github.com/falkcorp/audiobook-organizer/internal/plugins/acoustid"
	delugeplugin "github.com/falkcorp/audiobook-organizer/internal/plugins/deluge"
	metafetchplugin "github.com/falkcorp/audiobook-organizer/internal/plugins/metafetch"
)

// TaskInputs is what the books module builds its task definitions from
// (books.bookTaskLists) that only the server holds: each plugin's own narrow
// BooksTaskInputs over the services NewServer built. The fields the server does
// not own (the run store, the plan-row reader, the schedule hook and the
// scan stand-down key) are left for the books module to set.
//
// Every value is the same service HEAD's operation bodies read, passed as the
// narrow interface its package declares, never database.Store. A service HEAD
// tolerated being absent (the metadata fetch service) becomes an input whose
// every use refuses with HEAD's own message, not a nil that fails the
// whole registration at boot.
type TaskInputs struct {
	Maintenance maintenanceplugin.BooksTaskInputs
	Metafetch   metafetchplugin.BooksTaskInputs
	Deluge      delugeplugin.BooksTaskInputs
	Acoustid    acoustidplugin.BooksTaskInputs
	Scheduler   scheduler.BooksTaskInputs
	Repairs     repairs.BooksTaskInputs
}

// TaskInputs builds the server's share of the task-list inputs. It reads no
// request state and starts nothing; it can be called once NewServer has
// returned.
func (s *Server) TaskInputs() (TaskInputs, error) {
	store := s.storeForWiring()
	// The two inputs the wide interface does not carry are capabilities of the
	// store behind it (the server's store is a decorator that hides them from a
	// bare assertion, database.AsCapability looks through it). A store without
	// them is a build that cannot run those kinds at all, so it is an error here
	// and not a kind that fails at its first run.
	delugeSrc, ok := database.AsCapability[delugeplugin.KindSource](store)
	if !ok {
		return TaskInputs{}, fmt.Errorf("server: the store %T cannot serve the Deluge kinds", store)
	}
	userState, ok := database.AsCapability[repairs.UserStateStore](store)
	if !ok {
		return TaskInputs{}, fmt.Errorf("server: the store %T cannot serve the Repairs user-state writer", store)
	}
	mfs := s.metadataFetchService

	var candidates metafetchplugin.CandidateReads = unavailableCandidates{}
	var buildChain func()
	var upgrader scheduler.MetadataUpgrader = unavailableUpgrader{}
	if mfs != nil {
		candidates = mfs
		buildChain = func() { mfs.BuildSourceChain() }
		upgrader = metabatch.NewMetadataUpgradeService(store, mfs)
	}

	// The registered AcoustID plugin shares its worker hub, scan probe and tool
	// resolver with the window kind; a build without one (embedding off, the
	// service not registered) gets a plugin with no engine, whose kinds no-op
	// as they did.
	acoustid, ok := serviceregistry.TryGet[*acoustidplugin.Plugin](s.container, "acoustidplugin")
	if !ok || acoustid == nil {
		acoustid = acoustidplugin.New(nil, store, nil)
	}

	return TaskInputs{
		Maintenance: maintenanceplugin.BooksTaskInputs{
			Jobs:     store,
			G2Source: store,
			G2Host:   s,
			RootDir:  s.RootDir,
			G3Source: store,
			G3Host:   s,
			// The tag reader the credits backfill's franchise guard reads. The
			// store, as HEAD's repairsGuardTags passes it.
			GuardTags: s.BookTagReader(),
			Ops:       store,
			G4Host:    s,
			// Scan nil is HEAD's degraded caller (no scanner to stand down): a
			// native kind stands the scan down through rc.StandDown over the key
			// the books module passes, not through the server's registry.
			AuxStores: s,
			G7: maintenanceplugin.G7Inputs{
				Reads:                 store,
				Writes:                store,
				UserMerge:             s.UserMerge(),
				BulkWriteBack:         s.RunBulkWriteBack,
				InvalidateSeriesCache: s.InvalidateSeriesCache,
				SupportedExtensions:   func() []string { return config.SupportedExtensionSet().Sorted() },
			},
		},
		Metafetch: metafetchplugin.BooksTaskInputs{
			Source:           store,
			Candidates:       candidates,
			Scoring:          func() config.MetadataScoringConfig { return config.Snapshot().MetadataScoring },
			NewAudible:       func() metafetchplugin.AudibleSearcher { return metadata.NewAudibleClient() },
			BuildSourceChain: buildChain,
		},
		Deluge: delugeplugin.BooksTaskInputs{
			Source: delugeSrc,
			// nil when Deluge is not configured, as at HEAD (centralize then imports
			// without moving torrent storage). The client is the one NewServer's
			// protected-path cache was built over.
			Client: deluge.GetClient(),
			Cache:  s.protectedPathCache,
			Config: &config.AppConfig,
		},
		Acoustid: acoustidplugin.BooksTaskInputs{
			Plugin: acoustid,
			Source: store,
			// Candidates and NewEngine stay nil: acoustid.scan refuses and the
			// scan summary and reset pass skip their candidate step, as HEAD's did
			// with embedding off. The engine writes candidates through its own
			// stores, so a native run needs the gated engine the dedup builder
			// (F5-18 A6d) hands it; passing s.dedupEngine here would write
			// outside the run's gate.
		},
		Scheduler: scheduler.BooksTaskInputs{Upgrader: upgrader},
		Repairs: repairs.BooksTaskInputs{
			Fixers:    maintenanceplugin.New(s).Repairs().List(),
			Guard:     store,
			Tags:      s.BookTagReader(),
			Series:    seriesNamerOver(store),
			Stores:    store,
			TagWriter: store,
			UserState: userState,
			BindStores: func(ctx context.Context) context.Context {
				return repairs.WithStores[maintenanceplugin.FixerStores](ctx, s)
			},
		},
	}, nil
}

// seriesNamerOver resolves a series id through the store on each call. HEAD
// built the namer once per run from the full series list; the inputs here are
// built once for the process, so a per-call read keeps a rename visible and
// costs one keyed read per guarded book.
func seriesNamerOver(st interface {
	GetSeriesByID(id int) (*database.Series, error)
}) repairs.SeriesNamer {
	return func(id int) string {
		if sr, err := st.GetSeriesByID(id); err == nil && sr != nil {
			return sr.Name
		}
		return ""
	}
}

// AIBatchSubmitter is maintenance.G4BootHost's submitter of the author dedup
// batch: HEAD built an OpenAI parser from the live configuration for each run
// (runAIDedupBatch). nil, the untyped nil the caller tests for, when AI parsing
// is not enabled.
func (s *Server) AIBatchSubmitter() maintenanceplugin.AIBatchSubmitter {
	cfg := config.Snapshot()
	parser := ai.NewOpenAIParser(&cfg, cfg.OpenAIAPIKey, cfg.EnableAIParsing)
	if !parser.IsEnabled() {
		// The untyped nil: a nil *OpenAIParser in the interface would pass the
		// caller's nil check and be called.
		return nil
	}
	return parser
}

// ScheduleHooks are the live checks books' schedule gates read (books.ScheduleDeps)
// that only the server can answer. They are HEAD's SchedulerDeps hooks of the
// same names: the schedules decide the same way over the same services.
type ScheduleHooks struct {
	HasDedupPlugin            func() bool
	HasActivitySvc            func() bool
	HasMetadataFetchSvc       func() bool
	IdentificationPending     func() (bool, error)
	CollectionsRefreshPending func() (bool, error)
}

// ScheduleHooks returns the hooks over this server. Each reads the server's
// services when called, not now, so one built before the server's start still
// answers for the services that exist then.
func (s *Server) ScheduleHooks() ScheduleHooks {
	d := s.schedulerDeps()
	return ScheduleHooks{
		HasDedupPlugin:            d.HasDedupPlugin,
		HasActivitySvc:            d.HasActivitySvc,
		HasMetadataFetchSvc:       d.HasMetadataFetchSvc,
		IdentificationPending:     s.identificationPending,
		CollectionsRefreshPending: d.CollectionsRefreshPending,
	}
}

// identDirtyPager is the store capability the identification-advance schedule
// reads (database.PebbleStore.IdentDirtyPage).
type identDirtyPager interface {
	IdentDirtyPage(after string, limit int) ([]database.IdentDirtyEntry, error)
}

// identificationPending is the scheduler's identDirtySetNonEmpty for the books
// schedule: whether the identification dirty set holds a book (one key read).
// A store that does not expose the set answers true, so the schedule fires and
// the op refuses with the reason in its log rather than the schedule skipping
// in silence forever. No store answers false.
func (s *Server) identificationPending() (bool, error) {
	st := s.storeForWiring()
	if st == nil {
		return false, nil
	}
	pager, ok := database.AsCapability[identDirtyPager](st)
	if !ok {
		return true, nil
	}
	page, err := pager.IdentDirtyPage("", 1)
	if err != nil {
		return false, err
	}
	return len(page) > 0, nil
}

// unavailableCandidates stands for the absent metadata fetch service as
// calibrate-scoring's candidate read: the run refuses with HEAD's message.
type unavailableCandidates struct{}

func (unavailableCandidates) GetCachedCandidates(string) (*database.MetadataCandidateCache, bool, error) {
	return nil, false, errors.New("metafetch service not available")
}

// unavailableUpgrader stands for the absent metadata fetch service as the
// upgrade sweep: the run refuses with HEAD's message.
type unavailableUpgrader struct{}

func (unavailableUpgrader) RunUpgrade(context.Context, int, operations.ProgressReporter) (*metabatch.UpgradeResult, error) {
	return nil, errors.New("metadata fetch service not initialized")
}
