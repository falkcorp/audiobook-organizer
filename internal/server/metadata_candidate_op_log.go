// file: internal/server/metadata_candidate_op_log.go
// version: 1.2.0
// guid: 17ed77c1-1759-4abb-947e-4de2acc4000c
// last-edited: 2026-10-06
//
// Per-book outcome lines and running outcome counts for the
// metadata.candidate-fetch op log.

package server

import (
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/falkcorp/audiobook-organizer/internal/authorname"
	"github.com/falkcorp/audiobook-organizer/internal/logger"
	"github.com/falkcorp/audiobook-organizer/internal/metabatch"
	opsregistry "github.com/falkcorp/audiobook-organizer/internal/operations/registry"
	"github.com/falkcorp/audiobook-organizer/internal/organizer"
)

// candidateFetchProgressEvery is how many finished books sit between rebuilds
// of the count-bearing progress message.
//
// VOLUME. The op log keeps every line (op_logs_v2 has no per-op cap; the
// detail endpoint serves the last 50 by default, up to 5000, and the download
// serves all of them). dbReporter writes one log row per DISTINCT progress
// message, so a message rebuilt on every book adds a second row per book next
// to the per-book outcome line, doubling the rows every detail poll reads and
// halving how many outcomes fit in the default 50-line tail. Rebuilding the
// counts only every N books keeps the outcome lines dominant; between rebuilds
// UpdateProgress is still called per book with the unchanged message, so the
// bar and the stuck-op watchdog advance and no extra row is written.
const candidateFetchProgressEvery = 25

// candidateFetchTally is the running outcome count of one candidate-fetch
// attempt. Every method is safe for concurrent use by the worker pool.
type candidateFetchTally struct {
	matched, noMatch, skipped, errored atomic.Int64
	// deferred: books whose fallback lookup was put off (the Google Books
	// daily budget spent, or the provider held or failing): left for a
	// later run, not a no-match (candidate_fallback.go).
	deferred atomic.Int64
	// fromCache and knownEmpty count books answered from the candidate cache
	// without a provider call (see fetchCandidateForBook); they overlap the
	// outcome counts above rather than adding to them.
	fromCache, knownEmpty atomic.Int64

	mu  sync.Mutex
	msg string
}

// record counts one book's outcome.
func (t *candidateFetchTally) record(r CandidateResult) {
	switch r.Status {
	case "matched":
		t.matched.Add(1)
	case "no_match":
		t.noMatch.Add(1)
	case "skipped":
		t.skipped.Add(1)
	case candidateStatusDeferred:
		t.deferred.Add(1)
	default:
		t.errored.Add(1)
	}
	switch r.Cached {
	case candidateCachedCandidates:
		t.fromCache.Add(1)
	case candidateCachedKnownEmpty:
		t.knownEmpty.Add(1)
	}
}

// counts renders the outcome counts, e.g. "matched 820, no match 55, skipped
// 2, errors 0, from cache 0, known empty 3".
func (t *candidateFetchTally) counts() string {
	return fmt.Sprintf("matched %d, no match %d, skipped %d, deferred %d, errors %d, from cache %d, known empty %d",
		t.matched.Load(), t.noMatch.Load(), t.skipped.Load(), t.deferred.Load(), t.errored.Load(),
		t.fromCache.Load(), t.knownEmpty.Load())
}

// progressLine returns the progress message for finished of total books. The
// counts are rebuilt every candidateFetchProgressEvery books and on the last
// one; in between the previous message is returned unchanged (see the VOLUME
// note on candidateFetchProgressEvery).
func (t *candidateFetchTally) progressLine(finished int64, total int) string {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.msg == "" || finished%candidateFetchProgressEvery == 0 || finished >= int64(total) {
		t.msg = fmt.Sprintf("fetched %d/%d — %s", finished, total, t.counts())
	}
	return t.msg
}

// opLogQuoted renders a user-controlled value for an op log line: sanitized (no
// line breaks or control characters can forge a second line) and quoted.
func opLogQuoted(s string) string {
	return fmt.Sprintf("%q", logger.SanitizeLogValue(s))
}

// opLogBookLabel renders a book as `"Title" by "Author"` for a log line.
func opLogBookLabel(title, author string) string {
	if strings.TrimSpace(author) == "" {
		return opLogQuoted(title)
	}
	return opLogQuoted(title) + " by " + opLogQuoted(author)
}

// opLogBookRef is opLogBookLabel for a book whose title may be blank or a
// placeholder: such a title identifies nothing ("skipped: \"\"" repeated a
// hundred times), so the book's id and file path are appended.
func opLogBookRef(title, author, id, path string) string {
	label := opLogBookLabel(title, author)
	if !organizer.IsPlaceholderTitle(title) {
		return label
	}
	ref := "book " + opLogQuoted(id)
	if strings.TrimSpace(path) != "" {
		ref += ", " + opLogQuoted(path)
	}
	return label + " (" + ref + ")"
}

