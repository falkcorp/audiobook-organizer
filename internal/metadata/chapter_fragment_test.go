// file: internal/metadata/chapter_fragment_test.go
// version: 1.2.0
// guid: 3a9c0e21-6f48-4b7d-95a2-1c8f0d4e7b52
// last-edited: 2026-09-30

package metadata

import "testing"

func TestIsLikelyChapterFragment(t *testing.T) {
	cases := []struct {
		name  string
		title string
		want  bool
	}{
		// --- Chapter fragments: must be TRUE ---
		{"number then chapter", "06 Chapter 6", true},
		{"bare chapter", "Chapter 6", true},
		{"number dash track", "01 - Track 1", true},
		{"number dot track", "01. Track 1", true},
		{"disc with number", "Disc 2", true},
		{"cd with number", "CD 3", true},
		{"section with number", "Section 4", true},
		{"part with number", "Part 2", true},
		{"number then part", "12 Part 3", true},
		{"zero padded two digit", "06", true},
		{"zero padded one digit", "01", true},
		{"zero padded three digit", "012", true},
		{"lowercase chapter", "chapter 12", true},
		// Copy suffixes are fragments on their text alone.
		{"copy suffix", "Elantris_copy179", true},
		{"bare copy suffix", "Elantris_copy", true},
		// Counted parts are NOT: only the folder and duration can tell a
		// chapter file from a dramatized product (metabatch's titleJudge).
		{"N of M after a title", "Before They Are Hanged 002 of 341", false},
		{"N of with count cut off", "Elantris 084 of", false},
		{"Part N of M inside a subtitle", "The Tears of the Sun A Novel of the Change Part 02 of 63", false},
		{"GraphicAudio part", "Golden Son (Part 1 of 2)", false},
		{"dramatized part", "Dark Age (2 of 3)", false},
		{"dramatized part with tag", "Shadow's Edge (1 of 2) [Dramatized Adaptation]", false},
		{"Dune part", "Dune (1 of 2)", false},
		{"hash count", "Wheel of Time #3 of 14", false},
		{"series count", "Mistborn Series 1 of 3", false},

		// --- Real books: must be FALSE ---
		{"real title moons", "The Moons of Barsk", false},
		{"real title metro", "Metro 2034", false},
		{"year title 1984", "1984", false},
		{"year title 2001", "2001", false},
		{"bare number 451", "451", false},
		{"part of your world", "Part of Your World", false},
		{"discworld", "Discworld", false},
		{"catch 22", "Catch-22", false},
		{"empty", "", false},
		{"whitespace only", "   ", false},
		{"chapter no number", "Chapter House Dune", false},
		{"section no number", "Sectional Sofas", false},
		{"track no number", "Off the Beaten Track", false},
		{"number of word", "13 of Hearts", false},
		{"words of words", "One of Us Is Lying", false},
		{"number of word mid-title", "4 of a Kind Stories", false},
		{"copyright is not a copy suffix", "Notes_copyright", false},
		{"dune messiah", "Dune Messiah", false},
		{"series book number", "Mistborn Book 1", false},
		{"series position out of a count", "The Dragon Reborn (Book 3 of 14)", false},
		{"volume out of a count", "Collected Stories Vol. 2 of 3", false},
		{"disc out of a count leads with Disc", "Disc 2 of 12", true},
		{"split part", "The Way of Kings, Part 1", false},
		{"subtitled book number", "Halls of Power: Ancient Dreams, Book 3", false},
		// Sibling-conditional shapes are NOT fragments on their own.
		{"trailing number alone", "The Sunrise Lands 1", false},
		{"trailing letter alone", "Sealed to the Flame E", false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := IsLikelyChapterFragment(tc.title); got != tc.want {
				t.Errorf("IsLikelyChapterFragment(%q) = %v, want %v", tc.title, got, tc.want)
			}
		})
	}
}

