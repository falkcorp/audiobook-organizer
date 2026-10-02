// file: internal/catalog/rules_test.go
// version: 1.0.0
// guid: 1f8b3d52-6e9a-4c07-b4d1-8a2c5e7f9b30
// last-edited: 2026-10-01

package catalog

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/metadata"
)

func TestParseSequence(t *testing.T) {
	cases := []struct {
		in     string
		lo, hi float64
		ok     bool
	}{
		{"1", 1, 1, true},
		{"2.5", 2.5, 2.5, true},
		{"1-3", 1, 3, true},
		{"1 – 3", 1, 3, true},
		{"Books 1-3", 1, 3, true},
		{"1.5-2", 1.5, 2, true},
		{"", 0, 0, false},
		{"   ", 0, 0, false},
		{"Book 2", 2, 2, true},
		{"III", 3, 3, true},
		{"3-1", 0, 0, false}, // reversed range: no position, never a gap
		{"Prequel", 0, 0, false},
		{"0", 0, 0, true},
		{"10", 10, 10, true},
	}
	for _, c := range cases {
		lo, hi, ok := ParseSequence(c.in)
		if ok != c.ok || lo != c.lo || hi != c.hi {
			t.Errorf("ParseSequence(%q) = %v,%v,%v; want %v,%v,%v", c.in, lo, hi, ok, c.lo, c.hi, c.ok)
		}
	}
}

func TestStripEditionMarkers(t *testing.T) {
	cases := map[string]string{
		"Cage of Souls":                                       "Cage of Souls",
		"Cage of Souls (Unabridged)":                          "Cage of Souls",
		"Cage of Souls [Abridged Edition]":                    "Cage of Souls",
		"Cage of Souls: A Novel":                              "Cage of Souls",
		"Cage of Souls (A Novel)":                             "Cage of Souls",
		"Cage of Souls: Dramatized Adaptation":                "Cage of Souls",
		"Cage of Souls (BBC Radio 4 Full-Cast Dramatisation)": "Cage of Souls",
		"Cage of Souls - Full Cast Audio Drama":               "Cage of Souls",
		"Cage of Souls: A Novel (Unabridged)":                 "Cage of Souls",
		"Cage of Souls: Narrated by Some Reader":              "Cage of Souls",
		// Not edition markers: must survive.
		"A Novel Idea":                 "A Novel Idea",
		"Full Cast of Characters":      "Full Cast of Characters",
		"The Unabridged Life of Trees": "The Unabridged Life of Trees",
		"Dramatized":                   "Dramatized",
	}
	for in, want := range cases {
		if got := StripEditionMarkers(in); got != want {
			t.Errorf("StripEditionMarkers(%q) = %q; want %q", in, got, want)
		}
	}
}

func TestEditionGroupKey(t *testing.T) {
	base := EditionGroupKey("Adrian Tchaikovsky", "B002XLHS8Q", "Cage of Souls")
	if base == "" {
		t.Fatal("empty key for a fully identified product")
	}
	for _, title := range []string{"Cage of Souls (Unabridged)", "Cage of Souls: A Novel", "CAGE OF SOULS", "Cage of Souls: Dramatized Adaptation"} {
		if got := EditionGroupKey("Adrian Tchaikovsky", "B002XLHS8Q", title); got != base {
			t.Errorf("edition %q keyed %q, want %q", title, got, base)
		}
	}
	// Same author, different work: different group.
	if got := EditionGroupKey("Adrian Tchaikovsky", "B002XLHS8Q", "Children of Time"); got == base || got == "" {
		t.Errorf("two works by one author share a group (%q)", got)
	}
	// Different author identity: different group, same title.
	if got := EditionGroupKey("Someone Else", "B000000001", "Cage of Souls"); got == base {
		t.Error("two authors with one title share a group")
	}
	// Author ASIN wins over the spelling of the name.
	if a, b := EditionGroupKey("A. Tchaikovsky", "B002XLHS8Q", "Cage of Souls"), base; a != b {
		t.Errorf("same author ASIN under another spelling keyed %q vs %q", a, b)
	}
	// Name-only identity still groups; it is the fold of the name.
	if got := EditionGroupKey("Adrian Tchaikovsky", "", "Cage of Souls"); got != "name:adriantchaikovsky|cageofsouls" {
		t.Errorf("name-only key = %q", got)
	}
	// Never title-only.
	if got := EditionGroupKey("", "", "Cage of Souls"); got != "" {
		t.Errorf("no author identity must give no key, got %q", got)
	}
	if got := EditionGroupKey("Adrian Tchaikovsky", "B002XLHS8Q", "(Unabridged)"); got != "" {
		t.Errorf("no title must give no key, got %q", got)
	}
}

