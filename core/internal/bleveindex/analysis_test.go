// file: core/internal/bleveindex/analysis_test.go
// version: 0.1.0
// guid: c4b7e2a9-5d18-4f63-a0e1-9b2d8c6f4a75
// last-edited: 2026-11-20

package bleveindex_test

import (
	"context"
	"errors"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/falkcorp/mediacore/coretest"
	"github.com/falkcorp/mediacore/storage"
	"github.com/falkcorp/mediacore/storage/index"
)

// These tests pin the analysis, weighting and wildcard surfaces F7-09's books
// index needs (seams.d/F7-09-T6-core.md), on the real engine through
// coretest.IndexEngine, under an owner that is not indextest's.

const (
	arkham = storage.Owner("arkham")
	cases  = "cases"
)

// bookish is the analyzer books' production index uses (book_en_nostop):
// '_' to space, Unicode words, possessive, lowercase, Porter, and no stop
// filter, so a quoted phrase keeps its stop words in place.
var bookish = index.Analyzer{
	Name:         "bookish",
	CharFilters:  []index.CharFilter{index.CharUnderscoreToSpace},
	Tokenizer:    index.TokenizeUnicodeWords,
	TokenFilters: []index.TokenFilter{index.FilterEnglishPossessive, index.FilterLowercase, index.FilterPorterStem},
}

func caseFields(boosts bool) []index.Field {
	b := func(v float64) float64 {
		if boosts {
			return v
		}
		return 0
	}
	return []index.Field{
		{Name: "title", Kind: index.Text, Analyzer: "bookish", Store: true, Boost: b(3)},
		{Name: "author", Kind: index.Text, Analyzer: "bookish", Boost: b(2)},
		{Name: "description", Kind: index.Text, Analyzer: "bookish", Boost: b(0.5)},
		{Name: "plain", Kind: index.Text}, // the title again, under the standard analyzer
		{Name: "format", Kind: index.Keyword, Analyzer: index.AnalyzerKeywordLowercase, Facet: true},
		{Name: "alias", Kind: index.Keyword}, // case-sensitive
	}
}

var caseDocs = map[string]index.Doc{
	"todd":    {"title": "A Death in the Family", "author": "Jason Todd", "description": "Robin", "plain": "A Death in the Family", "format": "MP3", "alias": "Robin"},
	"joker":   {"title": "Death of the Family", "author": "The Joker", "description": "Clown", "plain": "Death of the Family", "format": "M4B", "alias": "Clown"},
	"wayne":   {"title": "Batman_Year_One", "author": "Bruce Wayne", "description": "Origin", "plain": "Batman_Year_One", "format": "mp3", "alias": "Bat"},
	"desc":    {"title": "Hush", "author": "Thomas Elliot", "description": "Gotham", "plain": "Hush", "format": "FLAC", "alias": "Hush"},
	"byline":  {"title": "Knightfall", "author": "Gotham Girl", "description": "Bane", "plain": "Knightfall", "format": "FLAC", "alias": "Venom"},
	"heading": {"title": "Gotham by Gaslight", "author": "Jack", "description": "Ripper", "plain": "Gotham by Gaslight", "format": "FLAC", "alias": "Gaslight"},
}

func caseSpec(fields []index.Field, analyzers ...index.Analyzer) index.Spec {
	return index.Spec{
		Name: cases, MappingVersion: "1", Fields: fields, Analyzers: analyzers,
		Rebuild: func(_ context.Context, w index.RebuildWriter) error {
			for id, d := range caseDocs {
				if err := w.Put(id, d); err != nil {
					return err
				}
			}
			return nil
		},
		Document: func(_ context.Context, id string) (index.Doc, bool, error) {
			d, ok := caseDocs[id]
			return d, ok, nil
		},
	}
}

func arkhamStore(t *testing.T) *coretest.Store {
	t.Helper()
	return coretest.NewStore(t,
		storage.OwnerSpec{Owner: arkham, Enabled: true, Spec: storage.Spec{
			Keyspace: true,
			Derived:  []storage.DerivedIndex{{Name: cases, Version: "1"}},
		}},
		coretest.IndexOwner(),
	)
}

// openCases opens and fully rebuilds the cases index of a fresh arkham store.
func openCases(t *testing.T, o coretest.IndexEngineOptions, s index.Spec) (*coretest.IndexEngine, index.Index) {
	t.Helper()
	x := coretest.NewIndexEngine(t, arkhamStore(t), o)
	ix, err := x.Opener(arkham).Open(t.Context(), s)
	if err != nil {
		t.Fatal(err)
	}
	if n, err := x.Rebuild(t.Context(), arkham, cases); err != nil || n != int64(len(caseDocs)) {
		t.Fatalf("rebuild = %d, %v; want %d documents", n, err, len(caseDocs))
	}
	if st := ix.State(); st != index.Ready {
		t.Fatalf("state after rebuild = %s, want ready", st)
	}
	return x, ix
}

