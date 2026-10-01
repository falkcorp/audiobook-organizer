// file: internal/metafetch/search_fanout.go
// version: 1.2.0
// guid: f2309d86-b2ad-4db6-9612-f5872d0e00df
// last-edited: 2026-10-01

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
)

var searchFanoutLog = logger.New("metafetch.search")

// maxASINEnrich caps the Audnexus lookups one search spends filling the
// runtime of pooled answers that carry an ASIN but no runtime. With the
// own-ASIN fallback it shares ONE Audnexus lookup per book (of at most
// maxAudnexusRegions requests): the fallback is reserved first.
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
// carries the book's own ASIN AND agrees with the book on something else
// (strongCriteria.ownASINAgrees), which no title filter may drop.
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

type fanoutParams struct {
	ctx      context.Context
	limiter  *rate.Limiter
	bookID   string
	identity string
	opts     SearchOptions
	people   string
	strong   strongCriteria
}

// variantCacheSource is the per-variant fetch-cache "source" key: the
// provider key plus a digest of the question. It lives under the same
// metadata_fetch_cache:<bookID>: prefix as the per-provider rows, so a
// book's prefix delete still clears it, and it never collides with them.
func variantCacheSource(src metadata.MetadataSource, v queryVariant) string {
	sum := sha256.Sum256([]byte(v.key()))
	return metadata.ProviderKey(src) + "#q" + hex.EncodeToString(sum[:8])
}

// runSearchFanout asks every source its variants in ROUNDS: round k asks
// each source that wants it variants[k], concurrently across sources, then
// waits; once the pool holds a strong match (strongCriteria) no further
// round starts. Rounds keep which questions were asked deterministic (an
// async stop flag would make it depend on timing), and let a source that can
// never produce a runtime-verified match by itself (Open Library) stop on
// Audible's evidence instead of spending its whole cap.
func (mfs *Service) runSearchFanout(p fanoutParams, sources []metadata.MetadataSource, variants []queryVariant) []*fanoutSource {
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
		// Never returns an error: a failing source is recorded on its state.
		_ = g.Wait()
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

// askVariant asks st one variant: from the per-variant fetch cache unless
// opts.BypassFetchCache, else live after a limiter token. The raw answer is
// cached (never an empty one) and the variant's filter is applied on both
// paths, so an answer that is non-empty but entirely filtered is replayed
// rather than re-asked.
func (mfs *Service) askVariant(p fanoutParams, st *fanoutSource, v queryVariant) {
	st.asked++
	cacheSource := variantCacheSource(st.src, v)
	if !p.opts.BypassFetchCache {
		maxAge := time.Duration(config.AppConfig.MetadataFetchCacheTTLDays) * 24 * time.Hour
		if cached, _, err := database.GetCachedMetadataFetchWithMaxAge(mfs.db, p.bookID, cacheSource, p.identity, maxAge); err == nil && cached != nil {
			var rs []metadata.BookMetadata
			if jerr := json.Unmarshal(cached.Results, &rs); jerr == nil {
				// Entries cached before #1940 lack the year-kind flag; re-derive
				// it from the source, as the per-provider replay does.
				isRelease := metadata.SourceProducesAudiobookReleaseYear(st.name)
				for i := range rs {
					rs[i].PublishYearIsAudiobookRelease = isRelease
				}
				st.results = append(st.results, p.accept(v, rs)...)
				return
			}
		}
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
	if len(rs) > 0 {
		if blob, merr := json.Marshal(rs); merr == nil {
			if perr := database.PutCachedMetadataFetch(mfs.db, p.bookID, cacheSource, p.identity, blob, 0); perr != nil {
				searchFanoutLog.Warn("search variant cache put failed: book=%s source=%s err=%v",
					logger.SanitizeLogValue(p.bookID), st.name, perr)
			}
		}
	}
	st.results = append(st.results, p.accept(v, rs)...)
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
		// Ctx-aware: a batch cancel aborts its region loop promptly. At most
		// maxAudnexusRegions requests, so one lookup fits the per-book budget
		// MaxSearchCallsPerBook reports.
		res, err = metadata.NewAudnexusClient().LookupByASINInRegions(ctx, asin, audnexusSearchRegions)
	}
	// "No such ASIN" (a 404, an empty product) is the provider answering,
	// not failing: it is recorded as a success and returned as (nil, nil), so
	// the search records the source as answered rather than failed -- a
	// failed source makes ErrNoSourceAnswered and the batch re-asks the book
	// on every run, forever, for an ASIN that simply does not exist.
	if metadata.IsNotFound(err) {
		res, err = nil, nil
	}
	if err != nil {
		reg.RecordFailure(providerID, err)
	} else {
		reg.RecordSuccess(providerID, startedAt)
	}
	return res, err
}

// enrichRuntimeByASIN fills the runtime (and an empty narrator/series) of up
// to maxASINEnrich pooled answers that carry an ASIN but no runtime, from
// Audnexus. Only when the book's own runtime is known: otherwise a runtime
// cannot change the ranking, and the lookup would make the book wait on
// Audnexus for nothing. Audible's own answers always carry a runtime, so in
// practice this is the only way Audnexus is asked about an ASIN another
// source found.
func (mfs *Service) enrichRuntimeByASIN(ctx context.Context, limiter *rate.Limiter, states []*fanoutSource, bookDurationSec int) {
	if bookDurationSec <= 0 {
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
			got, err := mfs.lookupASIN(ctx, limiter, metadata.SourceIDAudnexus, asin)
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
func newSearchCandidate(r metadata.BookMetadata, source string, score float64, breakdown *ScoreBreakdown, bookDurationSec int, transcriptionBoosted bool) MetadataCandidate {
	durationDelta := 0
	if bookDurationSec > 0 && r.DurationSec > 0 {
		durationDelta = bookDurationSec - r.DurationSec
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
		ScoreBreakdown:          breakdown,
		DurationSec:             r.DurationSec,
		DurationDeltaSec:        durationDelta,
		DurationScore:           computeDurationScore(bookDurationSec, r.DurationSec),
		CategoryTags:            r.CategoryTags,
		DurationMismatch:        durationDelta > 600,
		TranscriptionBoosted:    transcriptionBoosted,
		AudibleRatingOverall:    r.AudibleRatingOverall,
		AudibleRatingCount:      r.AudibleRatingCount,
		GoogleRatingAverage:     r.GoogleRatingAverage,
		GoogleRatingCount:       r.GoogleRatingCount,
	}
}
