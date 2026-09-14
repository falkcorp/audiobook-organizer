// file: internal/metafetch/search_segment_variant_test.go
// version: 1.0.0
// guid: 3e7a9c41-6b2d-4f80-a1c5-8d0e2f4b7a93
// last-edited: 2026-09-14

package metafetch

import (
	"reflect"
	"sort"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/metadata"
)

func TestSegmentVariant_RipTitlesSearchTheBooksOwnName(t *testing.T) {
	tests := []struct {
		raw    string
		query  string
		anchor []string
	}{
		{"Legend of Drizzt Book 03 - The Dark Elf Trilogy - Sojourn", "Sojourn", []string{"sojourn"}},
		{"Legend of Drizzt Book 09 - Legacy of the Drow - Siege of Darkness", "Siege of Darkness", []string{"siege", "darkness"}},
		{"The Sorcerer's Ring - 04 - A Cry of Honor", "A Cry of Honor", []string{"cry", "honor"}},
		{"Brandon Sanderson - Mistborn 01 - The Final Empire", "The Final Empire", []string{"final", "empire"}},
		{"Knaves over Queens : Wild Cards (Unabridged)", "Knaves over Queens", []string{"knaves", "over", "queens"}},
	}
	for _, tt := range tests {
		t.Run(tt.raw, func(t *testing.T) {
			got := extraTitleVariants(tt.raw, stripChapterFromTitle(tt.raw))
			if len(got) == 0 {
				t.Fatalf("no variants for %q", tt.raw)
			}
			v := got[len(got)-1]
			if v.Query != tt.query || !v.Exact || v.Allowed == nil {
				t.Fatalf("last variant = %+v, want Exact %q with Allowed", v, tt.query)
			}
			var words []string
			for w := range v.Anchor {
				words = append(words, w)
			}
			sort.Strings(words)
			want := append([]string(nil), tt.anchor...)
			sort.Strings(want)
			if !reflect.DeepEqual(words, want) {
				t.Errorf("anchor = %v, want %v", words, want)
			}
		})
	}
}

func TestSegmentVariant_RefusesTitlesWithNoBookField(t *testing.T) {
	for _, raw := range []string{
		"Dune - Frank Herbert",           // two fields, no number: may be title - author
		"Big Cats 3 - 001",               // last field is only a number
		"Star Wars - Heir to the Empire", // two fields, no number
		"Mistborn: The Final Empire",     // unspaced colon is a real subtitle
		"A Plain Title",
		"1984 - George Orwell",    // a numeric title is not a series position
		"11/22/63 - Stephen King", // nor is a date
	} {
		if got := segmentVariant(raw, map[string]bool{}); got != nil {
			t.Errorf("segmentVariant(%q) = %v, want nil", raw, queries(got))
		}
	}
}

func TestKeepVariant_SegmentAcceptsOwnTitleWordsOnly(t *testing.T) {
	raw := "Brandon Sanderson - Mistborn 01 - The Final Empire"
	v := segmentVariant(raw, map[string]bool{})[0]
	res := []metadata.BookMetadata{
		{Title: "The Final Empire", Author: "Brandon Sanderson"},
		{Title: "Mistborn: The Final Empire", Author: "Brandon Sanderson"},
		{Title: "The Final Empire Companion", Author: "Brandon Sanderson"}, // word we lack
		{Title: "The Well of Ascension", Author: "Brandon Sanderson"},      // sibling
		{Title: "The Final Empire", Author: ""},                            // no author
	}
	got := keepVariant(res, v, "Brandon Sanderson")
	if len(got) != 2 || got[0].Title != "The Final Empire" || got[1].Title != "Mistborn: The Final Empire" {
		t.Fatalf("kept %+v", got)
	}
	// Our author's name in our title is not a title word a result may carry.
	if got := keepVariant([]metadata.BookMetadata{{Title: "Brandon Sanderson: The Final Empire", Author: "Brandon Sanderson"}}, v, "Brandon Sanderson"); len(got) != 0 {
		t.Fatalf("author-named title kept: %+v", got)
	}
	// With no person of ours to vouch, a segment hit never stands.
	if got := keepVariant(res[:1], v, " "); len(got) != 0 {
		t.Fatalf("segment hit kept with no people: %+v", got)
	}
	// A one-word field needs our author to vouch.
	s := segmentVariant("Legend of Drizzt Book 03 - The Dark Elf Trilogy - Sojourn", map[string]bool{})[0]
	if got := keepVariant([]metadata.BookMetadata{{Title: "Sojourn", Author: "Someone Else"}}, s, "R. A. Salvatore"); len(got) != 0 {
		t.Fatalf("another author's Sojourn kept: %+v", got)
	}
	if got := keepVariant([]metadata.BookMetadata{{Title: "Sojourn", Author: "R. A. Salvatore"}}, s, "R. A. Salvatore"); len(got) != 1 {
		t.Fatalf("Salvatore's Sojourn dropped")
	}
}
