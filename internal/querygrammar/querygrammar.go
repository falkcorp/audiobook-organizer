// file: internal/querygrammar/querygrammar.go
// version: 1.3.0
// guid: 9b2e4c71-0f3a-4d6e-8a15-7c3d9e2f1b40
// last-edited: 2026-10-10

// Package querygrammar is the ONE value grammar behind the Library search bar
// and the Review → Metadata Title filter (owner decision 2026-10-06: one
// syntax, standard RE2 regex, so everyone already knows how to use it).
//
// A field value is one of:
//
//	word          case-insensitive substring
//	"two words"   literal substring (quotes switch every operator off)
//	/RE2/         RE2 regex, case-insensitive by default ((?-i) opts out)
//	a*  *a  *a*   glob on the WHOLE (trimmed) value; only * is a wildcard
//	*             the value is non-empty
//
// and, for numeric fields, ParseNumericExpr's comparisons and ranges.
//
// Every malformed value is an ERROR, never a pattern that matches nothing or
// everything: a bad filter that silently answers "0 books" reads as a fact
// about the library, which is the defect this package exists to end.
//
// So is a value too large to evaluate safely (MaxTextValueBytes,
// MaxPatternInst). A text value is typed by whoever is searching and matched
// against every row of a library-sized scan, and RE2's linear-time guarantee
// is linear in the program size times the input: before these limits,
// /(?:.?){1000}zzz/ took 23.5 s over 40,000 titles and a 30 KB pattern
// (accepted, under the 1 MB header limit) 2 m 10 s. The size limits refuse
// the absurd values; a value under them can still be slow over a whole
// library, so every caller that scans one also gives the scan a Budget
// (budget.go) and refuses the search when it is spent.
package querygrammar

import (
	"errors"
	"fmt"
	"math"
	"regexp"
	"regexp/syntax"
	"strconv"
	"strings"
)

// Limits on a text value. Both refuse the value with an error (a 400 at the
// HTTP boundary), never truncate it. They do NOT bound how long a search
// takes; Budget (budget.go) does that. Their job is to refuse, in
// microseconds, programs too large to be a real search, and to keep ONE match
// short enough that a Budget, which can only stop between matches, stops
// close to its limit.
//
// Measured on an M1 Max (one match, (?i) prefix included in the count):
//
//	pattern              instructions  85-char title  3.7 KB description
//	.{100,}                       103          8 ns          70 µs
//	.{120}                        122          6 ns          89 µs
//	.{250}                        252          6 ns         387 µs
//	(.*){100}                     402          4 µs          15 µs
//	(?:.?){120}zzz                245        158 µs         7.9 ms
//	(?:\pL?){120}zzz              245        258 µs          11 ms
//	(.*){250}                   1,002         15 µs          12 µs
//	(?:.?){1000}zzz             2,005        1.1 ms           43 ms
//	(.*){1000}                  4,002         39 µs          35 µs
//
// MaxPatternInst = 500 accepts every cleanup search above that is under 500
// (.{100,} and .{120} find over-long titles) and refuses (.*){1000} and
// (?:.?){1000}zzz, which took 1.3 s and 23.5 s over 40,000 titles before
// these limits. The worst program it admits, nested optional repetition near
// 500 instructions, costs about 0.5 ms per title and about 20 ms per
// description-sized field, so a Budget overshoots its limit by at most one
// such match.
const (
	// MaxTextValueBytes is the longest text value accepted, in bytes. A
	// title, author or series search term is a few dozen characters; 256
	// leaves room for a long literal or regex.
	MaxTextValueBytes = 256
	// MaxPatternInst is the largest compiled program (regexp/syntax
	// instructions, after Simplify expands counted repetition) a regex or a
	// wildcard may compile to.
	MaxPatternInst = 500
)

// checkTextValueLength refuses a value over MaxTextValueBytes.
func checkTextValueLength(raw string) error {
	if len(raw) > MaxTextValueBytes {
		return fmt.Errorf("value is %d bytes long; the limit is %d (search for a shorter part of it)", len(raw), MaxTextValueBytes)
	}
	return nil
}

