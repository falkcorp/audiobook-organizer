// file: internal/catalog/harvest.go
// version: 1.0.0
// guid: 7c2e4a90-1d6f-4b38-8e57-3a9b5c1f0d26
// last-edited: 2026-10-01

package catalog

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"golang.org/x/time/rate"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/logger"
	"github.com/falkcorp/audiobook-organizer/internal/metadata"
	"github.com/falkcorp/audiobook-organizer/internal/operations/registry"
)

var harvestLog = logger.New("catalog-harvest")

// ErrProviderStandDown ends an author's fetch when the provider is held off
// by the throttle registry (a 429/quota hold set by any metadata path). The
// author is recorded partial and retried on the next run.
var ErrProviderStandDown = errors.New("provider is throttled; harvest standing down")

// Defaults (Q5 and R10).
const (
	DefaultPageSize          = 50
	DefaultMaxProducts       = 2000
	DefaultReharvestInterval = 30 * 24 * time.Hour
	DefaultConcurrency       = 4
	MaxConcurrency           = 16
)

// Settings configures one harvest run.
type Settings struct {
	Provider          string
	Marketplace       string
	Language          string
	PageSize          int
	MaxProducts       int
	ReharvestInterval time.Duration
	Concurrency       int
	// Limiter is the harvest's own sub-limiter, waited on before every
	// provider request on top of the provider's shared token bucket. Nil
	// means no extra pacing (tests).
	Limiter *rate.Limiter
	// Throttled reports whether the provider is currently held off. Nil
	// means never.
	Throttled func() bool
	OpID      string
	Now       func() time.Time
}

func (s *Settings) normalize() {
	if s.Provider == "" {
		s.Provider = metadata.SourceIDAudible
	}
	if s.Marketplace == "" {
		s.Marketplace = "us"
	}
	if s.PageSize <= 0 || s.PageSize > 50 {
		s.PageSize = DefaultPageSize
	}
	if s.MaxProducts <= 0 {
		s.MaxProducts = DefaultMaxProducts
	}
	if s.ReharvestInterval <= 0 {
		s.ReharvestInterval = DefaultReharvestInterval
	}
	if s.Concurrency < 1 {
		s.Concurrency = DefaultConcurrency
	}
	s.Concurrency = min(s.Concurrency, MaxConcurrency)
	if s.Now == nil {
		s.Now = time.Now
	}
}

// Harvester fetches author listings and writes catalog entries.
type Harvester struct {
	Lister metadata.AuthorLister
	Store  *database.CatalogStore
	Cfg    Settings
}

// NewHarvester builds a harvester with normalized settings.
func NewHarvester(l metadata.AuthorLister, st *database.CatalogStore, cfg Settings) *Harvester {
	cfg.normalize()
	return &Harvester{Lister: l, Store: st, Cfg: cfg}
}

// Due reports whether an author needs a harvest: never harvested, last run
// partial or failed (retried regardless of the interval, R10), or complete
// longer ago than the interval. force (the manual single-author trigger)
// always harvests.
func Due(prev *database.CatalogAuthorState, now time.Time, interval time.Duration, force bool) bool {
	if force || prev == nil {
		return true
	}
	if prev.State != database.CatalogHarvestComplete || prev.LastCompleteAt == nil {
		return true
	}
	return now.Sub(*prev.LastCompleteAt) >= interval
}

// waitTurn paces one provider request: stand-down check, then the
// sub-limiter.
func (h *Harvester) waitTurn(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if h.Cfg.Throttled != nil && h.Cfg.Throttled() {
		return ErrProviderStandDown
	}
	if h.Cfg.Limiter != nil {
		if err := h.Cfg.Limiter.Wait(ctx); err != nil {
			return err
		}
	}
	return nil
}

// fetchResult is one author's raw listing.
type fetchResult struct {
	products map[string]metadata.CatalogProduct
	order    []string
	pages    int
	raw      int
	total    int
	capped   bool
	err      error
}

