// file: internal/plugins/maintenance/series_embedded_position_test.go
// version: 1.0.0
// guid: 7c41ba05-3e92-4d18-b6a7-51fd0e93c284
// last-edited: 2026-08-06

package maintenance

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Every name below is a REAL production series name, taken from the whole-library
// shape census in docs/specs/2026-08-06-series-embedded-positions-design.md.
// They are not illustrative — each one is a case the parser gets wrong if a
// discriminator is dropped.

// 🔴 THE CORRUPTION THIS MUST NOT CAUSE.
//
// "86—EIGHTY-SIX" is a real series title covering 17 production books: the number
// IS the name. It has the same shape as "08. Battle for the Abyss", where the
// number is a genuine Horus Heresy position. The only thing separating them in
// the string is how the number is punctuated — a list-style "08. " versus a
// number fused to the next word by an unspaced dash.
//
// If this test fails, 17 books get scattered into a series called "EIGHTY-SIX"
// and the real name is deleted.
func TestSplitSeriesPosition_RefusesRealTitlesThatOpenWithANumber(t *testing.T) {
	for _, n := range []string{
		"86—EIGHTY-SIX",
		"5-Minute Sherlock",
		"24-Hour Comics",
		"1-800-Where-R-You",
	} {
		if got, ok := SplitSeriesPosition(n); ok {
			t.Errorf("%q was split into base=%q pos=%d — it is a real title",
				n, got.Base, got.Position)
		}
	}
}

// A keyword introducing the number is the evidence, whether it sits at the end of
// the name or in the middle of it. These are the only shape allowed to apply
// unattended on the first production run.
func TestSplitSeriesPosition_EmbeddedKeywordIsHighConfidence(t *testing.T) {
	cases := []struct {
		name string
		base string
		pos  int
	}{
		{"Vampire Hunter D: Vol 09: The Rose Princess", "Vampire Hunter D", 9},
		{"Evil Genius: Book 4: Becoming the Apex Supervillain", "Evil Genius", 4},
		{"Frontiers Saga Part 2: Rogue Castes", "Frontiers Saga", 2},
		{"The Best of the Best: Volume 2: Twenty Years of the Best", "The Best of the Best", 2},
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
		if got.Shape != ShapeEmbeddedKeyword {
			t.Errorf("%q: shape=%q, want %q", c.name, got.Shape, ShapeEmbeddedKeyword)
		}
		if got.Confidence != ConfidenceHigh {
			t.Errorf("%q: confidence=%q, want high", c.name, got.Confidence)
		}
	}
}

// 🔑 Explicit must stay false for embedded shapes. SeriesDenumber's evidence
// switch had a `case sp.Explicit` arm that applies unattended; if an embedded
// keyword inherited it, 61 books' worth of merges would ride in on the trailing
// shape's permission with no gate of their own.
func TestSplitSeriesPosition_EmbeddedKeywordDoesNotInheritExplicit(t *testing.T) {
	got, ok := SplitSeriesPosition("Evil Genius: Book 4: Becoming the Apex Supervillain")
	if !ok {
		t.Fatal("no position found")
	}
	if got.Explicit {
		t.Error("Explicit=true — embedded shapes must be gated by Shape, not by the trailing shape's flag")
	}
}

// A bracketed bare number is a deliberate mark — no real title wears one — but
// nothing vouches for the number itself, so it is medium and needs an operator
// to opt in.
func TestSplitSeriesPosition_BracketedIsMediumConfidence(t *testing.T) {
	got, ok := SplitSeriesPosition("Dragon Born [04]")
	if !ok {
		t.Fatal("Dragon Born [04]: no position found")
	}
	if got.Base != "Dragon Born" || got.Position != 4 {
		t.Fatalf("base=%q pos=%d, want Dragon Born/4", got.Base, got.Position)
	}
	if got.Confidence != ConfidenceMedium {
		t.Fatalf("confidence=%q, want medium", got.Confidence)
	}
	if !got.Padded {
		t.Error("Padded=false for \"04\"")
	}
}

// 🔴 Spec D5. Two bracketed numbers offer two candidate positions and the string
// says nothing about which is the series'. In "The Demon Wars Saga [07]
// Immortalis [02]" the answer is 07, and a last-match regex takes 02 — writing a
// wrong position AND a wrong base. Refuse instead of guessing.
func TestSplitSeriesPosition_RefusesTwoCandidateNumbers(t *testing.T) {
	for _, n := range []string{
		"The Demon Wars Saga [07] Immortalis [02]",
		"The Stormlight Archive [01] The Way Of Kings [02]",
	} {
		if got, ok := SplitSeriesPosition(n); ok {
			t.Errorf("%q was split into base=%q pos=%d — two numbers means guessing",
				n, got.Base, got.Position)
		}
	}
}

