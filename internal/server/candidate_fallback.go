// file: internal/server/candidate_fallback.go
// version: 1.0.0
// guid: 487502d2-faea-4677-8c9d-171a796ad643
// last-edited: 2026-10-06
//
// The batch candidate fetch's provider fallback: Open Library, then Google
// Books under a persisted daily budget, asked only for books the rest of the
// chain found nothing for. Owner decision 2026-10-06, "Both, spread over days".

package server

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sync"
	"time"

	"golang.org/x/time/rate"

	"github.com/falkcorp/audiobook-organizer/internal/applygate"
	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/metabatch"
	"github.com/falkcorp/audiobook-organizer/internal/metadata"
	"github.com/falkcorp/audiobook-organizer/internal/metafetch"
)

// candidateFallbackState is the process's Google Books fallback budget. It is
// built on first use from the server's store, so every candidate fetch (the
// op's workers, the lost-candidates refetch) reserves from one count. A test
// sets budget before the first fetch to install its own.
type candidateFallbackState struct {
	once   sync.Once
	budget *metafetch.DailyBudget
}

// googleBooksFallbackLimit is the configured daily limit
// (config.GoogleBooksFallbackDailyLimit): 0 is the default, negative is off
// (0 lookups).
func googleBooksFallbackLimit() int {
	switch n := config.AppConfig.GoogleBooksFallbackDailyLimit; {
	case n == 0:
		return metafetch.DefaultGoogleBooksFallbackDailyLimit
	case n < 0:
		return 0
	default:
		return n
	}
}

// googleBooksFallbackOn reports whether the Google Books fallback is turned on
// (a negative limit turns it off).
func googleBooksFallbackOn() bool { return config.AppConfig.GoogleBooksFallbackDailyLimit >= 0 }

// googleFallbackBudget returns the process's Google Books fallback budget,
// persisted through the server's store (GetRaw/SetRaw) so a restart keeps the
// day's count.
func (s *Server) googleFallbackBudget() *metafetch.DailyBudget {
	s.candidateFallback.once.Do(func() {
		if s.candidateFallback.budget != nil {
			return
		}
		var kv metafetch.RawKV
		if st := s.storeForWiring(); st != nil {
			kv = st
		}
		s.candidateFallback.budget = metafetch.NewDailyBudget(kv, metadata.SourceIDGoogleBooks, googleBooksFallbackLimit)
	})
	return s.candidateFallback.budget
}

// fallbackProvider is one enabled fallback provider: its id and the display
// name the cache and OnlySources key it by.
type fallbackProvider struct{ id, name string }

// candidateFallbackPlan returns the enabled fallback providers in fallback
// order (metafetch.CandidateFallbackProviderIDs).
func candidateFallbackPlan(nameByID map[string]string) []fallbackProvider {
	var plan []fallbackProvider
	for _, id := range metafetch.CandidateFallbackProviderIDs {
		if name, ok := nameByID[id]; ok {
			plan = append(plan, fallbackProvider{id: id, name: name})
		}
	}
	return plan
}

// splitFallback splits ask (the providers a search would ask: the cache
// verdict's unanswered list, or every active source) into the primary chain
// and the fallback providers still owed an answer, in fallback order. A
// fallback provider missing from ask has already answered this search
// identity with nothing (the cache's EmptyAnswers) and is not asked again.
func splitFallback(ask []string, plan []fallbackProvider) (primary []string, pending []fallbackProvider) {
	isFallback := make(map[string]bool, len(plan))
	for _, fb := range plan {
		isFallback[fb.name] = true
	}
	for _, n := range ask {
		if !isFallback[n] {
			primary = append(primary, n)
		}
	}
	for _, fb := range plan {
		if slices.Contains(ask, fb.name) {
			pending = append(pending, fb)
		}
	}
	return primary, pending
}

// fallbackGateStore is what fallbackGateReason reads: the owner-manual
// check's readers (applygate.ManualOnlyReaders).
type fallbackGateStore interface {
	applygate.ManualOnlyFilesReader
	applygate.ManualOnlySeriesReader
	applygate.ManualOnlyTagReader
	database.BookAuthorReader
}

// fallbackGateReason is why the fetch-side gates refuse a fallback lookup for
// book ("" = none): the same fetch-time gates the scheduled selection applies
// (metadata applied, unfetchedCandidateBookIDs) plus the owner-manual-only
// rule (Doctor Who / Big Finish and the other ManualOnly franchises,
// applygate.BulkManualOnlyGuard) -- such a book is never bulk-applied, so no
// quota is spent fetching for it unattended. A read failure in the manual-only
// check refuses too (fail closed).
func fallbackGateReason(store fallbackGateStore, book *database.Book, searchQuery string) string {
	if database.MetadataApplied(book.MetadataReviewStatus) {
		return "metadata already applied"
	}
	g := applygate.BulkManualOnlyGuard(applygate.ManualOnlyReaders{Files: store, Series: store, Authors: store, Tags: store}, book, searchQuery)
	if g.ReadErr != "" {
		return "owner-manual check failed: " + g.ReadErr
	}
	if g.StoreDetail != "" {
		return "owner-manual only: " + g.StoreDetail
	}
	return ""
}

