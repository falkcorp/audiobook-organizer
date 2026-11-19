// file: internal/server/catalog_harvest_op.go
// version: 1.9.0
// guid: 3e6b9d24-7a1c-4f85-b2e0-5c8d1a4f7e93
// last-edited: 2026-11-18
//
// Registers catalog.harvest-authors, phase P1 of the author catalog design
// (.claude/notes/catalog-wanted-requests-design-2026-10-01.md, Draft 3).

package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"golang.org/x/time/rate"

	"github.com/falkcorp/mediacore/tasks"

	"github.com/falkcorp/audiobook-organizer/internal/auth"
	"github.com/falkcorp/audiobook-organizer/internal/authority"
	database "github.com/falkcorp/audiobook-organizer/internal/books/store"
	"github.com/falkcorp/audiobook-organizer/internal/catalog"
	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/logger"
	"github.com/falkcorp/audiobook-organizer/internal/metadata"
	"github.com/falkcorp/audiobook-organizer/internal/metadata/providerhttp"
	"github.com/falkcorp/audiobook-organizer/internal/metrics"
	"github.com/falkcorp/audiobook-organizer/internal/operations/opmode"
	opsregistry "github.com/falkcorp/audiobook-organizer/internal/operations/registry"
	"github.com/falkcorp/audiobook-organizer/pkg/ops"
)

const catalogHarvestOpID = "catalog.harvest-authors"

// catalogHarvestMinCheckpointInterval arms the watchdog's uncheckpointed
// strike (ResumeRestart + nonzero interval). Sized from the worst case: the
// run checkpoints after an author finishes, and one author is at most 40
// listing pages plus owned-ASIN lookups at a per-worker share of the
// sub-limiter, plus up to 5 minutes of Retry-After backoff per throttled
// request. 30 minutes covers that without striking a healthy run.
const catalogHarvestMinCheckpointInterval = 30 * time.Minute

// catalogHarvestParams are the op's params. Every field is optional.
//
// dry_run defaults to TRUE (owner rule 2026-09-25): the census reports
// author counts, how many have an ASIN-tagged book, estimated requests and
// estimated entries/bytes from a small page-0 sample, and writes nothing.
type catalogHarvestParams struct {
	DryRun      *bool `json:"dry_run,omitempty"`
	DryRunCamel *bool `json:"dryRun,omitempty"`
	// Authors restricts the run to these author names (the manual
	// single-author trigger). Named authors are harvested regardless of the
	// re-harvest interval.
	Authors []string `json:"authors,omitempty"`
	// ReharvestIntervalDays: a complete author is refetched after this many
	// days (default 30). Partial and failed authors are always retried.
	ReharvestIntervalDays int `json:"reharvest_interval_days,omitempty"`
	// Concurrency is the worker count (default 4, max 16). Every worker
	// shares one sub-limiter, so more workers do not mean more requests/s.
	Concurrency int `json:"concurrency,omitempty"`
	// SampleAuthors is how many authors the dry run samples page 0 of
	// (default 5; 0 = no network at all).
	SampleAuthors *int `json:"sample_authors,omitempty"`
}

var catalogHarvestLog = logger.New("catalog-harvest-op")

func init() {
	addOpRegistrar(func(s *Server, reg *opsregistry.Registry) error {
		return s.RegisterCatalogHarvestOp(reg)
	})
}

// RegisterCatalogHarvestOp registers catalog.harvest-authors. It is
// registered whether or not catalog.enabled is set (the op-id ledger needs
// the def to exist); Run refuses while the flag is off.
func (s *Server) RegisterCatalogHarvestOp(reg *opsregistry.Registry) error {
	catalogHarvestLog.Info("startup: catalog.enabled=%v (author catalog op and /catalog routes are %s)",
		config.AppConfig.Catalog.Enabled, map[bool]string{true: "live", false: "refusing"}[config.AppConfig.Catalog.Enabled])
	return reg.RegisterOp(s.catalogHarvestDef())
}

