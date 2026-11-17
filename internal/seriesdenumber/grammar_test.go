// file: internal/seriesdenumber/grammar_test.go
// version: 2.1.0
// guid: 7ebf2f9c-4978-464b-9c4c-c768027dd6ce
// last-edited: 2026-11-17

package seriesdenumber

import (
	"testing"

	"golang.org/x/text/unicode/norm"
)

func TestParseName_Accepts(t *testing.T) {
	cases := []struct {
		name   string
		base   string
		n      int
		marker string
	}{
		{"Court of Owls 05", "Court of Owls", 5, ""},
		{"Gotham Knights: Book 11", "Gotham Knights", 11, "Book"},
		{"Gotham Knights, Volume 3", "Gotham Knights", 3, "Volume"},
		{"Gotham Knights - 03", "Gotham Knights", 3, ""},
		{"Gotham Knights – 03", "Gotham Knights", 3, ""},
		{"Court of Owls #5", "Court of Owls", 5, "#"},
		{"Court of Owls # 5", "Court of Owls", 5, "#"},
		{"Court of Owls Vol. 2", "Court of Owls", 2, "Vol."},
		{"Court of Owls Vol.2", "Court of Owls", 2, "Vol."},
		{"Court of Owls No 7", "Court of Owls", 7, "No"},
		{"Court of Owls bk. 7", "Court of Owls", 7, "bk."},
		{"BAT 07", "BAT", 7, ""},
		{"BAT-05", "BAT", 5, ""},
		{"Arkham Asylum 99", "Arkham Asylum", 99, ""},
		{"Discord Knights 05", "Discord Knights", 5, ""},
		{"Batman-Beyond 03", "Batman-Beyond", 3, ""},
		{"Don't Look Up 03", "Don't Look Up", 3, ""},
		{"Bats’ Last Stand 04", "Bats’ Last Stand", 4, ""},
		{"Maître Chauve-Souris 02", "Maître Chauve-Souris", 2, ""},
		{"Готэм Рыцари 05", "Готэм Рыцари", 5, ""},
		{"  Gotham   Knights   005 ", "Gotham Knights", 5, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p, v, ok := ParseName(tc.name)
			if !ok {
				t.Fatalf("held as %s (%s), want base %q at %d", v.Code, v.Reason, tc.base, tc.n)
			}
			if p.Base != tc.base || p.N != tc.n || p.Marker != tc.marker {
				t.Fatalf("got base %q marker %q n %d, want %q %q %d", p.Base, p.Marker, p.N, tc.base, tc.marker, tc.n)
			}
		})
	}
}

