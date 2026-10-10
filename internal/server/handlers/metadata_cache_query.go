// file: internal/server/handlers/metadata_cache_query.go
// version: 1.2.0
// guid: 3e8b5d17-6a0c-4f92-b1d4-9c27e0a5f6b3
// last-edited: 2026-10-10

package handlers

import (
	"context"
	"errors"
	"fmt"
	"math"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gin-gonic/gin"
	"golang.org/x/sync/singleflight"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/httputil"
	"github.com/falkcorp/audiobook-organizer/internal/metabatch"
	"github.com/falkcorp/audiobook-organizer/internal/metafetch"
	"github.com/falkcorp/audiobook-organizer/internal/querygrammar"
)

// The server-side review query: GET /audiobooks/metadata/cache/review with
// view=page.
//
// The Review > Metadata lane loads every reviewable row into the browser and
// filters, counts, groups and pages them there (useMetadataLane.ts). view=page
// does that work on the server, over the review snapshot the server already
// holds in RAM: ONE evaluation per filter yields the ordered row list, from
// which the page, the total, the chip facets and the ids-only "select all N"
// list are all cut. Nothing in the web app calls it until task 02-PR18 switches
// the lane over; view=index is unchanged.
//
// The filter chain is a port of the lane's, rule for rule, and the shared
// fixture testdata/review_query_cases.json is what proves the two agree (02-PR18
// runs the same file through the TypeScript chain).
//
// Cost: the evaluation is a single sequential pass over the reviewable rows
// plus one pass for the multi-book key. BenchmarkReviewQuery_40k (40,000
// synthetic rows, M1 Max, 2026-10-10): chips only 0.5 ms, chips plus a
// substring title 0.6 ms, chips plus an RE2 title 1.7 ms, an RE2 matching
// every row then sorting all of them 8.7-9.1 ms; the per-generation base (the
// overlay and the row derivation) 13.5 ms. That is inside the interactive
// budget, so the pass is not sharded across workers: a pool would add
// goroutine handoff to a pass that finishes in single milliseconds, and the
// multi-book grouping needs the whole filtered set in one place anyway.
//
// Limits (a user pattern is code the server runs over every row): q is at
// most reviewQueryMaxQBytes, each value at most querygrammar.MaxTextValueBytes
// and each compiled pattern at most querygrammar.MaxPatternInst instructions,
// all refused at parse time with a 400. An evaluation with a regex or glob
// takes one of the process-wide pattern slots (querygrammar.AcquirePatternSlot;
// full past its wait is a 503) and a querygrammar.Budget of
// reviewPatternBudget: once its pattern matches have spent that much time it
// stops with a 400, never a partial list.

const (
	reviewViewPage = "page"

	// reviewPageDefaultLimit is the page size when the request sends none:
	// the lane's default page.
	reviewPageDefaultLimit = 50
	// reviewPageMaxLimit bounds one page. "Select all" uses ids=all, which
	// returns ids only, never rows.
	reviewPageMaxLimit = 500

	// reviewQueryLRUSize is how many evaluated result lists are kept for the
	// current generation, so paging, the count and ids=all for one filter
	// reuse one evaluation.
	reviewQueryLRUSize = 32
	// reviewQueryBaseMaxAge bounds how long one overlay (and its single
	// freshness clock read) is reused at an unchanged generation, so a
	// row's stale flag never lags the clock by more than this.
	reviewQueryBaseMaxAge = 30 * time.Second

	// reviewQueryMaxQBytes bounds the whole q parameter (free text plus every
	// field:value token). Each value inside it is further bounded by
	// querygrammar.MaxTextValueBytes and each pattern by
	// querygrammar.MaxPatternInst; this cap keeps a pasted wall of tokens
	// from being parsed at all.
	reviewQueryMaxQBytes = 1024
)

// reviewPatternBudget is the regex/glob matching time one evaluation may
// spend (querygrammar.Budget). The budget is created inside the shared
// evaluation (the singleflight), not from any one request's context, so a
// requester who gives up neither cancels the work for the others nor lets it
// run on unbounded. A var so tests can shrink it.
var reviewPatternBudget = querygrammar.DefaultPatternBudget

// Review chip views (the lane's ChipFilter). A chip view lists exactly the
// rows its chip counts, narrowed only by the title filter.
const (
	reviewChipMatched              = "matched"
	reviewChipNoMatch              = "no_match"
	reviewChipTotal                = "total"
	reviewChipErrors               = "errors"
	reviewChipNoCandidates         = "no_candidates"
	reviewChipResolvedNoCandidates = "resolved_no_candidates"
	reviewChipStale                = "stale"
	reviewChipDeferred             = "deferred"
)

var reviewChips = []string{
	reviewChipMatched, reviewChipNoMatch, reviewChipTotal, reviewChipErrors,
	reviewChipNoCandidates, reviewChipResolvedNoCandidates, reviewChipStale, reviewChipDeferred,
}

// Review sort orders. The default is the listing's own order (status rank,
// then the snapshot's FetchedAt-descending order).
const (
	reviewSortDefault    = ""
	reviewSortTitle      = "title"
	reviewSortConfidence = "confidence"
)

// ReviewQuery is a parsed view=page request. Every field is optional.
type ReviewQuery struct {
	// Q is the Title box, in the Library search grammar: a bare value, or
	// title: tokens, any of them negated with "-" or "NOT ".
	Q string
	// Source is a provider id (reviewProviderID's vocabulary).
	Source string
	// MinConfidence (0-100) applies to matched rows only, as the lane does.
	MinConfidence float64
	// The lane's switches.
	HideApplied, HideRejected, HideSkipped, HideNoMatch bool
	HideRuntime, MatchLanguage                          bool
	HasTranscription, TranscriptionMatched              bool
	HideMultiBook                                       bool
	// Ungrouped is the books the owner pulled out of a group (the lane's
	// client-only ungroupedIds); they never count toward a multi-book group.
	Ungrouped []string
	// Skipped is the lane's client-only `skipped` row state: skip never calls
	// the server, so the server cannot know it. HideSkipped hides these.
	Skipped []string
	// Chip is a chip view (reviewChip*); it pauses every filter but Q.
	Chip string
	// Bucket is reviewable (default) or unreviewable; with unreviewable and no
	// chip the list is the unreviewable rows narrowed by Q only.
	Bucket string
	// Sort is reviewSort*.
	Sort string
	// Limit and Offset page the list.
	Limit, Offset int
	// IDsAll asks for {ids, total, generation} only.
	IDsAll bool

	title *reviewTitleFilter
	// normalized is every field that decides the list (not the page), in a
	// fixed order: the LRU key.
	normalized string
}