// fetchAll pages through an author listing: page 0, 1, ... until the
// provider's total_results is reached, an empty page arrives, or the cap is
// hit (counted on RAW products received, not on kept ones). ASINs are
// deduped across pages.
func (h *Harvester) fetchAll(ctx context.Context, name string, touch func()) fetchResult {
	r := fetchResult{products: map[string]metadata.CatalogProduct{}}
	for page := 0; ; page++ {
		if err := h.waitTurn(ctx); err != nil {
			r.err = err
			return r
		}
		pg, err := h.Lister.ListByAuthor(ctx, name, page, h.Cfg.PageSize)
		if err != nil {
			r.err = err
			return r
		}
		if touch != nil {
			touch()
		}
		r.pages++
		r.total = pg.TotalResults
		r.raw += len(pg.Products)
		for _, p := range pg.Products {
			if p.ASIN == "" {
				continue
			}
			if _, dup := r.products[p.ASIN]; !dup {
				r.order = append(r.order, p.ASIN)
			}
			r.products[p.ASIN] = p
		}
		if len(pg.Products) == 0 || r.raw >= pg.TotalResults {
			return r
		}
		if r.raw >= h.Cfg.MaxProducts {
			r.capped = true
			return r
		}
	}
}

// HarvestAuthor fetches, disambiguates, filters and stores one author's
// catalog, and writes its state. touch is called after each provider
// response (liveness inside a long author). The returned state is also
// persisted; the error is non-nil only for a store failure or cancellation.
func (h *Harvester) HarvestAuthor(ctx context.Context, a ScopeAuthor, touch func()) (database.CatalogAuthorState, error) {
	now := h.Cfg.Now().UTC()
	st := database.CatalogAuthorState{Key: a.Key, Name: a.Name, LastAttemptAt: now, OpID: h.Cfg.OpID}
	var prevASINs []string
	if prev, err := h.Store.GetAuthorState(a.Key); err != nil {
		return st, fmt.Errorf("catalog harvest %q: read state: %w", a.Name, err)
	} else if prev != nil {
		st.LastCompleteAt = prev.LastCompleteAt
		prevASINs = prev.AuthorASINs
	}

	fr := h.fetchAll(ctx, a.Name, touch)
	st.PagesDone, st.Fetched, st.TotalResults, st.Capped = fr.pages, fr.raw, fr.total, fr.capped
	if fr.capped {
		harvestLog.Warn("author %q hit the %d-product cap (provider total %d); entries kept, nothing marked stale",
			logger.SanitizeLogValue(a.Name), h.Cfg.MaxProducts, fr.total)
	}

	lookup := func(ctx context.Context, asin string) (*metadata.CatalogProduct, error) {
		if err := h.waitTurn(ctx); err != nil {
			return nil, err
		}
		p, err := h.Lister.LookupProduct(ctx, asin)
		if touch != nil {
			touch()
		}
		return p, err
	}
	// A fetch that stopped early still resolves from what it did receive, but
	// makes no lookups (the provider just failed or stood down).
	if fr.err != nil {
		lookup = nil
	}
	res, resErr := ResolveAuthorASINs(ctx, a.Name, a.OwnedASINs, fr.products, lookup)
	if (fr.err != nil || resErr != nil || res.LookupErrors > 0) && prevASINs != nil {
		// An incomplete resolution must not downgrade entries an earlier
		// complete run confirmed by ASIN to name_only (or keep a homonym the
		// earlier run dropped): fall back to the identities already known.
		for _, asin := range prevASINs {
			if !slices.Contains(res.AuthorASINs, asin) {
				res.AuthorASINs = append(res.AuthorASINs, asin)
			}
		}
		slices.Sort(res.AuthorASINs)
		res.Conflict = len(res.AuthorASINs) > 1
	}
	st.AuthorASINs, st.Conflict = res.AuthorASINs, res.Conflict

	// Keep what was fetched even when the fetch stopped early: those rows are
	// real. Stale marking below is what a partial fetch must never do.
	items := make([]database.CatalogUpsert, 0, len(fr.order))
	langDropped := 0
	for _, asin := range fr.order {
		p := fr.products[asin]
		if !LanguageMatches(p.Language, h.Cfg.Language) {
			langDropped++
			st.Dropped++
			continue
		}
		d := Decide(p, a.Name, res)
		if !d.Keep {
			st.Dropped++
			continue
		}
		e := BuildEntry(p, h.Cfg.Provider, h.Cfg.Marketplace)
		e.NameOnlyAuthor, e.AuthorConflict, e.HarvestOpID = d.NameOnly, d.Conflict, h.Cfg.OpID
		if d.NameOnly {
			st.NameOnly++
		}
		items = append(items, database.CatalogUpsert{Entry: e, Raw: p.Raw})
	}
	st.Kept = len(items)

	seen := map[string]bool{}
	if len(items) > 0 {
		ur, err := h.Store.UpsertEntries(items, a.Key)
		if err != nil {
			st.State, st.LastError = database.CatalogHarvestFailed, err.Error()
			upErr := fmt.Errorf("catalog harvest %q: upsert: %w", a.Name, err)
			if perr := h.Store.PutAuthorState(&st); perr != nil {
				upErr = errors.Join(upErr, fmt.Errorf("write failed state: %w", perr))
			}
			return st, upErr
		}
		for _, id := range ur.IDs {
			seen[id] = true
		}
	}

	switch {
	case fr.err != nil || resErr != nil:
		err := fr.err
		if err == nil {
			err = resErr
		}
		st.State = database.CatalogHarvestFailed
		if st.PagesDone > 0 {
			st.State = database.CatalogHarvestPartial
		}
		st.LastError = err.Error()
	case res.LookupErrors > 0:
		// The listing is whole but the identity check is not: keep what was
		// kept, mark nothing stale, retry next run.
		st.State = database.CatalogHarvestPartial
		st.LastError = fmt.Sprintf("%d of %d owned-ASIN lookups failed", res.LookupErrors, res.Lookups)
	default:
		st.State = database.CatalogHarvestComplete
		t := now
		st.LastCompleteAt = &t
		if !fr.capped {
			n, err := h.Store.MarkUnseen(a.Key, seen)
			if err != nil {
				return st, fmt.Errorf("catalog harvest %q: stale pass: %w", a.Name, err)
			}
			st.MarkedStale = n
		}
	}
	if err := h.Store.PutAuthorState(&st); err != nil {
		return st, fmt.Errorf("catalog harvest %q: write state: %w", a.Name, err)
	}
	harvestLog.Info("author %q: state=%s pages=%d fetched=%d/%d kept=%d dropped=%d (language=%d) name_only=%d author_asins=%v conflict=%v stale=%d",
		logger.SanitizeLogValue(a.Name), st.State, st.PagesDone, st.Fetched, st.TotalResults, st.Kept, st.Dropped,
		langDropped, st.NameOnly, st.AuthorASINs, st.Conflict, st.MarkedStale)
	if errors.Is(fr.err, context.Canceled) || errors.Is(resErr, context.Canceled) {
		return st, context.Canceled
	}
	return st, nil
}

