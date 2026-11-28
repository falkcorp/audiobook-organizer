// file: internal/books/searchindex/native_test.go
// version: 0.1.0
// guid: 9d3b6f20-7a14-4c58-b2e9-1f5c8a0d4e63
// last-edited: 2026-11-28

package searchindex_test

import (
	"errors"
	"fmt"
	"reflect"
	"slices"
	"testing"

	"github.com/falkcorp/mediacore/coretest"
	"github.com/falkcorp/mediacore/storage/index"

	"github.com/falkcorp/audiobook-organizer/internal/books/searchindex"
	"github.com/falkcorp/audiobook-organizer/internal/books/searchindex/searchindextest"
	database "github.com/falkcorp/audiobook-organizer/internal/books/store"
	"github.com/falkcorp/audiobook-organizer/internal/search"
)

// These tests pin the F7-09 T6 fold switches (seams.d/F7-09-T6-core.md, "What
// T6 should switch to at the fold") on core's real engine: the analyzer, the
// weights carried as index.Boost, the match-all, wildcards and prefixes as AST
// nodes, and the ErrTooBroad answer.

func ids(t *testing.T, ix index.Index, text string) []string {
	t.Helper()
	tr, err := searchindex.Translate(text, now)
	if err != nil {
		t.Fatalf("translate %q: %v", text, err)
	}
	q := tr.Query
	if tr.MatchAll {
		q = searchindex.MatchAllQuery()
	}
	got, _ := searchindextest.Search(t, ix, q, "_id")
	slices.Sort(got)
	return got
}

func node(t *testing.T, text string) index.Node {
	t.Helper()
	tr, err := searchindex.Translate(text, now)
	if err != nil {
		t.Fatalf("translate %q: %v", text, err)
	}
	n, err := index.Describe(tr.Query)
	if err != nil {
		t.Fatalf("describe %q: %v", text, err)
	}
	return n
}

// TestAnalyzerIsHeadsBookEnNostopOnEveryFreeTextField pins item 1: the analyzer
// is declared exactly as HEAD's, and every free-text field names it.
func TestAnalyzerIsHeadsBookEnNostopOnEveryFreeTextField(t *testing.T) {
	want := []index.Analyzer{{
		Name:         "book_en_nostop",
		CharFilters:  []index.CharFilter{index.CharUnderscoreToSpace},
		Tokenizer:    index.TokenizeUnicodeWords,
		TokenFilters: []index.TokenFilter{index.FilterEnglishPossessive, index.FilterLowercase, index.FilterPorterStem},
	}}
	if got := searchindex.Analyzers(); !reflect.DeepEqual(got, want) {
		t.Errorf("Analyzers() = %+v\nwant %+v", got, want)
	}
	spec := searchindex.NewSpec(library(1), searchindex.Options{})
	if !reflect.DeepEqual(spec.Analyzers, want) {
		t.Errorf("the Spec does not declare the analyzer: %+v", spec.Analyzers)
	}
	if err := spec.Validate(); err != nil {
		t.Fatal(err)
	}
	byName := map[string]index.Field{}
	for _, f := range spec.Fields {
		byName[f.Name] = f
	}
	for _, n := range []string{"title", "author", "narrator", "series", "publisher", "description", "file_path"} {
		f, ok := byName[n]
		if !ok || f.Kind != index.Text || f.Analyzer != "book_en_nostop" {
			t.Errorf("free-text field %q = %+v, want a Text field on book_en_nostop (no fallback to the stock analyzer)", n, f)
		}
	}
}

// TestFreeTextFieldsAnalyseLikeHead shows the analyzer at work on each of the
// seven free-text fields: the stem of "families" is the stem of "family", so a
// match on the stored word "Families" answers a query for "family" only where
// the field really uses book_en_nostop (the standard analyzer does not stem).
func TestFreeTextFieldsAnalyseLikeHead(t *testing.T) {
	families := func() *string { return new("Families") }
	src := &searchindextest.FakeSource{
		Authors: map[int]*database.Author{1: {ID: 1, Name: "Families"}},
		Series:  map[int]*database.Series{1: {ID: 1, Name: "Families"}},
		Books: []database.Book{
			{ID: "t", Title: "Families"},
			{ID: "a", Title: "x", AuthorID: new(1)},
			{ID: "s", Title: "x", SeriesID: new(1)},
			{ID: "n", Title: "x", Narrator: families()},
			{ID: "p", Title: "x", Publisher: families()},
			{ID: "d", Title: "x", Description: families()},
			{ID: "f", Title: "x", FilePath: "/lib/Families/part1.mp3"},
		},
	}
	e := searchindextest.OpenEngine(t, src, searchindex.Options{})
	e.Rebuild(t)
	for field, id := range map[string]string{
		"title": "t", "author": "a", "series": "s", "narrator": "n", "publisher": "p", "description": "d", "file_path": "f",
	} {
		if got := ids(t, e.Index, field+":family"); !slices.Equal(got, []string{id}) {
			t.Errorf("%s:family = %v, want [%s] (stemmed under book_en_nostop)", field, got, id)
		}
	}
}

