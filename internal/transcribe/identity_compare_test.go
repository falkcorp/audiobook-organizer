// file: internal/transcribe/identity_compare_test.go
// version: 1.0.0
// guid: 0f4a7c92-3e58-4b16-9d20-b5c8e1a6f379
// last-edited: 2026-11-17

package transcribe

import "testing"

func annParse(title, author, narr string) ClipParse {
	return ClipParse{Kind: ClipKindTitle, Title: title, Author: author, Narrator: narr}
}

// Invented text only: Batman titles and characters.
func TestCompareIdentity(t *testing.T) {
	cases := []struct {
		name     string
		stored   Identity
		parse    ClipParse
		verdict  IdentityVerdict
		conflict ConflictKind
		fillable int
	}{
		{"not an announcement", Identity{Title: "Hush"}, ClipParse{Kind: ClipKindChapter}, VerdictNotAnnouncement, ConflictNone, 0},
		{"narration is not an announcement", Identity{}, ClipParse{Kind: ClipKindNarration}, VerdictNotAnnouncement, ConflictNone, 0},
		{
			"agree, case and punctuation ignored",
			Identity{Title: "The Long Halloween", Author: "Alfred Pennyworth"},
			annParse("the long halloween!", "Alfred  Pennyworth.", ""),
			VerdictAgree, ConflictNone, 0,
		},
		{
			"agree when the clip names less than is stored",
			Identity{Title: "Hush", Author: "Selina Kyle", Narrator: "Barbara Gordon"},
			annParse("Hush", "", ""),
			VerdictAgree, ConflictNone, 0,
		},
		{
			"fill an empty title and author",
			Identity{},
			annParse("Hush", "Selina Kyle", ""),
			VerdictFill, ConflictNone, 2,
		},
		{
			"fill only the narrator",
			Identity{Title: "Hush", Author: "Selina Kyle"},
			annParse("Hush", "Selina Kyle", "Barbara Gordon"),
			VerdictFill, ConflictNone, 1,
		},
		{
			"publisher prefix",
			Identity{Title: "Gotham Audio presents, Year One"},
			ClipParse{Kind: ClipKindTitle, Title: "Year One", Publisher: "Gotham Audio"},
			VerdictConflict, ConflictPublisherPrefix, 0,
		},
		{
			"publisher prefix named by the parse's publisher",
			Identity{Title: "Gotham Audio Year One"},
			ClipParse{Kind: ClipKindTitle, Title: "Year One", Publisher: "Gotham Audio"},
			VerdictConflict, ConflictPublisherPrefix, 0,
		},
		{
			"a longer stored title is not a publisher prefix",
			Identity{Title: "Batman Year One"},
			ClipParse{Kind: ClipKindTitle, Title: "Year One"},
			VerdictConflict, ConflictDifferentTitle, 0,
		},
		{
			"truncation, parse shorter",
			Identity{Title: "The Long Halloween Part One"},
			annParse("The Long Halloween", "", ""),
			VerdictConflict, ConflictTruncation, 0,
		},
		{
			"truncation, stored shorter",
			Identity{Title: "The Long"},
			annParse("The Long Halloween", "", ""),
			VerdictConflict, ConflictTruncation, 0,
		},
		{
			"a shared first letter is not truncation",
			Identity{Title: "Hushed"},
			annParse("Hush", "", ""),
			VerdictConflict, ConflictDifferentTitle, 0,
		},
		{
			"different title",
			Identity{Title: "Hush"},
			annParse("The Long Halloween", "", ""),
			VerdictConflict, ConflictDifferentTitle, 0,
		},
		{
			"other field differs",
			Identity{Title: "Hush", Author: "Selina Kyle"},
			annParse("Hush", "Harvey Dent", ""),
			VerdictConflict, ConflictOtherField, 0,
		},
		{
			"title conflict wins over another field's fill",
			Identity{Title: "Hush"},
			annParse("The Long Halloween", "Harvey Dent", ""),
			VerdictConflict, ConflictDifferentTitle, 1,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := CompareIdentity(tc.stored, tc.parse)
			if got.Verdict != tc.verdict || got.Conflict != tc.conflict || got.Fillable != tc.fillable {
				t.Fatalf("CompareIdentity = %+v, want verdict %s conflict %q fillable %d", got, tc.verdict, tc.conflict, tc.fillable)
			}
		})
	}
}

func TestStoredTitleJunk(t *testing.T) {
	cases := []struct {
		title string
		want  JunkReason
	}{
		{"", JunkNone},
		{"Hush", JunkNone},
		{"The Long Halloween", JunkNone},
		{"Narrated", JunkCreditVerbOnly},
		{"This has been narrated", JunkCreditVerbOnly},
		{"Written and narrated", JunkCreditVerbOnly},
		{"Year One Narrated by Selina Kyle", JunkCreditWordingInside},
		{"Gotham Nights 1 Written", JunkEndsInCreditVerb},
		// A title that merely uses the word is not junk unless it ends in it.
		{"Written in the Stars of Gotham", JunkNone},
		{"The Narrated Life", JunkNone},
	}
	for _, tc := range cases {
		if got := StoredTitleJunk(tc.title); got != tc.want {
			t.Errorf("StoredTitleJunk(%q) = %q, want %q", tc.title, got, tc.want)
		}
	}
}
