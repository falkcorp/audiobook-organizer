// file: internal/server/candidate_fallback.go
// version: 2.1.1
// guid: 487502d2-faea-4677-8c9d-171a796ad643
// last-edited: 2026-10-06
//
// The batch candidate fetch's provider fallback: Open Library, then Google
// Books, asked only for books the rest of the chain left without a usable
// candidate. Owner decisions 2026-10-06: "Both, spread over days"; the trigger
// is "no usable candidate" (none, all owner-rejected, all refused by the
// ASIN/identity checks, or the best below the apply score floor); Google
// Books is counted by ONE daily budget shared with every other Google caller
// and enforced in its HTTP transport (metadata.GoogleBooksBudget).

package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"slices"
	"strings"
	"time"

	"golang.org/x/time/rate"

	"github.com/falkcorp/audiobook-organizer/internal/applygate"
	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/logger"
	"github.com/falkcorp/audiobook-organizer/internal/metabatch"
	"github.com/falkcorp/audiobook-organizer/internal/metadata"
	"github.com/falkcorp/audiobook-organizer/internal/metadata/dailyquota"
	"github.com/falkcorp/audiobook-organizer/internal/metafetch"
)

// googleBooksFallbackOn reports whether the Google Books fallback step is on
// (a negative google_books_fallback_daily_limit turns it off).
func googleBooksFallbackOn() bool { return config.AppConfig.GoogleBooksFallbackDailyLimit >= 0 }

// googleBackgroundRemaining is how many background Google Books lookups
// today's shared budget has left -- what the scheduled candidate fetch (a
// background op) may still spend.
func googleBackgroundRemaining() int {
	return metadata.GoogleBooksBudget().Remaining(dailyquota.Background)
}

// googleRequestsPerBook is the most Google Books requests one fallback lookup
// sends (metafetch.MaxSearchCallsPerBook: Google is asked the best query
// variant only). The selection funds Google-owed books by this, since the
// budget counts requests, not books. Transport retries (a 429/5xx answer)
// are not estimated: they are rare, and a request past the share is refused
// by the transport before it is sent, deferring the book whole.
func googleRequestsPerBook() int {
	return max(metafetch.MaxSearchCallsPerBook(metadata.SourceIDGoogleBooks), 1)
}

// fallbackProvider is one enabled fallback provider: its id and the display
// name the cache and OnlySources key it by.
type fallbackProvider struct{ id, name string }

func (fb fallbackProvider) isGoogle() bool { return fb.id == metadata.SourceIDGoogleBooks }

// candidateFallbackPlan returns the enabled fallback providers in fallback
// order (metafetch.CandidateFallbackProviderIDs). Every one of them is kept
// out of the primary chain (splitFallback), whether or not it is asked.
func candidateFallbackPlan(nameByID map[string]string) []fallbackProvider {
	var plan []fallbackProvider
	for _, id := range metafetch.CandidateFallbackProviderIDs {
		if name, ok := nameByID[id]; ok {
			plan = append(plan, fallbackProvider{id: id, name: name})
		}
	}
	return plan
}

// activeFallbackPlan is plan less the providers turned off (Google Books
// when google_books_fallback_daily_limit < 0): the ones the fallback asks.
func activeFallbackPlan(plan []fallbackProvider) []fallbackProvider {
	if googleBooksFallbackOn() {
		return plan
	}
	return slices.DeleteFunc(slices.Clone(plan), func(fb fallbackProvider) bool { return fb.isGoogle() })
}

// splitFallback returns the primary chain of ask (the providers a search
// would ask: the cache verdict's unanswered list, or every active source):
// ask less every fallback provider.
func splitFallback(ask []string, plan []fallbackProvider) (primary []string) {
	isFallback := make(map[string]bool, len(plan))
	for _, fb := range plan {
		isFallback[fb.name] = true
	}
	for _, n := range ask {
		if !isFallback[n] {
			primary = append(primary, n)
		}
	}
	return primary
}

