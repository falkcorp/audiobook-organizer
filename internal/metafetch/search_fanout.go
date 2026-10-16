// file: internal/metafetch/search_fanout.go
// version: 1.7.1
// guid: f2309d86-b2ad-4db6-9612-f5872d0e00df
// last-edited: 2026-10-15

package metafetch

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"
	"time"

	"golang.org/x/sync/errgroup"
	"golang.org/x/time/rate"

	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/logger"
	"github.com/falkcorp/audiobook-organizer/internal/metadata"
	"github.com/falkcorp/audiobook-organizer/internal/metrics"
)

var searchFanoutLog = logger.New("metafetch.search")

// maxASINEnrich caps the Audnexus lookups one search spends filling the
// runtime of pooled answers that carry an ASIN but no runtime. With the
// own-ASIN fallback it shares ONE Audnexus lookup per book (of at most
// maxAudnexusRequests region requests): the fallback is reserved first.
const maxASINEnrich = 1

// fanoutSource is one provider's state across the fan-out rounds. A round
// touches each state from exactly one goroutine and rounds run one after
// another, so no field needs a lock.
type fanoutSource struct {
	src    metadata.MetadataSource
	name   string
	policy sourcePolicy

	results    []metadata.BookMetadata
	lastErr    error
	stepFailed bool
	// open is false once a throttle hold or an open breaker answered: every
	// later variant would get the same refusal.
	open bool
	// live counts requests actually sent; asked counts variants asked live
	// or from the cache.
	live  int
	asked int
}

func (st *fanoutSource) note(ctx context.Context, err error) {
	st.stepFailed = true
	st.lastErr = keepDiagnosis(st.lastErr, err)
	if providerSentinel(err) || ctx.Err() != nil {
		st.open = false
	}
}

// wants reports whether st asks v, the variant of round k.
func (st *fanoutSource) wants(k int, v queryVariant) bool {
	switch {
	case !st.open:
		return false
	case v.onlyIfEmpty && len(st.results) > 0:
		return false
	case st.policy == policyASINOnly:
		return false
	case st.policy == policyBestOnly:
		return k == 0
	case st.policy == policyUntilFound:
		return st.asked < maxOpenLibraryAsks && len(st.results) == 0
	}
	return true
}

// answered: every variant st asked completed without an error, throttle
// hold or cancel. A source asked nothing (Audnexus with no ASIN to look up)
// has truthfully answered too; leaving it out would make the batch re-ask it
// (OnlySources) on every run.
func (st *fanoutSource) answered(ctx context.Context) bool {
	return !st.stepFailed && st.open && ctx.Err() == nil
}

// accept is what one variant's answers add to the pool: v.accept, minus any
// answer naming a different series position than the title's
// (positionConflicts: "Rogue Ascension 7" for book 8), plus an answer that
// carries the book's own ASIN AND agrees with the book (strongCriteria.
// ownASINAgrees: the book's position and no other, and its title or a
// runtime within positionOverrideTolerance), which no title filter may drop.
func (p fanoutParams) accept(v queryVariant, rs []metadata.BookMetadata) []metadata.BookMetadata {
	kept := v.accept(rs, p.people)
	p.strong.noteNameEvidence(rs)
	kept = p.strong.dropConflicts(kept)
	if p.strong.asin == "" {
		return kept
	}
	for _, r := range rs {
		if !p.strong.ownASINAgrees(r) {
			continue
		}
		dup := false
		for _, k := range kept {
			if strings.EqualFold(strings.TrimSpace(k.ASIN), p.strong.asin) {
				dup = true
				break
			}
		}
		if !dup {
			kept = append(kept, r)
		}
	}
	return kept
}