// catalogHarvestDef is HEAD's definition of catalog.harvest-authors. Its Run is
// the v2 shell.
func (s *Server) catalogHarvestDef() opsregistry.OperationDef {
	return opsregistry.OperationDef{
		ID:          catalogHarvestOpID,
		Preview:     opsregistry.PreviewSpec{Params: catalogHarvestParams{}},
		Liveness:    opsregistry.LivenessRunItems,
		Plugin:      "catalog",
		DisplayName: "Harvest Author Catalogs",
		Description: "Lists every Audible title by each owned author into the author catalog (cat:* keys only; never touches books or files). Dry run by default: census and estimates only.",
		// Expected WARNs (not an outage): a legitimately empty first harvest
		// -- never-harvested authors whose Audible listing really is empty --
		// trips the zero-total breaker, or leaves the run too small to judge,
		// on every run until those authors' empty streak spans
		// catalog.ZeroTotalAcceptWindow (7 days). With a daily schedule that
		// is a breaker-trip WARN per day for up to 7 days. The message splits
		// the zero answers into first-time and repeat-zero: a run whose zeros
		// are mostly repeat-zero is those empty authors working through their
		// window; an outage shows up as first-time zeros from authors that
		// have entries on file.
		//
		// Cancellable: an author interrupted mid-listing is recorded partial
		// and retried next run.
		Cancellable:     true,
		DefaultPriority: opsregistry.PriorityLow,
		Timeout:         24 * time.Hour,
		// Resume skips by per-author state, not by slice position: the
		// author list is rebuilt from the library on every start, so an
		// index into it would point at a different author after any import.
		ResumePolicy:          opsregistry.ResumeRestart,
		MinCheckpointInterval: catalogHarvestMinCheckpointInterval,
		ConcurrencyKey:        "catalog.harvest",
		// The harvest, metadata.candidate-fetch, metafetch.asin-backfill
		// and identification.advance all spend Audible's token bucket;
		// DependsOn is one-directional ("must not be running for THIS op to
		// start"), so each of the others carries the reverse entry for
		// catalog.harvest-authors.
		//
		// catalog.coverage-report is listed too: it reads the author blocks
		// this op writes and itself depends on this op, so each refuses to
		// start while the other runs (the write-set gate does not consult
		// Reads, so DependsOn is the only way to hold the harvest back).
		DependsOn:    []string{"metadata.candidate-fetch", "metafetch.asin-backfill", identificationAdvanceOpID, catalogCoverageOpID},
		Writes:       []opsregistry.Resource{opsregistry.ResCatalog},
		Permissions:  []auth.Permission{auth.PermSettingsManage},
		Capabilities: []opsregistry.Capability{opsregistry.CapLibraryRead, opsregistry.CapLibraryWrite, opsregistry.CapNetworkAudible},
		Run:          s.runCatalogHarvestOp,
	}
}

// catalogHarvestRPS is the harvest's sub-limiter rate: a fraction of
// Audible's EFFECTIVE budget, read at op start because metafetch installs
// the configured budget (SetLimits) after registration.
func catalogHarvestRPS(fraction float64) float64 {
	if fraction <= 0 || fraction > 1 {
		fraction = 0.5
	}
	return providerhttp.EffectiveLimitsFor(metadata.SourceIDAudible).RPS * fraction
}

// errCatalogDisabled is returned by Run while catalog.enabled is off.
var errCatalogDisabled = errors.New("catalog.harvest-authors: catalog.enabled is false; enable the author catalog in settings first")

func (s *Server) runCatalogHarvestOp(ctx context.Context, raw json.RawMessage, reporter opsregistry.Reporter) error {
	cfg := config.AppConfig.Catalog
	if err := catalogHarvestStart(cfg); err != nil {
		return err
	}
	var p catalogHarvestParams
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &p); err != nil {
			return fmt.Errorf("%s: decode params: %w", catalogHarvestOpID, err)
		}
	}
	dryRun, err := opmode.ResolveDryRun(catalogHarvestOpID, p.DryRun, p.DryRunCamel)
	if err != nil {
		return err
	}
	store := s.storeForWiring()
	cat := database.NewCatalogStoreFromStore(store)
	if cat == nil {
		return fmt.Errorf("%s: catalog store unavailable (store is not Pebble-backed)", catalogHarvestOpID)
	}
	return runCatalogHarvest(ctx, reporter, store, cat, metadata.NewAudibleClient(), metadata.DefaultThrottleRegistry(), p, dryRun, cfg)
}