// reviewQueryError is a request the query refuses (400).
type reviewQueryError struct{ msg string }

func (e *reviewQueryError) Error() string { return e.msg }

func badReviewQuery(format string, args ...any) error {
	return &reviewQueryError{msg: fmt.Sprintf(format, args...)}
}

// parseReviewQuery reads a view=page request. get returns a query parameter
// ("" when absent).
func parseReviewQuery(get func(string) string) (*ReviewQuery, error) {
	q := &ReviewQuery{
		Q:      get("q"),
		Source: strings.TrimSpace(get("source")),
		Chip:   strings.TrimSpace(get("chip")),
		Bucket: strings.TrimSpace(get("bucket")),
		Sort:   strings.TrimSpace(get("sort")),
		Limit:  reviewPageDefaultLimit,
	}
	var err error
	boolParam := func(name string, dst *bool) {
		if err != nil {
			return
		}
		raw := strings.TrimSpace(get(name))
		if raw == "" {
			return
		}
		v, perr := strconv.ParseBool(raw)
		if perr != nil {
			err = badReviewQuery("%s must be true or false, got %q", name, raw)
			return
		}
		*dst = v
	}
	boolParam("hide_applied", &q.HideApplied)
	boolParam("hide_rejected", &q.HideRejected)
	boolParam("hide_skipped", &q.HideSkipped)
	boolParam("hide_no_match", &q.HideNoMatch)
	boolParam("hide_runtime", &q.HideRuntime)
	boolParam("match_language", &q.MatchLanguage)
	boolParam("has_transcription", &q.HasTranscription)
	boolParam("transcription_matched", &q.TranscriptionMatched)
	boolParam("hide_multi_book", &q.HideMultiBook)
	if err != nil {
		return nil, err
	}
	if raw := strings.TrimSpace(get("min_confidence")); raw != "" {
		v, perr := strconv.ParseFloat(raw, 64)
		if perr != nil || math.IsNaN(v) || v < 0 || v > 100 {
			return nil, badReviewQuery("min_confidence must be a number from 0 to 100, got %q", raw)
		}
		q.MinConfidence = v
	}
	intParam := func(name string, lo, hi int, dst *int) error {
		raw := strings.TrimSpace(get(name))
		if raw == "" {
			return nil
		}
		v, perr := strconv.Atoi(raw)
		if perr != nil || v < lo || v > hi {
			return badReviewQuery("%s must be a whole number from %d to %d, got %q", name, lo, hi, raw)
		}
		*dst = v
		return nil
	}
	if err := intParam("limit", 1, reviewPageMaxLimit, &q.Limit); err != nil {
		return nil, err
	}
	if err := intParam("offset", 0, math.MaxInt32, &q.Offset); err != nil {
		return nil, err
	}
	switch ids := strings.TrimSpace(get("ids")); ids {
	case "":
	case "all":
		q.IDsAll = true
	default:
		return nil, badReviewQuery("ids must be all on view=page (a detail lookup by id uses the listing without view=page)")
	}
	if q.Chip != "" && !slices.Contains(reviewChips, q.Chip) {
		return nil, badReviewQuery("chip must be one of %s", strings.Join(reviewChips, ", "))
	}
	if len(q.Q) > reviewQueryMaxQBytes {
		return nil, badReviewQuery("q is %d bytes long; the limit is %d (search for a shorter part of it)", len(q.Q), reviewQueryMaxQBytes)
	}
	if q.Bucket == "" {
		q.Bucket = reviewBucketReviewable
	}
	if q.Bucket != reviewBucketReviewable && q.Bucket != reviewBucketUnreviewable {
		return nil, badReviewQuery("bucket must be reviewable or unreviewable")
	}
	switch q.Sort {
	case reviewSortDefault, reviewSortTitle, reviewSortConfidence:
	default:
		return nil, badReviewQuery("sort must be title or confidence (or absent for the listing order)")
	}
	q.Ungrouped = idList(get("ungrouped"))
	q.Skipped = idList(get("skipped"))
	title, terr := compileReviewTitleFilter(q.Q)
	if terr != nil {
		return nil, &reviewQueryError{msg: terr.Error()}
	}
	q.title = title
	q.normalized = q.normalize()
	return q, nil
}

// idList splits a comma list into sorted, de-duplicated, non-empty ids.
func idList(raw string) []string {
	if strings.TrimSpace(raw) == "" {
		return nil
	}
	var out []string
	for _, id := range strings.Split(raw, ",") {
		if id = strings.TrimSpace(id); id != "" {
			out = append(out, id)
		}
	}
	slices.Sort(out)
	return slices.Compact(out)
}

// normalize renders every field that decides the result LIST in a fixed
// order. limit, offset and ids do not: they cut the list. A chip view
// ignores every filter but the title, and the ungrouped and skipped lists
// matter only under their switches, so they are left out otherwise and more
// requests share one evaluation.
func (q *ReviewQuery) normalize() string {
	var b strings.Builder
	fmt.Fprintf(&b, "q=%q|sort=%s|", strings.TrimSpace(q.Q), q.Sort)
	if q.Chip != "" {
		fmt.Fprintf(&b, "chip=%s", q.Chip)
		return b.String()
	}
	if q.Bucket == reviewBucketUnreviewable {
		b.WriteString("bucket=unreviewable")
		return b.String()
	}
	fmt.Fprintf(&b, "src=%q|minc=%s|ha=%t|hr=%t|hs=%t|hn=%t|rt=%t|lang=%t|tr=%t|trm=%t|mb=%t",
		q.Source, strconv.FormatFloat(q.MinConfidence, 'g', -1, 64),
		q.HideApplied, q.HideRejected, q.HideSkipped, q.HideNoMatch, q.HideRuntime,
		q.MatchLanguage, q.HasTranscription, q.TranscriptionMatched, q.HideMultiBook)
	if q.HideMultiBook {
		fmt.Fprintf(&b, "|ug=%s", strings.Join(q.Ungrouped, ","))
	}
	if q.HideSkipped || q.HideRejected {
		// A skipped row is not "rejected" either (the lane's row state is
		// one value), so the list matters to hide_rejected too.
		fmt.Fprintf(&b, "|sk=%s", strings.Join(q.Skipped, ","))
	}
	return b.String()
}

