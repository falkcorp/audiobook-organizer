// file: internal/querygrammar/querygrammar_test.go
// version: 1.4.0
// guid: 4d8a1e63-2b7c-4f90-a5e1-8c6d3b0f2a97
// last-edited: 2026-10-10

package querygrammar

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"regexp/syntax"
	"slices"
	"strings"
	"testing"
	"time"
)

// conformanceCase is one row of testdata/conformance.json, the corpus shared
// with web/src/utils/queryGrammar.test.ts so the Go engine and the TypeScript
// RE2 translation cannot drift apart without one of the two suites failing.
type conformanceCase struct {
	Name            string   `json:"name"`
	Pattern         string   `json:"pattern"`
	Quoted          bool     `json:"quoted"`
	Input           string   `json:"input"`
	WantMatch       optBool  `json:"want_match"`
	WantError       bool     `json:"want_error"`
	GoErrorContains string   `json:"go_error_contains"`
	TSErrorMatches  string   `json:"ts_error_matches"`
	Engines         []string `json:"engines"`
	SkipReason      string   `json:"skip_reason"`
}

// optBool is a bool that knows whether the row set it. A JSON null is
// rejected outright: the TS reader sees null as "present" and Go's *bool
// would see it as "absent", so the two suites would classify the same row
// differently. Rejecting it at decode time keeps one rule for both.
type optBool struct {
	set bool
	val bool
}

func (o *optBool) UnmarshalJSON(b []byte) error {
	if string(b) == "null" {
		return errors.New("want_match must be true or false, not null")
	}
	o.set = true
	return json.Unmarshal(b, &o.val)
}

// conformanceEngines is every engine a row may name. The same list lives in
// web/src/utils/queryGrammar.test.ts (CONFORMANCE_ENGINES).
var conformanceEngines = []string{"go", "ts"}

// validate applies the row-shape rules both suites share, so a malformed row
// fails in both rather than running in one and skipping in the other:
// exactly one of want_match / want_error; engines absent or a non-empty
// subset of conformanceEngines with no unknown names; skip_reason required
// only when engines actually excludes one.
func (c conformanceCase) validate() error {
	if c.WantMatch.set == c.WantError {
		return errors.New("a case sets exactly one of want_match and want_error")
	}
	if c.Engines == nil {
		return nil
	}
	if len(c.Engines) == 0 {
		return errors.New("engines must be absent or a non-empty subset of go,ts")
	}
	for _, e := range c.Engines {
		if !slices.Contains(conformanceEngines, e) {
			return fmt.Errorf("unknown engine %q (want one of %v)", e, conformanceEngines)
		}
	}
	if len(c.Engines) < len(conformanceEngines) && c.SkipReason == "" {
		return errors.New("a case that excludes an engine must carry a skip_reason")
	}
	return nil
}

func (c conformanceCase) runsOn(engine string) bool {
	return c.Engines == nil || slices.Contains(c.Engines, engine)
}

func TestConformanceCorpus(t *testing.T) {
	b, err := os.ReadFile("testdata/conformance.json")
	if err != nil {
		t.Fatal(err)
	}
	var cases []conformanceCase
	if err := json.Unmarshal(b, &cases); err != nil {
		t.Fatalf("testdata/conformance.json: %v", err)
	}
	skipped := 0
	for _, tc := range cases {
		if !tc.runsOn("go") {
			skipped++
		}
		t.Run(tc.Name, func(t *testing.T) {
			if err := tc.validate(); err != nil {
				t.Fatal(err)
			}
			if !tc.runsOn("go") {
				t.Skip(tc.SkipReason)
			}
			m, err := CompileText(tc.Pattern, tc.Quoted)
			if tc.WantError {
				if err == nil {
					t.Fatalf("CompileText(%q) = nil error, want error containing %q", tc.Pattern, tc.GoErrorContains)
				}
				if tc.GoErrorContains == "" || !strings.Contains(err.Error(), tc.GoErrorContains) {
					t.Fatalf("CompileText(%q) error %q, want it to contain %q", tc.Pattern, err, tc.GoErrorContains)
				}
				return
			}
			if err != nil {
				t.Fatalf("CompileText(%q): %v", tc.Pattern, err)
			}
			if got := m.Match(tc.Input); got != tc.WantMatch.val {
				t.Fatalf("Match(%q) with %q = %v, want %v", tc.Input, tc.Pattern, got, tc.WantMatch.val)
			}
		})
	}
	t.Logf("conformance corpus (go): %d cases, %d run, %d skipped", len(cases), len(cases)-skipped, skipped)
}

