// file: internal/plugins/maintenance/series_denumber_test.go
// version: 1.0.0
// guid: ba5b477d-1d56-4a6a-8a46-3896b785d5f2
// last-edited: 2026-08-04

package maintenance

import "testing"

// Every one of these is a real production series name. metadata.StripSeriesContamination
// returns all of them unchanged with pos="", which is why series-normalize never
// collapsed them and why 37 separate "Discworld NN" series exist.
func TestSplitSeriesPosition_RealProductionNames(t *testing.T) {
	cases := []struct {
		name     string
		base     string
		pos      int
		explicit bool
		padded   bool
	}{
		{"Discworld 05", "Discworld", 5, false, true},
		{"Safehold 01", "Safehold", 1, false, true},
		{"Frontiers Saga 07", "Frontiers Saga", 7, false, true},
		{"Leveling up the World 01", "Leveling up the World", 1, false, true},
		{"Mistborn 3", "Mistborn", 3, false, false},
		{"The Stormlight Archive 2", "The Stormlight Archive", 2, false, false},
		{"Monster Girl Islands 12", "Monster Girl Islands", 12, false, false},
		{"Schooled in Magic: Book 11", "Schooled in Magic", 11, true, false},
		{"Reclaiming Honor bk 6", "Reclaiming Honor", 6, true, false},
		{"The Long WInter Trilogy Book 3", "The Long WInter Trilogy", 3, true, false},
	}
	for _, c := range cases {
		got, ok := SplitSeriesPosition(c.name)
		if !ok {
			t.Errorf("%q: no position found", c.name)
			continue
		}
		if got.Base != c.base || got.Position != c.pos {
			t.Errorf("%q → base=%q pos=%d, want base=%q pos=%d",
				c.name, got.Base, got.Position, c.base, c.pos)
		}
		if got.Explicit != c.explicit {
			t.Errorf("%q: Explicit=%v, want %v", c.name, got.Explicit, c.explicit)
		}
		if got.Padded != c.padded {
			t.Errorf("%q: Padded=%v, want %v", c.name, got.Padded, c.padded)
		}
	}
}

// A name with no trailing position must be reported as such, never mangled.
func TestSplitSeriesPosition_LeavesCleanNamesAlone(t *testing.T) {
	for _, n := range []string{"Discworld", "The Stormlight Archive", "Bobiverse", ""} {
		if _, ok := SplitSeriesPosition(n); ok {
			t.Errorf("%q was treated as carrying a position", n)
		}
	}
}

// 🔴 THE CORRUPTION THIS MUST NOT CAUSE. A real name ending in a number is
// indistinguishable from a position by shape alone. Unpadded and unique, it must
// be left alone — merging "Fahrenheit 451" into a "Fahrenheit" series would be a
// permanent, silent corruption of the library.
func TestSeriesDenumber_LeavesALoneUnpaddedNumberAlone(t *testing.T) {
	in := []SeriesInput{
		{ID: 1, Name: "Fahrenheit 451", AuthorID: 7, Books: 1},
		{ID: 2, Name: "Blake's 7", AuthorID: 8, Books: 1},
	}
	if got := SeriesDenumber(in); len(got) != 0 {
		t.Fatalf("planned %d merges for names that carry no evidence: %+v", len(got), got)
	}
}

// Zero-padding is evidence on its own — nobody titles a work "Discworld 05".
func TestSeriesDenumber_TrustsZeroPadding(t *testing.T) {
	in := []SeriesInput{{ID: 1, Name: "Discworld 05", AuthorID: 3, Books: 1}}
	got := SeriesDenumber(in)
	if len(got) != 1 {
		t.Fatalf("planned %d merges, want 1", len(got))
	}
	if got[0].IntoName != "Discworld" || got[0].Position != 5 {
		t.Fatalf("got base=%q pos=%d, want Discworld/5", got[0].IntoName, got[0].Position)
	}
	if got[0].Reason != "zero-padded position" {
		t.Fatalf("reason = %q", got[0].Reason)
	}
}

// A repeated base is the other form of corroboration: "Mistborn 3" alongside
// "Mistborn 6" proves the number is a position, without any padding.
func TestSeriesDenumber_TrustsARepeatedBase(t *testing.T) {
	in := []SeriesInput{
		{ID: 1, Name: "Mistborn 3", AuthorID: 4, Books: 1},
		{ID: 2, Name: "Mistborn 6", AuthorID: 4, Books: 1},
	}
	got := SeriesDenumber(in)
	if len(got) != 2 {
		t.Fatalf("planned %d merges, want 2", len(got))
	}
	for _, p := range got {
		if p.IntoName != "Mistborn" {
			t.Fatalf("base = %q, want Mistborn", p.IntoName)
		}
		if p.Reason != "another series shares this base" {
			t.Fatalf("reason = %q", p.Reason)
		}
	}
}