func TestSiblingPartStem(t *testing.T) {
	cases := []struct {
		title, stem, token string
		ok                 bool
	}{
		{"The Sunrise Lands 1", "The Sunrise Lands", "1", true},
		{"Cobra 100", "Cobra", "100", true},
		{"Sealed to the Flame E", "Sealed to the Flame", "E", true},
		{"A Promise to Lews Therin C", "A Promise to Lews Therin", "C", true},
		{"Elantris_12", "Elantris", "12", true},
		// The shape matches; only sibling rows make it a part (metabatch).
		{"Apollo 13", "Apollo", "13", true},
		{"Plan B", "Plan", "B", true},
		// Never split.
		{"1984", "", "", false},
		{"Catch-22", "", "", false},
		{"Metro 2034", "", "", false},
		{"Blade Runner 2049", "", "", false},
		{"Dune Messiah", "", "", false},
		{"Mistborn Book 1", "", "", false},
		{"The Way of Kings, Part 1", "", "", false},
		{"Halls of Power: Ancient Dreams, Book 3", "", "", false},
		{"Volume 2", "", "", false},
		{"Sealed to the Flame e", "", "", false},
		{"12345", "", "", false},
	}
	for _, tc := range cases {
		t.Run(tc.title, func(t *testing.T) {
			stem, token, ok := SiblingPartStem(tc.title)
			if ok != tc.ok || stem != tc.stem || token != tc.token {
				t.Fatalf("SiblingPartStem(%q) = (%q, %q, %v), want (%q, %q, %v)", tc.title, stem, token, ok, tc.stem, tc.token, tc.ok)
			}
		})
	}
}

func TestStripRipJunk(t *testing.T) {
	cases := []struct {
		title, want string
		had         bool
	}{
		{"2002 - Neil Gaiman - American Gods [64k 20;57;42 577MB]", "2002 - Neil Gaiman - American Gods", true},
		{"American Gods (128kbps)", "American Gods", true},
		{"American Gods [1.2 GB]", "American Gods", true},
		{"[64k 577MB]", "", true},
		{"Dune [Unabridged]", "Dune [Unabridged]", false},
		{"The Expanse (Book 9)", "The Expanse (Book 9)", false},
		{"Catch-22", "Catch-22", false},
	}
	for _, tc := range cases {
		t.Run(tc.title, func(t *testing.T) {
			got, had := StripRipJunk(tc.title)
			if got != tc.want || had != tc.had {
				t.Fatalf("StripRipJunk(%q) = (%q, %v), want (%q, %v)", tc.title, got, had, tc.want, tc.had)
			}
		})
	}
}

func TestMayBeUnsearchableTitle_LeavesFolderShapesOut(t *testing.T) {
	for title, want := range map[string]bool{
		"The Sunrise Lands 1":                    false,
		"Sealed to the Flame E":                  false,
		"American Gods [64k 20;57;42 577MB]":     false,
		"Cobra 100 of 151":                       false, // NeedsFolderEvidence instead
		"Chapter 3":                              true,
		"Prologue":                               true,
		"Dune Messiah":                           false,
		"1984":                                   false,
		"Catch-22":                               false,
		"Metro 2034":                             false,
		"Halls of Power: Ancient Dreams, Book 3": false,
	} {
		if got := MayBeUnsearchableTitle(title); got != want {
			t.Errorf("MayBeUnsearchableTitle(%q) = %v, want %v", title, got, want)
		}
	}
}

func TestIsCountedPartTitle_AndNeedsFolderEvidence(t *testing.T) {
	for title, want := range map[string]bool{
		"Before They Are Hanged 002 of 341": true,
		"Cobra 100 of 151":                  true,
		"Elantris 084 of":                   true,
		"The Tears of the Sun A Novel of the Change Part 02 of 63": true,
		"Golden Son (Part 1 of 2)":                                 true,
		"Dark Age (2 of 3)":                                        true,
		"Shadow's Edge (1 of 2) [Dramatized Adaptation]":           true,
		"Dune (1 of 2)":                                            true,
		"Wheel of Time #3 of 14":                                   true,
		"Mistborn Series 1 of 3":                                   true,
		"The Dragon Reborn (Book 3 of 14)":                         false,
		"13 of Hearts":                                             false,
		"Dune Messiah":                                             false,
	} {
		if got := IsCountedPartTitle(title); got != want {
			t.Errorf("IsCountedPartTitle(%q) = %v, want %v", title, got, want)
		}
	}
	for title, want := range map[string]bool{
		"Cobra 100 of 151":          true,
		"The Sunrise Lands 1":       true,
		"Apollo 13":                 true,
		"American Gods [64k 577MB]": true,
		"Dune Messiah":              false,
		"1984":                      false,
		"Catch-22":                  false,
		"Mistborn Book 1":           false,
		"Dune [Unabridged]":         false,
	} {
		if got := NeedsFolderEvidence(title); got != want {
			t.Errorf("NeedsFolderEvidence(%q) = %v, want %v", title, got, want)
		}
	}
}