// candidateOutcomeLine renders one book's outcome as an op log line and picks
// its level: Info for matched / no match / skipped, Warn for an error. The
// facts are in the MESSAGE, not only the attrs, because the operation log view
// and the log download print the message.
func candidateOutcomeLine(r CandidateResult) (slog.Level, string, []slog.Attr) {
	book := opLogBookRef(r.Book.Title, r.Book.Author, r.Book.ID, r.Book.FilePath)
	attrs := []slog.Attr{
		slog.String("book_id", logger.SanitizeLogValue(r.Book.ID)),
		slog.String("outcome", r.Status),
	}
	if r.SearchQuery != "" {
		attrs = append(attrs,
			slog.String("search_query", logger.SanitizeLogValue(r.SearchQuery)),
			slog.String("search_query_source", r.SearchQuerySource))
	}
	// The query is spelled out whenever it is not simply the book's title, so
	// a match found via the transcribed title says so.
	query := ""
	if r.SearchQuery != "" && r.SearchQuerySource != metabatch.SearchQuerySourceTitle {
		query = fmt.Sprintf(" [searched %s by %s]", r.SearchQuerySource, opLogQuoted(r.SearchQuery))
	}
	if fb := fallbackSummary(r.Fallback); fb != "" {
		attrs = append(attrs, slog.String("fallback", fb))
		query += " [fallback " + fb + "]"
	}
	cache := ""
	switch r.Cached {
	case candidateCachedCandidates:
		cache = " (from cache)"
	case candidateCachedKnownEmpty:
		cache = " (known empty)"
	}
	reason := strings.TrimPrefix(r.Error, "skipped: ")

	switch r.Status {
	case "matched":
		c := r.Candidate
		if c == nil {
			return slog.LevelInfo, fmt.Sprintf("matched: %s%s%s", book, query, cache), attrs
		}
		attrs = append(attrs,
			slog.String("candidate_source", logger.SanitizeLogValue(c.Source)),
			slog.Float64("candidate_score", c.Score))
		return slog.LevelInfo, fmt.Sprintf("matched: %s → %s (%s, score %.2f)%s%s",
			book, opLogBookLabel(c.Title, c.Author), logger.SanitizeLogValue(c.Source), c.Score, query, cache), attrs
	case "no_match":
		if reason == "" {
			reason = "no provider returned a candidate"
		}
		searched := r.SearchQuery
		if searched == "" {
			searched = r.Book.Title
		}
		src := r.SearchQuerySource
		if src == "" {
			src = metabatch.SearchQuerySourceTitle
		}
		return slog.LevelInfo, fmt.Sprintf("no match: %s — searched %s %s, %s: %s%s",
			book, src, opLogQuoted(searched), searchedAuthorPhrase(r), logger.SanitizeLogValue(reason), cache), attrs
	case "skipped":
		return slog.LevelInfo, fmt.Sprintf("skipped: %s — %s", book, logger.SanitizeLogValue(reason)), attrs
	case candidateStatusDeferred:
		return slog.LevelInfo, fmt.Sprintf("deferred: %s — %s", book, logger.SanitizeLogValue(strings.TrimPrefix(r.Error, "deferred: "))), attrs
	default:
		return slog.LevelWarn, fmt.Sprintf("error: %s — %s%s", book, logger.SanitizeLogValue(r.Error), query), attrs
	}
}

// fallbackSummary renders a book's fallback turns for a log line, e.g.
// "openlibrary=no_match google-books=deferred" ("" when there were none).
func fallbackSummary(steps []metabatch.FallbackStep) string {
	parts := make([]string, 0, len(steps))
	for _, st := range steps {
		parts = append(parts, st.Provider+"="+st.Outcome)
	}
	return strings.Join(parts, " ")
}

// searchedAuthorPhrase names the author a no-match search actually asked
// with (CandidateResult.SearchAuthor), not the book's stored author: a
// placeholder author is never sent, and a line saying "author \"Unknown
// Author\"" would claim a search that did not happen.
func searchedAuthorPhrase(r CandidateResult) string {
	if r.SearchAuthor != "" {
		return "author " + opLogQuoted(r.SearchAuthor)
	}
	if authorname.IsPlaceholderAuthor(r.Book.Author) && strings.TrimSpace(r.Book.Author) != "" {
		return "no author hint (placeholder " + opLogQuoted(r.Book.Author) + " not sent)"
	}
	return "no author hint"
}

// logCandidateOutcome writes one book's outcome line to the op log.
func logCandidateOutcome(reporter opsregistry.Reporter, r CandidateResult) {
	level, msg, attrs := candidateOutcomeLine(r)
	_ = reporter.Log(level, msg, attrs...)
}