// The corroboration is per AUTHOR. Two different authors each having one
// unpadded numbered series is not evidence that either is a position.
func TestSeriesDenumber_DoesNotCorroborateAcrossAuthors(t *testing.T) {
	in := []SeriesInput{
		{ID: 1, Name: "Genesis 2", AuthorID: 1, Books: 1},
		{ID: 2, Name: "Genesis 3", AuthorID: 2, Books: 1},
	}
	if got := SeriesDenumber(in); len(got) != 0 {
		t.Fatalf("corroborated across authors: %+v", got)
	}
}

// An explicit keyword needs no corroboration — the word IS the evidence.
func TestSeriesDenumber_ExplicitKeywordNeedsNoCorroboration(t *testing.T) {
	in := []SeriesInput{{ID: 1, Name: "Schooled in Magic: Book 11", AuthorID: 5, Books: 1}}
	got := SeriesDenumber(in)
	if len(got) != 1 || got[0].IntoName != "Schooled in Magic" || got[0].Position != 11 {
		t.Fatalf("got %+v", got)
	}
}

// 🔴 Chapter and disc tags leak into the series field in bulk — production has
// 176 "Chapter N" and 49 "Disc N". Folding them together would manufacture one
// enormous bogus series, which is worse than the split state.
func TestSeriesDenumber_RefusesJunkBases(t *testing.T) {
	in := []SeriesInput{
		{ID: 1, Name: "Chapter 12", AuthorID: 1, Books: 1},
		{ID: 2, Name: "Chapter 13", AuthorID: 1, Books: 1},
		{ID: 3, Name: "Disc 3", AuthorID: 1, Books: 1},
		{ID: 4, Name: "Disc 4", AuthorID: 1, Books: 1},
	}
	if got := SeriesDenumber(in); len(got) != 0 {
		t.Fatalf("planned merges into a tag-artefact base: %+v", got)
	}
}

// When a series already carries the plain base name it becomes the merge target,
// so books land in the existing row rather than a freshly created duplicate.
func TestSeriesDenumber_TargetsAnExistingBaseSeries(t *testing.T) {
	in := []SeriesInput{
		{ID: 10, Name: "Discworld", AuthorID: 3, Books: 4},
		{ID: 11, Name: "Discworld 05", AuthorID: 3, Books: 1},
	}
	got := SeriesDenumber(in)
	if len(got) != 1 {
		t.Fatalf("planned %d merges, want 1", len(got))
	}
	if got[0].IntoID != 10 {
		t.Fatalf("IntoID = %d, want the existing 'Discworld' series (10)", got[0].IntoID)
	}
}

// With no existing base series, IntoID is 0 and the caller must create one.
func TestSeriesDenumber_ReportsWhenTheBaseMustBeCreated(t *testing.T) {
	in := []SeriesInput{{ID: 11, Name: "Safehold 01", AuthorID: 3, Books: 1}}
	got := SeriesDenumber(in)
	if len(got) != 1 || got[0].IntoID != 0 {
		t.Fatalf("got %+v, want a single plan with IntoID=0", got)
	}
}

func TestIsJunkSeriesBase(t *testing.T) {
	for _, s := range []string{"Chapter", "disc", " CD ", "Track"} {
		if !IsJunkSeriesBase(s) {
			t.Errorf("IsJunkSeriesBase(%q) = false, want true", s)
		}
	}
	for _, s := range []string{"Discworld", "Mistborn", ""} {
		if IsJunkSeriesBase(s) && s != "" {
			t.Errorf("IsJunkSeriesBase(%q) = true, want false", s)
		}
	}
}

// 🔴 What the production dry run caught. An equality-only junk check let through
// 76 merges into a base ending ", Chapter", 39 into "- Chapter", 126 into a base
// left dangling on an en-dash, and 11 each into bases of "01".."04". Every one
// would have manufactured a series named after a tag artefact.
func TestIsJunkSeriesBase_RejectsShapesTheProductionDryRunExposed(t *testing.T) {
	for _, s := range []string{
		"The Darkling Child: The Defenders of Shannara (Unabridged), Chapter",
		"The Tower of Nero - Chapter",
		"Drew Hayes – Bones of the Past –",
		"Nicole Kornher-Stace – Firebreak –",
		"01", "02", "—", "3.",
		"Something: ",
	} {
		if !IsJunkSeriesBase(s) {
			t.Errorf("IsJunkSeriesBase(%q) = false, want true", s)
		}
	}
	// …while real names with numbers or punctuation inside stay acceptable.
	for _, s := range []string{
		"Discworld", "Wheel of Time", "Schooled in Magic", "Honor Harrington Universe",
		"Drew Hayes - Bones of the Past", "Star Trek: Deep Space Nine",
	} {
		if IsJunkSeriesBase(s) {
			t.Errorf("IsJunkSeriesBase(%q) = true, want false", s)
		}
	}
}