// --- the Title filter --------------------------------------------------------

// reviewSearchFields is web/src/utils/searchParser.ts SEARCH_FIELDS. The Title
// box is parsed by that parser (compileTitleFilter), where a known field name
// makes a field filter and anything else is free text, so the server must know
// the same list to read a value the same way. Keep the two in step.
var reviewSearchFields = map[string]struct{}{}

func init() {
	for _, f := range []string{
		"title", "author", "narrator", "series", "series_number", "genre", "year", "language",
		"publisher", "edition", "description", "format", "duration", "has_duration", "metadata",
		"file_size", "bitrate", "codec", "sample_rate", "channels", "bit_depth", "quality",
		"library_state", "created_at", "updated_at", "isbn10", "isbn13", "work_id", "tag", "review",
		"has_cover", "has_written", "needs_writeback", "has_organized", "itunes_sync_status",
		"read_status", "progress_pct", "last_played",
	} {
		reviewSearchFields[f] = struct{}{}
	}
}

func isReviewSearchField(f string) bool {
	_, ok := reviewSearchFields[f]
	return ok
}

// searchFieldFilter is one field:value token (searchParser.ts FieldFilter).
type searchFieldFilter struct {
	field, value    string
	negated, quoted bool
}

// parseSearchFilters is searchParser.ts parseSearch, ported rule for rule:
// field:value tokens (quoted, /regex/ to the next unescaped slash and then
// the next space, [range]), "NOT " and "-" negation only in front of a known
// field, and everything else free text. Every delimiter is ASCII, so byte
// indices behave as the TypeScript's UTF-16 ones.
func parseSearchFilters(str string) (freeText string, filters []searchFieldFilter) {
	if strings.TrimSpace(str) == "" {
		return "", nil
	}
	var free []string
	pos := 0
	for pos < len(str) {
		if str[pos] == ' ' {
			pos++
			continue
		}
		negated := false
		startPos := pos
		if strings.HasPrefix(str[pos:], "NOT ") && pos+4 < len(str) {
			if fm, ok := tryMatchFieldValue(str, pos+4); ok && isReviewSearchField(fm.field) {
				negated = true
				pos += 4
			}
		}
		if !negated && str[pos] == '-' && pos+1 < len(str) {
			if fm, ok := tryMatchFieldValue(str, pos+1); ok && isReviewSearchField(fm.field) {
				negated = true
				pos++
			}
		}
		if fm, ok := tryMatchFieldValue(str, pos); ok && isReviewSearchField(fm.field) {
			if v := strings.TrimSpace(fm.value); v != "" {
				filters = append(filters, searchFieldFilter{field: fm.field, value: v, negated: negated, quoted: fm.quoted})
			}
			pos = fm.endPos
			continue
		}
		// Not a field:value: one free-text word.
		pos = startPos
		rel := strings.IndexByte(str[pos:], ' ')
		if rel == -1 {
			free = append(free, str[pos:])
			pos = len(str)
			continue
		}
		wordEnd := pos + rel
		word := str[pos:wordEnd]
		if word == "NOT" {
			nextWordEnd := -1
			if r := strings.IndexByte(str[wordEnd+1:], ' '); r != -1 {
				nextWordEnd = wordEnd + 1 + r
			}
			nextWord := str[wordEnd+1:]
			if nextWordEnd != -1 {
				nextWord = str[wordEnd+1 : nextWordEnd]
			}
			if colon := strings.IndexByte(nextWord, ':'); colon > 0 && !isReviewSearchField(nextWord[:colon]) {
				// NOT + an unknown field: both are free text.
				free = append(free, word, nextWord)
				if nextWordEnd == -1 {
					pos = len(str)
				} else {
					pos = nextWordEnd
				}
				continue
			}
		}
		free = append(free, word)
		pos = wordEnd
	}
	parts := free[:0]
	for _, p := range free {
		if p != "" {
			parts = append(parts, p)
		}
	}
	return strings.TrimSpace(strings.Join(parts, " ")), filters
}

type fieldValueMatch struct {
	field, value string
	quoted       bool
	endPos       int
}

func isSearchWordChar(c byte) bool {
	return c == '_' || (c >= '0' && c <= '9') || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z')
}

// tryMatchFieldValue is searchParser.ts tryMatchFieldValue.
func tryMatchFieldValue(str string, pos int) (fieldValueMatch, bool) {
	fieldEnd := pos
	for fieldEnd < len(str) && isSearchWordChar(str[fieldEnd]) {
		fieldEnd++
	}
	if fieldEnd == pos || fieldEnd >= len(str) || str[fieldEnd] != ':' {
		return fieldValueMatch{}, false
	}
	field := str[pos:fieldEnd]
	valueStart := fieldEnd + 1
	if valueStart >= len(str) {
		return fieldValueMatch{}, false
	}
	switch str[valueStart] {
	case '"':
		rel := strings.IndexByte(str[valueStart+1:], '"')
		if rel == -1 {
			// Unclosed quote: the rest of the input is the value.
			return fieldValueMatch{field: field, value: str[valueStart+1:], quoted: true, endPos: len(str)}, true
		}
		closeQuote := valueStart + 1 + rel
		return fieldValueMatch{field: field, value: str[valueStart+1 : closeQuote], quoted: true, endPos: closeQuote + 1}, true
	case '/':
		// A regex runs to the next UNESCAPED slash, then to the next space;
		// unterminated, it takes the rest of the input.
		closeSlash := -1
		for i := valueStart + 1; i < len(str); i++ {
			if str[i] == '\\' {
				i++
				continue
			}
			if str[i] == '/' {
				closeSlash = i
				break
			}
		}
		if closeSlash == -1 {
			return fieldValueMatch{field: field, value: str[valueStart:], endPos: len(str)}, true
		}
		end := closeSlash + 1
		for end < len(str) && str[end] != ' ' {
			end++
		}
		return fieldValueMatch{field: field, value: str[valueStart:end], endPos: end}, true
	case '[':
		if rel := strings.IndexByte(str[valueStart+1:], ']'); rel != -1 {
			closeBracket := valueStart + 1 + rel
			return fieldValueMatch{field: field, value: str[valueStart : closeBracket+1], endPos: closeBracket + 1}, true
		}
	}
	valueEnd := valueStart
	for valueEnd < len(str) && str[valueEnd] != ' ' {
		valueEnd++
	}
	if valueEnd == valueStart {
		return fieldValueMatch{}, false
	}
	return fieldValueMatch{field: field, value: str[valueStart:valueEnd], endPos: valueEnd}, true
}

