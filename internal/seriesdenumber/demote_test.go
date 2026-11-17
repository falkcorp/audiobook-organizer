// file: internal/seriesdenumber/demote_test.go
// version: 1.0.2
// guid: c2a81e47-5f30-4d96-b7a3-19e60d8f4b25
// last-edited: 2026-11-16

package seriesdenumber

import "testing"

func TestDemote(t *testing.T) {
	const author = "Alfred Pennyworth"
	cases := []struct {
		name string
		c    Candidate
		want Code // "" = must still apply
	}{
		// ── Must still apply: the tier must not collapse to nothing. ──
		{"keyword book", Candidate{"Gotham Knights: Book 11", "Gotham Knights", 11, 3, author, nil}, ""},
		{"keyword volume", Candidate{"Gotham Knights, Volume 3", "Gotham Knights", 3, 3, author, nil}, ""},
		{"zero padded", Candidate{"Court of Owls 05", "Court of Owls", 5, 3, author, nil}, ""},
		{"part marker without format words", Candidate{"The Long Halloween Part 2", "The Long Halloween", 2, 3, author, nil}, CodeFilePart},
		{"disc is a whole word only", Candidate{"Discord Knights 05", "Discord Knights", 5, 3, author, nil}, ""},
		{"three letter base is allowed", Candidate{"BAT 07", "BAT", 7, 3, author, nil}, ""},
		{"position 99 is allowed", Candidate{"Arkham Asylum 99", "Arkham Asylum", 99, 3, author, nil}, ""},
		{"author name unknown skips that check", Candidate{"Alfred Pennyworth 12", "Alfred Pennyworth", 12, 3, "", nil}, ""},

		// ── Each wrong shape, named. ──
		{"file part marker", Candidate{"Year One Unabridged Part 1", "Year One Unabridged", 1, 3, author, nil}, CodeFilePart},
		{"file part marker no space", Candidate{"The Cowl Unabridged Part2", "The Cowl Unabridged", 2, 3, author, nil}, CodeFilePart},
		{"file part marker in brackets", Candidate{"Hush (Unabridged) Part 2", "Hush (Unabridged)", 2, 3, author, nil}, CodeFilePart},
		{"file part beside disc", Candidate{"Hush Disc 1 Part 2", "Hush Disc 1", 2, 3, author, nil}, CodeFilePart},
		{"format word in base", Candidate{"Hush Disc 2", "Hush Disc", 2, 3, author, nil}, CodeFormatWord},
		{"cd in base", Candidate{"Hush CD 3", "Hush CD", 3, 3, author, nil}, CodeFormatWord},
		{"audio base", Candidate{"Audio 01", "Audio", 1, 3, author, nil}, CodeFormatWord},
		{"position 100", Candidate{"Knightfall 100", "Knightfall", 100, 3, author, nil}, CodeHighPosition},
		{"position 201", Candidate{"Knightfall 201", "Knightfall", 201, 3, author, nil}, CodeHighPosition},
		{"base is the author", Candidate{"Alfred Pennyworth 12", "Alfred Pennyworth", 12, 3, author, nil}, CodeAuthorBase},
		{"base is the author, punctuation differs", Candidate{"alfred-pennyworth 12", "alfred-pennyworth", 12, 3, author, nil}, CodeAuthorBase},
		{"two numbers", Candidate{"No Mans Land 2-1", "No Mans Land 2", 1, 3, author, nil}, CodeBaseTrailingDigit},
		{"dot number left in base", Candidate{"Dark Victory.1 04", "Dark Victory.1", 4, 3, author, nil}, CodeBaseTrailingDigit},
		{"one letter base", Candidate{"B 05", "B", 5, 3, author, nil}, CodeTinyBase},
		{"two letter base", Candidate{"BM 05", "BM", 5, 3, author, nil}, CodeTinyBase},
		{"chap", Candidate{"chap-01", "chap", 1, 3, author, nil}, CodeStructuralBase},
		{"chapter", Candidate{"Chapter 4", "Chapter", 4, 3, author, nil}, CodeStructuralBase},
		{"digit leading base", Candidate{"01 Knightfall 04", "01 Knightfall", 4, 3, author, nil}, CodeDigitLeadingBase},
		{"no author", Candidate{"Gotham Knights: Book 11", "Gotham Knights", 11, 0, author, nil}, CodeNoAuthor},
		{"no author wins over everything", Candidate{"Hush Disc 2", "Hush Disc", 2, 0, author, nil}, CodeNoAuthor},
		{"file part wins over position", Candidate{"Year One Unabridged Part 1", "Year One Unabridged", 120, 3, author, nil}, CodeFilePart},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			v, held := Demote(tc.c)
			if tc.want == "" {
				if held {
					t.Fatalf("held as %s (%s), want it to apply", v.Code, v.Reason)
				}
				return
			}
			if !held {
				t.Fatalf("applied, want held as %s", tc.want)
			}
			if v.Code != tc.want {
				t.Fatalf("held as %s, want %s", v.Code, tc.want)
			}
			if v.Reason == "" {
				t.Fatalf("held as %s with no reason", v.Code)
			}
		})
	}
}

// A base that is ANY author's name is held, not only the series' own author.
func TestDemote_BaseEqualToAnyAuthorsName(t *testing.T) {
	authors := NewAuthorSet([]string{"Bruce Wayne", "  alfred   PENNYWORTH ", ""})
	own := Candidate{FromName: "Alfred Pennyworth 05", Base: "Alfred Pennyworth", Position: 5, AuthorID: 3, AuthorName: "Bruce Wayne", Authors: authors}
	if v, ok := Demote(own); !ok || v.Code != CodeAuthorBase {
		t.Fatalf("a base that is another author's name: got %+v held=%v, want %s", v, ok, CodeAuthorBase)
	}
	notAuthor := Candidate{FromName: "Court of Owls 05", Base: "Court of Owls", Position: 5, AuthorID: 3, AuthorName: "Bruce Wayne", Authors: authors}
	if v, ok := Demote(notAuthor); ok {
		t.Fatalf("a base that is nobody's name was held: %+v", v)
	}
	if (AuthorSet)(nil).Has("Bruce Wayne") {
		t.Fatalf("a nil set matched")
	}
}