// The multi-number refusal must NOT reach back into the trailing shapes. They
// already resolve correctly in production, and refusing a name like "The 100 Book
// 3" would be a coverage regression dressed up as caution.
func TestSplitSeriesPosition_MultiNumberGateSparesTrailingShapes(t *testing.T) {
	got, ok := SplitSeriesPosition("The 100 Book 3")
	if !ok {
		t.Fatal("The 100 Book 3: trailing keyword shape stopped matching")
	}
	if got.Base != "The 100" || got.Position != 3 {
		t.Fatalf("base=%q pos=%d, want \"The 100\"/3", got.Base, got.Position)
	}
}

// A bare number in front of a title is right often enough to be worth surfacing
// and wrong often enough that it can never apply itself.
//
// Note what the base becomes for "08. Battle for the Abyss": "Battle for the
// Abyss", which is the BOOK's title, not the series ("Horus Heresy"). The correct
// series name simply is not present in the string. That is the clearest statement
// of why this tier holds rather than applies.
func TestSplitSeriesPosition_BareNumbersAreLowConfidence(t *testing.T) {
	cases := []struct {
		name  string
		base  string
		pos   int
		shape SeriesShape
	}{
		{"08. Battle for the Abyss", "Battle for the Abyss", 8, ShapeLeadingBare},
		{"11. Fallen Angels", "Fallen Angels", 11, ShapeLeadingBare},
		{"22. Shadows of Treachery", "Shadows of Treachery", 22, ShapeLeadingBare},
		{"Station 64: The Doll Dungeon", "Station", 64, ShapeMidColon},
	}
	for _, c := range cases {
		got, ok := SplitSeriesPosition(c.name)
		if !ok {
			t.Errorf("%q: no position found", c.name)
			continue
		}
		if got.Base != c.base || got.Position != c.pos || got.Shape != c.shape {
			t.Errorf("%q → base=%q pos=%d shape=%q, want %q/%d/%q",
				c.name, got.Base, got.Position, got.Shape, c.base, c.pos, c.shape)
		}
		if got.Confidence != ConfidenceLow {
			t.Errorf("%q: confidence=%q, want low", c.name, got.Confidence)
		}
	}
}

// Spec D6. "Rebirth Online 2: Rebirth Online" splits into a base and a title that
// are the same string — which means the split found a repetition, not a volume.
func TestSplitSeriesPosition_RefusesWhenBaseEqualsTitle(t *testing.T) {
	for _, n := range []string{
		"Rebirth Online 2: Rebirth Online",
		"Renegade Star 3: renegade star",
	} {
		if got, ok := SplitSeriesPosition(n); ok {
			t.Errorf("%q was split into base=%q — base and title are the same name",
				n, got.Base)
		}
	}
}

// Spec D6. A bundle number is not a series position: "Publisher's Pack 7" numbers
// the pack. Merging on it would gather unrelated bundles into one bogus series.
func TestIsJunkSeriesBase_RejectsBundleWords(t *testing.T) {
	for _, s := range []string{
		"Renegade Star: Publisher's Pack",
		"Pack", "Omnibus", "The Expanse - Box Set",
		"D", // single character — the embedded shapes can strip a name this far
	} {
		if !IsJunkSeriesBase(s) {
			t.Errorf("IsJunkSeriesBase(%q) = false, want true", s)
		}
	}
}

// 🔑 The mirror-image artefact. The shipped guard only checked SUFFIXES because
// the parser only stripped suffixes; a leading-number strip strands the
// punctuation at the FRONT, where none of those checks can see it.
func TestIsJunkSeriesBase_RejectsAStrandedLeadingSeparator(t *testing.T) {
	for _, s := range []string{
		". Battle for the Abyss",
		"- Fallen Angels",
		": Shadows of Treachery",
		"— Immortalis",
	} {
		if !IsJunkSeriesBase(s) {
			t.Errorf("IsJunkSeriesBase(%q) = false, want true", s)
		}
	}
}

// The whole point of the shape census is that "Renegade Star: Publisher's Pack 7:
// Renegade Star" must not survive to a plan. It carries a number in a shape the
// parser now reads, and only the junk guard stops it.
func TestSeriesDenumber_RefusesAPackNumber(t *testing.T) {
	in := []SeriesInput{
		{ID: 1, Name: "Renegade Star: Publisher's Pack 7: Renegade Star", AuthorID: 2, Books: 1},
	}
	if got := SeriesDenumber(in); len(got) != 0 {
		t.Fatalf("planned a merge on a pack number: %+v", got)
	}
}

// 🔒 The gate itself. Low must never be eligible, at any setting, and medium only
// when an operator has opted in.
func TestApplyEligible_LowNeverApplies(t *testing.T) {
	cases := []struct {
		conf        SeriesConfidence
		allowMedium bool
		want        bool
	}{
		{ConfidenceHigh, false, true},
		{ConfidenceHigh, true, true},
		{ConfidenceMedium, false, false},
		{ConfidenceMedium, true, true},
		{ConfidenceLow, false, false},
		{ConfidenceLow, true, false}, // ← the one that matters
	}
	for _, c := range cases {
		got := ApplyEligible(SeriesMergePlan{Confidence: c.conf}, c.allowMedium)
		if got != c.want {
			t.Errorf("ApplyEligible(%s, allowMedium=%v) = %v, want %v",
				c.conf, c.allowMedium, got, c.want)
		}
	}
}

