// file: internal/server/catalog_harvest_op.go
// version: 1.8.1
// guid: 3e6b9d24-7a1c-4f85-b2e0-5c8d1a4f7e93
// last-edited: 2026-10-18
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

	"github.com/falkcorp/audiobook-organizer/internal/auth"
	"github.com/falkcorp/audiobook-organizer/internal/authority"
	"github.com/falkcorp/audiobook-organizer/internal/catalog"
	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/logger"
	"github.com/falkcorp/audiobook-organizer/internal/metadata"
	"github.com/falkcorp/audiobook-organizer/internal/metadata/providerhttp"
	"github.com/falkcorp/audiobook-organizer/internal/metrics"
	"github.com/falkcorp/audiobook-organizer/internal/operations/opmode"
	opsregistry "github.com/falkcorp/audiobook-organizer/internal/operations/registry"
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
	return reg.RegisterOp(opsregistry.OperationDef{
		ID:          catalogHarvestOpID,
		Preview:     opsregistry.PreviewSpec{Key: opsregistry.PreviewDryRun, Params: catalogHarvestParams{}},
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
	})
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
	catalogHarvestLog.Info("op start: catalog.enabled=%v language=%q marketplace=%q", cfg.Enabled, cfg.Language, cfg.Marketplace)
	if !cfg.Enabled {
		return errCatalogDisabled
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
	return runCatalogHarvest(ctx, reporter, store, cat, metadata.NewAudibleClient(), p, dryRun, cfg)
}

// runCatalogHarvest is the op body with its dependencies injected, so the
// tests can drive it with a fixture lister and a real Pebble store.
func runCatalogHarvest(ctx context.Context, reporter opsregistry.Reporter, store database.Store, cat *database.CatalogStore,
	lister metadata.AuthorLister, p catalogHarvestParams, dryRun bool, cfg config.CatalogConfig) error {
	authors, census, err := catalog.BuildScope(ctx, store, store)
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
	h := catalog.NewHarvester(lister, cat, catalog.Settings{
		Provider:          metadata.SourceIDAudible,
		Marketplace:       cfg.Marketplace,
		Language:          cfg.Language,
		MaxProducts:       cfg.MaxProductsPerAuthor,
		ReharvestInterval: interval,
		Concurrency:       p.Concurrency,
		Limiter:           rate.NewLimiter(rate.Limit(rps), 1),
		Throttled:         func() bool { return metadata.DefaultThrottleRegistry().Throttled(metadata.SourceIDAudible) },
		OnProviderError: func(err error) {
			metadata.DefaultThrottleRegistry().RecordFailure(metadata.SourceIDAudible, err)
		},
		OpID: opsregistry.ReporterOpID(reporter),
	})
	due, err := h.SelectDue(authors, force)
	if err != nil {
		return err
	}
	_ = reporter.Log(slog.LevelInfo, fmt.Sprintf("scope: %d authors (%d with an ASIN-tagged book; skipped: %d junk, %d publisher-shaped, %d role-marked; %d role-marked credit lists split), %d books in scope, %d without an author; %d due (interval %s); dry_run=%v; harvest rate %.2f req/s (fraction %.2f of audible), concurrency %d",
		census.Authors, census.AuthorsWithASIN, census.JunkAuthorsSkipped, census.PublisherShapedSkipped,
		census.RoleMarkedSkipped, census.RoleMarkedSplit, census.BooksInScope, census.BooksNoAuthor,
		len(due), interval, dryRun, rps, cfg.HarvestRateFraction, h.Cfg.Concurrency))

	if dryRun {
		sampleN := 5
		if p.SampleAuthors != nil {
			sampleN = max(*p.SampleAuthors, 0)
		}
		est := h.EstimateRun(ctx, census, due, sampleN)
		b, err := json.Marshal(est)
		if err != nil {
			return fmt.Errorf("%s: encode census: %w", catalogHarvestOpID, err)
		}
		_ = reporter.Log(slog.LevelInfo, "dry run census (nothing written): "+string(b))
		catalogHarvestLog.Info("dry run census: %s", string(b))
		_ = reporter.UpdateProgress(1, 1, fmt.Sprintf("dry run: %d authors due, ~%d requests, ~%d entries", est.AuthorsDue, est.EstimatedRequests, est.EstimatedEntries))
		return nil
	}

	tally, runErr := h.Run(ctx, reporter, due, catalog.RunOptions{
		Checkpoint:   func() error { return reporter.Checkpoint(p) },
		OnAuthorDone: harvestMarksIdentDirty(store),
	})
	publishCatalogGauges(cat)
	var scanner authority.Scanner
	if sc, ok := database.AsCapability[authority.Scanner](store); ok {
		scanner = sc
	}
	backfillCatalogAliases(ctx, reporter, cat, scanner)
	summary := fmt.Sprintf("harvested %d/%d authors — %s", tally.Done.Load(), len(due), tally.Summary())
	_ = reporter.Log(slog.LevelInfo, summary)
	catalogHarvestLog.Info("%s", summary)
	return runErr
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