// filterCarried returns the cached candidates (MetadataCandidate JSON) whose
// answer this search's position rules keep (positionConflicts), after the
// same pool-wide tagline check a search runs (noteNameEvidence). A candidate
// that does not decode is kept: it is not this filter's to judge.
func (c strongCriteria) filterCarried(raw []json.RawMessage) []json.RawMessage {
	if c.position == "" || len(raw) == 0 {
		return raw
	}
	answers := make([]*metadata.BookMetadata, len(raw))
	var pool []metadata.BookMetadata
	for i, r := range raw {
		var mc MetadataCandidate
		if json.Unmarshal(r, &mc) != nil {
			continue
		}
		bm := metadata.BookMetadata{Title: mc.Title, Author: mc.Author, Narrator: mc.Narrator, Series: mc.Series,
			SeriesPosition: mc.SeriesPosition, ASIN: mc.ASIN, DurationSec: mc.DurationSec}
		answers[i] = &bm
		pool = append(pool, bm)
	}
	c.noteNameEvidence(pool)
	out := raw[:0:0]
	for i, r := range raw {
		if answers[i] == nil || !c.positionConflicts(*answers[i]) {
			out = append(out, r)
		}
	}
	return out
}

type fanoutParams struct {
	ctx      context.Context
	limiter  *rate.Limiter
	bookID   string
	identity string
	opts     SearchOptions
	people   string
	strong   strongCriteria
	// pkeys collects the shared-cache (P1) keys this search reads or writes,
	// for the book's pointer row; runSearchFanout sets it when the shared
	// cache is on.
	pkeys *pcacheKeyLog
}

// variantCacheSource is the per-variant fetch-cache "source" key: the
// provider key plus a digest of the question. It lives under the same
// metadata_fetch_cache:<bookID>: prefix as the per-provider rows, so a
// book's prefix delete still clears it, and it never collides with them.
//
// These per-book rows are the LEGACY layer: with metadata_fetch.shared_cache
// on, a variant is answered from the shared P1 row (variantQuestion) first
// and this row is only read as a fallback, never written.
func variantCacheSource(src metadata.MetadataSource, v queryVariant) string {
	sum := sha256.Sum256([]byte(v.key()))
	return metadata.ProviderKey(src) + "#q" + hex.EncodeToString(sum[:8])
}

// variantQuestion is the shared P1 endpoint and key of the call askVariant
// makes for v: title and author when the variant carries an author
// (SearchByTitleAndAuthor), else the title alone (SearchByTitle).
func variantQuestion(provider string, v queryVariant) (endpoint, key string) {
	if v.Author != "" {
		endpoint = endpointWithMarketplace(provider, EndpointSearchTitleAuthor)
		return endpoint, NormalizeQuestion(provider, endpoint, Question{Title: v.Title, Authors: CreditsFromFlat(v.Author)})
	}
	endpoint = endpointWithMarketplace(provider, EndpointSearchTitle)
	return endpoint, NormalizeQuestion(provider, endpoint, Question{Title: v.Title})
}

