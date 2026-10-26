// file: internal/search/tag_terms_test.go
// version: 1.0.0
// guid: 7a3d9c52-1e84-4b06-8f27-c5d0e9b4a361
// last-edited: 2026-10-26

package search

import (
	"errors"
	"slices"
	"sort"
	"testing"
	"time"
)

// TestSplitTagTerms_PullsPlainConjunctsOut pins which tag: terms become exact
// filters: those that are a conjunct of the whole query, on the top level or in a
// group that is only ANDed. The rest of the query keeps its other terms.
func TestSplitTagTerms_PullsPlainConjunctsOut(t *testing.T) {
	for _, tc := range []struct {
		name     string
		query    string
		wantRest string
		wantTags []string
	}{
		{"alone", "tag:noir", "", []string{"noir"}},
		{"quoted, with a hyphen", `tag:"to-read"`, "", []string{"to-read"}},
		{"quoted, with a space", `tag:"dark knight"`, "", []string{"dark knight"}},
		{"with a field", "tag:noir title:Hush", "title:Hush", []string{"noir"}},
		{"with a per-user term", "read_status:finished tag:noir", "read_status:finished", []string{"noir"}},
		{"explicit AND", "read_status:finished AND tag:noir", "read_status:finished", []string{"noir"}},
		{"&&", "read_status:finished && tag:gotham", "read_status:finished", []string{"gotham"}},
		{"two tags, order kept", `tag:noir tag:"dark-knight"`, "", []string{"noir", "dark-knight"}},
		{"in an AND-only group", "(read_status:finished tag:noir)", "read_status:finished", []string{"noir"}},
		{"groups nest", "title:Hush (author:kyle (tag:noir))", "(AND title:Hush author:kyle)", []string{"noir"}},
		{"value is exactly as written", "tag:Noir", "", []string{"Noir"}},
		{"no tag term at all", "title:Hush author:kyle", "(AND title:Hush author:kyle)", nil},
		{"another field that starts with tag", "tags:noir", "tags:noir", nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rest, tags, err := SplitTagTerms(mustParse(t, tc.query))
			if err != nil {
				t.Fatalf("%q: %v", tc.query, err)
			}
			gotRest := ""
			if rest != nil {
				gotRest = rest.String()
			}
			if gotRest != tc.wantRest {
				t.Errorf("%q: rest = %q, want %q", tc.query, gotRest, tc.wantRest)
			}
			if !slices.Equal(tags, tc.wantTags) {
				t.Errorf("%q: tags = %v, want %v", tc.query, tags, tc.wantTags)
			}
		})
	}
	if rest, tags, err := SplitTagTerms(nil); rest != nil || tags != nil || err != nil {
		t.Fatalf("nil node: %v %v %v", rest, tags, err)
	}
}

// TestSplitTagTerms_RefusesWhatItCannotApplyExactly: a tag: term under an OR, an
// alternation or a NOT depends on the rest of the boolean tree, so it cannot be
// applied on its own. It is refused with a message, never answered as a word
// match (and never, negated, as the whole library).
func TestSplitTagTerms_RefusesWhatItCannotApplyExactly(t *testing.T) {
	for _, q := range []string{
		"tag:noir OR tag:gotham",
		"tag:noir || author:kyle",
		"author:kyle OR tag:noir",
		"read_status:finished (author:kyle OR tag:noir)",
		"tag:(noir|gotham)",
		"read_status:finished tag:(noir|gotham)",
		"-tag:noir",
		"NOT tag:noir",
		`-tag:"it"`,
		"read_status:finished -tag:noir",
		"-(tag:noir author:kyle)",
		"NOT (title:Hush tag:noir)",
		"title:Hush (author:kyle OR (tag:noir tag:gotham))",
	} {
		t.Run(q, func(t *testing.T) {
			_, _, err := SplitTagTerms(mustParse(t, q))
			if !IsTagTermError(err) {
				t.Fatalf("%q: want a tag term error, got %v", q, err)
			}
			if !IsQueryTermError(err) || QueryTermReason(err) == "" {
				t.Fatalf("%q: the refusal is not a query-term error with a reason: %v", q, err)
			}
			if got := QueryTermReason(err); got != tagShapeReason {
				t.Fatalf("%q: reason = %q", q, got)
			}
		})
	}
}

// TestSplitTagTerms_RefusesAValueThatIsNotOneWholeTag: tag:read* would be a
// prefix, tag:noir~ fuzzy, tag:>a a comparison; none is "that tag exactly".
func TestSplitTagTerms_RefusesAValueThatIsNotOneWholeTag(t *testing.T) {
	for _, q := range []string{
		"tag:read*", "tag:*read", "tag:noir~", "tag:noir^3", "tag:>a", "tag:<=a", `tag:""`,
		"tag:[a TO c]",
	} {
		t.Run(q, func(t *testing.T) {
			_, _, err := SplitTagTerms(mustParse(t, q))
			if !IsTagTermError(err) {
				t.Fatalf("%q: want a tag term error, got %v", q, err)
			}
		})
	}
	// Quoted, the same characters are part of the name.
	if _, tags, err := SplitTagTerms(mustParse(t, `tag:"c++*"`)); err != nil || !slices.Equal(tags, []string{"c++*"}) {
		t.Fatalf("quoted: %v %v", tags, err)
	}
}

