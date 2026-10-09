// file: internal/audiobooks/filter_compiled.go
// version: 1.3.0
// guid: 6a1f3c8e-9d24-4b7a-b0e5-2f8c4d1a7e36
// last-edited: 2026-10-09

package audiobooks

import (
	"fmt"
	"strconv"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/logger"
	"github.com/falkcorp/audiobook-organizer/internal/querygrammar"
)

// filterCompiledLog carries this file's diagnostics through the log-injection barrier.
var filterCompiledLog = logger.New("audiobooks.filter")

// compiledFilter is a FieldFilter with its value parsed ONCE, before the row
// loop. Regex compilation per row would run regexp.Compile ~68K times per
// request; compiling here also means the validator (ValidateFilterValue →
// compileFieldFilter) and the matcher read the value through the same code,
// so they cannot disagree about what is valid.
type compiledFilter struct {
	FieldFilter
	text *querygrammar.TextMatcher // text forms (substring/glob/regex)
	cmp  *querygrammar.Comparison  // numeric comparison / range / equality
}

// numericFilterFields are evaluated as numbers when the value is a
// comparison, a range, or a bare number. A regex or glob on one of them is
// matched against its rendered decimal text instead.
var numericFilterFields = map[string]bool{
	"year": true, "series_number": true,
	"bitrate": true, "bitrate_kbps": true,
	"file_size": true, "file_size_bytes": true,
	"sample_rate": true, "sample_rate_hz": true,
	"channels": true, "bit_depth": true,
	"user_rating_overall": true, "user_rating_story": true, "user_rating_performance": true,
	"progress_pct": true,
}

// numericUnitParser returns the operand parser for a numeric field: units
// for file_size (1024-based k/kb/m/mb/g/gb/t/tb), bitrate (k/kbps) and
// sample_rate (hz/khz); plain numbers for everything else.
func numericUnitParser(field string) querygrammar.NumberParser {
	switch field {
	case "file_size", "file_size_bytes":
		return querygrammar.ParseBytes
	case "bitrate", "bitrate_kbps":
		return querygrammar.ParseKbps
	case "sample_rate", "sample_rate_hz":
		return querygrammar.ParseHz
	}
	return nil
}

// isBareNumericValue reports whether v is a bare operand the field's unit
// parser accepts (2019, 20mb, 64k), i.e. a numeric equality.
func isBareNumericValue(field, v string) bool {
	p := numericUnitParser(field)
	if p == nil {
		return querygrammar.IsNumber(v)
	}
	_, err := p(v)
	return err == nil
}

// ratingFields accept ONLY numeric values; they have no text rendering.
var ratingFields = map[string]bool{
	"user_rating_overall": true, "user_rating_story": true, "user_rating_performance": true,
	"progress_pct": true,
}

// compileFieldFilter parses f.Value per the unified grammar (see
// internal/querygrammar). The error names the field and the value, and is
// what the list handler returns as its 400.
func compileFieldFilter(f FieldFilter) (compiledFilter, error) {
	cf := compiledFilter{FieldFilter: f}
	wrap := func(err error) error { return fmt.Errorf("%s:%s — %w", f.Field, f.Value, err) }
	switch {
	case durationFilterFields[f.Field]:
		if _, err := parseDurationExpr(f.Value); err != nil {
			return cf, wrap(err)
		}
		return cf, nil
	case f.Field == "has_duration":
		if _, ok := parseYesNo(f.Value); !ok {
			return cf, wrap(fmt.Errorf("value must be yes or no"))
		}
		return cf, nil
	case f.Field == "metadata":
		if _, ok := metadataFilterWantsApplied(f.Value); !ok {
			return cf, wrap(fmt.Errorf("value must be applied (or none)"))
		}
		return cf, nil
	case f.Field == "read_status":
		// Exact (EqualFold) match in matchesPerUserFilter: a pattern would be
		// compared as literal text and match nothing, so reject it loudly.
		if !querygrammar.IsPlainLiteral(f.Value, f.Quoted) {
			return cf, wrap(fmt.Errorf("read_status takes an exact status (finished, in_progress, ...), not a pattern"))
		}
		return cf, nil
	case f.Field == "last_played":
		return cf, nil // presence semantics, see matchesPerUserFilter
	case ratingFields[f.Field] ||
		(numericFilterFields[f.Field] && !f.Quoted &&
			(querygrammar.LooksLikeComparison(f.Value) || isBareNumericValue(f.Field, f.Value))):
		// Comparison grammar (never regex/substring): file_size:>20mb,
		// bitrate:<64k, year:[2015 TO 2020]. A malformed operand is a 400.
		c, err := querygrammar.ParseNumericExprUnits(f.Value, numericUnitParser(f.Field))
		if err != nil {
			return cf, wrap(err)
		}
		cf.cmp = &c
		return cf, nil
	}
	m, err := querygrammar.CompileText(f.Value, f.Quoted)
	if err != nil {
		return cf, wrap(err)
	}
	cf.text = m
	return cf, nil
}