func hitIDs(t *testing.T, ix index.Index, q index.Query, sortBy ...string) []string {
	t.Helper()
	res, err := ix.Search(t.Context(), q, index.Page{Size: 50, SortBy: sortBy})
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	out := []string{}
	for _, h := range res.Hits {
		out = append(out, h.ID)
	}
	return out
}

func TestSpecAnalyzerAndKeywordLowercase(t *testing.T) {
	_, ix := openCases(t, coretest.IndexEngineOptions{}, caseSpec(caseFields(true), bookish))
	for name, tc := range map[string]struct {
		q    index.Query
		want []string
	}{
		// Porter stemming: "families" and "family" share the stem "famili".
		"stemmed match":         {index.Match("title", "families"), []string{"joker", "todd"}},
		"standard has no stems": {index.Match("plain", "families"), []string{}},
		// Stop words kept: the phrase holds "in the" in place, so "Death of
		// the Family" is not a match. Under the standard analyzer the stop
		// words drop out, the phrase becomes "death _ _ family", and both
		// titles match: the analyzer is what tells them apart.
		"stopword phrase":          {index.QueryString(`title:"death in the family"`), []string{"todd"}},
		"standard phrase is loose": {index.QueryString(`plain:"death in the family"`), []string{"joker", "todd"}},
		"leading stopword phrase":  {index.QueryString(`title:"a death"`), []string{"todd"}},
		// '_' maps to a space before tokenising.
		"underscore split":           {index.Match("title", "year"), []string{"wayne"}},
		"standard keeps underscores": {index.Match("plain", "year"), []string{}},
		"possessive stripped":        {index.Match("author", "Joker's"), []string{"joker"}},
		// keyword_lowercase is case-insensitive, whole-value.
		"keyword lowercase term":       {index.Term("format", "mp3"), []string{"todd", "wayne"}},
		"keyword lowercase term upper": {index.Term("format", "Mp3"), []string{"todd", "wayne"}},
		"keyword lowercase match":      {index.Match("format", "M4B"), []string{"joker"}},
		"keyword lowercase prefix":     {index.Prefix("format", "M"), []string{"joker", "todd", "wayne"}},
		"keyword lowercase is whole":   {index.Term("format", "mp"), []string{}},
		// keyword stays case-sensitive.
		"keyword exact case":   {index.Term("alias", "Clown"), []string{"joker"}},
		"keyword other case":   {index.Term("alias", "clown"), []string{}},
		"text term is folded":  {index.Term("title", "Hush"), []string{"desc"}},
		"text prefix folded":   {index.Prefix("title", "KNIGHT"), []string{"byline"}},
		"spec analyzer term":   {index.Term("title", "famili"), []string{"joker", "todd"}},
		"unfielded is _all":    {index.QueryString("hush"), []string{"desc"}},
		"_all uses standard":   {index.QueryString("families"), []string{}},
		"_all finds the stems": {index.QueryString("famili"), []string{"joker", "todd"}},
	} {
		t.Run(name, func(t *testing.T) {
			got := hitIDs(t, ix, tc.q, "_id")
			if !slices.Equal(got, tc.want) {
				t.Fatalf("hits = %v, want %v", got, tc.want)
			}
		})
	}
	facets, err := ix.Facets(t.Context(), index.Prefix("format", "m"), []string{"format"}, 5)
	if err != nil {
		t.Fatal(err)
	}
	if got := facets["format"]; len(got) != 2 || got[0] != (index.FacetValue{Term: "mp3", Count: 2}) || got[1] != (index.FacetValue{Term: "m4b", Count: 1}) {
		t.Fatalf("format facets = %v, want the lowercased terms mp3:2, m4b:1", got)
	}
}

