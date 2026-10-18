// file: internal/search/query_parser_test.go
// version: 1.0.0
// guid: 5f1c8a2d-4b9e-4f70-a7c6-2d8e0f1b9a57

package search

import "testing"

func mustParse(t *testing.T, q string) Node {
	t.Helper()
	n, err := ParseQuery(q)
	if err != nil {
		t.Fatalf("ParseQuery(%q): %v", q, err)
	}
	return n
}

func TestParse_BareField(t *testing.T) {
	n := mustParse(t, "author:sanderson")
	fn, ok := n.(*FieldNode)
	if !ok {
		t.Fatalf("got %T, want *FieldNode", n)
	}
	if fn.Field != "author" || fn.Value != "sanderson" {
		t.Errorf("got %+v", fn)
	}
}

func TestParse_FreeText(t *testing.T) {
	n := mustParse(t, "vampire")
	if _, ok := n.(*FreeTextNode); !ok {
		t.Fatalf("got %T, want *FreeTextNode", n)
	}
}

func TestParse_ImplicitAnd(t *testing.T) {
	n := mustParse(t, "author:sanderson tag:scifi")
	and, ok := n.(*AndNode)
	if !ok {
		t.Fatalf("got %T, want *AndNode", n)
	}
	if len(and.Children) != 2 {
		t.Errorf("AND children = %d, want 2", len(and.Children))
	}
}

func TestParse_ExplicitAnd(t *testing.T) {
	cases := []string{
		"a:1 && b:2",
		"a:1 AND b:2",
	}
	for _, q := range cases {
		n := mustParse(t, q)
		if _, ok := n.(*AndNode); !ok {
			t.Errorf("%q → %T, want *AndNode", q, n)
		}
	}
}

func TestParse_Or(t *testing.T) {
	cases := []string{
		"a:1 || b:2",
		"a:1 OR b:2",
	}
	for _, q := range cases {
		n := mustParse(t, q)
		if _, ok := n.(*OrNode); !ok {
			t.Errorf("%q → %T, want *OrNode", q, n)
		}
	}
}

func TestParse_NotDashPrefix(t *testing.T) {
	n := mustParse(t, "-tag:romance")
	not, ok := n.(*NotNode)
	if !ok {
		t.Fatalf("got %T, want *NotNode", n)
	}
	if _, ok := not.Child.(*FieldNode); !ok {
		t.Errorf("inner: got %T, want *FieldNode", not.Child)
	}
}

func TestParse_NotKeyword(t *testing.T) {
	n := mustParse(t, "NOT tag:romance")
	if _, ok := n.(*NotNode); !ok {
		t.Fatalf("got %T, want *NotNode", n)
	}
}

func TestParse_Grouping(t *testing.T) {
	// (-title:twilight && -title:"New Dawn" && title:vampire) || title:fangtown
	n := mustParse(t, `(-title:twilight && -title:"New Dawn" && title:vampire) || title:fangtown`)
	or, ok := n.(*OrNode)
	if !ok {
		t.Fatalf("top = %T, want *OrNode", n)
	}
	if len(or.Children) != 2 {
		t.Fatalf("OR children = %d, want 2", len(or.Children))
	}
	if _, ok := or.Children[0].(*AndNode); !ok {
		t.Errorf("first branch = %T, want *AndNode", or.Children[0])
	}
	if _, ok := or.Children[1].(*FieldNode); !ok {
		t.Errorf("second branch = %T, want *FieldNode", or.Children[1])
	}
}

func TestParse_ValueAlternation(t *testing.T) {
	n := mustParse(t, `title:(Fangtown|fangtown|"fang town")`)
	alt, ok := n.(*ValueAltNode)
	if !ok {
		t.Fatalf("got %T, want *ValueAltNode", n)
	}
	if alt.Field != "title" {
		t.Errorf("field = %q, want title", alt.Field)
	}
	if len(alt.Values) != 3 {
		t.Fatalf("values = %d, want 3", len(alt.Values))
	}
	if alt.Values[2] != "fang town" {
		t.Errorf("quoted value = %q, want 'fang town'", alt.Values[2])
	}
}

func TestParse_NumericComparators(t *testing.T) {
	cases := map[string]string{
		"year:>2000":  ">",
		"year:<2010":  "<",
		"year:>=2000": ">=",
		"year:<=2020": "<=",
	}
	for q, wantOp := range cases {
		n := mustParse(t, q)
		fn, ok := n.(*FieldNode)
		if !ok {
			t.Fatalf("%q → %T, want *FieldNode", q, n)
		}
		if fn.Op != wantOp {
			t.Errorf("%q: Op = %q, want %q", q, fn.Op, wantOp)
		}
	}
}

func TestParse_NumericRange(t *testing.T) {
	n := mustParse(t, "year:[2000 TO 2010]")
	fn, ok := n.(*FieldNode)
	if !ok {
		t.Fatalf("got %T, want *FieldNode", n)
	}
	if fn.Op != "range" {
		t.Errorf("Op = %q, want range", fn.Op)
	}
	if fn.RangeMin != "2000" || fn.RangeMax != "2010" {
		t.Errorf("range = [%s TO %s], want [2000 TO 2010]", fn.RangeMin, fn.RangeMax)
	}
}