// Tally counts one run's outcomes. Atomic because RunItems calls both the
// item callback and the Label closure inside every worker goroutine.
type Tally struct {
	Done, Complete, Partial, Failed, Kept, Capped, Conflicts atomic.Int64
}

// Summary renders the tally for progress labels and the final log line.
func (t *Tally) Summary() string {
	return fmt.Sprintf("complete=%d partial=%d failed=%d kept=%d capped=%d conflicts=%d",
		t.Complete.Load(), t.Partial.Load(), t.Failed.Load(), t.Kept.Load(), t.Capped.Load(), t.Conflicts.Load())
}

func (t *Tally) record(st database.CatalogAuthorState) {
	t.Done.Add(1)
	t.Kept.Add(int64(st.Kept))
	switch st.State {
	case database.CatalogHarvestComplete:
		t.Complete.Add(1)
	case database.CatalogHarvestPartial:
		t.Partial.Add(1)
	default:
		t.Failed.Add(1)
	}
	if st.Capped {
		t.Capped.Add(1)
	}
	if st.Conflict {
		t.Conflicts.Add(1)
	}
}

// SelectDue filters authors to those Due now. force names harvest keys
// that bypass the interval (the manual single-author trigger).
func (h *Harvester) SelectDue(authors []ScopeAuthor, force map[string]bool) ([]ScopeAuthor, error) {
	now := h.Cfg.Now()
	out := make([]ScopeAuthor, 0, len(authors))
	for _, a := range authors {
		prev, err := h.Store.GetAuthorState(a.Key)
		if err != nil {
			return nil, err
		}
		if Due(prev, now, h.Cfg.ReharvestInterval, force[a.Key]) {
			out = append(out, a)
		}
	}
	return out, nil
}

