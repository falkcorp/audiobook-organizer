// file: internal/metafetch/candidate_fallback.go
// version: 1.1.0
// guid: 2b452994-605f-4efc-9523-eccefaafce31
// last-edited: 2026-10-06

package metafetch

import (
	"github.com/falkcorp/audiobook-organizer/internal/metadata"
	"github.com/falkcorp/audiobook-organizer/internal/metadata/dailyquota"
)

// CandidateFallbackProviderIDs are the providers the batch candidate fetch
// (metadata.candidate-fetch) asks only AFTER the rest of the chain found
// nothing for a book, in this order: Open Library, then Google Books.
//
// Owner decision 2026-10-06 ("Both, spread over days"): a book the chain
// has no usable candidate for is tried on Open Library first, then on Google
// Books. Every Google Books lookup, on this path or any other, is counted by
// ONE daily budget enforced in Google's HTTP transport
// (metadata.GoogleBooksBudget): background lookups stop at 800 of the key's
// 1,000/day, the rest stays for the interactive search dialog. Every other
// search path still asks every enabled source at once; only the batch
// candidate fetch tiers them. (The ASIN backfill asks Audible only.)
var CandidateFallbackProviderIDs = []string{metadata.SourceIDOpenLibrary, metadata.SourceIDGoogleBooks}

// IsCandidateFallbackProvider reports whether id is one of
// CandidateFallbackProviderIDs.
func IsCandidateFallbackProvider(id string) bool {
	for _, f := range CandidateFallbackProviderIDs {
		if f == id {
			return true
		}
	}
	return false
}

// ActiveSourceNamesByID maps the provider id of every source a search would
// ask right now (the test override, or the configured chain) to its display
// name. The fetch cache's EmptyAnswers and SearchOptions.OnlySources are
// keyed by display name ("Google Books"), and the fallback order by provider
// id ("google-books"); this is the one translation between them. A source
// that declares no id (a test stub) is left out: it is never a fallback.
func (mfs *Service) ActiveSourceNamesByID() map[string]string {
	if mfs == nil {
		return nil
	}
	sources := mfs.overrideSources
	if len(sources) == 0 {
		sources = mfs.BuildSourceChain()
	}
	out := make(map[string]string, len(sources))
	for _, src := range sources {
		if id := metadata.ProviderIDOf(src); id != "" {
			out[id] = src.Name()
		}
	}
	return out
}

// DailyBudget is the per-provider daily lookup counter, moved to the leaf
// package internal/metadata/dailyquota (2026-10-06) so the provider HTTP
// transport can enforce it for every caller. These names are kept for the
// candidate fetch.
type DailyBudget = dailyquota.DailyBudget

// RawKV is the store slice a DailyBudget persists through.
type RawKV = dailyquota.RawKV

// ErrDailyBudgetSpent is dailyquota.ErrDailyBudgetSpent.
var ErrDailyBudgetSpent = dailyquota.ErrDailyBudgetSpent

// IsTitleSearchingProvider reports whether the provider with config id id is
// asked by title (every source but the ASIN-only Audnexus, policyASINOnly).
// A failure of such a source means the chain's question went unanswered; an
// ASIN-only source's "no such ASIN" says nothing about the title.
func IsTitleSearchingProvider(id string) bool { return sourcePolicyFor(id) != policyASINOnly }
