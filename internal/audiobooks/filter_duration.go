// file: internal/audiobooks/filter_duration.go
// version: 1.0.1
// guid: 7c1e5a92-3f04-4d8b-b6e1-0a9d2c47f815
// last-edited: 2026-10-05

package audiobooks

import (
	"fmt"
	"math"
	"strings"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/search"
)

// Library-list filters that are NOT substring matches: duration comparisons
// with human units, has_duration, and metadata:applied.
//
// WHY: until 2026-09-27 "duration" went through bookFieldValue like every other
// column — rendered as a decimal string and SUBSTRING-matched. So
// duration:>1200 asked "does the text '3600' contain the text '>1200'?" and
// answered no for every book in the library (count 0), while duration:0 matched
// every book whose seconds merely contained a zero digit (26,507 on prod). The
// search bar offered duration: and nothing could ever have worked with it.

// DurationFilterFields are the filter names evaluated as runtime comparisons.
// "duration_seconds" is the backend spelling kept for existing callers.
var durationFilterFields = map[string]bool{"duration": true, "duration_seconds": true}

// durationExpr is a parsed duration filter value.
type durationExpr struct {
	op     string // ">", ">=", "<", "<=", "==", "!=", "range"
	a, b   int    // seconds; b only for range
	parsed bool
}

// parseDurationValue parses a human duration into whole seconds.
//
// Accepted: a bare number (seconds, the unit the store keeps: "1200",
// "90.5"), and anything time.ParseDuration accepts ("20m", "1h30m", "1.5h",
// "45s"). Also "1h 30m" with a space, since the range form puts spaces in
// front of people. Negative values are rejected.
func parseDurationValue(s string) (int, error) {
	return search.ParseDurationSeconds(s)
}

// parseDurationExpr parses a duration filter value:
//
//	>20m  >=1200  <1h30m  <=90s  ==600  !=600  20m (equality)
//	[10m TO 2h]   (inclusive range; "*" leaves a side open)
func parseDurationExpr(expr string) (durationExpr, error) {
	e := strings.TrimSpace(expr)
	if strings.HasPrefix(e, "[") && strings.HasSuffix(e, "]") {
		inner := strings.TrimSpace(e[1 : len(e)-1])
		parts := strings.Split(strings.ToUpper(inner), " TO ")
		if len(parts) != 2 {
			return durationExpr{}, fmt.Errorf("invalid range %q: use [10m TO 2h]", expr)
		}
		// Re-slice the original (not upper-cased) text by the split lengths so
		// units keep their case-insensitive parse either way.
		lo := strings.TrimSpace(inner[:len(parts[0])])
		hi := strings.TrimSpace(inner[len(parts[0])+len(" TO "):])
		out := durationExpr{op: "range", a: 0, b: math.MaxInt32, parsed: true}
		if lo != "*" {
			v, err := parseDurationValue(lo)
			if err != nil {
				return durationExpr{}, err
			}
			out.a = v
		}
		if hi != "*" {
			v, err := parseDurationValue(hi)
			if err != nil {
				return durationExpr{}, err
			}
			out.b = v
		}
		return out, nil
	}
	op, rest := "==", e
	for _, p := range []string{">=", "<=", "!=", "==", ">", "<", "="} {
		if strings.HasPrefix(e, p) {
			op, rest = p, e[len(p):]
			break
		}
	}
	if op == "=" {
		op = "=="
	}
	v, err := parseDurationValue(rest)
	if err != nil {
		return durationExpr{}, err
	}
	return durationExpr{op: op, a: v, parsed: true}, nil
}

// ValidateFilterValue reports an error for a filter value the matcher cannot
// evaluate, so the handler can answer 400 instead of a silent count:0. Only
// fields with a grammar are checked; substring fields accept anything.
func ValidateFilterValue(f FieldFilter) error {
	switch {
	case durationFilterFields[f.Field]:
		if _, err := parseDurationExpr(f.Value); err != nil {
			return fmt.Errorf("%s: %w", f.Field, err)
		}
	case f.Field == "has_duration":
		if _, ok := parseYesNo(f.Value); !ok {
			return fmt.Errorf("has_duration: value must be yes or no, got %q", f.Value)
		}
	case f.Field == "metadata":
		if _, ok := metadataFilterWantsApplied(f.Value); !ok {
			return fmt.Errorf("metadata: value must be applied (or none), got %q", f.Value)
		}
	}
	return nil
}