// runSearchFanout asks every source its variants in ROUNDS: round k asks
// each source that wants it variants[k], concurrently across sources, then
// waits; once the pool holds a strong match (strongCriteria) no further
// round starts. Rounds keep which questions were asked deterministic (an
// async stop flag would make it depend on timing), and let a source that can
// never produce a runtime-verified match by itself (Open Library) stop on
// Audible's evidence instead of spending its whole cap.
//
// With the shared cache on, a forced search (BypassFetchCache: "Search
// again", a forced batch refetch) first deletes the shared answers the book's
// pointer row lists for these sources (database.DeletePCacheForBook), and the
// keys the search touches are recorded on the pointer row when it ends.
func (mfs *Service) runSearchFanout(p fanoutParams, sources []metadata.MetadataSource, variants []queryVariant) []*fanoutSource {
	if sharedCacheEnabled() && p.bookID != "" {
		if p.opts.BypassFetchCache {
			providers := make([]string, 0, len(sources))
			for _, s := range sources {
				providers = append(providers, metadata.ProviderKey(s))
			}
			if _, err := database.DeletePCacheForBook(mfs.db, p.bookID, providers); err != nil {
				searchFanoutLog.Warn("search again: shared cache bust failed: book=%s err=%v",
					logger.SanitizeLogValue(p.bookID), err)
			}
		}
		if p.pkeys == nil {
			p.pkeys = newPCacheKeyLog()
		}
		defer p.pkeys.flush(mfs.db, p.bookID)
	}
	states := make([]*fanoutSource, len(sources))
	for i, s := range sources {
		states[i] = &fanoutSource{src: s, name: s.Name(), policy: sourcePolicyFor(metadata.ProviderIDOf(s)), open: true}
	}
	for k, v := range variants {
		if p.ctx.Err() != nil {
			break
		}
		var round []*fanoutSource
		for _, st := range states {
			if st.wants(k, v) {
				round = append(round, st)
			}
		}
		if len(round) == 0 {
			continue
		}
		// Sized to the round: each provider paces itself with its own token
		// bucket, so a round's sources never contend with each other, and a
		// limit below the round would make a fifth source wait out the first
		// four for nothing.
		var g errgroup.Group
		g.SetLimit(max(sourceFanoutLimit(), len(round)))
		for _, st := range round {
			g.Go(func() error {
				mfs.askVariant(p, st, v)
				return nil
			})
		}
		// The callbacks return nil -- a failing source is recorded on its
		// state -- so an error here is a broken invariant: logged, never
		// dropped, and the round's recorded answers still stand.
		if err := g.Wait(); err != nil {
			searchFanoutLog.Error("search fan-out round %d of %d: unexpected error (book %s): %v",
				k+1, len(variants), logger.SanitizeLogValue(p.bookID), err)
		}
		// The pool-wide position pass: two sources may each have pooled one
		// sibling sharing the book's "name", which only the whole pool shows
		// to be a tagline (noteNameEvidence). Re-filtered before the strong
		// check and before the next round reads len(st.results).
		var pool []metadata.BookMetadata
		for _, st := range states {
			pool = append(pool, st.results...)
		}
		p.strong.noteNameEvidence(pool)
		for _, st := range states {
			st.results = p.strong.dropConflicts(st.results)
		}
		if poolHasStrong(states, p.strong) {
			searchFanoutLog.Debug("search fan-out stopped after round %d of %d: strong match (book %s)",
				k+1, len(variants), logger.SanitizeLogValue(p.bookID))
			break
		}
	}
	return states
}

func poolHasStrong(states []*fanoutSource, c strongCriteria) bool {
	for _, st := range states {
		for _, r := range st.results {
			if c.matches(r) {
				return true
			}
		}
	}
	return false
}

// askVariant asks st one variant: from the cache unless
// opts.BypassFetchCache, else live after a limiter token. A cache hit costs
// no limiter token; a miss still waits for one.
//
// With metadata_fetch.shared_cache on, the cache is the shared P1 row of the
// exact call (variantQuestion), shared by every book that asks the same
// question, then the book's legacy per-variant row as a fallback (read old,
// write new: a legacy hit is copied into P1 with its original age). The RAW
// answer is stored, an EMPTY one included (until the provider's negative
// TTL), and only after the call returned no error: an error, a throttle
// refusal or a 429 writes nothing. The variant's filter (p.accept) runs
// after the read on both paths, so a strict variant that keeps nothing can
// never make another book's looser variant read "no answer".
//
// With it off, the book's per-variant row is read and written, and only a
// non-empty answer is cached, exactly as before the shared cache.
func (mfs *Service) askVariant(p fanoutParams, st *fanoutSource, v queryVariant) {
	st.asked++
	provider := metadata.ProviderKey(st.src)
	shared := sharedCacheEnabled()
	var endpoint, pkey string
	if shared {
		endpoint, pkey = variantQuestion(provider, v)
		p.pkeys.add(provider, pkey)
	}
	if p.opts.BypassFetchCache {
		metrics.RecordCacheMiss(searchCacheName, searchCacheMissBypass)
		if shared {
			metrics.RecordCacheMiss(pcacheName, pcacheMissBypass)
		}
	} else {
		if rs, ok := mfs.variantFromCache(p, st, v, provider, endpoint, pkey); ok {
			// Entries cached before #1940 lack the year-kind flag; re-derive
			// it from the source, as the per-provider replay does.
			isRelease := metadata.SourceProducesAudiobookReleaseYear(st.name)
			for i := range rs {
				rs[i].PublishYearIsAudiobookRelease = isRelease
			}
			metrics.IncMetadataFetch(provider, metrics.FetchSourceCacheHit)
			metrics.RecordCacheHit(searchCacheName)
			st.results = append(st.results, p.accept(v, rs)...)
			return
		}
		metrics.IncMetadataFetch(provider, metrics.FetchSourceCacheMiss)
		metrics.RecordCacheMiss(searchCacheName, searchCacheMissNotCached)
	}
	if err := waitForLimiter(p.ctx, p.limiter); err != nil {
		st.note(p.ctx, err)
		return
	}
	st.live++
	var rs []metadata.BookMetadata
	var err error
	if v.Author != "" {
		rs, err = st.src.SearchByTitleAndAuthor(p.ctx, v.Title, v.Author)
	} else {
		rs, err = st.src.SearchByTitle(p.ctx, v.Title)
	}
	if err != nil {
		st.note(p.ctx, err)
		searchFanoutLog.Debug("search variant %s on %s failed: title=%s author=%s err=%v", v.Kind, st.name,
			logger.SanitizeLogValue(v.Title), logger.SanitizeLogValue(v.Author), err)
		return
	}
	switch {
	case shared:
		pcacheWrite(mfs.db, pkey, provider, endpoint, rs, len(rs) == 0, questionTTL(provider), time.Time{})
	case len(rs) > 0:
		if blob, merr := json.Marshal(rs); merr == nil {
			if perr := database.PutCachedMetadataFetch(mfs.db, p.bookID, variantCacheSource(st.src, v), p.identity, blob, 0); perr != nil {
				searchFanoutLog.Warn("search variant cache put failed: book=%s source=%s err=%v",
					logger.SanitizeLogValue(p.bookID), st.name, perr)
			} else {
				metrics.RecordCacheSet(searchCacheName)
			}
		}
	}
	st.results = append(st.results, p.accept(v, rs)...)
}