// TestUnderscoreIsAWordBreak pins the char filter: "Arcane_Chef_2" is words.
func TestUnderscoreIsAWordBreak(t *testing.T) {
	src := &searchindextest.FakeSource{Books: []database.Book{
		{ID: "u", Title: "Arcane_Chef_2"}, {ID: "v", Title: "Arcanechef"},
	}}
	e := searchindextest.OpenEngine(t, src, searchindex.Options{})
	e.Rebuild(t)
	if got := ids(t, e.Index, "title:chef"); !slices.Equal(got, []string{"u"}) {
		t.Errorf("title:chef = %v, want [u]", got)
	}
}

// TestKeywordFieldsKeepHeadsStandardAnalysis pins ruling 1 (parity): HEAD's
// "keyword" fields match by word, lowercased, not by whole value.
func TestKeywordFieldsKeepHeadsStandardAnalysis(t *testing.T) {
	spec := searchindex.NewSpec(library(1), searchindex.Options{})
	byName := map[string]index.Field{}
	for _, f := range spec.Fields {
		byName[f.Name] = f
	}
	for _, n := range []string{"tags", "format", "genre", "language", "library_state", "isbn10", "isbn13", "asin"} {
		f := byName[n]
		if f.Kind != index.Keyword || f.Analyzer != index.AnalyzerStandard {
			t.Errorf("keyword field %q = %+v, want Kind Keyword on the standard analyzer", n, f)
		}
	}
	src := &searchindextest.FakeSource{Books: []database.Book{
		{ID: "g", Title: "x", Genre: new("Epic Fantasy")},
		{ID: "h", Title: "x", Genre: new("Noir")},
	}}
	e := searchindextest.OpenEngine(t, src, searchindex.Options{})
	e.Rebuild(t)
	// A real keyword (keyword_lowercase) would stop genre:fantasy matching
	// "Epic Fantasy": the word is a term of its own only under standard.
	if got := ids(t, e.Index, "genre:fantasy"); !slices.Equal(got, []string{"g"}) {
		t.Errorf("genre:fantasy = %v, want [g] (HEAD matches by word)", got)
	}
	if got := ids(t, e.Index, "genre:NOIR"); !slices.Equal(got, []string{"h"}) {
		t.Errorf("genre:NOIR = %v, want [h] (lowercased)", got)
	}
}

// TestNoFieldDeclaresABoost pins item 2: the parity fold declares no
// Field.Boost; every weight is an index.Boost on a node.
func TestNoFieldDeclaresABoost(t *testing.T) {
	for _, f := range searchindex.NewSpec(library(1), searchindex.Options{}).Fields {
		if f.Boost != 0 {
			t.Errorf("field %q declares Boost %v; weights belong to the query (index.Boost)", f.Name, f.Boost)
		}
	}
}

// TestFreeTextIsAnOrOfBoostedMatchesAndAnUnweightedMatchAnyField pins the shape
// item 2 names, with HEAD's weights in HEAD's order.
func TestFreeTextIsAnOrOfBoostedMatchesAndAnUnweightedMatchAnyField(t *testing.T) {
	n := node(t, "gotham")
	if n.Op != index.OpOr || len(n.Children) != 8 {
		t.Fatalf("free text = op %v with %d children, want an Or of 8", n.Op, len(n.Children))
	}
	for i, w := range []struct {
		field  string
		weight float64
	}{
		// publisher's weight is 1, the default: no Boost node, the same score.
		{"title", 3}, {"author", 2}, {"series", 1.5}, {"narrator", 1.2}, {"publisher", 0}, {"description", 0.5}, {"file_path", 0.5},
	} {
		c := n.Children[i]
		if c.Op != index.OpMatch || c.Field != w.field || c.Text != "gotham" || c.Boost != w.weight {
			t.Errorf("child %d = %+v, want Boost(Match(%s, gotham), %v)", i, c, w.field, w.weight)
		}
	}
	if c := n.Children[7]; c.Op != index.OpMatchAny || c.Text != "gotham" || c.Boost != 0 {
		t.Errorf("last child = %+v, want an unweighted MatchAnyField(gotham)", c)
	}
	// A fielded term HEAD does not weight stays at 1, and a user's ^n is carried.
	if n := node(t, "title:gotham"); n.Op != index.OpMatch || n.Boost != 0 {
		t.Errorf("title:gotham = %+v, want an unweighted Match", n)
	}
	if n := node(t, "title:gotham^4"); n.Op != index.OpMatch || n.Boost != 4 {
		t.Errorf("title:gotham^4 = %+v, want Boost(Match, 4)", n)
	}
}