// candidateFallbackInput is what runCandidateFallback needs from
// fetchCandidateForBook: the book, its result skeleton and the exact search
// inputs the primary pass used, so the fallback's cache row carries the same
// SourceHash and fingerprint (the apply gate would otherwise read it as
// identity_stale).
type candidateFallbackInput struct {
	store     candidateFetchStore
	limiter   *rate.Limiter
	book      *database.Book
	bookInfo  CandidateBookInfo
	query     string
	author    string
	force     bool
	pending   []fallbackProvider
	entry     *metafetch.MetadataCandidateCache // the primary pass's row; nil when it asked nothing
	withQuery func(CandidateResult) CandidateResult
}

// runCandidateFallback asks the pending fallback providers, in order, until
// one returns candidates. It is called only when the primary chain found
// nothing. Google Books is asked only under its daily budget and only while
// no throttle holds it; otherwise the book is DEFERRED: its result row says
// so, and its cache row carries no Google answer, so the next run (the next
// quota day) asks Google again. A deferral never records no_match.
func (s *Server) runCandidateFallback(ctx context.Context, mfs *metafetch.Service, in candidateFallbackInput) CandidateResult {
	var steps []metabatch.FallbackStep
	finish := func(r CandidateResult) CandidateResult {
		r.Fallback = steps
		return in.withQuery(r)
	}
	noMatch := func() CandidateResult {
		if in.entry != nil {
			return candidateResultFromEntry(in.store, in.bookInfo, in.book.ID, in.query, in.entry)
		}
		return CandidateResult{Book: in.bookInfo, Status: "no_match"}
	}
	now := time.Now().UTC()
	if why := fallbackGateReason(in.store, in.book, in.query); why != "" {
		for _, fb := range in.pending {
			steps = append(steps, metabatch.FallbackStep{Provider: fb.id, Outcome: metabatch.FallbackSkipped, At: now, Detail: why})
		}
		return finish(noMatch())
	}
	for _, fb := range in.pending {
		step := metabatch.FallbackStep{Provider: fb.id, At: time.Now().UTC()}
		if fb.id == metadata.SourceIDGoogleBooks {
			if !googleBooksFallbackOn() {
				step.Outcome, step.Detail = metabatch.FallbackSkipped, "Google Books fallback turned off (google_books_fallback_daily_limit < 0)"
				steps = append(steps, step)
				continue
			}
			if hold, held := metadata.DefaultThrottleRegistry().Get(fb.id); held {
				step.Outcome = metabatch.FallbackDeferred
				step.Detail = fmt.Sprintf("held by a %s throttle until %s", hold.Reason, hold.Until.UTC().Format(time.RFC3339))
				steps = append(steps, step)
				return finish(deferredResult(in.bookInfo, step))
			}
			used, limit, err := s.googleFallbackBudget().Reserve()
			if err != nil {
				step.Outcome = metabatch.FallbackDeferred
				if errors.Is(err, metafetch.ErrDailyBudgetSpent) {
					step.Detail = fmt.Sprintf("daily budget spent (%d/%d); left for the next quota day", used, limit)
				} else {
					step.Detail = "daily budget unreadable: " + err.Error()
				}
				steps = append(steps, step)
				return finish(deferredResult(in.bookInfo, step))
			}
			step.Detail = fmt.Sprintf("budget %d/%d", used, limit)
		}
		entry, err := mfs.FetchAndCacheLimited(ctx, in.limiter, in.book.ID, in.query, in.author, "", "",
			metafetch.SearchOptions{OnlySources: []string{fb.name}, BypassFetchCache: in.force})
		if err != nil {
			if fb.id == metadata.SourceIDGoogleBooks {
				// A failed Google lookup (a 429 quota hold, an outage) is not an
				// answer: the book waits for the next run rather than being
				// reported as an error every 6 hours or recorded as no_match.
				step.Outcome, step.Detail = metabatch.FallbackDeferred, joinDetail(step.Detail, err.Error())
				steps = append(steps, step)
				return finish(deferredResult(in.bookInfo, step))
			}
			step.Outcome, step.Detail = metabatch.FallbackError, err.Error()
			steps = append(steps, step)
			return finish(CandidateResult{Book: in.bookInfo, Status: "error",
				Error: fmt.Sprintf("fallback search on %s failed: %v", fb.name, err)})
		}
		in.entry = entry
		if len(entry.Candidates) > 0 {
			step.Outcome = metabatch.FallbackMatched
			steps = append(steps, step)
			return finish(candidateResultFromEntry(in.store, in.bookInfo, in.book.ID, in.query, entry))
		}
		step.Outcome = metabatch.FallbackNoMatch
		steps = append(steps, step)
	}
	return finish(noMatch())
}

// deferredResult is the result row of a book whose fallback was deferred.
func deferredResult(info CandidateBookInfo, step metabatch.FallbackStep) CandidateResult {
	return CandidateResult{Book: info, Status: candidateStatusDeferred,
		Error: fmt.Sprintf("deferred: %s fallback not asked (%s)", step.Provider, step.Detail)}
}

// candidateStatusDeferred is the result status of a book whose fallback lookup
// was put off (budget spent, provider held or failed): not a no_match.
const candidateStatusDeferred = "deferred"

func joinDetail(a, b string) string {
	if a == "" {
		return b
	}
	return a + "; " + b
}