// reviewTitleFilter is a compiled Title box: every part must hold.
type reviewTitleFilter struct {
	parts []reviewTitlePart
}

type reviewTitlePart struct {
	m       *querygrammar.TextMatcher
	negated bool
	// needle is strings.ToLower(m.Raw) for a substring value: what
	// TextMatcher.Match compares a lower-cased title against, so the row's
	// precomputed fold can be used instead.
	needle string
}

// compileReviewTitleFilter is queryGrammar.ts compileTitleFilter: nil for an
// empty box; an error, worded as the lane words it, for a box that names
// another field, mixes title: tokens with bare text, or holds a value the
// grammar rejects.
func compileReviewTitleFilter(input string) (*reviewTitleFilter, error) {
	trimmed := strings.TrimSpace(input)
	if trimmed == "" {
		return nil, nil
	}
	freeText, filters := parseSearchFilters(trimmed)
	if len(filters) == 0 {
		quoted := len(trimmed) >= 2 && strings.HasPrefix(trimmed, `"`) && strings.HasSuffix(trimmed, `"`)
		value := trimmed
		if quoted {
			value = trimmed[1 : len(trimmed)-1]
		}
		filters = []searchFieldFilter{{field: "title", value: value, quoted: quoted}}
	} else {
		for _, f := range filters {
			if f.field != "title" {
				return nil, fmt.Errorf("%s: only title: filters apply here", f.field)
			}
		}
		if freeText != "" {
			return nil, fmt.Errorf(`"%s": mix of title: tokens and bare text — write every part as title:…`, freeText)
		}
	}
	out := &reviewTitleFilter{parts: make([]reviewTitlePart, 0, len(filters))}
	for _, f := range filters {
		m, err := querygrammar.CompileText(f.value, f.quoted)
		if err != nil {
			return nil, fmt.Errorf("title:%s — %s", querygrammar.ShortToken(f.value), err.Error())
		}
		p := reviewTitlePart{m: m, negated: f.negated}
		if m.Kind == querygrammar.KindSubstring {
			p.needle = strings.ToLower(m.Raw)
		}
		out.parts = append(out.parts, p)
	}
	return out, nil
}

// costly reports whether any part runs a compiled program (regex or glob),
// i.e. whether an evaluation needs a pattern slot.
func (f *reviewTitleFilter) costly() bool {
	if f == nil {
		return false
	}
	for i := range f.parts {
		if f.parts[i].m.Costly() {
			return true
		}
	}
	return false
}

// match reports whether a row's live title passes every part. A substring
// part compares against the row's precomputed fold (exactly what
// TextMatcher.Match does after lower-casing); a regex or glob part gets the
// title as written, so (?-i) keeps its meaning.
func (f *reviewTitleFilter) match(row *snapshotRow, title string, budget *querygrammar.Budget) bool {
	for i := range f.parts {
		p := &f.parts[i]
		var hit bool
		if p.m.Kind == querygrammar.KindSubstring {
			hit = strings.Contains(row.foldedTitle(title), p.needle)
		} else {
			hit = budget.Match(p.m, title)
		}
		if hit == p.negated {
			return false
		}
	}
	return true
}

// --- the lane's row rules, ported ---------------------------------------------

// reviewProviderID is useMetadataLane.ts reviewProviderID: the display names
// stored in candidate cache rows mapped onto the rail's provider chip ids.
func reviewProviderID(source string) string {
	n := strings.ToLower(strings.TrimSpace(source))
	switch n {
	case "audible", "audnexus", "audnexus (audible)":
		return "audible"
	case "google", "google books", "google_books":
		return "google_books"
	case "open library", "open_library", "openlibrary":
		return "openlibrary"
	}
	return n
}

var reviewLanguageCanonical = map[string]string{
	"english": "en", "eng": "en",
	"spanish": "es", "spa": "es",
	"french": "fr", "fre": "fr", "fra": "fr",
	"german": "de", "ger": "de", "deu": "de",
	"italian": "it", "ita": "it",
	"japanese": "ja", "jpn": "ja",
	"chinese": "zh", "chi": "zh", "zho": "zh", "mandarin": "zh",
	"portuguese": "pt", "por": "pt",
	"russian": "ru", "rus": "ru",
	"dutch": "nl", "nld": "nl",
	"korean": "ko", "kor": "ko",
	"arabic": "ar", "ara": "ar",
}

// normalizeReviewLanguage is useMetadataLane.ts normalizeLanguage: an ISO
// 639-1 code for the spellings it knows, else the lower-cased value.
func normalizeReviewLanguage(lang string) string {
	s := strings.ToLower(strings.TrimSpace(lang))
	if c, ok := reviewLanguageCanonical[s]; ok {
		return c
	}
	return s
}

// reviewLanguageHidden is the lane's language rule: hidden only when BOTH
// sides name a language and they differ. An unknown language on either side
// is a no-op, not a hide: a book with no language set must still be offered
// its candidates. candNorm is the candidate's language already normalized.
func reviewLanguageHidden(bookLang *string, candNorm string) bool {
	if candNorm == "" || bookLang == nil {
		return false
	}
	b := normalizeReviewLanguage(*bookLang)
	return b != "" && b != candNorm
}

// reviewCandidateKey is useMetadataLane.ts candidateKey: the multi-book group
// key, asin, then isbn, then source+title+author.
func reviewCandidateKey(c *metafetch.MetadataCandidate) string {
	if c.ASIN != "" {
		return "asin:" + c.ASIN
	}
	if c.ISBN != "" {
		return "isbn:" + c.ISBN
	}
	return c.Source + ":" + strings.ToLower(strings.TrimSpace(c.Title)) + ":" + strings.ToLower(strings.TrimSpace(c.Author))
}

// The runtime switch's two rules (spine/rowState.ts): the spine's warning
// chip (a candidate delta over ten minutes) and the apply gate's 10% rule.
const (
	reviewRuntimeWarnSec    = 600
	reviewRuntimeBlockRatio = 0.1
)

