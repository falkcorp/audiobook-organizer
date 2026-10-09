// file: internal/querygrammar/querygrammar_test.go
// version: 1.2.0
// guid: 4d8a1e63-2b7c-4f90-a5e1-8c6d3b0f2a97
// last-edited: 2026-10-09

package querygrammar

import (
	"encoding/json"
	"os"
	"slices"
	"strings"
	"testing"
)

// conformanceCase is one row of testdata/conformance.json, the corpus shared
// with web/src/utils/queryGrammar.test.ts so the Go engine and the TypeScript
// RE2 translation cannot drift apart without one of the two suites failing.
type conformanceCase struct {
	Name            string   `json:"name"`
	Pattern         string   `json:"pattern"`
	Quoted          bool     `json:"quoted"`
	Input           string   `json:"input"`
	WantMatch       *bool    `json:"want_match"`
	WantError       bool     `json:"want_error"`
	GoErrorContains string   `json:"go_error_contains"`
	TSErrorMatches  string   `json:"ts_error_matches"`
	Engines         []string `json:"engines"`
	SkipReason      string   `json:"skip_reason"`
}

func (c conformanceCase) runsOn(engine string) bool {
	return len(c.Engines) == 0 || slices.Contains(c.Engines, engine)
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
			if (tc.WantMatch == nil) == !tc.WantError {
				t.Fatal("a case sets exactly one of want_match and want_error")
			}
			if len(tc.Engines) > 0 && tc.SkipReason == "" {
				t.Fatal("a case restricted by engines must carry a skip_reason")
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
			if got := m.Match(tc.Input); got != *tc.WantMatch {
				t.Fatalf("Match(%q) with %q = %v, want %v", tc.Input, tc.Pattern, got, *tc.WantMatch)
			}
		})
	}
	t.Logf("conformance corpus (go): %d cases, %d run, %d skipped", len(cases), len(cases)-skipped, skipped)
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