func TestParse_PrefixWildcardFuzzy(t *testing.T) {
	type wantShape struct {
		prefix   bool
		wildcard bool
		fuzzy    bool
		value    string
	}
	cases := map[string]wantShape{
		"title:vamp*":   {prefix: true, value: "vamp"},
		"title:*vamp":   {wildcard: true, value: "vamp"},
		"author:smith~": {fuzzy: true, value: "smith"},
	}
	for q, want := range cases {
		n := mustParse(t, q)
		fn, ok := n.(*FieldNode)
		if !ok {
			t.Fatalf("%q → %T, want *FieldNode", q, n)
		}
		if fn.Prefix != want.prefix {
			t.Errorf("%q Prefix = %v, want %v", q, fn.Prefix, want.prefix)
		}
		if fn.Wildcard != want.wildcard {
			t.Errorf("%q Wildcard = %v, want %v", q, fn.Wildcard, want.wildcard)
		}
		if fn.Fuzzy != want.fuzzy {
			t.Errorf("%q Fuzzy = %v, want %v", q, fn.Fuzzy, want.fuzzy)
		}
		if fn.Value != want.value {
			t.Errorf("%q Value = %q, want %q", q, fn.Value, want.value)
		}
	}
}

func TestParse_Boost(t *testing.T) {
	n := mustParse(t, "title:vampire^3")
	fn, ok := n.(*FieldNode)
	if !ok {
		t.Fatalf("got %T", n)
	}
	if fn.Boost != 3 {
		t.Errorf("Boost = %g, want 3", fn.Boost)
	}
	if fn.Value != "vampire" {
		t.Errorf("Value = %q, want vampire", fn.Value)
	}
}

func TestParse_BackwardCompat(t *testing.T) {
	// Existing queries (no new operators) must parse identically to
	// before: implicit whitespace = AND, field:value, quoted values,
	// dash-negation.
	n := mustParse(t, `author:"Joshua Dalzelle" tag:scifi -tag:romance great books`)
	and, ok := n.(*AndNode)
	if !ok {
		t.Fatalf("got %T, want *AndNode", n)
	}
	// author + tag + NOT tag + "great" + "books" = 5 children
	if len(and.Children) != 5 {
		t.Errorf("AND children = %d, want 5", len(and.Children))
	}
}

func TestParse_WorkedExample(t *testing.T) {
	// Full worked example from the 3.4 spec — both operator styles
	// must parse into structurally equivalent trees.
	style1 := `(-title:twilight && -title:"New Dawn" && title:vampire) || title:(Fangtown|fangtown|"fang town")`
	style2 := `(NOT title:twilight AND NOT title:"New Dawn" AND title:vampire) OR title:(Fangtown|fangtown|"fang town")`

	n1 := mustParse(t, style1)
	n2 := mustParse(t, style2)

	if n1.String() != n2.String() {
		t.Errorf("styles produce different trees:\nstyle1: %s\nstyle2: %s", n1.String(), n2.String())
	}
}

func TestParse_Errors(t *testing.T) {
	bad := []string{
		`(author:foo`,          // missing ')'
		`author:"unterminated`, // unclosed quote
		`author:[2000 2010]`,   // missing TO
	}
	for _, q := range bad {
		if _, err := ParseQuery(q); err == nil {
			t.Errorf("ParseQuery(%q) should have errored", q)
		}
	}
}

// ── relative dates (D76) ────────────────────────────────────────────────────

// TestParseQuery_RelativeDateTerms pins that the lexer already carries a signed
// offset after a comparison operator, and the ".." range for date fields.
func TestParseQuery_RelativeDateTerms(t *testing.T) {
	cases := []struct {
		q    string
		want FieldNode
	}{
		{"last_played:>-30d", FieldNode{Field: "last_played", Op: ">", Value: "-30d"}},
		{"date_added:>=-1y", FieldNode{Field: "date_added", Op: ">=", Value: "-1y"}},
		{"date_added:<+2w", FieldNode{Field: "date_added", Op: "<", Value: "+2w"}},
		{"last_played:-30d..now", FieldNode{Field: "last_played", Op: "range", RangeMin: "-30d", RangeMax: "now"}},
		{"added:2026-01-01..2026-02-01", FieldNode{Field: "added", Op: "range", RangeMin: "2026-01-01", RangeMax: "2026-02-01"}},
		{"date_modified:-1y..-6m", FieldNode{Field: "date_modified", Op: "range", RangeMin: "-1y", RangeMax: "-6m"}},
	}
	for _, tc := range cases {
		t.Run(tc.q, func(t *testing.T) {
			node, err := ParseQuery(tc.q)
			if err != nil {
				t.Fatalf("ParseQuery(%q): %v", tc.q, err)
			}
			got, ok := node.(*FieldNode)
			if !ok {
				t.Fatalf("node is %T, want *FieldNode", node)
			}
			if got.Field != tc.want.Field || got.Op != tc.want.Op || got.Value != tc.want.Value ||
				got.RangeMin != tc.want.RangeMin || got.RangeMax != tc.want.RangeMax {
				t.Fatalf("got %+v, want %+v", *got, tc.want)
			}
		})
	}
}

// ".." is only a range on a date field: elsewhere it is part of the value.
func TestParseQuery_DotDotOutsideDateFieldsIsAValue(t *testing.T) {
	node, err := ParseQuery("title:wait..what")
	if err != nil {
		t.Fatal(err)
	}
	got := node.(*FieldNode)
	if got.Op == "range" || got.Value != "wait..what" {
		t.Fatalf("title:wait..what parsed as %+v", *got)
	}
}

// A leading '-' on the whole term is still NOT; the sign after the colon is not.
func TestParseQuery_NegatedDateTermStillNegates(t *testing.T) {
	node, err := ParseQuery("-last_played:>-30d")
	if err != nil {
		t.Fatal(err)
	}
	n, ok := node.(*NotNode)
	if !ok {
		t.Fatalf("node is %T, want *NotNode", node)
	}
	f := n.Child.(*FieldNode)
	if f.Field != "last_played" || f.Op != ">" || f.Value != "-30d" {
		t.Fatalf("child = %+v", *f)
	}
}