func TestParseName_Holds(t *testing.T) {
	cases := []struct {
		name string
		want Code
	}{
		// B1: a Part in any punctuation, or beside a format word.
		{"Hush Part: 2", CodePartOrFormatWord},
		{"Hush Part #2", CodePartOrFormatWord},
		{"Hush Part 1 - Unabridged", CodePartOrFormatWord},
		{"Hush Pt. 2", CodePartOrFormatWord},
		{"Hush Part 2", CodePartOrFormatWord},
		{"Hush Part.2", CodePartOrFormatWord},
		{"Year One Unabridged Part 1", CodePartOrFormatWord},
		{"Hush Disc 2", CodePartOrFormatWord},
		{"Hush CD 3", CodePartOrFormatWord},
		{"Audio 01", CodePartOrFormatWord},
		{"Chapter 4", CodePartOrFormatWord},
		{"chap-01", CodePartOrFormatWord},
		{"Knightfall (Unabridged) 3", CodePartOrFormatWord},
		{"Gotham Knights Special Edition 3", CodePartOrFormatWord},
		// B2: a base that still carries a number or a cut-off phrase.
		{"Gotham Knights 01 of 03", CodeBaseHasDigit},
		{"Gotham Knights Part 02 of 05", CodePartOrFormatWord},
		{"Gotham Knights 01 & 02", CodeBaseHasDigit},
		{"Gotham Knights 01 to 03", CodeBaseHasDigit},
		{"Gotham Knights 1-03", CodeBaseHasDigit},
		{"No Mans Land 2-1", CodeBaseHasDigit},
		{"Dark Victory.1 04", CodeBaseHasDigit},
		{"01 Knightfall 04", CodeBaseHasDigit},
		{"Batman Vol. 2: Knightfall Book 3", CodeBaseHasDigit},
		{"Gotham Knights 5 6", CodeBaseHasDigit},
		{"Gotham Knights of 3", CodeBaseEndsConnector},
		{"Gotham Knights & 3", CodeBaseEndsConnector},
		{"Gotham Knights and 3", CodeBaseEndsConnector},
		{"Gotham Knights to 3", CodeBaseEndsConnector},
		// A base made only of structural words is a slice of a file, not a series
		// (the words the part/format list and the volume list do not already hold).
		{"Chapters 03", CodeStructuralBase},
		// B3: a decimal point.
		{"Gotham Knights Book .5", CodeFractionalNumber},
		{"Gotham Knights #.5", CodeFractionalNumber},
		{"Gotham Knights Book 0.5", CodeFractionalNumber},
		{"Gotham Knights 2.5", CodeFractionalNumber},
		{"Gotham Knights, Book 2.5", CodeFractionalNumber},
		// Numbers the grammar will not read.
		{"Knightfall 100", CodeNumberOutOfRange},
		{"Knightfall 201", CodeNumberOutOfRange},
		{"Gotham Central 2099", CodeNumberOutOfRange},
		{"Batman 1989", CodeNumberOutOfRange},
		{"Gotham Knights Book 0", CodeNumberOutOfRange},
		{"Batman '66", CodeNoSeparator},
		{"Hush2", CodeNoSeparator},
		{"Hush", CodeNoTrailingNumber},
		{"Gotham Knights Book IV", CodeNoTrailingNumber},
		{"Gotham Knights Book One", CodeNoTrailingNumber},
		{"", CodeNoTrailingNumber},
		// Base shapes.
		{"Book 5", CodeBaseEmpty},
		{"Gotham Knights (Special) 3", CodeBaseNotClean},
		{"Gotham Knights Tome 1", CodeVolumeWordInBase},
		{"Gotham Knights Band 1", CodeVolumeWordInBase},
		{"Gotham Knights Libro 1", CodeVolumeWordInBase},
		{"Gotham Knights Teil 1", CodeVolumeWordInBase},
		{"Gotham Knights Book Book 3", CodeVolumeWordInBase},
		// B2(a): a volume word in ANY position of the base, not only the last.
		{"Hush Book One 01", CodeVolumeWordInBase},
		{"Hush Book One 02", CodeVolumeWordInBase},
		{"Tome of Owls 03", CodeVolumeWordInBase},
		{"Gotham Libro Knights 03", CodeVolumeWordInBase},
		{"Gotham V Knights 03", CodeVolumeWordInBase},
		{"Gotham T Knights 03", CodeVolumeWordInBase},
		{"Gotham Bd Knights 03", CodeVolumeWordInBase},
		{"Gotham Nr Knights 03", CodeVolumeWordInBase},
		{"Gotham Vols Knights 03", CodeVolumeWordInBase},
		{"Gotham Knights Vols 03", CodeVolumeWordInBase},
		{"Gotham Knights V 03", CodeVolumeWordInBase},
		{"Gotham Knights T 03", CodeVolumeWordInBase},
		{"Gotham Knights Bd 03", CodeVolumeWordInBase},
		{"Gotham Knights Nr 03", CodeVolumeWordInBase},
		// B2(b): part and format words the first lists lacked.
		{"Hush Ch 03", CodePartOrFormatWord},
		{"Hush Ep 03", CodePartOrFormatWord},
		{"Hush Episode 03", CodePartOrFormatWord},
		{"Hush Episodes 03", CodePartOrFormatWord},
		{"Episodes 04", CodePartOrFormatWord},
		{"Hush Disc 03", CodePartOrFormatWord},
		{"Hush Discs 03", CodePartOrFormatWord},
		{"Hush Disks 03", CodePartOrFormatWord},
		{"Hush CDs 03", CodePartOrFormatWord},
		{"Hush Side 03", CodePartOrFormatWord},
		{"Hush Sides 03", CodePartOrFormatWord},
		{"Hush Reel 03", CodePartOrFormatWord},
		{"Hush Hour 03", CodePartOrFormatWord},
		{"Hush Hours 03", CodePartOrFormatWord},
		{"Hush Ch. 03", CodePartOrFormatWord},
		{"B 05", CodeTinyBase},
		{"BM 05", CodeTinyBase},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p, v, ok := ParseName(tc.name)
			if ok {
				t.Fatalf("parsed %+v, want it held as %s", p, tc.want)
			}
			if v.Code != tc.want {
				t.Fatalf("held as %s (%s), want %s", v.Code, v.Reason, tc.want)
			}
			if v.Reason == "" {
				t.Fatalf("held as %s with no reason", v.Code)
			}
		})
	}
}

// A tagger's Unicode form must not decide whether a series is read: a base that
// ends in an accented letter ends in a combining mark when decomposed, which the
// "ends in a letter" rule would otherwise hold while the composed spelling passes.
func TestParseName_ComposedAndDecomposedSpellingsAreOneName(t *testing.T) {
	for _, base := range []string{"Gotham Caf\u00e9", "D\u00e9tective Comics"} {
		composed, _, okC := ParseName(base + " 02")
		decomposed, _, okD := ParseName(norm.NFD.String(base) + " 02")
		if !okC || !okD {
			t.Fatalf("%q: composed accepted=%v, decomposed accepted=%v, want both", base, okC, okD)
		}
		if composed.Base != decomposed.Base {
			t.Errorf("%q: the composed base %q and the decomposed base %q differ", base, composed.Base, decomposed.Base)
		}
	}
}

func TestTitleKey_ComposedAndDecomposedTitlesAreOneKey(t *testing.T) {
	if a, b := TitleKey("Zo\u00eb Returns Part 1"), TitleKey(norm.NFD.String("Zo\u00eb Returns Part 2")); a != b || a == "" {
		t.Errorf("composed key %q, decomposed key %q, want one non-empty key", a, b)
	}
}