// compileFieldFilters compiles every filter, stopping at the first error.
func compileFieldFilters(filters []FieldFilter) ([]compiledFilter, error) {
	out := make([]compiledFilter, 0, len(filters))
	for _, f := range filters {
		cf, err := compileFieldFilter(f)
		if err != nil {
			return nil, err
		}
		out = append(out, cf)
	}
	return out, nil
}

// mustCompileForPredicate compiles filters for a predicate. The entry points
// (handler 400, GetAudiobooksPage, CountAudiobooksFiltered) have already
// validated, so an error here is a bypassed boundary: fail CLOSED (ok=false,
// the caller matches nothing) and say so, rather than widening to everything.
func mustCompileForPredicate(filters []FieldFilter) ([]compiledFilter, bool) {
	cfs, err := compileFieldFilters(filters)
	if err != nil {
		filterCompiledLog.Error("field filter reached the matcher without validation; matching nothing: err=%v", err)
		return nil, false
	}
	return cfs, true
}

// splitCompiledFilters is splitFieldFilters for compiled filters.
func splitCompiledFilters(filters []compiledFilter) (cheap, stripped []compiledFilter) {
	for _, f := range filters {
		if strippedMemdbFields[f.Field] {
			stripped = append(stripped, f)
		} else {
			cheap = append(cheap, f)
		}
	}
	return cheap, stripped
}

// matchesCompiledFilters ANDs every filter (with negation) against book.
func matchesCompiledFilters(book database.Book, filters []compiledFilter, rt runtimeFunc) bool {
	for i := range filters {
		f := &filters[i]
		// Fail CLOSED on an empty value — see FirstEmptyFilterValue.
		if f.Value == "" {
			return false
		}
		matches := fieldMatchesCompiled(&book, f, rt)
		if f.Negated == matches {
			return false
		}
	}
	return true
}

// fieldMatchesCompiled evaluates one compiled filter against a book.
func fieldMatchesCompiled(book *database.Book, f *compiledFilter, rt runtimeFunc) bool {
	switch {
	case durationFilterFields[f.Field]:
		return durationMatches(book, f.Value, rt)
	case f.Field == "has_duration":
		want, ok := parseYesNo(f.Value)
		if !ok {
			return false
		}
		if rt == nil {
			rt = storedRuntime
		}
		_, known := rt(book)
		return known == want
	case f.Field == "metadata":
		want, ok := metadataFilterWantsApplied(f.Value)
		if !ok {
			return false
		}
		return BookMetadataApplied(book) == want
	}
	if f.cmp != nil {
		return f.cmp.MatchAny(numericFieldValues(book, f.Field))
	}
	if f.text == nil {
		return false
	}
	// A numeric field in text form (*, glob, regex, quoted) is matched
	// against each KNOWN value's decimal text, never the "0" rendering of an
	// unset value: otherwise bitrate:* would match every unprobed book, and
	// year:/^20/ would only ever see the print year of "1999 2021".
	if numericFilterFields[f.Field] {
		for _, v := range numericFieldValues(book, f.Field) {
			if f.text.Match(strconv.FormatFloat(v, 'f', -1, 64)) {
				return true
			}
		}
		return false
	}
	bookValue, known := bookFieldValue(*book, f.Field)
	if !known {
		return false
	}
	return f.text.Match(bookValue)
}

// numericFieldValues returns a field's KNOWN numeric values. Empty means
// unknown, and an unknown value matches no comparison (the same rule as
// duration): bitrate:<64 must not list every book whose bitrate was never
// probed, which is what comparing the "0" rendering would do. Media
// technicals treat a stored 0 as unknown too, since none of them can really
// be zero. year carries both the print and the audiobook release year.
func numericFieldValues(b *database.Book, field string) []float64 {
	intVals := func(ps ...*int) []float64 {
		out := make([]float64, 0, len(ps))
		for _, p := range ps {
			if p != nil {
				out = append(out, float64(*p))
			}
		}
		return out
	}
	positive := func(p *int) []float64 {
		if p == nil || *p <= 0 {
			return nil
		}
		return []float64{float64(*p)}
	}
	floatVal := func(p *float64) []float64 {
		if p == nil {
			return nil
		}
		return []float64{*p}
	}
	switch field {
	case "year":
		return intVals(b.PrintYear, b.AudiobookReleaseYear)
	case "series_number":
		return intVals(b.SeriesSequence)
	case "bitrate", "bitrate_kbps":
		return positive(b.Bitrate)
	case "file_size", "file_size_bytes":
		if b.FileSize == nil || *b.FileSize <= 0 {
			return nil
		}
		return []float64{float64(*b.FileSize)}
	case "sample_rate", "sample_rate_hz":
		return positive(b.SampleRate)
	case "channels":
		return positive(b.Channels)
	case "bit_depth":
		return positive(b.BitDepth)
	case "user_rating_overall":
		return floatVal(b.UserRatingOverall)
	case "user_rating_story":
		return floatVal(b.UserRatingStory)
	case "user_rating_performance":
		return floatVal(b.UserRatingPerformance)
	}
	return nil
}
