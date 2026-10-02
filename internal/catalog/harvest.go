// file: internal/catalog/harvest.go
// version: 1.7.1
// guid: 7c2e4a90-1d6f-4b38-8e57-3a9b5c1f0d26
// last-edited: 2026-10-02

package catalog

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math"
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
// author is recorded partial and retried on the next run, and Run stops
// handing out further authors and returns it (wrapped).
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
	// OnProviderError is told about every provider failure (listing or
	// lookup) other than cancellation and not-found, so the shared throttle
	// registry can classify it: a 429 here must hold off every Audible
	// path, and the hold is what Throttled then reports. Nil means ignore.
	OnProviderError func(error)
	OpID            string
	Now             func() time.Time
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

// providerError feeds a provider failure to OnProviderError. Cancellation is
// ours, and a missing product is an answer, so neither is reported.
func (h *Harvester) providerError(ctx context.Context, err error) {
	if h.Cfg.OnProviderError == nil || err == nil || ctx.Err() != nil ||
		errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) ||
		errors.Is(err, metadata.ErrCatalogProductNotFound) {
		return
	}
	h.Cfg.OnProviderError(err)
}

// fetchResult is one author's raw listing.
type fetchResult struct {
	products map[string]metadata.CatalogProduct
	order    []string
	pages    int
	// raw counts every product the provider sent, decoded or skipped: it is
	// what is compared with total and with the cap.
	raw     int
	skipped int
	total   int
	capped  bool
	// short is set when the listing ended before it reached the provider's
	// own total (or the total itself is not believable). The rows received
	// are real and are kept, but the listing is NOT complete, so nothing may
	// be marked stale from it (R10: stale_since only after a complete fetch).
	short     string
	shortKind string
	// duplicates counts products whose ASIN an earlier page of the same walk
	// already returned. Audible repeats a product and drops another (a
	// 375-result author with 374 unique ASINs), so a walk that reaches the
	// total with duplicates has NOT seen every product.
	duplicates int
	// zero: the provider answered "0 results" with no products, whether or
	// not the author had entries (the breaker counts every such reply).
	zero bool
	err  error
}

// Short-listing kinds, and how many consecutive agreeing short runs it takes
// to accept one (see HarvestAuthor).
const (
	shortTruncated = "truncated"
	shortZeroTotal = "zero_total"
	// shortEmpty is the streak of a NEVER-HARVESTED author (no entries on
	// file) answering "0 results". It is not a short listing (nothing can be
	// staled), but when the run breaker holds such an author partial the
	// streak is what lets a really empty author be accepted eventually.
	shortEmpty      = "empty"
	ShortAcceptRuns = 3

	// ZeroTotalAcceptWindow is how long a run of agreeing "0 results"
	// replies must span before the catalog is accepted as gone (and its
	// entries staled). Run count alone is not evidence: the op is on demand
	// and partial authors are due every run, so three manual runs inside one
	// provider outage would reach any count. It is a fixed constant rather
	// than the reharvest interval on purpose: an operator who shortens the
	// interval (or forces an author) must not shorten the evidence window
	// with it. Seven days outlasts any provider incident seen so far and is
	// still well inside the 30-day default interval, so a truly gone catalog
	// is recognized within one reharvest cycle.
	//
	// The same rule (ShortAcceptRuns held zero runs spanning this window)
	// accepts a never-harvested author as EMPTY when the breaker keeps
	// holding it (a run that trips, or one too small to judge). Every held
	// zero run counts, an outage run included: deliberately, the window and
	// not the run count is the evidence, and an outage cannot fake seven
	// days. Not counting outage runs would instead strand the authors it is
	// for: once the authors with catalogs are complete, a run of only the
	// empty ones is all zeros and would never count. The cost of being wrong
	// is small here: an author with no entries has nothing to stale, so a
	// catalog hidden by a 7-day outage is only found one reharvest later.
	ZeroTotalAcceptWindow = 7 * 24 * time.Hour

	// The run-level zero-total breaker: when more than ZeroBreakerFraction of
	// a run's authors WITH ENTRIES ON FILE (filed > 0) whose listing answered
	// say "0 results", the provider is failing, not every catalog vanishing
	// at once. Only that population is judged: a zero answer from an author
	// we have never found anything for is uninformative (most obscure
	// authors legitimately have no catalog), while one from an author with
	// entries is anomalous. ZeroBreakerMinAuthors applies to the same
	// filed > 0 population.
	//
	// When fewer than ZeroBreakerMinAuthors filed authors answered (a first
	// harvest, or a small manual run), the same ratio and minimum are applied
	// to EVERY author whose listing answered, filed or not: a first harvest
	// during an outage is ten new authors all answering 0, and must trip.
	//
	// When even that population is under the minimum, the run is TOO SMALL
	// TO JUDGE (Tally.ZeroBreakerUnjudged, logged at Warn). Then unfiled zero
	// authors stay partial and are retried next run (an unjudged empty
	// answer is never recorded complete-and-empty for a reharvest
	// interval), while filed zero authors settle under their streak rules
	// (which already need ShortAcceptRuns runs over ZeroTotalAcceptWindow
	// before anything is staled).
	//
	// When the breaker trips, every zero-total author of the run, filed or
	// not, is left partial at its pre-run state (retried next run). When it
	// is judged and does not trip, zero-total authors settle normally:
	// unfiled ones complete empty.
	ZeroBreakerFraction   = 0.5
	ZeroBreakerMinAuthors = 5
)