// reviewRuntimeInputs is what the API row carries for the runtime rule:
// duration_seconds (the complete canonical runtime, else 0) and
// runtime_lower_bound_seconds (the known part of a partial one, else 0),
// derived as metabatch's applyRuntimeInfo derives them -- a failed file read
// is an unknown runtime.
func reviewRuntimeInputs(f metabatch.BookFileFacts) (complete, lowerBound int) {
	if f.Err != nil {
		return 0, 0
	}
	if sec, ok := f.Runtime.KnownSeconds(); ok {
		return sec, 0
	}
	if f.Runtime.Partial() {
		return 0, f.Runtime.Seconds
	}
	return 0, 0
}

// reviewRuntimeHidden is rowState.ts runtimeHiddenBySwitch: the warning
// chip's rule OR runtimeDiffersFromBook. An unknown runtime on either side is
// not evidence of a mismatch, so it is never hidden.
func reviewRuntimeHidden(c *metafetch.MetadataCandidate, f metabatch.BookFileFacts) bool {
	if c == nil {
		return false
	}
	if d := c.DurationDeltaSec; d > reviewRuntimeWarnSec || d < -reviewRuntimeWarnSec {
		return true
	}
	cand := c.DurationSec
	if cand <= 0 {
		return false
	}
	book, lb := reviewRuntimeInputs(f)
	if book > 0 {
		diff := book - cand
		if diff < 0 {
			diff = -diff
		}
		return float64(diff)/float64(book) > reviewRuntimeBlockRatio
	}
	if lb > cand {
		return float64(lb-cand)/float64(lb) > reviewRuntimeBlockRatio
	}
	return false
}

// --- evaluation -----------------------------------------------------------------

// reviewQueryBase is one overlay of one snapshot and everything derived from
// it that does not depend on the filter: the rows (buildReviewableRows) and
// the provider counts. Every list evaluated at the same (snapshot, cache
// generation, book generation) shares it, so the overlay is read once per
// generation, not once per keystroke or page.
type reviewQueryBase struct {
	key  reviewBaseKey
	set  reviewOverlay
	rows reviewRows
	// provider and candLang are, per reviewable row (same index), the
	// candidate's reviewProviderID and normalized language: the candidate
	// is immutable, so neither is re-derived per evaluation.
	provider   []string
	candLang   []string
	sources    map[string]int
	generation string
	builtAt    time.Time
}

type reviewBaseKey struct {
	snap              *reviewSnapshot
	cacheGen, bookGen uint64
}

func newReviewQueryBase(key reviewBaseKey, set reviewOverlay, now time.Time) *reviewQueryBase {
	b := &reviewQueryBase{
		key: key,
		set: set,
		// The label names the data the base was built from, not the live
		// generations it is keyed on: the snapshot can lag the live cache
		// generation (stale-while-revalidate serves the current snapshot
		// while a rebuild runs), and the cache rows come only from the
		// snapshot. The book half is the live book generation read BEFORE
		// the overlay, which the overlay therefore covers. Two bases with the
		// same label hold the same cache rows and the same book reads.
		generation: fmt.Sprintf("%d.%d", key.snap.cacheGen, key.bookGen),
		builtAt:    now,
	}
	b.rows = buildReviewableRows(&b.set, now, true)
	b.sources = map[string]int{}
	b.provider = make([]string, len(b.rows.reviewable))
	b.candLang = make([]string, len(b.rows.reviewable))
	for i := range b.rows.reviewable {
		cand := b.rows.reviewable[i].row.cand
		b.provider[i] = reviewProviderID(cand.Source)
		b.candLang[i] = normalizeReviewLanguage(cand.Language)
		if id := b.provider[i]; id != "" {
			b.sources[id]++
		}
	}
	return b
}

// reviewRef is one listed row: i >= 0 is base.rows.reviewable[i], i < 0 is
// base.rows.unreviewable[-i-1].
type reviewRef int32

// reviewResultList is one evaluated filter: the ordered rows, and the count
// the runtime switch hid. The page, the total and ids=all are cut from it.
type reviewResultList struct {
	base          *reviewQueryBase
	normalized    string
	refs          []reviewRef
	runtimeHidden int
}

func (l *reviewResultList) row(r reviewRef) (*snapshotRow, string) {
	if r >= 0 {
		rr := &l.base.rows.reviewable[r]
		return rr.row, rr.status
	}
	u := &l.base.rows.unreviewable[-r-1]
	return u.row, u.status
}

// evaluateReviewQuery runs one filter over a base, in ONE pass over its rows
// (plus one over the survivors for the multi-book key, which is computed over
// the WHOLE filtered set, never a page). It reads the base and never writes
// it: the snapshot is immutable by contract and the base is shared.
//
// Its regex and glob matches are timed against budget (nil: untimed); once
// the budget is spent it stops and returns the budget's *TooSlowError, and a
// partial list is never returned.
func evaluateReviewQuery(base *reviewQueryBase, q *ReviewQuery, budget *querygrammar.Budget) (*reviewResultList, error) {
	out := &reviewResultList{base: base, normalized: q.normalized}
	rows := &base.rows
	titleOK := func(row *snapshotRow) bool {
		return q.title == nil || q.title.match(row, row.book.Title, budget)
	}
	addReviewable := func(keep func(r *reviewableRow) bool) {
		for i := range rows.reviewable {
			if budget.Expired() {
				return
			}
			r := &rows.reviewable[i]
			if keep(r) && titleOK(r.row) {
				out.refs = append(out.refs, reviewRef(i))
			}
		}
	}
	addUnreviewable := func(keep func(u *unreviewableRow) bool) {
		for i := range rows.unreviewable {
			if budget.Expired() {
				return
			}
			u := &rows.unreviewable[i]
			if keep(u) && titleOK(u.row) {
				out.refs = append(out.refs, reviewRef(-i-1))
			}
		}
	}

	switch {
	case q.Chip != "":
		// A chip view: exactly the rows the chip counts, narrowed by the
		// title filter, with every other filter paused (the lane's chipRows).
		switch q.Chip {
		case reviewChipMatched:
			addReviewable(func(r *reviewableRow) bool { return r.status == reviewStatusMatched })
		case reviewChipNoMatch:
			addReviewable(func(r *reviewableRow) bool { return r.status == reviewStatusNoMatch })
		case reviewChipTotal:
			addReviewable(func(*reviewableRow) bool { return true })
		case reviewChipErrors:
			addUnreviewable(func(u *unreviewableRow) bool { return u.status == unreviewableStatusDecodeError })
		case reviewChipNoCandidates:
			addUnreviewable(func(u *unreviewableRow) bool { return u.status == unreviewableStatusNoCandidates })
		case reviewChipResolvedNoCandidates:
			addUnreviewable(func(u *unreviewableRow) bool { return u.status == unreviewableStatusResolvedNoCandidates })
		case reviewChipStale:
			// Both buckets, reviewable first: [...results, ...unreviewable].
			addReviewable(func(r *reviewableRow) bool { return r.stale })
			addUnreviewable(func(u *unreviewableRow) bool { return u.stale })
		case reviewChipDeferred:
			addReviewable(func(r *reviewableRow) bool { return r.row.fallbackDeferred })
			addUnreviewable(func(u *unreviewableRow) bool { return u.row.fallbackDeferred })
		}
	case q.Bucket == reviewBucketUnreviewable:
		addUnreviewable(func(*unreviewableRow) bool { return true })
	default:
		evaluateReviewFilters(out, q, budget)
	}
	if err := budget.Err(); err != nil {
		return nil, err
	}
	sortReviewList(out, q.Sort)
	return out, nil
}

