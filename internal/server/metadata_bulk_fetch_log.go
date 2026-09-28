// file: internal/server/metadata_bulk_fetch_log.go
// version: 1.0.0
// guid: c6c22a1e-80d9-4499-b80a-4a4d4112fa4d
// last-edited: 2026-09-27
//
// Per-book outcome lines and count wording for the bulk metadata fetch
// (runBulkMetadataFetchAll / runBulkMetadataFetchForBookIDs) op log.

package server

import (
	"fmt"

	"github.com/falkcorp/audiobook-organizer/internal/logger"
	"github.com/falkcorp/audiobook-organizer/internal/metafetch"
)

// bulkFetchCounts renders the bulk fetch's running outcome counts. "found"
// is the ledger's "cached" status: the providers returned candidates and they
// were cached for review.
func bulkFetchCounts(found, notFound, skipped, errored int64) string {
	return fmt.Sprintf("found %d, not found %d, skipped %d, errors %d", found, notFound, skipped, errored)
}

// bulkFetchOutcomeLine renders one book's bulk-fetch outcome for the op log
// and returns the operations.ProgressReporter level for it ("info", or "warn"
// for a provider error). status is out.Status() (a metafetch.FetchStatus*).
func bulkFetchOutcomeLine(title, author string, out metafetch.ChainOutcome, status string) (level, msg string) {
	book := opLogBookLabel(title, author)
	cache := ""
	if out.CacheHit {
		cache = " (from cache)"
	}
	switch status {
	case metafetch.FetchStatusCached:
		variant := ""
		if out.Variant != "" {
			variant = " via title variant " + opLogQuoted(out.Variant)
		}
		top := ""
		if len(out.Results) > 0 {
			top = fmt.Sprintf(", top %s", opLogBookLabel(out.Results[0].Title, out.Results[0].Author))
		}
		return "info", fmt.Sprintf("found: %s → %d candidate(s) from %s%s%s%s",
			book, len(out.Results), logger.SanitizeLogValue(out.SourceName), top, variant, cache)
	case metafetch.FetchStatusFetchError:
		errText := ""
		if out.Err != nil {
			errText = out.Err.Error()
		}
		return "warn", fmt.Sprintf("error: %s — provider %s failed, left retryable: %s",
			book, logger.SanitizeLogValue(out.ErrSource), logger.SanitizeLogValue(errText))
	default:
		return "info", fmt.Sprintf("no match: %s — searched title %s, author %s: no provider returned a result%s",
			book, opLogQuoted(title), opLogQuoted(author), cache)
	}
}

// bulkFetchSkippedFragmentLine is the op-log line for a chapter fragment the
// bulk fetch declines to search.
func bulkFetchSkippedFragmentLine(title, author string) string {
	return fmt.Sprintf("skipped: %s — chapter fragment, not searched", opLogBookLabel(title, author))
}

// bulkFetchUntitledLine is the one op-log line for the books the bulk fetch
// left out because their title is empty or a placeholder. They get no ledger
// row and no per-book line (they are not searched, and a whole-library run
// would otherwise repeat the same lines every night), but the count is said
// out loud rather than dropped.
func bulkFetchUntitledLine(n int) string {
	return fmt.Sprintf("skipped %d book(s) with an empty or placeholder title — not searched "+
		"(searching a blank title matches whatever the catalog ranks first; the candidate fetch "+
		"searches these by their transcribed title)", n)
}