// catalogHarvestStart logs the run's start and refuses it while catalog.enabled
// is off. Both shells call it before reading params.
func catalogHarvestStart(cfg config.CatalogConfig) error {
	catalogHarvestLog.Info("op start: catalog.enabled=%v language=%q marketplace=%q", cfg.Enabled, cfg.Language, cfg.Marketplace)
	if !cfg.Enabled {
		return errCatalogDisabled
	}
	return nil
}

// runCatalogHarvest is the op body with its dependencies injected, so the
// tests can drive it with a fixture lister, a real Pebble store and a private
// throttle registry. Production passes metadata.DefaultThrottleRegistry(), the
// process-wide one every other Audible caller shares; a test must NOT, because
// the harvest both reads that registry (a hold stands the whole run down) and
// writes to it (a provider error records one), so a hold left by any earlier
// test in the binary failed the run.
//
// It is HEAD's shell over catalogHarvestCore.
func runCatalogHarvest(ctx context.Context, reporter opsregistry.Reporter, store database.Store, cat *database.CatalogStore,
	lister metadata.AuthorLister, throttles *metadata.ThrottleRegistry, p catalogHarvestParams, dryRun bool, cfg config.CatalogConfig) error {
	h := headCatalogHarvest{store: store, cat: cat, lister: lister, throttles: throttles, reporter: reporter}
	env := catalogHarvestEnv{t7Env: t7HeadEnv(reporter, !dryRun), Store: h, Writer: h}
	// The checkpoint is HEAD's own params, exactly what HEAD saved; nothing
	// restores it (resume is by per-author state), it is the liveness save.
	env.Checkpoint = func(any) error { return reporter.Checkpoint(p) }
	return catalogHarvestCore(ctx, catalogHarvestNativeParams{
		Authors:               p.Authors,
		ReharvestIntervalDays: p.ReharvestIntervalDays,
		Concurrency:           p.Concurrency,
		SampleAuthors:         p.SampleAuthors,
	}, env, cfg)
}

// catalogHarvestNativeParams are the native kind's params: HEAD's without the
// two dry-run spellings, which the run's mode carries.
type catalogHarvestNativeParams struct {
	Authors               []string `json:"authors,omitempty"`
	ReharvestIntervalDays int      `json:"reharvest_interval_days,omitempty"`
	Concurrency           int      `json:"concurrency,omitempty"`
	SampleAuthors         *int     `json:"sample_authors,omitempty"`
}

// catalogHarvestOpen is what the harvester is built from: the run's settings
// that are computed from the params and config, not the engine's own wiring.
type catalogHarvestOpen struct {
	Cfg         config.CatalogConfig
	RPS         float64
	Interval    time.Duration
	Concurrency int
	OpID        string
}

// catalogHarvestStore is the reads of catalog.harvest-authors.
type catalogHarvestStore interface {
	// CatalogReady refuses a store that has no author catalog (one that is not
	// Pebble-backed).
	CatalogReady() error
	// CatalogScope is the in-scope authors and the census of how they were chosen.
	CatalogScope(ctx context.Context) ([]catalog.ScopeAuthor, catalog.ScopeCensus, error)
	// NewHarvester builds the harvester: the provider client, its sub-limiter
	// and throttle hold. SelectDue and EstimateRun only read the catalog's
	// per-author state; the run's writes go through catalogHarvestWriter.
	NewHarvester(open catalogHarvestOpen) *catalog.Harvester
}

// catalogHarvestWriter is the writes of catalog.harvest-authors (resource
// catalog; never books or files): the harvest's per-author listing and state
// writes, the marks it leaves for identification.advance, the gauges and the
// alias backfill.
type catalogHarvestWriter interface {
	// RunHarvest harvests the due authors, calling checkpoint as the run's
	// liveness save, and marks the books of an author whose catalog changed for
	// identification. It returns the tally and the run's error.
	RunHarvest(ctx context.Context, h *catalog.Harvester, due []catalog.ScopeAuthor, checkpoint func() error) (*catalog.Tally, error)
	// FinishHarvest publishes the catalog gauges and files the author alias
	// rows. A failure is logged, never fails the run.
	FinishHarvest(ctx context.Context)
}