func TestEditionGroupKey_SeriesIsNotPartOfTheKey(t *testing.T) {
	a := BuildEntry(metadata.CatalogProduct{ASIN: "A1", Title: "Book", Authors: []metadata.CatalogContributor{{Name: "X Y", ASIN: "AX"}},
		Series: []metadata.CatalogProductSeries{{Title: "S", Sequence: "1"}}}, "audible", "us")
	b := BuildEntry(metadata.CatalogProduct{ASIN: "A2", Title: "Book", Authors: []metadata.CatalogContributor{{Name: "X Y", ASIN: "AX"}},
		Series: []metadata.CatalogProductSeries{{Title: "Other", Sequence: "4"}}}, "audible", "us")
	if a.EditionGroupKey == "" || a.EditionGroupKey != b.EditionGroupKey {
		t.Errorf("series changed the group key: %q vs %q", a.EditionGroupKey, b.EditionGroupKey)
	}
}

func TestEditionKind(t *testing.T) {
	cases := []struct {
		format, title, sub, want string
	}{
		{"unabridged", "Children of Time", "", database.EditionUnabridged},
		{"abridged", "Children of Time", "", database.EditionAbridged},
		{"original_recording", "The Sandman", "", database.EditionUnknown},
		{"", "Children of Time", "", database.EditionUnknown},
		{"unabridged", "Dune", "Dramatized Adaptation", database.EditionDramatized},
		{"original_recording", "Dune: A Full-Cast Production", "", database.EditionFullCast},
		{"unabridged", "Good Omens (BBC Radio Dramatisation)", "", database.EditionDramatized},
		{"abridged", "X", "Radio Drama", database.EditionDramatized},
	}
	for _, c := range cases {
		if got := EditionKind(c.format, c.title, c.sub); got != c.want {
			t.Errorf("EditionKind(%q,%q,%q) = %q; want %q", c.format, c.title, c.sub, got, c.want)
		}
	}
}

func TestBuildEntry_ManualOnlyAndSequence(t *testing.T) {
	e := BuildEntry(metadata.CatalogProduct{ASIN: "B1", Title: "Doctor Who: The Daleks", FormatType: "unabridged",
		Authors: []metadata.CatalogContributor{{Name: "Terry Nation"}},
		Series:  []metadata.CatalogProductSeries{{Title: "Classic Novels", Sequence: "1-3"}}}, "audible", "us")
	if !e.ManualOnly {
		t.Error("Doctor Who product not flagged manual_only")
	}
	if len(e.Series) != 1 || e.Series[0].SeqLo == nil || *e.Series[0].SeqLo != 1 || *e.Series[0].SeqHi != 3 || e.Series[0].Sequence != "1-3" {
		t.Errorf("series = %+v", e.Series)
	}
	bf := BuildEntry(metadata.CatalogProduct{ASIN: "B2", Title: "Something", Publisher: "Big Finish Productions",
		Authors: []metadata.CatalogContributor{{Name: "A B"}}}, "audible", "us")
	if !bf.ManualOnly {
		t.Error("Big Finish publisher not flagged manual_only")
	}
	plain := BuildEntry(metadata.CatalogProduct{ASIN: "B3", Title: "Children of Time",
		Authors: []metadata.CatalogContributor{{Name: "Adrian Tchaikovsky"}},
		Series:  []metadata.CatalogProductSeries{{Title: "Children of Time", Sequence: ""}}}, "audible", "us")
	if plain.ManualOnly {
		t.Error("ordinary product flagged manual_only")
	}
	if plain.Series[0].SeqLo != nil {
		t.Error("empty sequence produced a position")
	}
}

func prod(asin string, authors ...metadata.CatalogContributor) metadata.CatalogProduct {
	return metadata.CatalogProduct{ASIN: asin, Title: "T " + asin, Authors: authors, Language: "english"}
}

func TestResolveAndDecide_Homonyms(t *testing.T) {
	js := func(asin string) metadata.CatalogContributor {
		return metadata.CatalogContributor{Name: "John Smith", ASIN: asin}
	}
	jane := metadata.CatalogContributor{Name: "Jane Doe", ASIN: "JZ"}
	listing := map[string]metadata.CatalogProduct{
		"A": prod("A", js("JX")),       // our John Smith
		"B": prod("B", js("JY")),       // a homonym
		"C": prod("C", js("")),         // name, no ASIN
		"D": prod("D", jane, js("JX")), // co-authored, ours second
		"E": prod("E", jane),           // not John Smith at all
	}
	// The owned book is the CO-AUTHORED one: only John Smith's ASIN may be
	// taken from it, never Jane Doe's.
	res, err := ResolveAuthorASINs(context.Background(), "John Smith", []string{"D"}, listing, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.AuthorASINs) != 1 || res.AuthorASINs[0] != "JX" || res.Conflict {
		t.Fatalf("resolution = %+v; want [JX], no conflict", res)
	}
	want := map[string]Decision{
		"A": {Keep: true},
		"B": {},
		"C": {Keep: true, NameOnly: true},
		"D": {Keep: true},
		"E": {},
	}
	for asin, w := range want {
		if got := Decide(listing[asin], "John Smith", res); got != w {
			t.Errorf("Decide(%s) = %+v; want %+v", asin, got, w)
		}
	}
	// Folded spelling variants of the name are the same author.
	if got := Decide(prod("F", metadata.CatalogContributor{Name: "JOHN  SMITH"}), "John Smith", res); !got.Keep || !got.NameOnly {
		t.Errorf("spelling variant: %+v", got)
	}
}