// TestConformanceCase_Validate locks the row-shape rules shared with the TS
// suite (validateCase in web/src/utils/queryGrammar.test.ts): the same inputs
// must be accepted or rejected on both sides.
func TestConformanceCase_Validate(t *testing.T) {
	cases := []struct {
		name    string
		row     string
		wantErr string // "" = valid; "decode" = rejected while decoding
	}{
		{"match row", `{"name":"x","pattern":"a","want_match":true}`, ""},
		{"error row", `{"name":"x","pattern":"a","want_error":true}`, ""},
		{"explicit both engines, no skip_reason", `{"name":"x","pattern":"a","want_match":true,"engines":["go","ts"]}`, ""},
		{"go only with skip_reason", `{"name":"x","pattern":"a","want_match":true,"engines":["go"],"skip_reason":"ts differs"}`, ""},
		{"want_match null", `{"name":"x","pattern":"a","want_match":null}`, "decode"},
		{"neither expectation", `{"name":"x","pattern":"a"}`, "exactly one of want_match and want_error"},
		{"both expectations", `{"name":"x","pattern":"a","want_match":false,"want_error":true}`, "exactly one of want_match and want_error"},
		{"empty engines", `{"name":"x","pattern":"a","want_match":true,"engines":[]}`, "non-empty subset"},
		{"unknown engine", `{"name":"x","pattern":"a","want_match":true,"engines":["golang"],"skip_reason":"r"}`, "unknown engine"},
		{"excludes an engine, no skip_reason", `{"name":"x","pattern":"a","want_match":true,"engines":["go"]}`, "must carry a skip_reason"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var c conformanceCase
			err := json.Unmarshal([]byte(tc.row), &c)
			if tc.wantErr == "decode" {
				if err == nil {
					t.Fatal("decoded, want a decode error")
				}
				return
			}
			if err != nil {
				t.Fatalf("decode: %v", err)
			}
			err = c.validate()
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("validate() = %v, want nil", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("validate() = %v, want error containing %q", err, tc.wantErr)
			}
		})
	}
	only := conformanceCase{Engines: []string{"go"}}
	if only.runsOn("ts") || !only.runsOn("go") {
		t.Fatal("engines [go] must run on go and not on ts")
	}
}

func TestIsPlainLiteral(t *testing.T) {
	cases := map[string]bool{
		"no_match": true, "/^no/": false, "org*": false, ">5": false, "[1 TO 2]": false,
	}
	for raw, want := range cases {
		if got := IsPlainLiteral(raw, false); got != want {
			t.Errorf("IsPlainLiteral(%q) = %v, want %v", raw, got, want)
		}
	}
	if !IsPlainLiteral("/^no/", true) {
		t.Error("a quoted value is always a literal")
	}
}

func TestParseNumericExpr(t *testing.T) {
	cases := []struct {
		raw  string
		vals []float64
		want bool
	}{
		{">2020", []float64{2021}, true},
		{">2020", []float64{2020}, false},
		{">=2020", []float64{2020}, true},
		{"<64", []float64{63.9}, true},
		{"<=3", []float64{3}, true},
		{"=2019", []float64{2019}, true},
		{"==2019", []float64{2018}, false},
		{"2019", []float64{2019}, true},
		{"!=2019", []float64{2018}, true},
		{"[2015 TO 2020]", []float64{2015}, true},
		{"[2015 TO 2020]", []float64{2020}, true},
		{"[2015 TO 2020]", []float64{2021}, false},
		{"[2015 to 2020]", []float64{2017}, true},
		{"[* TO 64]", []float64{1}, true},
		{"[2015 TO *]", []float64{3000}, true},
		// Either value may satisfy (a book's print and release years).
		{">2020", []float64{1999, 2022}, true},
		// != needs every value to differ.
		{"!=2019", []float64{2019, 2020}, false},
		// Unknown matches nothing, not even !=.
		{">0", nil, false},
		{"!=5", nil, false},
	}
	for _, tc := range cases {
		c, err := ParseNumericExpr(tc.raw)
		if err != nil {
			t.Fatalf("ParseNumericExpr(%q): %v", tc.raw, err)
		}
		if got := c.MatchAny(tc.vals); got != tc.want {
			t.Errorf("%q over %v = %v, want %v", tc.raw, tc.vals, got, tc.want)
		}
	}
}

func TestParseNumericExpr_Errors(t *testing.T) {
	for _, raw := range []string{">abc", "[2015 2020]", "[2015 TO", "[a TO b]", "[2020 TO 2015]", ">", "abc"} {
		if _, err := ParseNumericExpr(raw); err == nil {
			t.Errorf("ParseNumericExpr(%q) = nil error, want error", raw)
		}
	}
}