// variantFromCache is askVariant's read: the shared P1 row when pkey is set
// (an empty raw answer is a hit with no results), then the book's legacy
// per-variant row. ok reports a hit.
func (mfs *Service) variantFromCache(p fanoutParams, st *fanoutSource, v queryVariant, provider, endpoint, pkey string) ([]metadata.BookMetadata, bool) {
	if pkey != "" {
		if e, hit := pcacheRead(mfs.db, pkey, questionTTL(provider)); hit {
			if rs, ok := decodeEntry[metadata.BookMetadata](e); ok {
				return rs, true
			}
		}
	}
	cached, _, err := database.GetCachedMetadataFetchWithMaxAge(mfs.db, p.bookID, variantCacheSource(st.src, v), p.identity, fetchCacheMaxAge())
	if err != nil || cached == nil {
		return nil, false
	}
	var rs []metadata.BookMetadata
	if json.Unmarshal(cached.Results, &rs) != nil {
		return nil, false
	}
	if pkey != "" && len(rs) > 0 {
		// Read old, write new: the legacy row is the same question's raw
		// answer, so it is valid for every book; it keeps its age.
		pcacheWrite(mfs.db, pkey, provider, endpoint, rs, false, questionTTL(provider), cached.CachedAt)
	}
	return rs, true
}

// searchCacheName is the {cache} label of the interactive/batch SEARCH's
// fetch-cache decisions (audiobook_organizer_cache_{hits,misses,sets}_total):
// one count per question the search fan-out would otherwise send a provider
// (a title variant in askVariant, an ASIN lookup in cachedLookupASIN). It is
// deliberately a second view of the same rows: cache="metadata_fetch" is
// recorded by the database layer on EVERY row read (the single-book fetch,
// the bulk chain walk and the search alike, with a miss reason per check),
// while "metadata_search" answers "did this search ask a provider or not".
const (
	searchCacheName = "metadata_search"
	// searchCacheMissNotCached: no fresh row for this identity and question
	// (the database layer's metadata_fetch misses carry the precise reason).
	searchCacheMissNotCached = "not_found"
	// searchCacheMissBypass: the caller forced a refresh
	// (SearchOptions.BypassFetchCache), so the read was skipped on purpose.
	searchCacheMissBypass = "bypass"
)