// End-to-end on the tiers: a library holding one name of each shape produces
// plans whose confidences match, and only the keyword-vouched one is eligible
// under the default settings.
func TestSeriesDenumber_AssignsTiersAndGatesTheApplySet(t *testing.T) {
	in := []SeriesInput{
		{ID: 1, Name: "Evil Genius: Book 4: Becoming the Apex Supervillain", AuthorID: 1, Books: 3},
		{ID: 2, Name: "Dragon Born [04]", AuthorID: 2, Books: 2},
		{ID: 3, Name: "08. Battle for the Abyss", AuthorID: 3, Books: 1},
		{ID: 4, Name: "86—EIGHTY-SIX", AuthorID: 4, Books: 17},
	}
	plans := SeriesDenumber(in)

	byName := map[string]SeriesMergePlan{}
	for _, p := range plans {
		byName[p.FromName] = p
	}
	if _, planned := byName["86—EIGHTY-SIX"]; planned {
		t.Fatal("86—EIGHTY-SIX produced a plan — it is a real series name")
	}
	if len(plans) != 3 {
		t.Fatalf("planned %d merges, want 3: %+v", len(plans), plans)
	}
	for name, wantConf := range map[string]SeriesConfidence{
		"Evil Genius: Book 4: Becoming the Apex Supervillain": ConfidenceHigh,
		"Dragon Born [04]":                                    ConfidenceMedium,
		"08. Battle for the Abyss":                            ConfidenceLow,
	} {
		if got := byName[name].Confidence; got != wantConf {
			t.Errorf("%q: confidence=%q, want %q", name, got, wantConf)
		}
	}

	var eligible int
	for _, p := range plans {
		if ApplyEligible(p, false) {
			eligible++
		}
	}
	if eligible != 1 {
		t.Fatalf("%d plans eligible by default, want 1 (the keyword-vouched one)", eligible)
	}
}

// The report is the rollback artefact: a merge creates and deletes series rows,
// so there is no transaction to abort and replaying this file is the only way
// back. It must therefore list EVERY candidate — including the held ones, which
// are exactly what an operator is deciding about.
func TestWriteSeriesDenumberReport_ListsHeldCandidatesToo(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "report.tsv")
	plans := []SeriesMergePlan{
		{FromID: 1, FromName: "Evil Genius: Book 4: X", IntoName: "Evil Genius", Position: 4,
			Books: 3, Shape: ShapeEmbeddedKeyword, Confidence: ConfidenceHigh, Reason: "embedded position keyword"},
		{FromID: 2, FromName: "Dragon Born [04]", IntoName: "Dragon Born", Position: 4,
			Books: 2, Shape: ShapeBracketed, Confidence: ConfidenceMedium, Reason: "bracketed position"},
		{FromID: 3, FromName: "08. Battle for the Abyss", IntoName: "Battle for the Abyss", Position: 8,
			Books: 1, Shape: ShapeLeadingBare, Confidence: ConfidenceLow, Reason: "bare leading number"},
	}
	if err := writeSeriesDenumberReport(path, plans, false); err != nil {
		t.Fatalf("write: %v", err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(string(raw)), "\n")
	if len(lines) != 4 {
		t.Fatalf("%d lines, want 1 header + 3 candidates:\n%s", len(lines), raw)
	}
	if !strings.HasPrefix(lines[0], "from_series_id\tfrom_name") {
		t.Errorf("header = %q", lines[0])
	}
	// Eligibility is recorded per row so the file explains itself without the
	// params that produced it.
	wantEligible := []bool{true, false, false}
	for i, want := range wantEligible {
		cols := strings.Split(lines[i+1], "\t")
		if len(cols) != 9 {
			t.Fatalf("row %d has %d columns, want 9: %q", i, len(cols), lines[i+1])
		}
		got := cols[7] == "true"
		if got != want {
			t.Errorf("row %d (%s): eligible=%v, want %v", i, cols[1], got, want)
		}
	}
}

// A tab inside a series name would shift every column after it and silently
// misattribute the position and tier of that row.
func TestWriteSeriesDenumberReport_NeutralisesTabsInNames(t *testing.T) {
	path := filepath.Join(t.TempDir(), "report.tsv")
	plans := []SeriesMergePlan{{
		FromID: 1, FromName: "Broken\tName\nHere", IntoName: "Broken", Position: 2,
		Shape: ShapeMidColon, Confidence: ConfidenceLow, Reason: "bare number before the title",
	}}
	if err := writeSeriesDenumberReport(path, plans, false); err != nil {
		t.Fatalf("write: %v", err)
	}
	raw, _ := os.ReadFile(path)
	lines := strings.Split(strings.TrimSpace(string(raw)), "\n")
	if len(lines) != 2 {
		t.Fatalf("%d lines, want 2 — an embedded newline split the row:\n%s", len(lines), raw)
	}
	if cols := strings.Split(lines[1], "\t"); len(cols) != 9 {
		t.Fatalf("%d columns, want 9 — an embedded tab shifted the row: %q", len(cols), lines[1])
	}
}