// checkProgramSize refuses an expression whose compiled program is larger
// than MaxPatternInst instructions. expr has already compiled with regexp, so
// a parse error here cannot happen; it is reported rather than ignored all
// the same.
func checkProgramSize(expr, shown string) error {
	re, err := syntax.Parse(expr, syntax.Perl)
	if err != nil {
		return fmt.Errorf("invalid pattern %s: %w", shown, err)
	}
	prog, err := syntax.Compile(re.Simplify())
	if err != nil {
		return fmt.Errorf("invalid pattern %s: %w", shown, err)
	}
	if n := len(prog.Inst); n > MaxPatternInst {
		return fmt.Errorf("pattern %s is too complex to run over the library (%d instructions; the limit is %d) — simplify it: fewer * wildcards, no large counted repeats such as {100}, no nested optional groups", shown, n, MaxPatternInst)
	}
	return nil
}

// Kind is the form a text value was written in.
type Kind int

const (
	// KindSubstring is a plain or quoted case-insensitive substring.
	KindSubstring Kind = iota
	// KindGlob is a value containing * (unquoted).
	KindGlob
	// KindRegex is a /…/ value (unquoted).
	KindRegex
	// KindNonEmpty is a value made only of * — "the field has a value".
	KindNonEmpty
)

// TextMatcher is a compiled text value. Compile it once per request and call
// Match per row; it is safe for concurrent use.
type TextMatcher struct {
	Raw    string
	Kind   Kind
	needle string         // lower-cased, KindSubstring
	re     *regexp.Regexp // KindGlob, KindRegex
}

// IsRegexForm reports whether an unquoted value is written as a regex, i.e.
// starts with "/". Used to route values and to word error messages.
func IsRegexForm(raw string, quoted bool) bool {
	return !quoted && strings.HasPrefix(raw, "/")
}

// IsPlainLiteral reports whether a value carries no operator at all — no
// regex, glob, comparison or range — so a caller may safely treat it as an
// exact/substring literal (e.g. push it down to an exact-match index).
func IsPlainLiteral(raw string, quoted bool) bool {
	if quoted {
		return true
	}
	if IsRegexForm(raw, false) || strings.Contains(raw, "*") {
		return false
	}
	return !LooksLikeComparison(raw)
}

// CompileText compiles a text value. quoted is true when the user wrote the
// value in double quotes, which makes it a literal substring.
func CompileText(raw string, quoted bool) (*TextMatcher, error) {
	if raw == "" {
		return nil, errors.New("empty value")
	}
	if err := checkTextValueLength(raw); err != nil {
		return nil, err
	}
	if quoted {
		return &TextMatcher{Raw: raw, Kind: KindSubstring, needle: strings.ToLower(raw)}, nil
	}
	if IsRegexForm(raw, false) {
		re, err := compileRegexForm(raw)
		if err != nil {
			return nil, err
		}
		return &TextMatcher{Raw: raw, Kind: KindRegex, re: re}, nil
	}
	if strings.Contains(raw, "*") {
		if strings.Trim(raw, "*") == "" {
			return &TextMatcher{Raw: raw, Kind: KindNonEmpty}, nil
		}
		parts := strings.Split(raw, "*")
		for i, p := range parts {
			parts[i] = regexp.QuoteMeta(p)
		}
		expr := `(?is)^` + strings.Join(parts, ".*") + `$`
		re, err := regexp.Compile(expr)
		if err != nil { // unreachable: every piece is QuoteMeta'd
			return nil, fmt.Errorf("invalid wildcard %q: %w", raw, err)
		}
		if err := checkProgramSize(expr, raw); err != nil {
			return nil, err
		}
		return &TextMatcher{Raw: raw, Kind: KindGlob, re: re}, nil
	}
	return &TextMatcher{Raw: raw, Kind: KindSubstring, needle: strings.ToLower(raw)}, nil
}