// fetchCacheMaxAge is the configured fetch-cache TTL. Zero or negative means
// no age limit (GetCachedMetadataFetchWithMaxAge), never "cache disabled".
func fetchCacheMaxAge() time.Duration {
	return time.Duration(config.AppConfig.MetadataFetchCacheTTLDays) * 24 * time.Hour
}

// asinCacheSource is the fetch-cache "source" key of one ASIN lookup: the
// provider id plus the ASIN. Like the per-variant rows it lives under the
// book's metadata_fetch_cache:<bookID>: prefix, so the book's prefix delete
// (InvalidateAllCachedMetadataFetchesForBook) clears it too.
func asinCacheSource(providerID, asin string) string {
	return providerID + "#asin" + strings.ToUpper(strings.TrimSpace(asin))
}

// cachedLookupASIN is lookupASIN behind the fetch cache. Before this
// existed every search -- including an identical repeat of one the user had
// just run -- re-asked Audible/Audnexus for the book's own ASIN and for the
// runtime enrichment, because only the title variants were cached.
//
// Read unless p.opts.BypassFetchCache (a forced refresh still writes).
//
// With metadata_fetch.shared_cache on, the cache is the shared product row
// (P2, pcache:asin:<provider>:<ASIN>), then the book's legacy per-book row as
// a fallback. A found record is kept 30 days. A "no such ASIN" -- lookupASIN
// returns (nil, nil) ONLY for the provider's not-found, never for an error or
// a throttle refusal -- is stored as an empty negative entry for the
// provider's negative TTL, and never replaces a fresh found record
// (database.PutPCache), so an empty forced refresh cannot erase a good one.
//
// With it off, only a found record is written, to the book's own row: an
// error, a throttle refusal and a "no such ASIN" are never cached.
func (mfs *Service) cachedLookupASIN(p fanoutParams, providerID, asin string) (*metadata.BookMetadata, error) {
	cacheSource := asinCacheSource(providerID, asin)
	shared := sharedCacheEnabled()
	pkey := ""
	if shared {
		pkey = database.PCacheASINKey(providerID, asin, providerMarketplace(providerID))
	}
	if p.opts.BypassFetchCache {
		metrics.RecordCacheMiss(searchCacheName, searchCacheMissBypass)
		if shared {
			metrics.RecordCacheMiss(pcacheName, pcacheMissBypass)
		}
	} else {
		if shared {
			if e, hit := pcacheRead(mfs.db, pkey, productTTL(providerID)); hit {
				if rs, ok := decodeEntry[metadata.BookMetadata](e); ok {
					metrics.IncMetadataFetch(providerID, metrics.FetchSourceCacheHit)
					metrics.RecordCacheHit(searchCacheName)
					if len(rs) == 0 {
						return nil, nil // the provider's "no such ASIN", replayed
					}
					// Audible and Audnexus both report the audiobook RELEASE year.
					rs[0].PublishYearIsAudiobookRelease = true
					return &rs[0], nil
				}
			}
		}
		if cached, _, err := database.GetCachedMetadataFetchWithMaxAge(mfs.db, p.bookID, cacheSource, p.identity, fetchCacheMaxAge()); err == nil && cached != nil {
			var rs []metadata.BookMetadata
			if jerr := json.Unmarshal(cached.Results, &rs); jerr == nil && len(rs) > 0 {
				// Audible and Audnexus both report the audiobook RELEASE year.
				rs[0].PublishYearIsAudiobookRelease = true
				metrics.IncMetadataFetch(providerID, metrics.FetchSourceCacheHit)
				metrics.RecordCacheHit(searchCacheName)
				if shared {
					pcacheWrite(mfs.db, pkey, providerID, EndpointLookupASIN, rs[:1], false, productTTL(providerID), cached.CachedAt)
				}
				return &rs[0], nil
			}
		}
		metrics.IncMetadataFetch(providerID, metrics.FetchSourceCacheMiss)
		metrics.RecordCacheMiss(searchCacheName, searchCacheMissNotCached)
	}
	res, err := mfs.lookupASIN(p.ctx, p.limiter, providerID, asin)
	if err != nil {
		return res, err
	}
	if shared {
		var rs []metadata.BookMetadata
		if res != nil {
			rs = []metadata.BookMetadata{*res}
		}
		pcacheWrite(mfs.db, pkey, providerID, EndpointLookupASIN, rs, res == nil, productTTL(providerID), time.Time{})
		return res, nil
	}
	if res == nil {
		return nil, nil
	}
	if blob, merr := json.Marshal([]metadata.BookMetadata{*res}); merr == nil {
		if perr := database.PutCachedMetadataFetch(mfs.db, p.bookID, cacheSource, p.identity, blob, 0); perr != nil {
			searchFanoutLog.Warn("search ASIN lookup cache put failed: book=%s provider=%s err=%v",
				logger.SanitizeLogValue(p.bookID), providerID, perr)
		} else {
			metrics.RecordCacheSet(searchCacheName)
		}
	}
	return res, nil
}