// evaluateReviewFilters is the lane's default chain, in its order:
// beforeRuntime (title, source, confidence, row states, no-match), the
// runtime switch (counted as it hides), language, transcription, then the
// multi-book hide over the whole surviving set.
func evaluateReviewFilters(out *reviewResultList, q *ReviewQuery, budget *querygrammar.Budget) {
	base := out.base
	rows := &base.rows
	skipped := map[string]struct{}{}
	if q.HideSkipped || q.HideRejected {
		for _, id := range q.Skipped {
			skipped[id] = struct{}{}
		}
	}
	// rowState is the lane's row state for a row, from what the server knows
	// plus the client-only skipped list: the server's `applied` outranks every
	// local state (a fact about what was written), a skip outranks the
	// server's no-match, and a no-match row is `rejected`. Reject persists as
	// MetadataReviewStatus "no_match" (markNoMatch), so it is server state.
	rowState := func(r *reviewableRow) string {
		if r.status == reviewStatusApplied {
			return "applied"
		}
		if _, ok := skipped[r.row.sum.BookID]; ok {
			return "skipped"
		}
		if r.status == reviewStatusNoMatch {
			return "rejected"
		}
		return ""
	}

	out.refs = make([]reviewRef, 0, len(rows.reviewable))
	for i := range rows.reviewable {
		if budget.Expired() {
			return
		}
		r := &rows.reviewable[i]
		cand := r.row.cand
		book := r.row.book
		// The lane's beforeRuntime filters are a conjunction, so their order
		// does not change the result; the title (the one that can run a
		// regex) goes last, over only the rows the cheap ones kept.
		if q.Source != "" && base.provider[i] != q.Source {
			continue
		}
		if r.status == reviewStatusMatched && !(cand.Score*100 >= q.MinConfidence) {
			continue
		}
		if q.HideApplied || q.HideRejected || q.HideSkipped {
			switch rowState(r) {
			case "applied":
				if q.HideApplied {
					continue
				}
			case "rejected":
				if q.HideRejected {
					continue
				}
			case "skipped":
				if q.HideSkipped {
					continue
				}
			}
		}
		if q.HideNoMatch && r.status == reviewStatusNoMatch {
			continue
		}
		if q.title != nil && !q.title.match(r.row, book.Title, budget) {
			continue
		}
		// Every row past this point is one the lane's beforeRuntime keeps,
		// which is the set runtimeHidden is counted over.
		if q.HideRuntime && reviewRuntimeHidden(cand, r.row.files) {
			out.runtimeHidden++
			continue
		}
		if q.MatchLanguage && reviewLanguageHidden(book.Language, base.candLang[i]) {
			continue
		}
		if q.HasTranscription && (book.TranscribedTitle == nil || *book.TranscribedTitle == "") {
			continue
		}
		if q.TranscriptionMatched && !cand.TranscriptionBoosted {
			continue
		}
		out.refs = append(out.refs, reviewRef(i))
	}

	if !q.HideMultiBook {
		return
	}
	// Book ids sharing a candidate with at least one other book, over the
	// WHOLE filtered set: two files of one book on opposite sides of a page
	// boundary must both be hidden. Only pending (matched) rows group, and a
	// book the owner pulled out of its group never counts.
	ungrouped := make(map[string]struct{}, len(q.Ungrouped))
	for _, id := range q.Ungrouped {
		ungrouped[id] = struct{}{}
	}
	groupable := func(r *reviewableRow) bool {
		if r.status != reviewStatusMatched {
			return false
		}
		_, pulledOut := ungrouped[r.row.sum.BookID]
		return !pulledOut
	}
	counts := map[string]int{}
	keys := make([]string, len(out.refs))
	for n, ref := range out.refs {
		r := &rows.reviewable[ref]
		if groupable(r) {
			keys[n] = reviewCandidateKey(r.row.cand)
			counts[keys[n]]++
		}
	}
	kept := out.refs[:0]
	for n, ref := range out.refs {
		if keys[n] != "" && counts[keys[n]] > 1 {
			continue
		}
		kept = append(kept, ref)
	}
	out.refs = kept
}

// sortReviewList orders the list; stable, so ties keep the listing order.
func sortReviewList(l *reviewResultList, order string) {
	switch order {
	case reviewSortTitle:
		keys := make([]string, len(l.refs))
		for i, ref := range l.refs {
			row, _ := l.row(ref)
			keys[i] = row.foldedTitle(row.book.Title)
		}
		sort.Stable(reviewSortByKey{refs: l.refs, keys: keys})
	case reviewSortConfidence:
		// Highest score first; a row with no candidate last.
		scores := make([]float64, len(l.refs))
		for i, ref := range l.refs {
			row, _ := l.row(ref)
			scores[i] = math.Inf(-1)
			if ref >= 0 && row.cand != nil {
				scores[i] = row.cand.Score
			}
		}
		sort.Stable(reviewSortByScore{refs: l.refs, scores: scores})
	}
}

type reviewSortByKey struct {
	refs []reviewRef
	keys []string
}