// TestFieldedOrIsNotWeightedByTheField shows the arithmetic of ruling 2: a
// fielded "title:x OR author:y" scores both sides alike, as in HEAD.
func TestFieldedOrIsNotWeightedByTheField(t *testing.T) {
	src := &searchindextest.FakeSource{
		Authors: map[int]*database.Author{1: {ID: 1, Name: "Joker"}},
		Books: []database.Book{
			{ID: "a", Title: "Zero Year", AuthorID: new(1)}, // author side
			{ID: "b", Title: "Joker"},                       // title side
		},
	}
	e := searchindextest.OpenEngine(t, src, searchindex.Options{})
	e.Rebuild(t)
	tr, err := searchindex.Translate("title:joker OR author:joker", now)
	if err != nil {
		t.Fatal(err)
	}
	res, err := e.Index.Search(t.Context(), tr.Query, index.Page{Size: 10})
	if err != nil || len(res.Hits) != 2 {
		t.Fatalf("hits = %+v, %v", res.Hits, err)
	}
	if res.Hits[0].Score != res.Hits[1].Score {
		t.Errorf("scores %v and %v differ: a declared Field.Boost weights the title side", res.Hits[0].Score, res.Hits[1].Score)
	}
}

// rankCorpus has one book per weighted free-text field holding "gotham", plus a
// book that only the unweighted _all child (a genre) can reach.
func rankCorpus() *searchindextest.FakeSource {
	return &searchindextest.FakeSource{
		Authors: map[int]*database.Author{1: {ID: 1, Name: "Gotham Gazette"}},
		Series:  map[int]*database.Series{1: {ID: 1, Name: "Gotham Central"}},
		Books: []database.Book{
			{ID: "r1", Title: "Gotham Nights"},
			{ID: "r2", Title: "Year Two", AuthorID: new(1)},
			{ID: "r3", Title: "Year Three", SeriesID: new(1)},
			{ID: "r4", Title: "Year Four", Narrator: new("Gotham Voice")},
			{ID: "r5", Title: "Year Five", Publisher: new("Gotham Press")},
			{ID: "r6", Title: "Year Six", Description: new("A tale of Gotham")},
			{ID: "r7", Title: "Year Seven", FilePath: "/lib/gotham/part1.mp3"},
			{ID: "r8", Title: "Year Eight", Genre: new("Gotham")},
		},
	}
}

// headRanking is the order HEAD's own index answers q in, over the same books:
// the oracle the native answers are held to.
func headRanking(t *testing.T, src *searchindextest.FakeSource, text string) []string {
	t.Helper()
	idx, err := search.Open(t.TempDir() + "/bleve")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = idx.Close() })
	rel := &search.BookRelations{Authors: src.Authors, Series: src.Series, Tags: src.Tags}
	for i := range src.Books {
		if err := idx.IndexBook(search.BookToDocWithRelations(&src.Books[i], rel)); err != nil {
			t.Fatal(err)
		}
	}
	node, err := search.ParseQuery(text)
	if err != nil {
		t.Fatal(err)
	}
	tr, err := search.TranslateQuery(node, now)
	if err != nil {
		t.Fatal(err)
	}
	hits, _, err := idx.SearchNativeIDs(tr.Query, 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, h := range hits {
		out = append(out, h.BookID)
	}
	return out
}