// TestFieldBoostOrdersAnOrOfMatches: "gotham" is the whole description of
// desc, two words of byline's author and three of heading's title. Unweighted,
// the shortest field scores highest (desc, byline, heading); with the
// production weights (title 3, author 2, description 0.5) the order turns.
func TestFieldBoostOrdersAnOrOfMatches(t *testing.T) {
	q := index.Or(index.Match("title", "gotham"), index.Match("author", "gotham"), index.Match("description", "gotham"))
	_, flat := openCases(t, coretest.IndexEngineOptions{}, caseSpec(caseFields(false), bookish))
	if got, want := hitIDs(t, flat, q), []string{"desc", "byline", "heading"}; !slices.Equal(got, want) {
		t.Fatalf("unweighted order = %v, want %v (the fixture no longer discriminates)", got, want)
	}
	_, weighted := openCases(t, coretest.IndexEngineOptions{}, caseSpec(caseFields(true), bookish))
	if got, want := hitIDs(t, weighted, q), []string{"heading", "byline", "desc"}; !slices.Equal(got, want) {
		t.Fatalf("weighted order = %v, want %v", got, want)
	}
	// The boost weights Match only: a Prefix keeps its own score.
	pq := index.Or(index.Prefix("title", "gotham"), index.Prefix("description", "gotham"))
	if a, b := hitIDs(t, flat, pq), hitIDs(t, weighted, pq); !slices.Equal(a, b) {
		t.Fatalf("a boost moved a Prefix: unweighted %v, weighted %v", a, b)
	}
}

// TestBoostNodeReplacesTheFieldBoost: index.Boost on a Match replaces the
// field's declared weight, as HEAD's "title:gotham^2" scores 2, not 3 x 2. The
// orders are relative within one Or, since a lone clause's score is
// normalised by its own weight.
func TestBoostNodeReplacesTheFieldBoost(t *testing.T) {
	_, ix := openCases(t, coretest.IndexEngineOptions{}, caseSpec(caseFields(true), bookish))
	author := index.Match("author", "gotham") // weight 2, two words: byline
	for name, tc := range map[string]struct {
		q    index.Query
		want []string
	}{
		"declared weight 3":       {index.Or(index.Match("title", "gotham"), author), []string{"heading", "byline"}},
		"explicit weight 2":       {index.Or(index.Boost(index.Match("title", "gotham"), 2), author), []string{"byline", "heading"}},
		"query string ^2":         {index.Or(index.QueryString("title:gotham^2"), author), []string{"byline", "heading"}},
		"explicit weight 6":       {index.Or(index.Boost(index.Match("title", "gotham"), 6), author), []string{"heading", "byline"}},
		"weights a prefix":        {index.Or(index.Boost(index.Prefix("description", "goth"), 100), author), []string{"desc", "byline"}},
		"weights a group":         {index.Or(index.Boost(index.Or(index.Term("description", "gotham")), 100), author), []string{"desc", "byline"}},
		"weights a query string":  {index.Or(index.Boost(index.QueryString("description:gotham"), 100), author), []string{"desc", "byline"}},
		"weights a wildcard":      {index.Or(index.Boost(index.Wildcard("description", "*tham"), 100), author), []string{"desc", "byline"}},
		"last boost wins":         {index.Or(index.Boost(index.Boost(index.Match("title", "gotham"), 100), 2), author), []string{"byline", "heading"}},
		"weights all (no effect)": {index.Boost(index.All(), 5), []string{"byline", "desc", "heading", "joker", "todd", "wayne"}},
	} {
		t.Run(name, func(t *testing.T) {
			sortBy := []string{}
			if name == "weights all (no effect)" {
				sortBy = []string{"_id"}
			}
			if got := hitIDs(t, ix, tc.q, sortBy...); !slices.Equal(got, tc.want) {
				t.Fatalf("order = %v, want %v", got, tc.want)
			}
		})
	}
	for _, w := range []float64{0, -1} {
		if _, err := ix.Search(t.Context(), index.Boost(index.Match("title", "gotham"), w), index.Page{}); !errors.Is(err, index.ErrInvalidQuery) {
			t.Fatalf("Boost %v: %v, want ErrInvalidQuery", w, err)
		}
	}
}