func (s reviewSortByKey) Len() int           { return len(s.refs) }
func (s reviewSortByKey) Less(i, j int) bool { return s.keys[i] < s.keys[j] }
func (s reviewSortByKey) Swap(i, j int) {
	s.refs[i], s.refs[j] = s.refs[j], s.refs[i]
	s.keys[i], s.keys[j] = s.keys[j], s.keys[i]
}

type reviewSortByScore struct {
	refs   []reviewRef
	scores []float64
}

func (s reviewSortByScore) Len() int           { return len(s.refs) }
func (s reviewSortByScore) Less(i, j int) bool { return s.scores[i] > s.scores[j] }
func (s reviewSortByScore) Swap(i, j int) {
	s.refs[i], s.refs[j] = s.refs[j], s.refs[i]
	s.scores[i], s.scores[j] = s.scores[j], s.scores[i]
}

// --- the result-list cache -----------------------------------------------------

// reviewQueryCache keeps the current base and the last reviewQueryLRUSize
// lists evaluated over it, keyed by the normalized query. A list is valid
// only for its base's (snapshot, cache generation, book generation): any
// write moves a generation, the next request misses, and the lists of the
// old base are dropped with it. Concurrent identical requests share one
// evaluation (singleflight). Safe for concurrent use.
//
// Memory: the cached base holds a pointer to the snapshot it was built over
// (and its overlay), so it pins that snapshot in memory even after the
// snapshot cache has published a newer one. At most one old snapshot is
// pinned this way: the base is replaced by the first request after a
// generation moves, and is never served past reviewQueryBaseMaxAge, but it is
// not released until a request replaces it.
type reviewQueryCache struct {
	mu    sync.Mutex
	base  *reviewQueryBase
	lists []*reviewResultList // most recent first, all over base
	// flight dedups both base builds ("b|" keys) and list evaluations ("l|").
	flight singleflight.Group
	// evaluations counts evaluateReviewQuery runs; bases counts overlay reads.
	evaluations atomic.Int64
	bases       atomic.Int64
	now         func() time.Time
}

func newReviewQueryCache() *reviewQueryCache {
	return &reviewQueryCache{now: time.Now}
}

func (k reviewBaseKey) String() string {
	return fmt.Sprintf("%p|%d|%d", k.snap, k.cacheGen, k.bookGen)
}

// currentBase returns the cached base for key when it is still young enough.
// Caller holds mu.
func (c *reviewQueryCache) currentBase(key reviewBaseKey) *reviewQueryBase {
	if c.base != nil && c.base.key == key && c.now().Sub(c.base.builtAt) < reviewQueryBaseMaxAge {
		return c.base
	}
	return nil
}

// lookup returns a cached list for (key, normalized), moving it to the front.
func (c *reviewQueryCache) lookup(key reviewBaseKey, normalized string) *reviewResultList {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.currentBase(key) == nil {
		return nil
	}
	for i, l := range c.lists {
		if l.normalized == normalized {
			copy(c.lists[1:i+1], c.lists[:i])
			c.lists[0] = l
			return l
		}
	}
	return nil
}

// store adds a list over the current base, evicting the least recent past
// reviewQueryLRUSize. A list over a base that is no longer current is not
// kept.
func (c *reviewQueryCache) store(l *reviewResultList) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.base != l.base {
		return
	}
	c.lists = append([]*reviewResultList{l}, c.lists...)
	if len(c.lists) > reviewQueryLRUSize {
		c.lists = c.lists[:reviewQueryLRUSize]
	}
}

// baseFor returns the base for key, building it (once, however many requests
// ask) with build when the cached one is for another key or too old.
func (c *reviewQueryCache) baseFor(key reviewBaseKey, build func() (*reviewQueryBase, error)) (*reviewQueryBase, error) {
	c.mu.Lock()
	if b := c.currentBase(key); b != nil {
		c.mu.Unlock()
		return b, nil
	}
	c.mu.Unlock()
	v, err, _ := c.flight.Do("b|"+key.String(), func() (any, error) {
		c.mu.Lock()
		if b := c.currentBase(key); b != nil {
			c.mu.Unlock()
			return b, nil
		}
		c.mu.Unlock()
		b, err := build()
		if err != nil {
			return nil, err
		}
		c.bases.Add(1)
		c.mu.Lock()
		c.base, c.lists = b, nil
		c.mu.Unlock()
		return b, nil
	})
	if err != nil {
		return nil, err
	}
	return v.(*reviewQueryBase), nil
}

// liveReviewGenerations reads the metadata-cache and library generations
// NOW. ok is false when the store tracks either one not at all; the query is
// then never cached, since nothing would tell a cached list it went stale.
func (h *MetadataCacheHandler) liveReviewGenerations() (cacheGen, bookGen uint64, ok bool) {
	b := h.reviewBuilder
	if b == nil || b.cacheGen == nil || b.bookGen == nil {
		return 0, 0, false
	}
	return b.cacheGen(), b.bookGen(), true
}

// buildReviewQueryBase reads the overlay for snap and derives the base. An
// overlay that read more than overlayRebuildThreshold changed books (or could
// not bound its reads) asks the snapshot cache for a rebuild, exactly as the
// listing does, and is served as it is.
func (h *MetadataCacheHandler) buildReviewQueryBase(key reviewBaseKey) (*reviewQueryBase, error) {
	set, err := overlayLiveBooks(key.snap, h.store, nil, h.booksChangedSince)
	if err != nil {
		return nil, err
	}
	if set.rebuild {
		h.reviewSnap.requestRebuild()
	}
	return newReviewQueryBase(key, set, time.Now()), nil
}

