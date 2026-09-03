// file: internal/metafetch/chain_throttle.go
// version: 1.0.0
// guid: 13b1277e-34bb-49e5-959a-c749f34c537f
// last-edited: 2026-09-03

package metafetch

import (
	"fmt"

	"github.com/falkcorp/audiobook-organizer/internal/metadata"
)

// PrepareFetchChain applies the chain policy every bulk-fetch path needs:
// honour prefer_audible, then drop providers under a global throttle. It
// returns the ids it dropped so the caller can log them with its own context,
// and an error when nothing callable is left.
//
// ONE implementation, deliberately. This logic existed as two near-copies --
// the v2 op in internal/server and the maintenance job in
// internal/maintenance/jobs -- each with its own log line and its own error
// string. That is the shape that has already drifted once in this file's
// neighbourhood: the two chain BUILDERS are still near-copies and only one of
// them applies configured rate limits. A guard duplicated is a guard that gets
// fixed in one place.
func PrepareFetchChain(chain []metadata.MetadataSource, preferAudible bool) (live []metadata.MetadataSource, skipped []string, err error) {
	if preferAudible {
		// NewChainSource, not the bare client: a raw prepend would be the ONE
		// source in the chain with neither a circuit breaker nor a throttle,
		// and prefer_audible is exactly the flag the quota-exhausted run of
		// 2026-09-03 was dispatched with.
		audible := metadata.NewChainSource(metadata.NewAudibleClient())
		rest := make([]metadata.MetadataSource, 0, len(chain))
		for _, src := range chain {
			// Dedupe by provider id where both sides have one, falling back to
			// the display name. Keying on the display name alone missed an
			// already-wrapped Audible whose Name() differs, putting Audible in
			// the chain TWICE and double-calling it for every book.
			if metadata.ProviderIDOf(src) == metadata.SourceIDAudible || src.Name() == audible.Name() {
				continue
			}
			rest = append(rest, src)
		}
		chain = append([]metadata.MetadataSource{audible}, rest...)
	}

	live, skipped = metadata.UnthrottledSources(chain)
	if len(live) > 0 || len(skipped) == 0 {
		// An EMPTY chain is not an error here, deliberately. Nothing was held —
		// there is simply nothing configured — and the two callers already have
		// their own long-standing handling for that (the all-books op
		// substitutes Audible; the by-ids op proceeds and records not_found).
		// Turning "no sources configured" into a hard failure would be a
		// behaviour change unrelated to throttling, and it broke six existing
		// tests that pin the current shape. Only a THROTTLE stops a run.
		return live, skipped, nil
	}
	return nil, skipped, fmt.Errorf(
		"every configured metadata provider is throttled, so no book would be fetched; not starting. Holds: %s. Reset one at DELETE /api/v1/metadata/providers/throttles/{id}",
		metadata.ThrottleSummary(skipped))
}

// AllThrottledMidRunError reports that every provider became throttled while a
// run was already walking the library.
//
// The pre-flight check in PrepareFetchChain only sees the state at dispatch. The
// quota that motivated this feature ran out at book 350 of 22,934 -- so without
// a mid-run stop, books 351..N each got a ledger row, a warn line and a 200ms
// pause for work that provably could not happen, and the operation still
// reported success.
//
// Callers must ABORT on this rather than record a per-book failure: a book the
// run never reached has no ledger row and stays outstanding, so a later run
// picks it up. Marking books as tried when they were not is the outcome worth
// avoiding.
func AllThrottledMidRunError(chain []metadata.MetadataSource) error {
	_, skipped := metadata.UnthrottledSources(chain)
	return fmt.Errorf(
		"every metadata provider became throttled mid-run, so no further book could be fetched; stopping with the remainder untouched and retryable. Holds: %s. Reset one at DELETE /api/v1/metadata/providers/throttles/{id}",
		metadata.ThrottleSummary(skipped))
}