// compileRegexForm compiles "/pattern/" as a case-insensitive RE2 regex.
func compileRegexForm(raw string) (*regexp.Regexp, error) {
	closeIdx := closingSlash(raw)
	if closeIdx < 0 {
		return nil, fmt.Errorf("regex %s has no closing /; close it (e.g. /^\\s*\\p{L}/) or quote the value to search for a literal slash", raw)
	}
	if closeIdx != len(raw)-1 {
		return nil, fmt.Errorf("unexpected %q after the closing / of regex %s; flags are not supported (regex is case-insensitive by default, use (?-i) to opt out)", raw[closeIdx+1:], raw[:closeIdx+1])
	}
	pattern := raw[1:closeIdx]
	if pattern == "" {
		return nil, errors.New("empty regex //")
	}
	expr := "(?i)" + pattern
	re, err := regexp.Compile(expr)
	if err != nil {
		msg := err.Error()
		if strings.Contains(pattern, "(?=") || strings.Contains(pattern, "(?!") ||
			strings.Contains(pattern, "(?<=") || strings.Contains(pattern, "(?<!") {
			msg += " — RE2 has no lookahead/lookbehind; exclude with a negated filter instead, e.g. -title:/^\\s*\\d/"
		}
		return nil, fmt.Errorf("invalid regex %s: %s", raw, msg)
	}
	if err := checkProgramSize(expr, raw); err != nil {
		return nil, err
	}
	return re, nil
}

// closingSlash returns the index of the first unescaped "/" after position 0,
// or -1.
func closingSlash(raw string) int {
	for i := 1; i < len(raw); i++ {
		switch raw[i] {
		case '\\':
			i++ // skip the escaped character
		case '/':
			return i
		}
	}
	return -1
}

// Match reports whether s satisfies the value.
func (m *TextMatcher) Match(s string) bool {
	switch m.Kind {
	case KindRegex:
		return m.re.MatchString(s)
	case KindGlob:
		return m.re.MatchString(strings.TrimSpace(s))
	case KindNonEmpty:
		return strings.TrimSpace(s) != ""
	default:
		return strings.Contains(strings.ToLower(s), m.needle)
	}
}

// Comparison is a parsed numeric comparison or inclusive range.
type Comparison struct {
	Op     string // ">", ">=", "<", "<=", "==", "!=", "range"
	Lo, Hi float64
}

// LooksLikeComparison reports whether a value is written as a numeric
// comparison (leading operator) or a bracketed range.
func LooksLikeComparison(raw string) bool {
	v := strings.TrimSpace(raw)
	if v == "" {
		return false
	}
	switch v[0] {
	case '>', '<', '=', '!':
		return true
	case '[':
		return strings.HasSuffix(v, "]")
	}
	return false
}

// IsNumber reports whether raw is a bare decimal number.
func IsNumber(raw string) bool {
	_, err := strconv.ParseFloat(strings.TrimSpace(raw), 64)
	return err == nil
}

// ParseNumericExpr parses a numeric filter value:
//
//	>2020  >=4.5  <64  <=3  =2019  ==2019  !=2019  2019 (equality)
//	[2015 TO 2020]  [* TO 64]  [2015 TO *]   (inclusive; * = open side)
func ParseNumericExpr(raw string) (Comparison, error) {
	return ParseNumericExprUnits(raw, nil)
}

// NumberParser parses one operand of a numeric expression, applying a
// field's units (see ParseBytes, ParseKbps, ParseHz). nil = plain number.
type NumberParser func(s string) (float64, error)