// RunOptions are the op-side hooks of Run.
type RunOptions struct {
	// Checkpoint is called (serialized, at most once per CheckpointEvery)
	// after an author finishes. Resume skips by per-author state, not by a
	// slice index, so the checkpoint only needs to keep the watchdog fed and
	// the params intact.
	Checkpoint      func() error
	CheckpointEvery time.Duration
}

// Run harvests authors through a RunItems pool with an explicit
// Concurrency. Each worker owns one author at a time; two workers can
// still meet on one co-authored product, which CatalogStore.UpsertEntries
// serializes.
func (h *Harvester) Run(ctx context.Context, reporter registry.Reporter, authors []ScopeAuthor, opt RunOptions) (*Tally, error) {
	tally := &Tally{}
	var ckptMu sync.Mutex
	lastCkpt := time.Now()
	every := opt.CheckpointEvery
	if every <= 0 {
		every = 30 * time.Second
	}
	touch := func() { registry.TouchLiveness(reporter) }

	err := registry.RunItems(ctx, reporter, authors, func(ctx context.Context, a ScopeAuthor) error {
		st, err := h.HarvestAuthor(ctx, a, touch)
		tally.record(st)
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			// A store failure on one author is recorded on its state and
			// counted; it must not abort every other author.
			harvestLog.Error("author %q: %v", logger.SanitizeLogValue(a.Name), err)
		}
		if opt.Checkpoint != nil {
			ckptMu.Lock()
			if time.Since(lastCkpt) >= every {
				if cerr := opt.Checkpoint(); cerr != nil {
					harvestLog.Warn("checkpoint: %v", cerr)
				}
				lastCkpt = time.Now()
			}
			ckptMu.Unlock()
		}
		return nil
	}, registry.RunItemsOptions{
		Concurrency: h.Cfg.Concurrency,
		ErrMode:     registry.ErrModeCollect,
		Label: func(i, total int) string {
			return fmt.Sprintf("Authors %d/%d (%s)", tally.Done.Load(), total, tally.Summary())
		},
	})
	return tally, err
}

// Estimate is the dry-run census.
type Estimate struct {
	Scope              ScopeCensus `json:"scope"`
	AuthorsDue         int         `json:"authors_due"`
	DueWithASIN        int         `json:"authors_due_with_asin"`
	SampleAuthors      int         `json:"sample_authors"`
	SampleErrors       int         `json:"sample_errors"`
	AvgTotalResults    float64     `json:"avg_total_results"`
	AvgKeptRatio       float64     `json:"avg_kept_ratio"`
	AvgEntryBytes      float64     `json:"avg_entry_bytes"`
	EstimatedRequests  int         `json:"estimated_requests"`
	EstimatedEntries   int         `json:"estimated_entries"`
	EstimatedBytes     int64       `json:"estimated_bytes"`
	SampledAuthorNames []string    `json:"sampled_author_names,omitempty"`
}