// fallbackOwed returns the providers of plan that still owe entry's search
// identity an answer, in fallback order: neither an empty answer
// (EmptyAnswers) nor a settled attempt (FallbackAttempts: candidates, or a
// permanent refusal) within MetadataKnownEmptyTTL. A nil entry owes all.
func fallbackOwed(entry *database.MetadataCandidateCache, plan []fallbackProvider) []fallbackProvider {
	if entry == nil {
		return slices.Clone(plan)
	}
	var owed []fallbackProvider
	now := time.Now()
	for _, fb := range plan {
		if at, ok := entry.EmptyAnswers[fb.name]; ok && now.Sub(at) < database.MetadataKnownEmptyTTL {
			continue
		}
		if a, ok := entry.FallbackAttempts[fb.name]; ok && a.Settled && now.Sub(a.At) < database.MetadataKnownEmptyTTL {
			continue
		}
		owed = append(owed, fb)
	}
	return owed
}

// clearDeferral drops entry's deferred fallback attempts once the book has a
// usable candidate: it no longer waits on a fallback lookup, and the review
// page's "deferred" chip must not keep listing it.
func clearDeferral(mfs *metafetch.Service, bookID string, entry *database.MetadataCandidateCache) {
	if entry == nil || !entry.FallbackDeferred() {
		return
	}
	if err := mfs.ClearDeferredFallbackAttempts(bookID, entry.SourceHash, entry.SearchFingerprint); err != nil {
		candidateFetchLog.Warn("clear deferred fallback attempts: book=%s err=%v",
			logger.SanitizeLogValue(bookID), logger.SanitizeLogValue(err.Error()))
	}
}

// entryHasSource reports whether entry holds a candidate from source.
func entryHasSource(entry *database.MetadataCandidateCache, source string) bool {
	if entry == nil {
		return false
	}
	for _, raw := range entry.Candidates {
		var c metafetch.MetadataCandidate
		if json.Unmarshal(raw, &c) == nil && c.Source == source {
			return true
		}
	}
	return false
}

// fallbackGateStore is what fallbackGates reads: the owner-manual check's
// readers (applygate.ManualOnlyReaders).
type fallbackGateStore interface {
	applygate.ManualOnlyFilesReader
	applygate.ManualOnlySeriesReader
	applygate.ManualOnlyTagReader
	database.BookAuthorReader
}

// fallbackGate is what the fetch-side gates allow for one book.
type fallbackGate struct {
	// Applied: the book's metadata is applied; no fallback provider is asked
	// (the same fetch-time gate the scheduled selection applies).
	Applied bool
	// ManualOnly is the owner-manual-only finding (Doctor Who / Big Finish
	// and the other ManualOnly franchises, applygate.BulkManualOnlyGuard):
	// such a book is never bulk-applied, so no GOOGLE quota is spent on it
	// unattended. Open Library is free and is still asked: its candidates
	// land in the review list for the owner's manual apply.
	ManualOnly string
	// ReadErr: the manual-only check could not be read. Google Books is
	// then deferred (not skipped, not no_match): the next run checks again.
	ReadErr string
}

func fallbackGates(store fallbackGateStore, book *database.Book, searchQuery string) fallbackGate {
	if database.MetadataApplied(book.MetadataReviewStatus) {
		return fallbackGate{Applied: true}
	}
	g := applygate.BulkManualOnlyGuard(applygate.ManualOnlyReaders{Files: store, Series: store, Authors: store, Tags: store}, book, searchQuery)
	return fallbackGate{ManualOnly: g.StoreDetail, ReadErr: g.ReadErr}
}

// failedTitleSource names the first title-searching source of the chain that
// failed in resp ("" = none): a source asked by title
// (metafetch.IsTitleSearchingProvider; not the ASIN-only Audnexus, whose "no
// such ASIN" says nothing about the title). Such a failure means the chain's
// question went unanswered, so the fallback is not asked: a book Audible
// could not be asked about is not one Audible found nothing for.
func failedTitleSource(resp *metafetch.SearchMetadataResponse, idByName map[string]string) string {
	if resp == nil || len(resp.SourcesFailed) == 0 {
		return ""
	}
	names := make([]string, 0, len(resp.SourcesFailed))
	for n := range resp.SourcesFailed {
		names = append(names, n)
	}
	slices.Sort(names)
	for _, n := range names {
		if metafetch.IsTitleSearchingProvider(idByName[n]) {
			return n + ": " + resp.SourcesFailed[n]
		}
	}
	return ""
}