func TestResolve_ConflictIsNotAPick(t *testing.T) {
	js := func(asin string) metadata.CatalogContributor {
		return metadata.CatalogContributor{Name: "John Smith", ASIN: asin}
	}
	listing := map[string]metadata.CatalogProduct{"A": prod("A", js("JX")), "B": prod("B", js("JY"))}
	res, err := ResolveAuthorASINs(context.Background(), "John Smith", []string{"A", "B"}, listing, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !res.Conflict || len(res.AuthorASINs) != 2 {
		t.Fatalf("resolution = %+v; want conflict over 2 ASINs", res)
	}
	for _, a := range []string{"A", "B"} {
		if d := Decide(listing[a], "John Smith", res); !d.Keep || !d.Conflict {
			t.Errorf("%s: %+v; both identities are kept and flagged, neither picked", a, d)
		}
	}
}

func TestResolve_NoOwnedASIN_AllNameOnly(t *testing.T) {
	js := metadata.CatalogContributor{Name: "John Smith", ASIN: "JX"}
	res, _ := ResolveAuthorASINs(context.Background(), "John Smith", nil, nil, nil)
	if d := Decide(prod("A", js), "John Smith", res); !d.Keep || !d.NameOnly {
		t.Errorf("no resolution: %+v; want kept name_only", d)
	}
	if d := Decide(prod("E", metadata.CatalogContributor{Name: "Jane Doe"}), "John Smith", res); d.Keep {
		t.Error("product not crediting the name was kept")
	}
}

func TestResolve_LooksUpOnlyAbsentASINs(t *testing.T) {
	js := metadata.CatalogContributor{Name: "John Smith", ASIN: "JX"}
	listing := map[string]metadata.CatalogProduct{"A": prod("A", js)}
	var looked []string
	lookup := func(_ context.Context, asin string) (*metadata.CatalogProduct, error) {
		looked = append(looked, asin)
		if asin == "BAD" {
			return nil, errors.New("boom")
		}
		p := prod(asin, metadata.CatalogContributor{Name: "John Smith", ASIN: "JX"})
		return &p, nil
	}
	res, err := ResolveAuthorASINs(context.Background(), "John Smith", []string{"A", "Z", "BAD"}, listing, lookup)
	if err != nil {
		t.Fatal(err)
	}
	if len(looked) != 2 || looked[0] != "Z" || looked[1] != "BAD" {
		t.Errorf("looked up %v; want [Z BAD] (A came free from the listing)", looked)
	}
	if res.Lookups != 2 || res.LookupErrors != 1 {
		t.Errorf("lookups=%d errors=%d", res.Lookups, res.LookupErrors)
	}
}

func TestLanguageMatches(t *testing.T) {
	if !LanguageMatches("english", "english") || !LanguageMatches("English", "english") || !LanguageMatches("", "english") {
		t.Error("english/blank rejected")
	}
	if LanguageMatches("german", "english") {
		t.Error("german kept under an english filter")
	}
	if !LanguageMatches("german", "") {
		t.Error("empty filter must keep everything")
	}
}

func mustTime(s string) time.Time {
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		panic(err)
	}
	return t
}

func TestDue(t *testing.T) {
	now := mustTime("2026-10-01T00:00:00Z")
	recent := mustTime("2026-09-20T00:00:00Z")
	old := mustTime("2026-08-01T00:00:00Z")
	iv := DefaultReharvestInterval
	if !Due(nil, now, iv, false) {
		t.Error("never harvested must be due")
	}
	if Due(&database.CatalogAuthorState{State: database.CatalogHarvestComplete, LastCompleteAt: &recent}, now, iv, false) {
		t.Error("recent complete must not be due")
	}
	if !Due(&database.CatalogAuthorState{State: database.CatalogHarvestComplete, LastCompleteAt: &old}, now, iv, false) {
		t.Error("old complete must be due")
	}
	if !Due(&database.CatalogAuthorState{State: database.CatalogHarvestPartial, LastCompleteAt: &recent}, now, iv, false) {
		t.Error("partial must be due regardless of interval")
	}
	if !Due(&database.CatalogAuthorState{State: database.CatalogHarvestFailed}, now, iv, false) {
		t.Error("failed must be due")
	}
	if !Due(&database.CatalogAuthorState{State: database.CatalogHarvestComplete, LastCompleteAt: &recent}, now, iv, true) {
		t.Error("forced must be due")
	}
}