// complete reports whether the walk reached the provider's total: no error,
// not short. A capped walk is complete-but-capped; the caller handles that.
func (r fetchResult) complete() bool { return r.err == nil && r.short == "" }

// fetchAll pages through an author listing: page 0, 1, ... until the
// provider's total_results is reached or the cap is hit (counted on RAW
// products received, not on kept ones). ASINs are deduped across pages.
//
// Only reaching total_results completes a walk. An empty page that arrives
// before the total is a truncated reply, not the end of the listing: it ends
// the walk as short. A total of 0 is believed only for an author with no
// entries filed under its harvest key (filed == 0); for an author that has
// entries, an empty "0 results" reply is far likelier a transient provider
// answer than the author's whole catalog vanishing, and believing it would
// stale every entry. HarvestAuthor accepts it only after ShortAcceptRuns
// consecutive zero-total runs.
func (h *Harvester) fetchAll(ctx context.Context, name string, filed int, touch func()) fetchResult {
	r := fetchResult{products: map[string]metadata.CatalogProduct{}}
	for page := 0; ; page++ {
		if err := h.waitTurn(ctx); err != nil {
			r.err = err
			return r
		}
		pg, err := h.Lister.ListByAuthor(ctx, name, page, h.Cfg.PageSize)
		if err != nil {
			h.providerError(ctx, err)
			r.err = err
			return r
		}
		if touch != nil {
			touch()
		}
		r.pages++
		r.total = pg.TotalResults
		received := len(pg.Products) + pg.Skipped
		r.raw += received
		r.skipped += pg.Skipped
		for _, p := range pg.Products {
			if p.ASIN == "" {
				continue
			}
			if _, dup := r.products[p.ASIN]; dup {
				r.duplicates++
			} else {
				r.order = append(r.order, p.ASIN)
			}
			r.products[p.ASIN] = p
		}
		switch {
		case pg.TotalResults <= 0:
			r.zero = r.raw == 0
			if r.raw > 0 {
				r.short, r.shortKind = fmt.Sprintf("provider sent %d products but reported total_results %d", r.raw, pg.TotalResults), shortTruncated
			} else if filed > 0 {
				r.short, r.shortKind = fmt.Sprintf("provider reported 0 results for an author with %d catalog entries", filed), shortZeroTotal
			}
			return r
		case r.raw >= pg.TotalResults:
			return r
		case received == 0:
			r.short = fmt.Sprintf("listing ended at %d of %d products (empty page %d)", r.raw, pg.TotalResults, page)
			r.shortKind = shortTruncated
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
	return h.harvestAuthor(ctx, a, touch, nil)
}

// harvestAuthor is HarvestAuthor with an optional run-level zero-total
// ledger. With rz set (inside Run) every zero-total reply is reported to it
// and its persisted state and stale pass are held for it, so the
// breaker can judge the whole run before anything is staled.
func (h *Harvester) harvestAuthor(ctx context.Context, a ScopeAuthor, touch func(), rz *zeroLedger) (database.CatalogAuthorState, error) {
	now := h.Cfg.Now().UTC()
	st := database.CatalogAuthorState{Key: a.Key, Name: a.Name, LastAttemptAt: now, OpID: h.Cfg.OpID}
	var prevASINs []string
	prev, err := h.Store.GetAuthorState(a.Key)
	if err != nil {
		return st, fmt.Errorf("catalog harvest %q: read state: %w", a.Name, err)
	}
	if prev != nil {
		st.LastCompleteAt = prev.LastCompleteAt
		prevASINs = prev.AuthorASINs
	}
	// The zero-total guard asks whether the store holds entries this author
	// would stale, not what the last run kept: a short run keeps 0, so
	// prev.Kept would let the SECOND transient zero reply stale everything.
	filed, err := h.Store.CountHarvestedBy(a.Key)
	if err != nil {
		return st, fmt.Errorf("catalog harvest %q: count entries: %w", a.Name, err)
	}

	fr := h.fetchAll(ctx, a.Name, filed, touch)
	st.PagesDone, st.Fetched, st.TotalResults, st.Capped, st.Skipped, st.Duplicates =
		fr.pages, fr.raw, fr.total, fr.capped, fr.skipped, fr.duplicates
	// Consecutive short runs (R10 retry, bounded). A listing that comes back
	// short the same way ShortAcceptRuns runs in a row is the provider's
	// steady answer, not a transient one: re-walking it every run forever
	// buys nothing. "The same way" is the same kind, the same delivered
	// count (always 0 for a zero total) and the same total_results. Any
	// other outcome, an error or cancellation included, resets the count.
	//
	// A zero total is accepted only when the agreeing runs ALSO span
	// ZeroTotalAcceptWindow (ShortSince is when the streak began), because
	// accepting it stales every entry; an accepted truncated listing stales
	// nothing, so its run count alone is enough.
	acceptShort := false
	if fr.err == nil && fr.short != "" {
		st.ShortKind, st.ShortDelivered, st.ShortRuns = fr.shortKind, fr.raw, 1
		since := now
		if prev != nil && prev.ShortRuns > 0 && prev.ShortKind == fr.shortKind &&
			prev.ShortDelivered == fr.raw && prev.TotalResults == fr.total {
			st.ShortRuns = prev.ShortRuns + 1
			if prev.ShortSince != nil {
				since = *prev.ShortSince
			}
		}
		st.ShortSince = &since
		acceptShort = st.ShortRuns >= ShortAcceptRuns &&
			(fr.shortKind != shortZeroTotal || now.Sub(since) >= ZeroTotalAcceptWindow)
		if acceptShort {
			harvestLog.Warn("author %q: %s, the same for %d consecutive runs; accepted as complete",
				logger.SanitizeLogValue(a.Name), fr.short, st.ShortRuns)
		} else {
			harvestLog.Warn("author %q: %s (run %d of %d before it is accepted); entries kept, nothing marked stale, retried next run",
				logger.SanitizeLogValue(a.Name), fr.short, st.ShortRuns, ShortAcceptRuns)
		}
	}
	if fr.err == nil && fr.zero && filed == 0 {
		// A never-harvested author answering 0: its own streak, so a held
		// run still builds evidence (see shortEmpty). Any other answer
		// leaves these fields zero, which resets it.
		st.ShortKind, st.ShortDelivered, st.ShortRuns = shortEmpty, 0, 1
		since := now
		if prev != nil && prev.ShortKind == shortEmpty && prev.ShortRuns > 0 {
			st.ShortRuns = prev.ShortRuns + 1
			if prev.ShortSince != nil {
				since = *prev.ShortSince
			}
		}
		st.ShortSince = &since
	}
	if fr.duplicates > 0 {
		harvestLog.Warn("author %q: listing repeated %d products, so it may have omitted as many; nothing marked stale",
			logger.SanitizeLogValue(a.Name), fr.duplicates)
	}
	if fr.skipped > 0 {
		harvestLog.Warn("author %q: %d undecodable products skipped; nothing marked stale",
			logger.SanitizeLogValue(a.Name), fr.skipped)
	}
	if fr.capped {
		harvestLog.Warn("author %q hit the %d-product cap (provider total %d); entries kept, nothing marked stale",
			logger.SanitizeLogValue(a.Name), h.Cfg.MaxProducts, fr.total)
	}

	lookup := func(ctx context.Context, asin string) (*metadata.CatalogProduct, error) {
		if err := h.waitTurn(ctx); err != nil {
			return nil, err
		}
		p, err := h.Lister.LookupProduct(ctx, asin)
		h.providerError(ctx, err)
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
	if (!fr.complete() || resErr != nil || res.LookupErrors > 0) && prevASINs != nil {
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

	pendingStale := false
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
	case fr.short != "" && !acceptShort:
		// A short listing: keep what arrived, mark nothing stale, and stay
		// due so the next run retries it (R10).
		st.State = database.CatalogHarvestPartial
		st.LastError = fr.short
	case res.LookupErrors > 0:
		// The listing is whole but the identity check is not: keep what was
		// kept, mark nothing stale, retry next run.
		st.State = database.CatalogHarvestPartial
		st.LastError = fmt.Sprintf("%d of %d owned-ASIN lookups failed", res.LookupErrors, res.Lookups)
	default:
		st.State = database.CatalogHarvestComplete
		t := now
		st.LastCompleteAt = &t
		// "Not seen" means "gone" only for a walk that saw everything. Not
		// for a capped walk (never saw the tail), a skipped product (may be a
		// stored entry whose id cannot be read off it), a walk with
		// duplicates (each one stands in for a product it omitted), or an
		// accepted truncated listing (its missing tail was never delivered).
		// An accepted ZERO total does stale: three agreeing empty listings
		// are the evidence that the catalog is gone.
		staleOK := !fr.capped && fr.skipped == 0 && fr.duplicates == 0 &&
			(fr.short == "" || fr.shortKind == shortZeroTotal)
		if rz != nil && fr.zero {
			// Inside a run a zero-total author is settled by the breaker:
			// see the hold below. Its stale pass, if any, runs there.
			pendingStale = staleOK
		} else if staleOK {
			n, err := h.Store.MarkUnseen(a.Key, seen)
			if err != nil {
				return st, fmt.Errorf("catalog harvest %q: stale pass: %w", a.Name, err)
			}
			st.MarkedStale = n
		}
	}
	// Inside a run, a zero-total author is HELD: persist its PRE-RUN state
	// marked partial and hand the state this run would write to the ledger.
	// settleZero writes it (and runs the stale pass) only if the breaker
	// judges the run and does not trip. A crash before settling leaves the
	// author partial with its old streak, i.e. retried: the safe direction.
	// The ledger only learns of an author (answered or held) after its
	// write succeeded, so a failed write is never counted or settled.
	held := rz != nil && fr.err == nil && fr.zero
	write := st
	if held {
		write = pendingHold(prev, st, "zero-total reply; held until the run's zero-total breaker settles")
	}
	if err := h.Store.PutAuthorState(&write); err != nil {
		// Nothing was written: tally it as the store failure it is, never
		// as the state it would have been.
		ret := st
		ret.State = database.CatalogHarvestFailed
		return ret, fmt.Errorf("catalog harvest %q: write state: %w", a.Name, err)
	}
	ret := st
	if rz != nil && fr.err == nil {
		rz.answered(filed > 0)
	}
	if held {
		rz.observe(a.Key, zeroObs{prev: prev, final: st, filed: filed > 0, stale: pendingStale})
		harvestLog.Info("author %q: zero-total reply held for the run breaker (intended state=%s short_runs=%d)",
			logger.SanitizeLogValue(a.Name), st.State, st.ShortRuns)
		// What is stored now is partial; settleZero moves the tally if it
		// promotes the author. A non-complete intended state keeps its own
		// bucket (a failed author is tallied failed).
		if ret.State == database.CatalogHarvestComplete {
			ret.State = database.CatalogHarvestPartial
		}
	} else {
		harvestLog.Info("author %q: state=%s pages=%d fetched=%d/%d kept=%d dropped=%d (language=%d) name_only=%d author_asins=%v conflict=%v stale=%d",
			logger.SanitizeLogValue(a.Name), st.State, st.PagesDone, st.Fetched, st.TotalResults, st.Kept, st.Dropped,
			langDropped, st.NameOnly, st.AuthorASINs, st.Conflict, st.MarkedStale)
	}
	if errors.Is(fr.err, context.Canceled) || errors.Is(resErr, context.Canceled) {
		return ret, context.Canceled
	}
	return ret, nil
}

// Tally counts one run's outcomes. Atomic because RunItems calls both the
// item callback and the Label closure inside every worker goroutine.
type Tally struct {
	Done, Complete, Partial, Failed, Kept, Capped, Conflicts atomic.Int64
	// Duplicated counts authors whose walk repeated products across pages.
	// Such a walk is complete but never stales (a repeat may stand in for an
	// omitted product that is still for sale), so this is the census of
	// authors whose stale entries can only be found by a later clean walk.
	Duplicated atomic.Int64
	// ZeroTotal counts authors that answered "0 results"; ZeroBreakerTripped
	// is set when they were too many of the run to believe (see Run).
	ZeroTotal          atomic.Int64
	ZeroBreakerTripped atomic.Bool
	// ZeroBreakerUnjudged: too few authors answered for the breaker to
	// judge the run (see ZeroBreakerMinAuthors); unfiled zero authors were
	// left partial rather than recorded complete-and-empty.
	ZeroBreakerUnjudged atomic.Bool
	// ZeroRepeat counts, on a trip, the zero-total authors that were already
	// on a zero streak; EmptyAccepted counts never-harvested authors whose
	// held empty streak spanned the window and were accepted as empty.
	ZeroRepeat, EmptyAccepted atomic.Int64
}

// Summary renders the tally for progress labels and the final log line.
func (t *Tally) Summary() string {
	s := fmt.Sprintf("complete=%d partial=%d failed=%d kept=%d capped=%d conflicts=%d duplicated=%d zero_total=%d",
		t.Complete.Load(), t.Partial.Load(), t.Failed.Load(), t.Kept.Load(), t.Capped.Load(), t.Conflicts.Load(),
		t.Duplicated.Load(), t.ZeroTotal.Load())
	if t.ZeroBreakerTripped.Load() {
		s += fmt.Sprintf(" ZERO-TOTAL BREAKER TRIPPED (provider failure; nothing staled; %d of the zero-total authors were repeat-zero)", t.ZeroRepeat.Load())
	}
	if n := t.EmptyAccepted.Load(); n > 0 {
		s += fmt.Sprintf(" empty_accepted=%d", n)
	}
	if t.ZeroBreakerUnjudged.Load() {
		s += " zero-total breaker could not judge (too few authors answered; unfiled zero authors left partial)"
	}
	return s
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
	if st.Duplicates > 0 {
		t.Duplicated.Add(1)
	}
}

// zeroLedger collects one run's zero-total replies for the breaker.
type zeroLedger struct {
	mu sync.Mutex
	// filedAnswered counts authors with entries on file whose listing
	// answered (no error): the breaker's population. allAnswered counts
	// every author whose listing answered: the fallback population.
	filedAnswered, allAnswered int
	obs                        map[string]zeroObs
}

// zeroObs is one held zero-total author: its whole pre-run state (nil if
// never harvested), the state this run would write, whether it had entries
// on file, and whether its settle runs the stale pass.
type zeroObs struct {
	prev  *database.CatalogAuthorState
	final database.CatalogAuthorState
	filed bool
	stale bool
}

func newZeroLedger() *zeroLedger {
	return &zeroLedger{obs: map[string]zeroObs{}}
}

func (z *zeroLedger) answered(filed bool) {
	z.mu.Lock()
	defer z.mu.Unlock()
	z.allAnswered++
	if filed {
		z.filedAnswered++
	}
}

func (z *zeroLedger) observe(key string, o zeroObs) {
	z.mu.Lock()
	defer z.mu.Unlock()
	z.obs[key] = o
}

// pendingHold is the pre-run state (a fresh one for a never-harvested
// author) with only State, LastError, LastAttemptAt and OpID overlaid from
// this run: every streak field, TotalResults and LastCompleteAt stay as they
// were before it.
func pendingHold(prev *database.CatalogAuthorState, now database.CatalogAuthorState, reason string) database.CatalogAuthorState {
	hold := database.CatalogAuthorState{Key: now.Key, Name: now.Name}
	if prev != nil {
		hold = *prev
	}
	hold.State, hold.LastError, hold.LastAttemptAt, hold.OpID = database.CatalogHarvestPartial, reason, now.LastAttemptAt, now.OpID
	return hold
}

// emptyAccepted reports whether a never-harvested author's empty streak
// (as of the run that computed st) has reached ShortAcceptRuns runs over
// ZeroTotalAcceptWindow, and the run itself completed. A run that ended
// partial or failed (an owned-ASIN lookup failed, say) is not evidence of
// an empty catalog, and settleZero's tally move (partial -> complete)
// assumes a complete final.
func emptyAccepted(st database.CatalogAuthorState) bool {
	return st.State == database.CatalogHarvestComplete &&
		st.ShortKind == shortEmpty && st.ShortRuns >= ShortAcceptRuns && st.ShortSince != nil &&
		st.LastAttemptAt.Sub(*st.ShortSince) >= ZeroTotalAcceptWindow
}

// zeroVerdict is the breaker's judgement of one run.
type zeroVerdict int

const (
	zeroHolds    zeroVerdict = iota // judged; the zero answers are believed
	zeroTrips                       // judged; the zero answers are a provider failure
	zeroUnjudged                    // too few answers to judge either way
)

// judge applies the breaker (see ZeroBreakerFraction): over the filed
// population when it is large enough, else over every author that answered.
func (z *zeroLedger) judge() (v zeroVerdict, zeros, pop int, scope string) {
	zeroFiled := 0
	for _, o := range z.obs {
		if o.filed {
			zeroFiled++
		}
	}
	zeros, pop, scope = zeroFiled, z.filedAnswered, "authors with catalog entries"
	if pop < ZeroBreakerMinAuthors {
		zeros, pop, scope = len(z.obs), z.allAnswered, "authors that answered (too few with catalog entries)"
	}
	switch {
	case pop < ZeroBreakerMinAuthors:
		return zeroUnjudged, zeros, pop, scope
	case float64(zeros) > ZeroBreakerFraction*float64(pop):
		return zeroTrips, zeros, pop, scope
	}
	return zeroHolds, zeros, pop, scope
}

// settleZero runs after the pool. Every held zero-total author was written
// as its pre-run state marked partial. If the breaker trips they stay that
// way (with the breaker's reason). If the run is too small to judge,
// unfiled ones stay that way and filed ones settle. Otherwise each gets the
// state its run computed, and an accepted zero total runs its stale pass.
//
// Each settle re-reads the stored state and skips an author whose state
// changed since its hold (another OpID or a newer LastAttemptAt). Within one
// op that cannot happen: ConcurrencyKey "catalog.harvest" lets one harvest
// run at a time and a run gives each author to one worker. It guards against
// a direct HarvestAuthor call, or a future change to that key, landing a
// newer result that a stale settle would overwrite.
func (h *Harvester) settleZero(reporter registry.Reporter, z *zeroLedger, tally *Tally) error {
	z.mu.Lock()
	defer z.mu.Unlock()
	tally.ZeroTotal.Store(int64(len(z.obs)))
	if len(z.obs) == 0 {
		return nil
	}
	verdict, zeros, pop, scope := z.judge()
	var msg string
	switch verdict {
	case zeroTrips:
		tally.ZeroBreakerTripped.Store(true)
		first, repeat := 0, 0
		for _, o := range z.obs {
			if o.prev != nil && o.prev.ShortRuns > 0 && (o.prev.ShortKind == shortEmpty || o.prev.ShortKind == shortZeroTotal) {
				repeat++
			} else {
				first++
			}
		}
		tally.ZeroRepeat.Store(int64(repeat))
		msg = fmt.Sprintf("zero-total breaker: %d of %d %s answered 0 results (%d first-time, %d repeat-zero); treated as a provider failure, nothing staled, %d zero-total authors held partial (never-harvested repeat-zero authors are accepted as empty once their streak spans %s)",
			zeros, pop, scope, first, repeat, len(z.obs), ZeroTotalAcceptWindow)
	case zeroUnjudged:
		tally.ZeroBreakerUnjudged.Store(true)
		msg = fmt.Sprintf("zero-total breaker cannot judge: only %d %s (minimum %d); unfiled zero-total authors left partial and retried (accepted as empty once their streak spans the window)",
			pop, scope, ZeroBreakerMinAuthors)
	}
	if msg != "" {
		harvestLog.Warn("%s", msg)
		if reporter != nil {
			_ = reporter.Log(slog.LevelWarn, msg)
		}
	}

	var errs []error
	for key, o := range z.obs {
		cur, err := h.Store.GetAuthorState(key)
		if err != nil {
			errs = append(errs, fmt.Errorf("zero-total settle %q: read state: %w", key, err))
			continue
		}
		if cur == nil || cur.OpID != o.final.OpID || !cur.LastAttemptAt.Equal(o.final.LastAttemptAt) {
			harvestLog.Warn("zero-total settle: %q changed since its hold; leaving the newer state", logger.SanitizeLogValue(key))
			continue
		}
		if verdict == zeroTrips || (verdict == zeroUnjudged && !o.filed) {
			if !o.filed && emptyAccepted(o.final) {
				// Held every run, but its empty streak now spans the
				// window: accept it as empty (nothing on file to stale).
				final := o.final
				if err := h.Store.PutAuthorState(&final); err != nil {
					errs = append(errs, fmt.Errorf("zero-total accept empty %q: %w", key, err))
					continue
				}
				tally.Partial.Add(-1)
				tally.Complete.Add(1)
				tally.EmptyAccepted.Add(1)
				continue
			}
			hold := pendingHold(o.prev, o.final, msg)
			if !o.filed {
				// Everything else stays pre-run; the empty streak counts.
				hold.ShortKind, hold.ShortRuns, hold.ShortDelivered, hold.ShortSince =
					o.final.ShortKind, o.final.ShortRuns, o.final.ShortDelivered, o.final.ShortSince
			}
			if err := h.Store.PutAuthorState(&hold); err != nil {
				errs = append(errs, fmt.Errorf("zero-total hold %q: %w", key, err))
			}
			continue
		}
		final := o.final
		if o.stale {
			n, err := h.Store.MarkUnseen(key, map[string]bool{})
			if err != nil {
				// Leave the hold (partial, pre-run streak): retried.
				errs = append(errs, fmt.Errorf("zero-total stale pass %q: %w", key, err))
				continue
			}
			final.MarkedStale = n
		}
		if err := h.Store.PutAuthorState(&final); err != nil {
			errs = append(errs, fmt.Errorf("zero-total settle %q: %w", key, err))
			continue
		}
		if final.State == database.CatalogHarvestComplete {
			tally.Partial.Add(-1)
			tally.Complete.Add(1)
		}
	}
	return errors.Join(errs...)
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
//
// When the provider goes into a throttle hold mid-run, Run cancels the pool
// instead of letting every remaining author make one doomed request and be
// written failed: authors not yet started keep their previous state (and
// stay due), and Run returns ErrProviderStandDown.
func (h *Harvester) Run(parent context.Context, reporter registry.Reporter, authors []ScopeAuthor, opt RunOptions) (*Tally, error) {
	ctx, cancel := context.WithCancel(parent)
	defer cancel()
	var standDown atomic.Bool
	tally := &Tally{}
	rz := newZeroLedger()
	var ckptMu sync.Mutex
	lastCkpt := time.Now()
	every := opt.CheckpointEvery
	if every <= 0 {
		every = 30 * time.Second
	}
	touch := func() { registry.TouchLiveness(reporter) }

	err := registry.RunItems(ctx, reporter, authors, func(ctx context.Context, a ScopeAuthor) error {
		// An author dispatched after a stand-down must not be attempted: an
		// attempt would overwrite its state with a cancellation failure.
		if err := ctx.Err(); err != nil {
			return err
		}
		st, err := h.harvestAuthor(ctx, a, touch, rz)
		tally.record(st)
		if h.Cfg.Throttled != nil && h.Cfg.Throttled() && standDown.CompareAndSwap(false, true) {
			harvestLog.Warn("provider throttled after author %q; standing down the run (%s)",
				logger.SanitizeLogValue(a.Name), tally.Summary())
			cancel()
		}
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
	// The breaker judges the whole run, so it settles after the pool: no
	// zero-total author is staled (or counted toward acceptance) until the
	// run as a whole is known not to be one provider outage.
	if serr := h.settleZero(reporter, rz, tally); serr != nil {
		harvestLog.Error("zero-total settle: %v", serr)
		err = errors.Join(err, serr)
	}
	if standDown.Load() && parent.Err() == nil {
		return tally, fmt.Errorf("%w after %d of %d authors", ErrProviderStandDown, tally.Done.Load(), len(authors))
	}
	return tally, err
}

// Estimate is the dry-run census.
type Estimate struct {
	Scope           ScopeCensus `json:"scope"`
	AuthorsDue      int         `json:"authors_due"`
	DueWithASIN     int         `json:"authors_due_with_asin"`
	SampleAuthors   int         `json:"sample_authors"`
	SampleErrors    int         `json:"sample_errors"`
	AvgTotalResults float64     `json:"avg_total_results"`
	AvgKeptRatio    float64     `json:"avg_kept_ratio"`
	AvgEntryBytes   float64     `json:"avg_entry_bytes"`
	// AvgPagesPerAuthor is the mean over sampled authors of each author's
	// own page count, ceil(min(total, cap) / page size) and at least 1:
	// the mean of the ceilings, never the ceiling of the mean total.
	AvgPagesPerAuthor float64 `json:"avg_pages_per_author"`
	// EstimatedLookups counts one product lookup per owned ASIN of every due
	// author: the most ResolveAuthorASINs can make (an owned ASIN the listing
	// returns costs none).
	EstimatedLookups int `json:"estimated_lookups"`
	// EstimatedRequests is an ESTIMATE, not a bound: listing pages +
	// EstimatedLookups. The lookups term is bounded (one per owned ASIN);
	// the pages term is exact for sampled authors and extrapolated from the
	// sample mean for the rest; retries of short or partial runs are
	// excluded.
	EstimatedRequests  int      `json:"estimated_requests"`
	EstimatedEntries   int      `json:"estimated_entries"`
	EstimatedBytes     int64    `json:"estimated_bytes"`
	SampledAuthorNames []string `json:"sampled_author_names,omitempty"`
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
	var totSum, keptSum, rawSum, pageSum float64
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
		listed := min(pg.TotalResults, h.Cfg.MaxProducts)
		totSum += float64(listed)
		pageSum += float64(max((listed+h.Cfg.PageSize-1)/h.Cfg.PageSize, 1))
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
	// With no sample the page count is unknown; one page per author is the
	// least any harvest makes.
	est.AvgPagesPerAuthor = 1
	if est.SampleAuthors > 0 {
		est.AvgPagesPerAuthor = pageSum / float64(est.SampleAuthors)
	}
	for _, a := range due {
		est.EstimatedLookups += len(a.OwnedASINs)
	}
	est.EstimatedRequests = int(math.Ceil(est.AvgPagesPerAuthor*float64(len(due)))) + est.EstimatedLookups
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