// catalogHarvestEnv is what the shared core takes from its shell (rules R2, R10).
// Live is false for the dry run, which samples and writes nothing. The
// harvester runs its own worker pool (HEAD's RunItems at p.Concurrency), so the
// env carries none.
type catalogHarvestEnv struct {
	t7Env
	Store  catalogHarvestStore
	Writer catalogHarvestWriter
}

// catalogHarvestCore is the body of catalog.harvest-authors, shared by HEAD's
// shell and the native kind.
func catalogHarvestCore(ctx context.Context, p catalogHarvestNativeParams, env catalogHarvestEnv, cfg config.CatalogConfig) error {
	if err := env.Store.CatalogReady(); err != nil {
		return err
	}
	authors, census, err := env.Store.CatalogScope(ctx)
	if err != nil {
		return err
	}
	force := map[string]bool{}
	if len(p.Authors) > 0 {
		authors = catalog.FilterAuthors(authors, p.Authors)
		for _, a := range authors {
			force[a.Key] = true
		}
		if len(authors) == 0 {
			return fmt.Errorf("%s: none of %q is an in-scope author (an author of a live book with a present file)", catalogHarvestOpID, p.Authors)
		}
	}

	rps := catalogHarvestRPS(cfg.HarvestRateFraction)
	interval := catalog.DefaultReharvestInterval
	if p.ReharvestIntervalDays > 0 {
		interval = time.Duration(p.ReharvestIntervalDays) * 24 * time.Hour
	}
	h := env.Store.NewHarvester(catalogHarvestOpen{Cfg: cfg, RPS: rps, Interval: interval, Concurrency: p.Concurrency, OpID: env.OpID})
	due, err := h.SelectDue(authors, force)
	if err != nil {
		return err
	}
	env.Log(slog.LevelInfo, fmt.Sprintf("scope: %d authors (%d with an ASIN-tagged book; skipped: %d junk, %d publisher-shaped, %d role-marked; %d role-marked credit lists split), %d books in scope, %d without an author; %d due (interval %s); dry_run=%v; harvest rate %.2f req/s (fraction %.2f of audible), concurrency %d",
		census.Authors, census.AuthorsWithASIN, census.JunkAuthorsSkipped, census.PublisherShapedSkipped,
		census.RoleMarkedSkipped, census.RoleMarkedSplit, census.BooksInScope, census.BooksNoAuthor,
		len(due), interval, !env.Live, rps, cfg.HarvestRateFraction, h.Cfg.Concurrency))

	if !env.Live {
		sampleN := 5
		if p.SampleAuthors != nil {
			sampleN = max(*p.SampleAuthors, 0)
		}
		est := h.EstimateRun(ctx, census, due, sampleN)
		b, err := json.Marshal(est)
		if err != nil {
			return fmt.Errorf("%s: encode census: %w", catalogHarvestOpID, err)
		}
		env.Log(slog.LevelInfo, "dry run census (nothing written): "+string(b))
		catalogHarvestLog.Info("dry run census: %s", string(b))
		env.progress(1, 1, fmt.Sprintf("dry run: %d authors due, ~%d requests, ~%d entries", est.AuthorsDue, est.EstimatedRequests, est.EstimatedEntries))
		return nil
	}

	tally, runErr := env.Writer.RunHarvest(ctx, h, due, func() error { return env.Checkpoint(p) })
	env.Writer.FinishHarvest(ctx)
	summary := fmt.Sprintf("harvested %d/%d authors — %s", tally.Done.Load(), len(due), tally.Summary())
	env.Log(slog.LevelInfo, summary)
	catalogHarvestLog.Info("%s", summary)
	return runErr
}

// headCatalogHarvest is HEAD's side of both interfaces: the engine's own
// wiring, exactly as HEAD built it.
// headHarvestStore is what HEAD's adapter reads from the server's store: the
// scope's reads and the author-to-books lookup the identification hook uses. The
// value stays the server's own store, so database.AsCapability still finds the
// authority scanner on it.
type headHarvestStore interface {
	authorBooksLister
	catalog.ScopeReader
}