// sourceIDsByName inverts nameByID (ActiveSourceNamesByID) for
// failedTitleSource, adding the names the search's direct ASIN lookup files
// a failure under when its provider is not in the chain ("Audible", and
// "Audnexus (Audible)", the label it gives Audnexus), so an ASIN-only
// Audnexus lookup failing is never mistaken for a title source failing.
func sourceIDsByName(nameByID map[string]string) map[string]string {
	out := map[string]string{"Audible": metadata.SourceIDAudible, "Audnexus (Audible)": metadata.SourceIDAudnexus}
	for id, n := range nameByID {
		out[n] = id
	}
	return out
}

// fallbackDeferrable reports whether a fallback lookup's error is a passing
// one -- a spent daily budget, a throttle hold or an open breaker, a 429, a
// quota or rate refusal answered as 403, a 5xx, a timeout or transport
// failure, a cancelled run -- after which the book waits for a later run.
// Anything else (a 400, a 401/403 naming no quota, a decode failure) is not:
// deferring it would re-select the same book every run and starve the rest.
func fallbackDeferrable(err error) bool {
	switch {
	case err == nil:
		return false
	case metadata.IsDailyBudgetRefusal(err),
		errors.Is(err, metadata.ErrProviderThrottled),
		errors.Is(err, metadata.ErrCircuitOpen),
		errors.Is(err, context.Canceled),
		errors.Is(err, context.DeadlineExceeded):
		return true
	}
	var se *metadata.ProviderStatusError
	if errors.As(err, &se) {
		if se.Status == 429 || se.Status >= 500 {
			return true
		}
		reason, _, _ := metadata.ClassifyProviderError(se)
		return reason == metadata.ThrottleDailyQuota || reason == metadata.ThrottleRateLimit
	}
	var ne net.Error
	return errors.As(err, &ne)
}

// fallbackPermanent reports a provider's permanent refusal: a 4xx that is not
// a quota or rate refusal. It SETTLES the attempt (FallbackAttempts), so the
// provider is not asked about the same inputs again within the TTL.
func fallbackPermanent(err error) bool {
	var se *metadata.ProviderStatusError
	return errors.As(err, &se) && se.Status >= 400 && se.Status < 500 && !fallbackDeferrable(err)
}

// candidateFallbackInput is what runCandidateFallback needs from
// fetchCandidateForBook: the book, its result skeleton and the exact search
// inputs the primary pass used, so the fallback's cache row carries the same
// SourceHash and fingerprint (the apply gate would otherwise read it as
// identity_stale).
type candidateFallbackInput struct {
	store    candidateFetchStore
	limiter  *rate.Limiter
	book     *database.Book
	bookInfo CandidateBookInfo
	query    string
	author   string
	force    bool
	pending  []fallbackProvider
	entry    *metafetch.MetadataCandidateCache // the book's current row; nil when there is none
	why      string                            // why the chain's candidates were not usable
	// googleCapped: the selection put this book's Google step off
	// (metabatch.FetchOpParams.GoogleCappedBookIDs) -- deferred unasked and
	// unrecorded, so a later quota day still selects it first.
	googleCapped bool
	cached       string // CandidateResult.Cached when the primary answer came from the cache
	// carryFrom is the SourceHash of the book's row as the fetch found it,
	// when it was vouched for the book as it is now ("" when it was not, or
	// there was none): each fallback answer merges into that row, and an
	// empty one keeps it (metafetch.SearchOptions.CarryFromSourceHash).
	carryFrom string
	withQuery    func(CandidateResult) CandidateResult
}