func TestUnitParsers(t *testing.T) {
	cases := []struct {
		p    NumberParser
		in   string
		want float64
	}{
		{ParseBytes, "20mb", 20 * 1024 * 1024},
		{ParseBytes, "20MB", 20 * 1024 * 1024},
		{ParseBytes, "1.5g", 1.5 * 1024 * 1024 * 1024},
		{ParseBytes, "2k", 2048},
		{ParseBytes, "1tb", 1 << 40},
		{ParseBytes, "100", 100},
		{ParseKbps, "64k", 64},
		{ParseKbps, "64kbps", 64},
		{ParseKbps, "64", 64},
		{ParseHz, "44.1khz", 44100},
		{ParseHz, "22050", 22050},
		{ParseHz, "22050hz", 22050},
	}
	for _, tc := range cases {
		got, err := tc.p(tc.in)
		if err != nil || got != tc.want {
			t.Errorf("parse %q = %v, %v; want %v", tc.in, got, err, tc.want)
		}
	}
	for _, bad := range []string{"20zb", "abc", "mb", "-5mb"} {
		if _, err := ParseBytes(bad); err == nil {
			t.Errorf("ParseBytes(%q) = nil error", bad)
		}
	}
	c, err := ParseNumericExprUnits(">20mb", ParseBytes)
	if err != nil || !c.Match(30*1024*1024) || c.Match(10*1024*1024) {
		t.Fatalf(">20mb: %+v %v", c, err)
	}
	if _, err := ParseNumericExprUnits(">20zb", ParseBytes); err == nil {
		t.Fatal(">20zb must be an error")
	}
}

// TestLimits_RefuseQuickly: the patterns measured at 1.3 s, 23.5 s and
// 2 m 10 s over 40,000 titles before the limits are refused at compile time,
// in well under a millisecond each, with an error that says why.
func TestLimits_RefuseQuickly(t *testing.T) {
	cases := map[string]struct {
		raw  string
		want string
	}{
		"repeat of a star":         {`/(.*){1000}/`, "too complex"},
		"nested optional repeat":   {`/(?:.?){1000}zzz/`, "too complex"},
		"30 KB regex":              {"/" + strings.Repeat("(a|b)", 6000) + "/", "the limit is 256"},
		"30 KB literal":            {strings.Repeat("a", 30000), "the limit is 256"},
		"30 KB quoted literal":     {strings.Repeat("a", 30000), "the limit is 256"},
		"wildcard with many stars": {strings.Repeat("a*", 60), "too complex"},
	}
	for name, tc := range cases {
		start := time.Now()
		_, err := CompileText(tc.raw, name == "30 KB quoted literal")
		took := time.Since(start)
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Fatalf("%s: err = %v, want it to contain %q", name, err, tc.want)
		}
		if took > 50*time.Millisecond {
			t.Fatalf("%s: refusing took %s", name, took)
		}
		t.Logf("%s: refused in %s: %v", name, took, err)
	}
}

// TestLimits_OrdinaryPatternsFit pins the instruction counts the limits'
// doc comment quotes, and that ordinary title patterns compile.
func TestLimits_OrdinaryPatternsFit(t *testing.T) {
	inst := func(expr string) int {
		re, err := syntax.Parse(expr, syntax.Perl)
		if err != nil {
			t.Fatal(err)
		}
		prog, err := syntax.Compile(re.Simplify())
		if err != nil {
			t.Fatal(err)
		}
		return len(prog.Inst)
	}
	for expr, want := range map[string]int{
		`(?i)(?:.?){10}zzz`:   25,
		`(?i)(?:.?){30}zzz`:   65,
		`(?i)(.*){1000}`:      4002,
		`(?i)(?:.?){1000}zzz`: 2005,
		`(?i)[a-z]{50}`:       52,
	} {
		if got := inst(expr); got != want {
			t.Errorf("%s: %d instructions, the doc says %d", expr, got, want)
		}
	}
	for _, raw := range []string{`/^\s*\p{L}/`, `/[a-z]{50}/`, `/(a|b|c|d|e|f|g|h|i|j|k|l|m|n|o|p|q|r|s|t|u|v|w|x|y|z){20}/`, `/chapter \d+/`, "the*lestat", strings.Repeat("x", MaxTextValueBytes)} {
		if _, err := CompileText(raw, false); err != nil {
			t.Errorf("CompileText(%q): %v", raw, err)
		}
	}
}