type headCatalogHarvest struct {
	store     headHarvestStore
	cat       *database.CatalogStore
	lister    metadata.AuthorLister
	throttles *metadata.ThrottleRegistry
	reporter  opsregistry.Reporter
}

func (h headCatalogHarvest) CatalogReady() error {
	if h.cat == nil {
		return fmt.Errorf("%s: catalog store unavailable (store is not Pebble-backed)", catalogHarvestOpID)
	}
	return nil
}

func (h headCatalogHarvest) CatalogScope(ctx context.Context) ([]catalog.ScopeAuthor, catalog.ScopeCensus, error) {
	return catalog.BuildScope(ctx, h.store, h.store)
}

func (h headCatalogHarvest) NewHarvester(o catalogHarvestOpen) *catalog.Harvester {
	throttles := h.throttles
	return catalog.NewHarvester(h.lister, h.cat, catalog.Settings{
		Provider:          metadata.SourceIDAudible,
		Marketplace:       o.Cfg.Marketplace,
		Language:          o.Cfg.Language,
		MaxProducts:       o.Cfg.MaxProductsPerAuthor,
		ReharvestInterval: o.Interval,
		Concurrency:       o.Concurrency,
		Limiter:           rate.NewLimiter(rate.Limit(o.RPS), 1),
		Throttled:         func() bool { return throttles.Throttled(metadata.SourceIDAudible) },
		OnProviderError: func(err error) {
			throttles.RecordFailure(metadata.SourceIDAudible, err)
		},
		OpID: o.OpID,
	})
}

func (h headCatalogHarvest) RunHarvest(ctx context.Context, hv *catalog.Harvester, due []catalog.ScopeAuthor, checkpoint func() error) (*catalog.Tally, error) {
	return hv.Run(ctx, h.reporter, due, catalog.RunOptions{
		Checkpoint:   checkpoint,
		OnAuthorDone: harvestMarksIdentDirty(h.store),
	})
}

func (h headCatalogHarvest) FinishHarvest(ctx context.Context) {
	publishCatalogGauges(h.cat)
	var scanner authority.Scanner
	if sc, ok := database.AsCapability[authority.Scanner](h.store); ok {
		scanner = sc
	}
	backfillCatalogAliases(ctx, h.reporter, h.cat, scanner)
}

// catalogHarvestNative is the native books.catalog.harvest-authors kind. The mode
// is the start request's: Preview is HEAD's default dry run (a census from a
// small page-0 sample), Live harvests. It writes the author catalog only
// (resource catalog). The harvester runs its own pool at p.Concurrency.
func (s *Server) catalogHarvestNative(deps t7Deps, sched func(tasks.Kind) *tasks.Schedule) tasks.Definition {
	kind := t7KindFor(catalogHarvestOpID)
	c := t7Common(s.catalogHarvestDef(), sched)
	c.Effects = t7Writes(ops.ResCatalog)
	return tasks.Task(kind, tasks.TaskSpec[json.RawMessage, struct{}]{
		Common: c,
		Run: func(rc *tasks.RC, raw json.RawMessage) (struct{}, error) {
			cfg := config.AppConfig.Catalog
			p, err := t7DecodeNative[catalogHarvestNativeParams](kind, raw)
			if err != nil {
				return struct{}{}, err
			}
			if err := t7NeedStore(kind, deps); err != nil {
				return struct{}{}, err
			}
			env := catalogHarvestEnv{t7Env: t7NativeEnv(rc), Store: deps.Store}
			// A Preview run writes nothing, so it builds no writer.
			if env.Live {
				w, err := t7NewWriter(kind, deps, rc)
				if err != nil {
					return struct{}{}, err
				}
				env.Writer = w
			}
			if err := catalogHarvestStart(cfg); err != nil {
				return struct{}{}, err
			}
			return struct{}{}, catalogHarvestCore(rc.Context(), p, env, cfg)
		},
	})
}

// identStateReader is the store capability harvestMarksIdentDirty reads.
type identStateReader interface {
	IdentStateFor(bookID string) (database.IdentStateView, bool, error)
}