// TestRankingMatchesHead holds the native index to HEAD's own answer, hit for
// hit and in order, over a corpus where every weighted field has its own book.
func TestRankingMatchesHead(t *testing.T) {
	src := rankCorpus()
	e := searchindextest.OpenEngine(t, src, searchindex.Options{})
	e.Rebuild(t)
	for _, text := range []string{
		"gotham",
		"gotham^2",
		"title:gotham OR author:gotham",
		"author:gotham^3 OR title:gotham",
		"gotham -author:gotham",
	} {
		t.Run(text, func(t *testing.T) {
			want := headRanking(t, src, text)
			tr, err := searchindex.Translate(text, now)
			if err != nil {
				t.Fatal(err)
			}
			got, _ := searchindextest.Search(t, e.Index, tr.Query)
			if !slices.Equal(got, want) {
				t.Errorf("native order %v, HEAD order %v", got, want)
			}
		})
	}
	// The oracle itself ranks the title book first and the weighted fields
	// before the unweighted genre, so the comparison above has teeth.
	want := headRanking(t, src, "gotham")
	if len(want) != 8 || want[0] != "r1" || want[7] != "r8" {
		t.Errorf("HEAD ranking of gotham = %v, want r1 first and r8 last", want)
	}
}

// TestListingWithNoTermIsAll pins item 3.
func TestListingWithNoTermIsAll(t *testing.T) {
	if n, err := index.Describe(searchindex.MatchAllQuery()); err != nil || n.Op != index.OpAll {
		t.Fatalf("MatchAllQuery = %+v, %v; want index.All()", n, err)
	}
	src := library(7)
	e := searchindextest.OpenEngine(t, src, searchindex.Options{})
	e.Rebuild(t)
	tr, err := searchindex.Translate("  ", now)
	if err != nil || !tr.MatchAll {
		t.Fatalf("a blank search = %+v, %v; want MatchAll", tr, err)
	}
	if got := ids(t, e.Index, ""); len(got) != 7 {
		t.Errorf("listing = %v, want all 7", got)
	}
}

// arcane is a corpus for patterns.
func arcane() *searchindextest.FakeSource {
	return &searchindextest.FakeSource{Books: []database.Book{
		{ID: "k", Title: "Knightfall"},
		{ID: "n", Title: "Nightwing Rising"},
		{ID: "u", Title: "Arcane_Chef_2"},
		{ID: "x", Title: "Arcane Cookery"},
		{ID: "z", Title: "Zero Year"},
	}}
}

// TestPatternsAreWildcardAndPrefixNodes pins item 4: *foo and *foo* are
// index.Wildcard, foo* is index.Prefix, and the engine folds case.
func TestPatternsAreWildcardAndPrefixNodes(t *testing.T) {
	for _, tc := range []struct {
		text    string
		op      index.Op
		pattern string
	}{
		{"title:*tfal", index.OpWildcard, "*tfal"}, // Knightfall is stored as its stem, knightfal
		{"title:*ight*", index.OpWildcard, "*ight*"},
		{"title:night*", index.OpPrefix, "night"},
		{"title:NIGHT*", index.OpPrefix, "night"}, // HEAD lowercases (patternTerm) before the tree
	} {
		n := node(t, tc.text)
		if n.Op != tc.op || n.Field != "title" || n.Text != tc.pattern {
			t.Errorf("%s = %+v, want op %v on title with %q", tc.text, n, tc.op, tc.pattern)
		}
	}
	e := searchindextest.OpenEngine(t, arcane(), searchindex.Options{})
	e.Rebuild(t)
	for text, want := range map[string][]string{
		"title:*tfal":    {"k"},
		"title:*TFAL":    {"k"},
		"title:*ight*":   {"k", "n"},
		"title:night*":   {"n"},
		"title:NIGHT*":   {"n"},
		"title:*ightw*":  {"n"},
		"title:nomatch*": nil,
	} {
		if got := ids(t, e.Index, text); !slices.Equal(got, want) {
			t.Errorf("%s = %v, want %v", text, got, want)
		}
	}
}

// TestUnderscorePatternKeepsBooksSplit pins the books-code split: no char filter
// runs on pattern text, so "arcane_ch*" is an And of a word and a prefix.
func TestUnderscorePatternKeepsBooksSplit(t *testing.T) {
	n := node(t, "title:arcane_ch*")
	if n.Op != index.OpAnd || len(n.Children) != 2 ||
		n.Children[0].Op != index.OpMatch || n.Children[0].Text != "arcane" ||
		n.Children[1].Op != index.OpPrefix || n.Children[1].Text != "ch" {
		t.Fatalf("title:arcane_ch* = %+v, want And(Match(arcane), Prefix(ch))", n)
	}
	e := searchindextest.OpenEngine(t, arcane(), searchindex.Options{})
	e.Rebuild(t)
	if got := ids(t, e.Index, "title:arcane_ch*"); !slices.Equal(got, []string{"u"}) {
		t.Errorf("title:arcane_ch* = %v, want [u] (not Arcane Cookery)", got)
	}
	if got := ids(t, e.Index, "title:*rcan_ch*"); !slices.Equal(got, []string{"u"}) {
		t.Errorf("title:*rcan_ch* = %v, want [u]", got)
	}
}