// lookupASIN looks asin up on Audible or Audnexus. LookupByASIN is not on the
// MetadataSource interface, so these calls cannot go through ProtectedSource
// and are invisible to its breaker and throttle; they are gated and recorded
// by hand here (bypassed contexts skip the gate, as in ProtectedSource), and
// take a limiter token like every other live call.
func (mfs *Service) lookupASIN(ctx context.Context, limiter *rate.Limiter, providerID, asin string) (*metadata.BookMetadata, error) {
	reg := metadata.DefaultThrottleRegistry()
	if !metadata.ThrottleBypassed(ctx) && reg.Throttled(providerID) {
		return nil, metadata.ErrProviderThrottled
	}
	if err := waitForLimiter(ctx, limiter); err != nil {
		return nil, err
	}
	startedAt := time.Now()
	var res *metadata.BookMetadata
	var err error
	switch {
	case mfs.asinLookupOverride != nil:
		res, err = mfs.asinLookupOverride(ctx, providerID, asin)
	case providerID == metadata.SourceIDAudible:
		res, err = metadata.NewAudibleClient().LookupByASIN(asin)
	default:
		// Ctx-aware: a batch cancel aborts its region loop promptly. The
		// English stores first; the rest only when every one of those said
		// "not here", so a found ASIN costs at most 3 requests and a missing
		// one the full 8 MaxSearchCallsPerBook reports.
		client := metadata.NewAudnexusClient()
		res, err = client.LookupByASINInRegions(ctx, asin, audnexusSearchRegions)
		if res == nil && metadata.IsNotFound(err) {
			res, err = client.LookupByASINInRegions(ctx, asin, audnexusFallbackRegions)
		}
	}
	// "No such ASIN" (a 404, an empty product) is the provider answering,
	// not failing: it is recorded as a success and returned as (nil, nil), so
	// the search records the source as answered rather than failed -- a
	// failed source makes ErrNoSourceAnswered and the batch re-asks the book
	// on every run, forever, for an ASIN that simply does not exist.
	if metadata.IsNotFound(err) {
		res, err = nil, nil
	}
	// Counted by hand for the same reason the throttle is: this call does not
	// go through ProtectedSource.recordOutcome.
	if err != nil {
		reg.RecordFailure(providerID, err)
		metrics.IncMetadataFetch(providerID, metrics.FetchSourceError)
	} else {
		reg.RecordSuccess(providerID, startedAt)
		metrics.IncMetadataFetch(providerID, metrics.FetchSourceNetwork)
	}
	return res, err
}