func TestWildcardLeadingAndBounded(t *testing.T) {
	_, ix := openCases(t, coretest.IndexEngineOptions{}, caseSpec(caseFields(true), bookish))
	for name, tc := range map[string]struct {
		q    index.Query
		want []string
	}{
		"leading":                {index.Wildcard("title", "*mili"), []string{"joker", "todd"}},
		"both ends":              {index.Wildcard("title", "*ea*"), []string{"joker", "todd", "wayne"}}, // death, year
		"single char":            {index.Wildcard("title", "?ush"), []string{"desc"}},
		"trailing":               {index.Wildcard("title", "knight*"), []string{"byline"}},
		"folded on a text field": {index.Wildcard("title", "*MILI"), []string{"joker", "todd"}},
		"unstemmed pattern":      {index.Wildcard("title", "*family"), []string{}}, // the index holds "famili"
		"keyword lowercase":      {index.Wildcard("format", "*3"), []string{"todd", "wayne"}},
		"keyword exact case":     {index.Wildcard("alias", "*own"), []string{"joker"}},
		"keyword case kept":      {index.Wildcard("alias", "*OWN"), []string{}},
	} {
		t.Run(name, func(t *testing.T) {
			if got := hitIDs(t, ix, tc.q, "_id"); !slices.Equal(got, tc.want) {
				t.Fatalf("hits = %v, want %v", got, tc.want)
			}
		})
	}

	refused := map[string]index.Query{
		"no literal":            index.Wildcard("title", "*?*"),
		"empty":                 index.Wildcard("title", ""),
		"unknown field":         index.Wildcard("nope", "*x"),
		"query string leading":  index.QueryString("title:*mili"),
		"query string question": index.QueryString("title:?ush"),
	}
	for name, q := range refused {
		t.Run("refused/"+name, func(t *testing.T) {
			if _, err := ix.Search(t.Context(), q, index.Page{}); !errors.Is(err, index.ErrInvalidQuery) {
				t.Fatalf("Search: %v, want ErrInvalidQuery", err)
			}
		})
	}

	// The node budget: each Wildcard costs 32 of 1024 nodes, so an Or of 31
	// (plus the Or itself) fits and an Or of 32 does not.
	many := func(n int) index.Query {
		qs := make([]index.Query, n)
		for i := range qs {
			qs[i] = index.Wildcard("title", "*mili")
		}
		return index.Or(qs...)
	}
	if _, err := ix.Count(t.Context(), many(31)); err != nil {
		t.Fatalf("31 wildcards: %v", err)
	}
	if _, err := ix.Count(t.Context(), many(32)); !errors.Is(err, index.ErrInvalidQuery) {
		t.Fatalf("32 wildcards: %v, want ErrInvalidQuery (over the node budget)", err)
	}
}

// TestAllAndMatchAnyField: the match-all listing and the unfielded composite
// match (T6's MatchAllQuery stand-in and its unfielded fan-out).
func TestAllAndMatchAnyField(t *testing.T) {
	_, ix := openCases(t, coretest.IndexEngineOptions{}, caseSpec(caseFields(true), bookish))
	all := []string{"byline", "desc", "heading", "joker", "todd", "wayne"}
	for name, tc := range map[string]struct {
		q    index.Query
		want []string
	}{
		"all":                       {index.All(), all},
		"all and a term":            {index.And(index.All(), index.Term("format", "flac")), []string{"byline", "desc", "heading"}},
		"not of all":                {index.Not(index.All()), []string{}},
		"any field":                 {index.MatchAnyField("hush"), []string{"desc"}},
		"any field keyword":         {index.MatchAnyField("M4B"), []string{"joker"}},
		"any field author":          {index.MatchAnyField("gotham"), []string{"byline", "desc", "heading"}},
		"any field is standard":     {index.MatchAnyField("families"), []string{}},
		"any field sees stems":      {index.MatchAnyField("famili"), []string{"joker", "todd"}},
		"any field sees plain text": {index.MatchAnyField("family"), []string{"joker", "todd"}}, // plain's unstemmed term
	} {
		t.Run(name, func(t *testing.T) {
			if got := hitIDs(t, ix, tc.q, "_id"); !slices.Equal(got, tc.want) {
				t.Fatalf("hits = %v, want %v", got, tc.want)
			}
		})
	}
	if n, err := ix.Count(t.Context(), index.All()); err != nil || n != uint64(len(caseDocs)) {
		t.Fatalf("Count(All) = %d, %v; want %d", n, err, len(caseDocs))
	}
	// All costs a Not's 64 nodes: 15 of them under one Or fit, 16 do not.
	alls := func(n int) index.Query {
		qs := make([]index.Query, n)
		for i := range qs {
			qs[i] = index.All()
		}
		return index.Or(qs...)
	}
	if _, err := ix.Count(t.Context(), alls(15)); err != nil {
		t.Fatalf("15 Alls: %v", err)
	}
	if _, err := ix.Count(t.Context(), alls(16)); !errors.Is(err, index.ErrInvalidQuery) {
		t.Fatalf("16 Alls: %v, want ErrInvalidQuery (over the node budget)", err)
	}
}

