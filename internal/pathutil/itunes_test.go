// file: internal/pathutil/itunes_test.go
// version: 1.2.0
// guid: 5b1f8c47-2a93-4e06-bd75-8f2c4a1e903d
// last-edited: 2026-09-26

package pathutil

import "testing"

// 🔴 books/itunes/** is the externally-managed Original library, marked Frozen and
// read-only. Producers that PROPOSE structural changes must skip it: 561 of 777
// ambiguous regroup holds were iTunes AUTHOR folders, because that layout puts an
// author's whole catalogue in one directory and a folder-grouping classifier reads
// a shared folder as a shared book. Every one of those proposals was both wrong
// (distinct novels, 8-29h each) and unactionable (a tree we may not reorganise).
//
// The table covers the union of the two predicates this helper replaced: the
// old case-sensitive substring match ("audiobooks/itunes/" matched, "Books/
// iTunes/" did not) and author-path-link's case-insensitive whole-segment
// match (the reverse).
func TestUnderFrozenITunesTree(t *testing.T) {
	cases := []struct {
		name string
		path string
		want bool
	}{
		{"real library file", "/mnt/bigdata/books/itunes/iTunes Media/Audiobooks/Shirtaloon, Travis Deverell/x.m4b", true},
		{"real library folder", "/mnt/bigdata/books/itunes/iTunes Media/Audiobooks/Tamryn Tamer", true},
		{"relative", "books/itunes/anything", true},
		{"tree root, no trailing slash", "/mnt/bigdata/books/itunes", true},
		{"windows separators", `W:\books\itunes\iTunes Media\Audiobooks\A\b.m4b`, true},
		{"capitalised (old substring missed)", "/mnt/Books/iTunes/Audiobooks/x.m4b", true},
		{"upper case (old substring missed)", "/MNT/BOOKS/ITUNES/X.M4B", true},
		{"books suffix (old whole-segment missed)", "/mnt/audiobooks/itunes/x.m4b", true},
		{"books suffix, mixed case", "/mnt/AudioBooks/iTunes/x.m4b", true},
		{"padded segment", "/mnt/books/ itunes /x.m4b", true},

		{"organized tree", "/mnt/bigdata/books/audiobook-organizer/Author/Book/01.m4b", false},
		{"imported tree", "/mnt/bigdata/books/abooks/imported/Rysa Walker/x.mp3", false},
		{"newbooks tree", "/mnt/bigdata/books/newbooks/audiobooks/four hex/01.mp3", false},
		{"empty", "", false},
		{"book folder named iTunes", "/mnt/bigdata/books/audiobook-organizer/Some Author/iTunes/01.m4b", false},
		{"itunes as a longer segment", "/mnt/bigdata/books/itunes-backup/x.m4b", false},
		{"itunes with no parent", "itunes/x.m4b", false},
		{"iTunes Media alone", "/Users/me/Music/iTunes/iTunes Media/Audiobooks/x.m4b", false},
		{"books not the direct parent", "/mnt/books/x/itunes/y.m4b", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := UnderFrozenITunesTree(c.path); got != c.want {
				t.Errorf("UnderFrozenITunesTree(%q) = %v, want %v", c.path, got, c.want)
			}
		})
	}
}