// TestFuzzyAndPhrasesGoThroughQueryString pins the rest of item 4.
func TestFuzzyAndPhrasesGoThroughQueryString(t *testing.T) {
	if n := node(t, "title:knightfal~"); n.Op != index.OpString || n.Text != "title:knightfal~1" {
		t.Errorf("fuzzy = %+v, want QueryString title:knightfal~1", n)
	}
	if n := node(t, `title:"death in the family"`); n.Op != index.OpString || n.Text != `title:"death in the family"` {
		t.Errorf("phrase = %+v", n)
	}
	src := &searchindextest.FakeSource{
		Authors: map[int]*database.Author{1: {ID: 1, Name: "Lucius Fox"}},
		Books: []database.Book{
			{ID: "todd", Title: "A Death in the Family", AuthorID: new(1)},
			{ID: "joker", Title: "Death of the Family"},
			{ID: "k", Title: "Knightfall"},
		},
	}
	e := searchindextest.OpenEngine(t, src, searchindex.Options{})
	e.Rebuild(t)
	for text, want := range map[string][]string{
		// The phrase keeps its stop words in place under book_en_nostop, so only
		// one title holds "in the"; under the standard analyzer both would match.
		`title:"death in the family"`: {"todd"},
		`"death in the family"`:       {"todd"},
		// An unfielded phrase answers like HEAD's: the stems are compared, so a
		// quoted author name finds its book.
		`"lucius fox"`:     {"todd"},
		"title:knightfal~": {"k"},
		"knightfal~":       {"k"},
	} {
		if got := ids(t, e.Index, text); !slices.Equal(got, want) {
			t.Errorf("%s = %v, want %v", text, got, want)
		}
	}
}

// TestUnfieldedPatternsAnswerLikeHead holds the unfielded prefix and fuzzy
// answers to HEAD's own.
func TestUnfieldedPatternsAnswerLikeHead(t *testing.T) {
	src := &searchindextest.FakeSource{
		Authors: map[int]*database.Author{1: {ID: 1, Name: "Lucius Fox"}},
		Books: []database.Book{
			{ID: "todd", Title: "A Death in the Family", AuthorID: new(1), Genre: new("Noir")},
			{ID: "k", Title: "Knightfall"},
		},
	}
	e := searchindextest.OpenEngine(t, src, searchindex.Options{})
	e.Rebuild(t)
	for _, text := range []string{`"lucius fox"`, "knight*", "KNIGHT*", "knightfal~", "KNIGHTFAL~", "noi*", "lucius", `"death in the family"`} {
		want := headRanking(t, src, text)
		got := ids(t, e.Index, text)
		slices.Sort(want)
		if !slices.Equal(got, want) {
			t.Errorf("%s = %v, HEAD answers %v", text, got, want)
		}
	}
}

// broad has enough distinct "nightN" terms that a wildcard over them is past a
// small term-searcher cap.
func broad(n int) *searchindextest.FakeSource {
	src := &searchindextest.FakeSource{}
	for i := range n {
		src.Books = append(src.Books, database.Book{ID: fmt.Sprintf("b%03d", i), Title: fmt.Sprintf("Night%c%c", 'a'+i%26, 'a'+i/26)})
	}
	return src
}

// TestTooBroadIsANarrowAnswerNotAFallback pins item 5, end to end: the engine
// refuses a wildcard over more terms than its cap, and the answer is the
// 400-class "narrow your search", never the substring fallback a Degraded
// index gets.
func TestTooBroadIsANarrowAnswerNotAFallback(t *testing.T) {
	e := searchindextest.OpenEngine(t, broad(40), searchindex.Options{}, coretest.IndexEngineOptions{MaxTermSearchers: 8})
	e.Rebuild(t)
	tr, err := searchindex.Translate("title:*ight*", now)
	if err != nil {
		t.Fatal(err)
	}
	_, searchErr := e.Index.Search(t.Context(), tr.Query, index.Page{Size: 10})
	if !errors.Is(searchErr, index.ErrTooBroad) {
		t.Fatalf("Search = %v, want index.ErrTooBroad", searchErr)
	}
	if got := searchindex.Dispose(searchErr); got != searchindex.Narrow {
		t.Errorf("Dispose(Search error) = %v, want Narrow", got)
	}
	_, countErr := e.Index.Count(t.Context(), tr.Query)
	if got := searchindex.Dispose(countErr); got != searchindex.Narrow {
		t.Errorf("Dispose(Count error %v) = %v, want Narrow", countErr, got)
	}
	// A narrower query answers.
	if got := ids(t, e.Index, "title:nightaa*"); len(got) == 0 {
		t.Error("a narrower pattern found nothing")
	}
}