// authorBooksLister is the store slice harvestMarksIdentDirty lists an
// author's books through.
type authorBooksLister interface {
	GetBooksByAuthorIDCore(authorID int) ([]database.BookCore, error)
}

// harvestMarksIdentDirty returns the harvest's per-author hook that marks
// books for the identification driver (identification.advance): when an
// author's harvest completes AND changed the author's catalog (new entries,
// or entries marked stale), each of the author's primary books that is still
// unanswered (ident unasked or asked_empty) is marked dirty, because the
// catalog stage may answer it now. Books already holding candidates, applied
// or held are left alone. Nil (no hook) when the store has no
// identification index.
//
// It runs inside the harvest's RunItems workers; every call it makes is safe
// for concurrent use, and MarkIdentDirtyBestEffort never fails the harvest.
func harvestMarksIdentDirty(store authorBooksLister) func(catalog.ScopeAuthor, database.CatalogAuthorState) {
	reader, ok := database.AsCapability[identStateReader](store)
	if !ok {
		return nil
	}
	return func(a catalog.ScopeAuthor, st database.CatalogAuthorState) {
		if st.State != database.CatalogHarvestComplete || (st.Created == 0 && st.MarkedStale == 0) {
			return
		}
		marked := 0
		for _, authorID := range a.AuthorIDs {
			books, err := store.GetBooksByAuthorIDCore(authorID)
			if err != nil {
				catalogHarvestLog.Warn("author %q: list books of author %d to mark for identification: %v",
					logger.SanitizeLogValue(a.Name), authorID, err)
				continue
			}
			for i := range books {
				v, ok, err := reader.IdentStateFor(books[i].ID)
				if err != nil || !ok || !v.Primary {
					continue
				}
				if v.State != database.IdentUnasked && v.State != database.IdentAskedEmpty {
					continue
				}
				if database.MarkIdentDirtyBestEffort(store, books[i].ID, "catalog harvest") {
					marked++
				}
			}
		}
		if marked > 0 {
			catalogHarvestLog.Info("author %q: catalog changed (created=%d stale=%d); marked %d unanswered books for identification",
				logger.SanitizeLogValue(a.Name), st.Created, st.MarkedStale, marked)
		}
	}
}

// backfillCatalogAliases files the author alias rows (cat_author:alias:) the
// bulk matcher resolves variant spellings through, for entries stored before
// the harvest wrote them and for the spellings the authority lists add. The
// harvest writes aliases for what it stores itself; this catches the rest. A
// failure is logged and reported to the op log, never fails the run: aliases
// only widen what the matcher can find.
func backfillCatalogAliases(ctx context.Context, reporter opsregistry.Reporter, cat *database.CatalogStore, scanner authority.Scanner) {
	if ctx.Err() != nil {
		return // a cancelled run is not the place to start a pass over the catalog
	}
	res, err := catalog.BackfillAuthorAliases(ctx, cat, scanner)
	if err != nil {
		catalogHarvestLog.Warn("author alias backfill: %v", err)
		_ = reporter.Log(slog.LevelWarn, "author alias backfill failed: "+logger.SanitizeLogValue(err.Error()))
		return
	}
	msg := fmt.Sprintf("author aliases: %d entries read, %d author spellings, %d spelling aliases and %d authority aliases considered, %d written",
		res.Entries, res.Names, res.SpellingPairs, res.AuthorityPairs, res.Written)
	_ = reporter.Log(slog.LevelInfo, msg)
	catalogHarvestLog.Info("%s", msg)
}

// publishCatalogGauges sets the R12 gauges from the stored state, so they
// describe the whole catalog rather than the run that just ended.
func publishCatalogGauges(cat *database.CatalogStore) {
	if byState, err := cat.CountAuthorStates(); err == nil {
		metrics.SetCatalogHarvestAuthors(byState)
	} else {
		catalogHarvestLog.Warn("harvest_authors gauge: %v", err)
	}
	if n, err := cat.CountStale(); err == nil {
		metrics.SetCatalogEntriesStale(n)
	} else {
		catalogHarvestLog.Warn("catalog_entries_stale gauge: %v", err)
	}
}