// TestTranslateAt_NeverHandsATagTermToTheIndex: a caller that translates without
// splitting gets a refusal. The index would match the term by word and, negated,
// match the whole library.
func TestTranslateAt_NeverHandsATagTermToTheIndex(t *testing.T) {
	for _, q := range []string{"tag:noir", "title:Hush tag:noir", "-tag:noir", "tag:(noir|gotham)"} {
		_, _, err := TranslateAt(mustParse(t, q), time.Now())
		if !IsTagTermError(err) {
			t.Errorf("%q: want a tag term error from the translator, got %v", q, err)
		}
	}
}

func TestTranslateQuery_SplitsTheTagsOff(t *testing.T) {
	tr, err := TranslateQuery(mustParse(t, `read_status:finished AND tag:"dark-knight" title:Hush`), time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(tr.Tags, []string{"dark-knight"}) {
		t.Fatalf("tags = %v", tr.Tags)
	}
	if len(tr.PerUser) != 1 || tr.PerUser[0].Node.Field != "read_status" {
		t.Fatalf("per-user = %+v", tr.PerUser)
	}
	if tr.Query == nil {
		t.Fatal("no index query")
	}
	if _, err := TranslateQuery(mustParse(t, "tag:a OR tag:b"), time.Now()); !IsTagTermError(err) {
		t.Fatalf("grouped: %v", err)
	}
}

func TestCheckQueryTerms_RunsBothChecks(t *testing.T) {
	now := time.Now()
	if err := CheckQueryTerms(mustParse(t, "read_status:finished tag:noir"), now); err != nil {
		t.Fatalf("plain: %v", err)
	}
	if err := CheckQueryTerms(mustParse(t, "read_status:finished OR title:Hush"), now); !IsPerUserQueryError(err) || !IsQueryTermError(err) {
		t.Fatalf("per-user: %v", err)
	}
	if err := CheckQueryTerms(mustParse(t, "title:Hush OR tag:noir"), now); !IsTagTermError(err) || IsPerUserQueryError(err) {
		t.Fatalf("tag: %v", err)
	}
	if IsQueryTermError(errors.New("other")) || IsQueryTermError(nil) {
		t.Fatal("an unrelated error is not a query term error")
	}
}

// tagLister is a fake store: tag -> book ids, folded the way GetBooksByTag folds.
type tagLister map[string][]string

func (l tagLister) GetBooksByTag(tag string) ([]string, error) { return l[normTag(tag)], nil }

func normTag(s string) string {
	b := []byte(s)
	for i, c := range b {
		if c >= 'A' && c <= 'Z' {
			b[i] = c + 32
		}
	}
	return string(b)
}

func TestResolveTagIDs_IsTheIntersection(t *testing.T) {
	l := tagLister{
		"noir":    {"b3", "b1", "b2"},
		"gotham":  {"b2", "b3", "b4"},
		"to-read": {"b9"},
	}
	for _, tc := range []struct {
		tags []string
		want []string
	}{
		{[]string{"noir"}, []string{"b1", "b2", "b3"}},
		{[]string{"Noir"}, []string{"b1", "b2", "b3"}},
		{[]string{"noir", "gotham"}, []string{"b2", "b3"}},
		{[]string{"noir", "to-read"}, []string{}},
		{[]string{"read"}, []string{}},
		{[]string{"nobody"}, []string{}},
	} {
		got, err := ResolveTagIDs(l, tc.tags)
		if err != nil {
			t.Fatal(err)
		}
		sort.Strings(got)
		if !slices.Equal(got, tc.want) || got == nil {
			t.Errorf("%v: got %v, want %v", tc.tags, got, tc.want)
		}
	}
	if _, err := ResolveTagIDs(l, nil); err == nil {
		t.Fatal("no tags must be an error, not every book")
	}
}

// TestTagsIndexFieldMatchesByWord_WhichIsWhyTagIsNotAnIndexTerm documents the
// behaviour that makes tag: an exact filter applied outside the index. If the tags
// field is ever mapped as a real keyword (bookMappingVersion bump + reindex) this
// test starts failing and the exact filter can move back into the index.
func TestTagsIndexFieldMatchesByWord_WhichIsWhyTagIsNotAnIndexTerm(t *testing.T) {
	idx := openTestIndex(t)
	for _, d := range []BookDocument{
		{BookID: "b1", Title: "Hush", Tags: []string{"to-read"}},
		{BookID: "b2", Title: "Year One", Tags: []string{"read"}},
		{BookID: "b3", Title: "Knightfall", Tags: []string{"dark-knight"}},
		{BookID: "b4", Title: "Beyond", Tags: []string{"knight"}},
	} {
		if err := idx.IndexBook(d); err != nil {
			t.Fatal(err)
		}
	}
	for _, tc := range []struct {
		q    string
		want []string
	}{
		{`tags:"to-read"`, []string{"b1", "b2"}},
		{`tags:"knight"`, []string{"b3", "b4"}},
	} {
		bq, _, err := TranslateAt(mustParse(t, tc.q), time.Now())
		if err != nil {
			t.Fatal(err)
		}
		hits, _, err := idx.SearchNative(bq, 0, 100)
		if err != nil {
			t.Fatal(err)
		}
		got := hitIDs(hits)
		sort.Strings(got)
		if !slices.Equal(got, tc.want) {
			t.Errorf("%s: the index answered %v, want the by-word answer %v", tc.q, got, tc.want)
		}
	}
}