// reviewQueryList returns the evaluated list for q: from the LRU when the
// same filter was evaluated at the current generations, else evaluated now.
// The snapshot comes from the stale-while-revalidate path (reviewSnapshot):
// a filter is served the CURRENT snapshot and never waits on a build, except
// the first request after a cold start, which has nothing else to serve.
func (h *MetadataCacheHandler) reviewQueryList(ctx context.Context, q *ReviewQuery) (*reviewResultList, error) {
	// The generations are read BEFORE the snapshot and the overlay: a write
	// racing this request moves a generation past the key, so the next
	// request misses rather than reusing a list that may predate the write.
	cacheGen, bookGen, tracked := h.liveReviewGenerations()
	snap, err := h.reviewSnapshot(ctx)
	if err != nil {
		return nil, err
	}
	cache := h.reviewQuery
	if !tracked || cache == nil {
		base, err := h.buildReviewQueryBase(reviewBaseKey{snap: snap, cacheGen: snap.cacheGen, bookGen: snap.bookGen})
		if err != nil {
			return nil, err
		}
		if cache != nil {
			cache.evaluations.Add(1)
		}
		return runReviewEvaluation(ctx, base, q)
	}
	key := reviewBaseKey{snap: snap, cacheGen: cacheGen, bookGen: bookGen}
	if l := cache.lookup(key, q.normalized); l != nil {
		return l, nil
	}
	v, err, _ := cache.flight.Do("l|"+key.String()+"|"+q.normalized, func() (any, error) {
		if l := cache.lookup(key, q.normalized); l != nil {
			return l, nil
		}
		base, err := cache.baseFor(key, func() (*reviewQueryBase, error) { return h.buildReviewQueryBase(key) })
		if err != nil {
			return nil, err
		}
		cache.evaluations.Add(1)
		// The slot wait and the budget start here, inside the shared flight,
		// so they bound the evaluation every waiter shares and no single
		// waiter's context can cut it short (hence context.Background). A
		// refusal is not stored: the LRU holds lists only.
		l, err := runReviewEvaluation(context.Background(), base, q)
		if err != nil {
			return nil, err
		}
		cache.store(l)
		return l, nil
	})
	if err != nil {
		return nil, err
	}
	return v.(*reviewResultList), nil
}

// runReviewEvaluation evaluates q over base. A query with a regex or glob
// first takes a pattern slot (waiting at most querygrammar's slot wait, or
// until ctx is done) and runs under a fresh budget of reviewPatternBudget; a
// query without one is a plain scan and needs neither.
func runReviewEvaluation(ctx context.Context, base *reviewQueryBase, q *ReviewQuery) (*reviewResultList, error) {
	if !q.title.costly() {
		return evaluateReviewQuery(base, q, nil)
	}
	release, err := querygrammar.AcquirePatternSlot(ctx)
	if err != nil {
		return nil, err
	}
	defer release()
	return evaluateReviewQuery(base, q, querygrammar.NewBudget(reviewPatternBudget))
}

// --- responses -------------------------------------------------------------------

// reviewIDsResponse is the ids=all body: every listed book id, in list order.
func reviewIDsResponse(l *reviewResultList) gin.H {
	ids := make([]string, len(l.refs))
	for i, ref := range l.refs {
		row, _ := l.row(ref)
		ids[i] = row.sum.BookID
	}
	return gin.H{"ids": ids, "total": len(ids), "generation": l.base.generation}
}

// reviewFacets is what the rail renders: the chip counts over the whole
// review set (the listing's summary, unfiltered), the provider counts over
// every reviewable row, and the rows the runtime switch hid from THIS list.
func reviewFacets(l *reviewResultList) gin.H {
	f := l.base.rows.summary(&l.base.set)
	f["total"] = len(l.base.rows.reviewable)
	f["sources"] = l.base.sources
	f["runtime_hidden"] = l.runtimeHidden
	return f
}

// reviewPageResponse cuts the page [offset, offset+limit) from the list.
// Rows are index rows (candidate description, score_breakdown and
// category_tags dropped, as view=index serves them); the page fetches full
// rows for what it shows with ids=, as it does today.
func reviewPageResponse(l *reviewResultList, q *ReviewQuery, series metabatch.SeriesBatchGetter) gin.H {
	start := min(q.Offset, len(l.refs))
	end := min(start+q.Limit, len(l.refs))
	cutoff := l.base.rows.freshCutoff
	results := make([]metabatch.CandidateResult, 0, end-start)
	for _, ref := range l.refs[start:end] {
		if ref >= 0 {
			r := &l.base.rows.reviewable[ref]
			results = append(results, r.result(r.row.book, cutoff, true))
			continue
		}
		u := &l.base.rows.unreviewable[-ref-1]
		results = append(results, u.result(u.row.book, cutoff))
	}
	metabatch.ResolveSeriesNames(series, results)
	return gin.H{
		"results":     results,
		"total_count": len(l.refs),
		"offset":      q.Offset,
		"limit":       q.Limit,
		"truncated":   end-start < len(l.refs),
		"generation":  l.base.generation,
		"facets":      reviewFacets(l),
	}
}

// getCacheReviewPage serves view=page (see GetCacheReviewResults).
func (h *MetadataCacheHandler) getCacheReviewPage(c *gin.Context) {
	began := time.Now()
	// A page is a statement about one generation; a cached copy would be one
	// about a generation that may be gone.
	c.Header("Cache-Control", "no-store")
	q, err := parseReviewQuery(c.Query)
	if err != nil {
		httputil.RespondWithBadRequest(c, err.Error())
		return
	}
	l, err := h.reviewQueryList(c.Request.Context(), q)
	var refused *reviewQueryError
	if errors.As(err, &refused) {
		httputil.RespondWithBadRequest(c, refused.Error())
		return
	}
	if respondSearchLimit(c, err) {
		return
	}
	if err != nil {
		httputil.InternalError(c, "failed to query the review set", err)
		return
	}
	if q.IDsAll {
		httputil.RespondWithOK(c, reviewIDsResponse(l))
	} else {
		series, _ := database.AsCapability[metabatch.SeriesBatchGetter](h.store)
		httputil.RespondWithOK(c, reviewPageResponse(l, q, series))
	}
	if d := time.Since(began); d > slowReviewListing {
		metadataCacheLog.Warn("review query exceeded slow-request threshold: duration=%s query=%s listed=%d", d.Round(time.Millisecond), q.normalized, len(l.refs))
	}
}

// respondSearchLimit answers the querygrammar evaluation limits and reports
// whether it did: a spent pattern budget is a 400 (the search, as written,
// cannot be answered within the limit), every pattern slot busy is a 503
// with Retry-After. The audiobooks handler package has the same function;
// httputil and querygrammar are both leaf packages, so neither can host it.
func respondSearchLimit(c *gin.Context, err error) bool {
	var slow *querygrammar.TooSlowError
	if errors.As(err, &slow) {
		httputil.RespondWithBadRequest(c, slow.Error())
		return true
	}
	var busy *querygrammar.BusyError
	if errors.As(err, &busy) {
		c.Header("Retry-After", "2")
		httputil.RespondWithServiceUnavailable(c, busy.Error())
		return true
	}
	return false
}
