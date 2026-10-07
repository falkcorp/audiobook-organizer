// file: internal/querygrammar/querygrammar_test.go
// version: 1.0.0
// guid: 4d8a1e63-2b7c-4f90-a5e1-8c6d3b0f2a97
// last-edited: 2026-10-06

package querygrammar

import (
	"strings"
	"testing"
)

func TestCompileText_Match(t *testing.T) {
	cases := []struct {
		name   string
		raw    string
		quoted bool
		in     string
		want   bool
	}{
		{"substring case-insensitive", "vamp", false, "The VAMPIRE Lestat", true},
		{"substring miss", "zzz", false, "Dune", false},
		{"quoted literal star", "a*", true, "Is a* b", true},
		{"quoted literal star is not glob", "a*", true, "Anathem", false},
		{"quoted slash is literal", "/x/", true, "path /x/ here", true},
		{"glob prefix", "a*", false, "Anathem", true},
		{"glob prefix case-insensitive", "a*", false, "anathem", true},
		{"glob prefix trims leading space", "a*", false, "  Anathem", true},
		{"glob prefix is whole-value not any word", "a*", false, "The Anathem", false},
		{"glob suffix", "*saga", false, "Hyperion Saga", true},
		{"glob contains", "*vamp*", false, "The Vampire Lestat", true},
		{"glob middle", "the*lestat", false, "The Vampire Lestat", true},
		{"glob escapes regex metachars", "a.c*", false, "abcd", false},
		{"glob escapes regex metachars hit", "a.c*", false, "a.cd", true},
		{"star alone = non-empty", "*", false, "x", true},
		{"star alone empty", "*", false, "   ", false},
		{"regex anchored letter start", `/^\s*\p{L}/`, false, "  Émile", true},
		{"regex anchored letter start miss digit", `/^\s*\p{L}/`, false, "01 - Chapter", false},
		{"regex case-insensitive default", "/^dune$/", false, "DUNE", true},
		{"regex case-sensitive opt-out", "/(?-i)^dune$/", false, "DUNE", false},
		{"regex with spaces", "/chapter \\d+/", false, "Chapter 12", true},
		{"regex escaped slash", `/a\/b/`, false, "a/b", true},
		{"regex alternation", "/^(m4b|mp3)$/", false, "MP3", true},
		{"regex alternation miss", "/^(m4b|mp3)$/", false, "flac", false},
		{"regex unanchored", "/vamp/", false, "The Vampire", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m, err := CompileText(tc.raw, tc.quoted)
			if err != nil {
				t.Fatalf("CompileText(%q): %v", tc.raw, err)
			}
			if got := m.Match(tc.in); got != tc.want {
				t.Fatalf("Match(%q) with %q = %v, want %v", tc.in, tc.raw, got, tc.want)
			}
		})
	}
}

func TestCompileText_Errors(t *testing.T) {
	cases := []struct {
		name    string
		raw     string
		wantSub string
	}{
		{"empty", "", "empty value"},
		{"unterminated", "/abc", "no closing /"},
		{"empty regex", "//", "empty regex"},
		{"trailing flags", "/abc/i", "after the closing /"},
		{"bad syntax", "/a(b/", "invalid regex"},
		{"lookahead", "/^(?!The)/", "no lookahead"},
		{"lookbehind", "/(?<=a)b/", "no lookahead"},
		{"backref", `/(a)\1/`, "invalid regex"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := CompileText(tc.raw, false)
			if err == nil {
				t.Fatalf("CompileText(%q) = nil error, want error containing %q", tc.raw, tc.wantSub)
			}
			if !strings.Contains(err.Error(), tc.wantSub) {
				t.Fatalf("CompileText(%q) error %q, want it to contain %q", tc.raw, err, tc.wantSub)
			}
		})
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