// TestTooBroadIsTyped: a wildcard (or prefix) that expands past the engine's
// term-searcher cap fails with ErrTooBroad, in Search, Count and Facets, and
// one that stays under it answers. It also pins the bleve error text
// searchErr recognises.
func TestTooBroadIsTyped(t *testing.T) {
	_, ix := openCases(t, coretest.IndexEngineOptions{MaxTermSearchers: 2}, caseSpec(caseFields(true), bookish))
	wide := index.Wildcard("title", "*a*") // death, family, batman, year, gaslight, ...
	if _, err := ix.Search(t.Context(), wide, index.Page{}); !errors.Is(err, index.ErrTooBroad) {
		t.Fatalf("Search: %v, want ErrTooBroad", err)
	}
	if _, err := ix.Count(t.Context(), wide); !errors.Is(err, index.ErrTooBroad) {
		t.Fatalf("Count: %v, want ErrTooBroad", err)
	}
	if _, err := ix.Facets(t.Context(), wide, []string{"format"}, 3); !errors.Is(err, index.ErrTooBroad) {
		t.Fatalf("Facets: %v, want ErrTooBroad", err)
	}
	if _, err := ix.Count(t.Context(), index.Prefix("title", "")); !errors.Is(err, index.ErrTooBroad) {
		t.Fatalf("Count of an empty prefix: %v, want ErrTooBroad", err)
	}
	if got := hitIDs(t, ix, index.Wildcard("title", "*mili"), "_id"); !slices.Equal(got, []string{"joker", "todd"}) {
		t.Fatalf("a narrow wildcard under the cap = %v", got)
	}
}

// TestAnalyzerEditRecreatesAndBoostEditDoesNot: the mapping fingerprint holds
// the Spec's analyzers, so editing one under the same MappingVersion drops the
// index and queues a rebuild; Field.Boost is query-time and reopens Ready.
func TestAnalyzerEditRecreatesAndBoostEditDoesNot(t *testing.T) {
	st := arkhamStore(t)
	dir := t.TempDir()
	open := func(s index.Spec) (*coretest.IndexEngine, index.Index) {
		t.Helper()
		x := coretest.NewIndexEngine(t, st, coretest.IndexEngineOptions{Dir: dir})
		ix, err := x.Opener(arkham).Open(t.Context(), s)
		if err != nil {
			t.Fatal(err)
		}
		return x, ix
	}
	x, ix := open(caseSpec(caseFields(true), bookish))
	if _, err := x.Rebuild(t.Context(), arkham, cases); err != nil {
		t.Fatal(err)
	}
	if ix.State() != index.Ready {
		t.Fatalf("state = %s, want ready", ix.State())
	}
	if err := x.Close(); err != nil {
		t.Fatal(err)
	}

	x, ix = open(caseSpec(caseFields(false), bookish)) // boosts only
	if ix.State() != index.Ready || len(x.PendingRebuilds()) != 0 {
		t.Fatalf("after a boost edit: state %s, pending %v; want ready and nothing pending", ix.State(), x.PendingRebuilds())
	}
	if err := x.Close(); err != nil {
		t.Fatal(err)
	}

	stemless := bookish
	stemless.TokenFilters = []index.TokenFilter{index.FilterEnglishPossessive, index.FilterLowercase}
	x, ix = open(caseSpec(caseFields(false), stemless))
	if ix.State() != index.Rebuilding || !slices.Equal(x.PendingRebuilds(), []string{"arkham/cases"}) {
		t.Fatalf("after an analyzer edit: state %s, pending %v; want rebuilding and arkham/cases pending", ix.State(), x.PendingRebuilds())
	}
	if n, err := ix.Count(t.Context(), index.Match("title", "hush")); err != nil || n != 0 {
		t.Fatalf("the recreated index holds %d documents (%v), want 0 before its rebuild", n, err)
	}
	if _, err := x.Rebuild(t.Context(), arkham, cases); err != nil {
		t.Fatal(err)
	}
	if got := hitIDs(t, ix, index.Match("title", "families"), "_id"); !slices.Equal(got, []string{}) {
		t.Fatalf("without the stemmer, families = %v, want no hit", got)
	}
	if got := x.Path(arkham, cases); !strings.HasPrefix(got, dir+string(filepath.Separator)) {
		t.Fatalf("Path = %q, want it under %q", got, dir)
	}
}

func TestIndexEngineNeedsTheIndexOwner(t *testing.T) {
	st := coretest.NewStore(t, storage.OwnerSpec{Owner: arkham, Enabled: true, Spec: storage.Spec{Keyspace: true}})
	ft := &fatalRecorder{TB: t}
	func() {
		defer func() { _ = recover() }()
		coretest.NewIndexEngine(ft, st, coretest.IndexEngineOptions{})
	}()
	if !ft.failed {
		t.Fatal("NewIndexEngine on a store without c/index/ did not fail the test")
	}
}

// fatalRecorder records Fatalf instead of ending the test.
type fatalRecorder struct {
	testing.TB
	failed bool
}

func (f *fatalRecorder) Fatalf(string, ...any) { f.failed = true; panic("fatal") }
func (f *fatalRecorder) Helper()               {}