func TestDispose(t *testing.T) {
	wrapped := fmt.Errorf("search %q: %w", "title:*a*", index.ErrTooBroad)
	for name, tc := range map[string]struct {
		err  error
		want searchindex.Disposition
	}{
		"too broad":                  {index.ErrTooBroad, searchindex.Narrow},
		"too broad, wrapped":         {wrapped, searchindex.Narrow},
		"too broad joined":           {errors.Join(errors.New("x"), index.ErrTooBroad), searchindex.Narrow},
		"degraded":                   {index.ErrDegraded, searchindex.Fallback},
		"degraded, wrapped":          {fmt.Errorf("open: %w", index.ErrDegraded), searchindex.Fallback},
		"invalid query":              {index.ErrInvalidQuery, searchindex.Failed},
		"closed":                     {index.ErrClosed, searchindex.Failed},
		"anything else":              {errors.New("boom"), searchindex.Failed},
		"both: too broad is a query": {errors.Join(index.ErrDegraded, index.ErrTooBroad), searchindex.Narrow},
	} {
		if got := searchindex.Dispose(tc.err); got != tc.want {
			t.Errorf("%s: Dispose = %v, want %v", name, got, tc.want)
		}
	}
	if searchindex.NarrowMessage == "" {
		t.Error("no narrow message")
	}
}

// bareStar is a corpus with a book that has no text at all, which is the one a
// per-field fan-out of empty prefixes could leave out.
func bareStar() *searchindextest.FakeSource {
	return &searchindextest.FakeSource{
		Authors: map[int]*database.Author{1: {ID: 1, Name: "Lucius Fox"}},
		Books: []database.Book{
			{ID: "g", Title: "Gotham Knights", AuthorID: new(1)},
			{ID: "k", Title: "Knightfall"},
			{ID: "blank"},
		},
	}
}

// TestBareStarIsMatchAll: an unfielded `*` is HEAD's empty prefix on "_all",
// which lists every book. It translates to match-all, not to an Or over the
// fields' empty prefixes, and answers as HEAD does alone and under AND and OR.
func TestBareStarIsMatchAll(t *testing.T) {
	tr, err := searchindex.Translate("*", now)
	if err != nil || !tr.MatchAll || tr.Query != nil {
		t.Fatalf("`*` = %+v, %v; want MatchAll and no query", tr, err)
	}
	src := bareStar()
	e := searchindextest.OpenEngine(t, src, searchindex.Options{})
	e.Rebuild(t)
	for _, text := range []string{"*", "* knight*", "* OR gotham", "gotham OR *"} {
		want := headRanking(t, src, text)
		slices.Sort(want)
		if got := ids(t, e.Index, text); !slices.Equal(got, want) {
			t.Errorf("%s = %v, HEAD answers %v", text, got, want)
		}
	}
	if got := ids(t, e.Index, "*"); len(got) != 3 {
		t.Errorf("`*` = %v, want all 3 books, the blank one included", got)
	}
}

// TestFieldedEmptyPrefixStaysAPrefix: `title:*` is a prefix on one field and is
// left as HEAD builds it; only the unfielded `*` is a match-all.
func TestFieldedEmptyPrefixStaysAPrefix(t *testing.T) {
	n := node(t, "title:*")
	if n.Op != index.OpPrefix || n.Field != "title" || n.Text != "" {
		t.Errorf("title:* = %+v, want Prefix(title, \"\")", n)
	}
	src := bareStar()
	e := searchindextest.OpenEngine(t, src, searchindex.Options{})
	e.Rebuild(t)
	want := headRanking(t, src, "title:*")
	slices.Sort(want)
	if got := ids(t, e.Index, "title:*"); !slices.Equal(got, want) {
		t.Errorf("title:* = %v, HEAD answers %v", got, want)
	}
}