// runCandidateFallback asks the pending fallback providers, in order, until
// the book has a usable candidate (metabatch.NoUsableCandidate). A provider's
// candidates are MERGED into the book's cached ones (MergeWithCached), never
// replace them.
//
//   - Open Library: any failure or hold moves on to Google Books (S3).
//   - Google Books: skipped for an owner-manual-only book; deferred when the
//     manual check cannot be read, the shared daily budget's background
//     share is spent, a throttle holds it, or the lookup failed for a passing
//     reason (fallbackDeferrable). A permanent refusal is an error result and
//     is remembered (FallbackAttempts.Settled). A deferral is never no_match.
//
// Every attempt is stamped on the row (RecordFallbackAttempt), so the
// scheduled selection rotates deferred books by oldest attempt.
func (s *Server) runCandidateFallback(ctx context.Context, mfs *metafetch.Service, in candidateFallbackInput) CandidateResult {
	var steps []metabatch.FallbackStep
	finish := func(r CandidateResult) CandidateResult {
		r.Fallback = steps
		if r.Cached == "" {
			r.Cached = in.cached
		}
		return in.withQuery(r)
	}
	fromEntry := func() CandidateResult {
		if in.entry != nil {
			return candidateResultFromEntry(in.store, in.bookInfo, in.book.ID, in.query, in.entry)
		}
		return CandidateResult{Book: in.bookInfo, Status: "no_match"}
	}
	record := func(fb fallbackProvider, outcome string, settled bool) {
		if in.entry == nil {
			return
		}
		if err := mfs.RecordFallbackAttempt(in.book.ID, in.entry.SourceHash, in.entry.SearchFingerprint, fb.name,
			database.FallbackAttempt{At: time.Now().UTC(), Settled: settled, Outcome: outcome}); err != nil {
			// The attempt is bookkeeping for the selection's rotation; the
			// result row still says what happened.
			candidateFetchLog.Warn("record fallback attempt: book=%s provider=%s err=%v",
				logger.SanitizeLogValue(in.book.ID), fb.id, logger.SanitizeLogValue(err.Error()))
		}
	}
	gate := fallbackGates(in.store, in.book, in.query)
	if gate.Applied {
		for _, fb := range in.pending {
			steps = append(steps, metabatch.FallbackStep{Provider: fb.id, Outcome: metabatch.FallbackSkipped,
				At: time.Now().UTC(), Detail: "metadata already applied"})
		}
		return finish(fromEntry())
	}
	var failed []string // providers that failed without deferring the book
	for _, fb := range in.pending {
		step := metabatch.FallbackStep{Provider: fb.id, At: time.Now().UTC(), Detail: in.why}
		if fb.isGoogle() {
			defer1 := func(detail string) CandidateResult {
				step.Outcome, step.Detail = metabatch.FallbackDeferred, detail
				steps = append(steps, step)
				record(fb, metabatch.FallbackDeferred, false)
				return finish(deferredResult(in.bookInfo, step))
			}
			switch {
			case gate.ManualOnly != "":
				step.Outcome, step.Detail = metabatch.FallbackSkipped, "owner-manual only: "+gate.ManualOnly
				steps = append(steps, step)
				continue
			case gate.ReadErr != "":
				return defer1("owner-manual check failed: " + gate.ReadErr)
			case in.googleCapped:
				// Not an attempt: nothing was asked, and stamping one would
				// move the book behind every book attempted before today.
				step.Outcome = metabatch.FallbackDeferred
				step.Detail = "today's background Google Books budget is allotted to books waiting longer; left for a later quota day"
				steps = append(steps, step)
				return finish(deferredResult(in.bookInfo, step))
			}
			if hold, held := metadata.DefaultThrottleRegistry().Get(fb.id); held {
				return defer1(fmt.Sprintf("held by a %s throttle until %s", hold.Reason, hold.Until.UTC().Format(time.RFC3339)))
			}
			// The run's tier (dailyquota.PriorityOf): a single-book "Search
			// again" is interactive, everything else background.
			tier := dailyquota.PriorityOf(ctx)
			if b := metadata.GoogleBooksBudget(); b.Remaining(tier) <= 0 {
				return defer1(fmt.Sprintf("daily budget's %s share spent (%d used today, cap %d); left for the next quota day",
					tier, b.Used(), b.Limit(tier)))
			}
		}
		// The row merged into is the one this book was served or fetched --
		// also when it was vouched for under other hashed inputs (a raw
		// author credit, a pre-2026-09-28 no-author row), which a merge by
		// the search's own hash would not find and would replace. Only a
		// vouched row: in.entry may be one the verdict refused.
		entry, resp, err := mfs.FetchAndCacheWithResponse(ctx, in.limiter, in.book.ID, in.query, in.author, "", "",
			metafetch.SearchOptions{OnlySources: []string{fb.name}, BypassFetchCache: in.force, MergeWithCached: true,
				MergeUsable: metabatch.UsableRanker(in.store, in.book), CarryFromSourceHash: in.carryFrom})
		if err != nil {
			deferrable := fallbackDeferrable(err)
			if fb.isGoogle() && deferrable {
				step.Outcome, step.Detail = metabatch.FallbackDeferred, joinDetail(in.why, err.Error())
				steps = append(steps, step)
				record(fb, metabatch.FallbackDeferred, false)
				return finish(deferredResult(in.bookInfo, step))
			}
			step.Outcome, step.Detail = metabatch.FallbackError, joinDetail(in.why, err.Error())
			steps = append(steps, step)
			record(fb, metabatch.FallbackError, fallbackPermanent(err))
			if !fb.isGoogle() {
				// Open Library failing or held is no reason to keep Google
				// Books from the book: move on.
				failed = append(failed, fb.name+": "+err.Error())
				continue
			}
			return finish(CandidateResult{Book: in.bookInfo, Status: "error",
				Error: fmt.Sprintf("fallback search on %s failed: %v", fb.name, err)})
		}
		in.entry = entry
		step.Outcome = metabatch.FallbackNoMatch
		attemptOutcome := metabatch.FallbackNoMatch
		if resp != nil && len(resp.Results) > 0 {
			step.Outcome, attemptOutcome = metabatch.FallbackMatched, metabatch.FallbackMatched
			if !entryHasSource(entry, fb.name) {
				// Every candidate it found ranked below the row's cap
				// (metadataCacheTopN): it answered, but nothing it said is
				// kept. Settled -- asking again gives the same answer -- but
				// not recorded as a match.
				attemptOutcome = fallbackOutcomeRankedOut
				step.Detail = joinDetail(step.Detail, "its candidates ranked below the row's top 10 and were not kept")
			}
		}
		steps = append(steps, step)
		record(fb, attemptOutcome, true)
		v := metabatch.NoUsableCandidate(in.store, in.book, entry)
		if v.Usable {
			clearDeferral(mfs, in.book.ID, entry)
			return finish(candidateResultFromEntry(in.store, in.bookInfo, in.book.ID, in.query, entry))
		}
		in.why = v.Why
	}
	r := fromEntry()
	if len(failed) > 0 && r.Status == "no_match" {
		// A provider that failed did not answer: this is not a clean "nothing
		// found", and the next run asks it again.
		r.Status = "error"
		r.Error = "fallback incomplete: " + strings.Join(failed, "; ")
	}
	return finish(r)
}

// deferredResult is the result row of a book whose fallback was deferred.
func deferredResult(info CandidateBookInfo, step metabatch.FallbackStep) CandidateResult {
	return CandidateResult{Book: info, Status: candidateStatusDeferred,
		Error: fmt.Sprintf("deferred: %s fallback not asked (%s)", step.Provider, step.Detail)}
}

// fallbackOutcomeRankedOut is a settled fallback attempt whose candidates
// were all dropped by the row's top-N cap (database.FallbackAttempt.Outcome).
const fallbackOutcomeRankedOut = "ranked_out"

// candidateStatusDeferred is the result status of a book whose fallback lookup
// was put off (budget spent, provider held or failed for a passing reason):
// not a no_match.
const candidateStatusDeferred = "deferred"

func joinDetail(a, b string) string {
	if a == "" {
		return b
	}
	return a + "; " + b
}