// enrichRuntimeByASIN fills the runtime (and an empty narrator/series) of up
// to maxASINEnrich pooled answers that carry an ASIN but no runtime, from
// Audnexus. Only when the book's own runtime is known: otherwise a runtime
// cannot change the ranking, and the lookup would make the book wait on
// Audnexus for nothing. A partial runtime with a size-derived upper bound
// counts as known here: a candidate beyond it is penalised. Audible's own
// answers always carry a runtime, so in practice this is the only way Audnexus
// is asked about an ASIN another source found.
func (mfs *Service) enrichRuntimeByASIN(p fanoutParams, states []*fanoutSource, bookRuntime localRuntime) {
	ctx := p.ctx
	if !bookRuntime.informative() {
		return
	}
	enabled := false
	for _, st := range states {
		if st.policy == policyASINOnly && metadata.ProviderIDOf(st.src) == metadata.SourceIDAudnexus {
			enabled = true
		}
	}
	if !enabled {
		return
	}
	done := map[string]bool{}
	for _, st := range states {
		for i := range st.results {
			r := &st.results[i]
			asin := strings.ToUpper(strings.TrimSpace(r.ASIN))
			if asin == "" || r.DurationSec > 0 || !looksLikeASIN(asin) || done[asin] {
				continue
			}
			if len(done) >= maxASINEnrich || ctx.Err() != nil {
				return
			}
			done[asin] = true
			got, err := mfs.cachedLookupASIN(p, metadata.SourceIDAudnexus, asin)
			if err != nil || got == nil {
				continue
			}
			r.DurationSec = got.DurationSec
			if r.Narrator == "" {
				r.Narrator = got.Narrator
			}
			if r.Series == "" {
				r.Series, r.SeriesPosition = got.Series, got.SeriesPosition
			}
		}
	}
}

// poolHasASINWithRuntime reports whether a pooled answer already carries
// asin and a runtime: a direct lookup of it would add nothing to rank by.
func poolHasASINWithRuntime(states []*fanoutSource, asin string) bool {
	for _, st := range states {
		for _, r := range st.results {
			if r.DurationSec > 0 && strings.EqualFold(strings.TrimSpace(r.ASIN), asin) {
				return true
			}
		}
	}
	return false
}

// newSearchCandidate builds the MetadataCandidate for one scored answer. The
// runtime delta it carries (duration_delta_sec) drives the review UI's
// runtime-differs flag.
// rankOf is the ranking score of a freshly scored candidate: its breakdown's,
// or the score itself when no breakdown was recorded.
func rankOf(score float64, bd *ScoreBreakdown) float64 {
	if bd != nil && bd.RankScore != 0 {
		return bd.RankScore
	}
	return score
}

func newSearchCandidate(r metadata.BookMetadata, source string, score float64, breakdown *ScoreBreakdown, bookRuntime localRuntime, transcriptionBoosted bool) MetadataCandidate {
	durationDelta := 0
	if bookRuntime.Sec > 0 && r.DurationSec > 0 {
		durationDelta = bookRuntime.Sec - r.DurationSec
		if durationDelta < 0 {
			durationDelta = -durationDelta
		}
	}
	return MetadataCandidate{
		Title:                   r.Title,
		Author:                  r.Author,
		Narrator:                r.Narrator,
		Series:                  r.Series,
		SeriesPosition:          r.SeriesPosition,
		Year:                    r.PublishYear,
		Publisher:               r.Publisher,
		ISBN:                    r.ISBN,
		ISBN10:                  r.ISBN10,
		ISBN13:                  r.ISBN13,
		ASIN:                    r.ASIN,
		Genre:                   r.Genre,
		Abridged:                r.Abridged,
		Subtitle:                r.Subtitle,
		PageCount:               r.PageCount,
		SeriesSecondary:         r.SeriesSecondary,
		SeriesSecondaryPosition: r.SeriesSecondaryPosition,
		CoverURL:                r.CoverURL,
		Description:             r.Description,
		Language:                r.Language,
		Source:                  source,
		Score:                   score,
		RankScore:               rankOf(score, breakdown),
		ScoreBreakdown:          breakdown,
		DurationSec:             r.DurationSec,
		DurationDeltaSec:        durationDelta,
		DurationScore:           bookRuntime.score(r.DurationSec),
		CategoryTags:            r.CategoryTags,
		DurationMismatch:        durationDelta > 600 || bookRuntime.exceedsUpper(r.DurationSec),
		TranscriptionBoosted:    transcriptionBoosted,
		AudibleRatingOverall:    r.AudibleRatingOverall,
		AudibleRatingCount:      r.AudibleRatingCount,
		GoogleRatingAverage:     r.GoogleRatingAverage,
		GoogleRatingCount:       r.GoogleRatingCount,
	}
}