// FirstInvalidFilterValue returns the first validation error among filters.
func FirstInvalidFilterValue(filters []FieldFilter) error {
	for _, f := range filters {
		if err := ValidateFilterValue(f); err != nil {
			return err
		}
	}
	return nil
}

func parseYesNo(v string) (bool, bool) {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "yes", "true", "1":
		return true, true
	case "no", "false", "0":
		return false, true
	}
	return false, false
}

// metadataFilterWantsApplied maps a metadata: value to "wants applied".
// metadata:applied → true; metadata:none / metadata:unapplied → false.
// Exact values, never substrings: "applied" is a substring of "unapplied".
func metadataFilterWantsApplied(v string) (bool, bool) {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "applied", "yes":
		return true, true
	case "none", "unapplied", "not_applied", "no":
		return false, true
	}
	return false, false
}

// BookMetadataApplied is the definition behind metadata:applied.
//
// A book has had metadata applied when its MetadataReviewStatus is "matched"
// or "audio_confirmed". Those two values are written by
// metafetch.ApplyMetadataCandidateWithOptions — the one apply path shared by
// the single-book dialog, review-lane approvals, bulk apply and the
// automatic metabatch upgrade — whenever an apply records a match: always for
// a human-picked candidate, and for an automatic apply when the book now
// holds the candidate's title. An automatic apply that kept a different
// title deliberately leaves the status alone so the book stays in its review
// lane, and it therefore counts as NOT applied here too.
//
// Not used: MetadataSource (maintenance title repair stamps it without any
// candidate being applied) and the metadata:source:* tags (provenance, written
// best-effort outside the apply transaction). "no_match" is a ruling, not an
// apply, so it counts as not applied — combine with -review:no_match to hide
// those as well.
//
// Note the old help entry "-review:matched = still needing metadata" was
// wrong: review: substring-matches, and "audio_confirmed" does not contain
// "matched", so every audio-confirmed book was listed as needing metadata.
//
// The status test itself is database.MetadataApplied, shared with the cached
// bulk apply's already-applied skip and the lost-candidates fixer.
func BookMetadataApplied(b *database.Book) bool {
	return b != nil && database.MetadataApplied(b.MetadataReviewStatus)
}

// runtimeFunc returns a book's runtime in seconds and whether it is known.
type runtimeFunc func(b *database.Book) (int, bool)

// storedRuntime is the fallback runtime: the stored Book.Duration aggregate,
// known when positive. Used when no runtime index is available.
func storedRuntime(b *database.Book) (int, bool) {
	if b == nil || b.Duration == nil || *b.Duration <= 0 {
		return 0, false
	}
	return *b.Duration, true
}

// durationMatches evaluates a duration filter value against a book. A book
// whose runtime is unknown matches NO comparison — not >, not <, not a range
// — so an unprobed file is neither hidden by duration:>20m nor listed by
// duration:<20m. Find those with has_duration:no.
func durationMatches(b *database.Book, expr string, rt runtimeFunc) bool {
	e, err := parseDurationExpr(expr)
	if err != nil {
		return false
	}
	if rt == nil {
		rt = storedRuntime
	}
	sec, known := rt(b)
	if !known {
		return false
	}
	switch e.op {
	case ">":
		return sec > e.a
	case ">=":
		return sec >= e.a
	case "<":
		return sec < e.a
	case "<=":
		return sec <= e.a
	case "!=":
		return sec != e.a
	case "range":
		return sec >= e.a && sec <= e.b
	default:
		return sec == e.a
	}
}

// hasDurationFilter reports whether filters include a runtime comparison, so
// callers only pay for the runtime index when it is needed.
func hasDurationFilter(filters []FieldFilter) bool {
	for _, f := range filters {
		if durationFilterFields[f.Field] || f.Field == "has_duration" {
			return true
		}
	}
	return false
}