// ParseNumericExprUnits is ParseNumericExpr with a per-field unit parser for
// every operand: file_size:>20mb, bitrate:<64k, sample_rate:[22khz TO 48khz].
// It is the same comparison grammar the duration filter has used since
// 2026-09-27 (ops > >= < <= == != =, [a TO b] with * open), generalised.
func ParseNumericExprUnits(raw string, num NumberParser) (Comparison, error) {
	if num == nil {
		num = plainNumber
	}
	parseNum := func(s, raw string) (float64, error) {
		v, err := num(strings.TrimSpace(s))
		if err != nil || math.IsNaN(v) || math.IsInf(v, 0) {
			return 0, fmt.Errorf("%q is not a valid value in %q (use e.g. >2020, <=64, [2015 TO 2020])", strings.TrimSpace(s), raw)
		}
		return v, nil
	}
	e := strings.TrimSpace(raw)
	if strings.HasPrefix(e, "[") {
		if !strings.HasSuffix(e, "]") {
			return Comparison{}, fmt.Errorf("range %q has no closing ]; use [2015 TO 2020]", raw)
		}
		inner := strings.Fields(e[1 : len(e)-1])
		if len(inner) != 3 || !strings.EqualFold(inner[1], "TO") {
			return Comparison{}, fmt.Errorf("invalid range %q; use [2015 TO 2020] (* leaves a side open)", raw)
		}
		out := Comparison{Op: "range", Lo: math.Inf(-1), Hi: math.Inf(1)}
		if inner[0] != "*" {
			v, err := parseNum(inner[0], raw)
			if err != nil {
				return Comparison{}, err
			}
			out.Lo = v
		}
		if inner[2] != "*" {
			v, err := parseNum(inner[2], raw)
			if err != nil {
				return Comparison{}, err
			}
			out.Hi = v
		}
		if out.Lo > out.Hi {
			return Comparison{}, fmt.Errorf("range %q is empty: the low end is above the high end", raw)
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
	v, err := parseNum(rest, raw)
	if err != nil {
		return Comparison{}, err
	}
	return Comparison{Op: op, Lo: v, Hi: v}, nil
}

func plainNumber(s string) (float64, error) {
	return strconv.ParseFloat(strings.TrimSpace(s), 64)
}

// unitNumber parses "<number><suffix>" where suffix (case-insensitive) is a
// key of mult; an empty suffix is allowed when "" is a key.
func unitNumber(s string, mult map[string]float64) (float64, error) {
	t := strings.ToLower(strings.TrimSpace(s))
	i := len(t)
	for i > 0 && (t[i-1] < '0' || t[i-1] > '9') && t[i-1] != '.' {
		i--
	}
	m, ok := mult[t[i:]]
	if !ok {
		return 0, fmt.Errorf("unknown unit %q", t[i:])
	}
	v, err := strconv.ParseFloat(t[:i], 64)
	if err != nil || v < 0 {
		return 0, fmt.Errorf("invalid number %q", s)
	}
	return v * m, nil
}

var byteUnits = map[string]float64{
	"": 1, "b": 1,
	"k": 1 << 10, "kb": 1 << 10,
	"m": 1 << 20, "mb": 1 << 20,
	"g": 1 << 30, "gb": 1 << 30,
	"t": 1 << 40, "tb": 1 << 40,
}

// ParseBytes parses a size in bytes; k/kb/m/mb/g/gb/t/tb are 1024-based
// (20mb = 20*1024*1024). A bare number is bytes.
func ParseBytes(s string) (float64, error) { return unitNumber(s, byteUnits) }

// ParseKbps parses a bitrate in kbps; "k" and "kbps" suffixes are optional.
func ParseKbps(s string) (float64, error) {
	return unitNumber(s, map[string]float64{"": 1, "k": 1, "kbps": 1})
}

// ParseHz parses a sample rate in Hz; "hz" and "khz" (x1000) are accepted.
func ParseHz(s string) (float64, error) {
	return unitNumber(s, map[string]float64{"": 1, "hz": 1, "khz": 1000})
}

// Match reports whether x satisfies the comparison.
func (c Comparison) Match(x float64) bool {
	switch c.Op {
	case ">":
		return x > c.Lo
	case ">=":
		return x >= c.Lo
	case "<":
		return x < c.Lo
	case "<=":
		return x <= c.Lo
	case "!=":
		return x != c.Lo
	case "range":
		return x >= c.Lo && x <= c.Hi
	default:
		return x == c.Lo
	}
}

// MatchAny evaluates the comparison over every known value of a field (a book
// has two years). An empty set — the value is unknown — matches NOTHING, not
// even "!=", so an unprobed or unset field is neither included nor excluded
// by a comparison. "!=" requires every value to differ; every other operator
// needs one value to satisfy it.
func (c Comparison) MatchAny(values []float64) bool {
	if len(values) == 0 {
		return false
	}
	if c.Op == "!=" {
		for _, v := range values {
			if v == c.Lo {
				return false
			}
		}
		return true
	}
	for _, v := range values {
		if c.Match(v) {
			return true
		}
	}
	return false
}