// EstimateRun builds the dry-run census from the due authors, fetching
// page 0 for up to sampleN of them (network reads only; nothing is
// written). sampleN 0 skips the network and reports scope counts only.
func (h *Harvester) EstimateRun(ctx context.Context, scope ScopeCensus, due []ScopeAuthor, sampleN int) Estimate {
	est := Estimate{Scope: scope, AuthorsDue: len(due)}
	for _, a := range due {
		if len(a.OwnedASINs) > 0 {
			est.DueWithASIN++
		}
	}
	var totSum, keptSum, rawSum float64
	var byteSum, keptN float64
	for i := 0; i < len(due) && est.SampleAuthors < sampleN; i++ {
		a := due[i*max(len(due)/max(sampleN, 1), 1)%len(due)]
		if err := h.waitTurn(ctx); err != nil {
			est.SampleErrors++
			break
		}
		pg, err := h.Lister.ListByAuthor(ctx, a.Name, 0, h.Cfg.PageSize)
		if err != nil {
			est.SampleErrors++
			continue
		}
		est.SampleAuthors++
		est.SampledAuthorNames = append(est.SampledAuthorNames, a.Name)
		totSum += float64(min(pg.TotalResults, h.Cfg.MaxProducts))
		fetched := map[string]metadata.CatalogProduct{}
		for _, p := range pg.Products {
			fetched[p.ASIN] = p
		}
		// No lookup func: the only error is cancellation.
		res, err := ResolveAuthorASINs(ctx, a.Name, a.OwnedASINs, fetched, nil)
		if err != nil {
			est.SampleErrors++
			break
		}
		for _, p := range pg.Products {
			rawSum++
			if !LanguageMatches(p.Language, h.Cfg.Language) || !Decide(p, a.Name, res).Keep {
				continue
			}
			keptSum++
			e := BuildEntry(p, h.Cfg.Provider, h.Cfg.Marketplace)
			if b, err := json.Marshal(e); err == nil {
				byteSum += float64(len(b) + len(p.Raw))
				keptN++
			}
		}
	}
	if est.SampleAuthors > 0 {
		est.AvgTotalResults = totSum / float64(est.SampleAuthors)
	}
	if rawSum > 0 {
		est.AvgKeptRatio = keptSum / rawSum
	}
	if keptN > 0 {
		est.AvgEntryBytes = byteSum / keptN
	}
	pagesPer := 1
	if est.AvgTotalResults > 0 {
		pagesPer = max(int((est.AvgTotalResults+float64(h.Cfg.PageSize)-1)/float64(h.Cfg.PageSize)), 1)
	}
	// One listing walk per due author plus, at most, one owned-ASIN lookup
	// per author that has an ASIN-tagged book (most are answered by the
	// listing itself, so this is an upper bound).
	est.EstimatedRequests = len(due)*pagesPer + est.DueWithASIN
	est.EstimatedEntries = int(float64(len(due)) * est.AvgTotalResults * est.AvgKeptRatio)
	est.EstimatedBytes = int64(float64(est.EstimatedEntries) * est.AvgEntryBytes)
	return est
}

// FilterAuthors narrows authors to those whose harvest key or name matches
// one of names (the manual single-author trigger). Matching is on the
// folded key, so spelling variants of the same name select the same author.
func FilterAuthors(authors []ScopeAuthor, names []string) []ScopeAuthor {
	if len(names) == 0 {
		return authors
	}
	want := map[string]bool{}
	for _, n := range names {
		if k := HarvestKey(strings.TrimSpace(n)); k != "" {
			want[k] = true
		}
	}
	out := make([]ScopeAuthor, 0, len(want))
	for _, a := range authors {
		if want[a.Key] {
			out = append(out, a)
		}
	}
	return out
}
