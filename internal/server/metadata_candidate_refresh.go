// file: internal/server/metadata_candidate_refresh.go
// version: 1.0.0
// guid: e4b82a9d-1f63-4c75-8a0e-6d3c5b19f7a2
// last-edited: 2026-11-16
//
// The candidate-fetch op's refresh accounting: which sources a search could
// not ask (degraded / failed books) and the run's per-cause summary line.

package server

import (
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/falkcorp/audiobook-organizer/internal/metadata"
	"github.com/falkcorp/audiobook-organizer/internal/metafetch"
)

// chainFailure names the sources a search could not ask or hear from, sorted,
// and why, as one metadata.Cause* word: the first named source whose own error
// classifies, else ferr's. resp is nil when the search failed before it ran;
// then there are no names and the cause is ferr's.
func chainFailure(resp *metafetch.SearchMetadataResponse, ferr error) (names []string, cause string) {
	if resp != nil {
		for n := range resp.SourcesFailed {
			names = append(names, n)
		}
		slices.Sort(names)
		errs := resp.SourceErrors()
		for _, n := range names {
			if c := metadata.FailureCause(errs[n]); c != "" {
				return names, c
			}
		}
	}
	if c := metadata.FailureCause(ferr); c != "" {
		return names, c
	}
	if len(names) > 0 {
		return names, metadata.CauseOther
	}
	return nil, ""
}

// onlyASINSourcesFailed reports ErrNoSourceAnswered caused solely by sources
// that are asked by identifier alone (Audnexus): the title-searching sources
// were not among the failures, so the book's TITLE question was never refused
// and the fallback providers may still be asked about it. A search that failed
// for any other reason (nothing failed by name, a cancelled run, a title
// source down) is not this case.
func onlyASINSourcesFailed(resp *metafetch.SearchMetadataResponse, ferr error, idByName map[string]string) bool {
	return resp != nil && len(resp.SourcesFailed) > 0 &&
		errors.Is(ferr, metafetch.ErrNoSourceAnswered) &&
		failedTitleSource(resp, idByName) == ""
}

// chainIncompleteMessage is the Error of a book whose chain could not be asked
// in full and whose fallback found nothing: not a clean "no match".
func chainIncompleteMessage(resp *metafetch.SearchMetadataResponse) string {
	names := make([]string, 0, len(resp.SourcesFailed))
	for n := range resp.SourcesFailed {
		names = append(names, n)
	}
	slices.Sort(names)
	parts := make([]string, 0, len(names))
	for _, n := range names {
		parts = append(parts, n+": "+resp.SourcesFailed[n])
	}
	return "chain incomplete: " + strings.Join(parts, "; ")
}

// refreshCauseCounts renders by-cause counts in a stable order, e.g.
// "bad_asin 2, timeout 12" ("" when empty).
func refreshCauseCounts(m map[string]int64) string {
	keys := make([]string, 0, len(m))
	for k, n := range m {
		if n > 0 {
			keys = append(keys, k)
		}
	}
	slices.Sort(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, fmt.Sprintf("%s %d", k, m[k]))
	}
	return strings.Join(parts, ", ")
}
