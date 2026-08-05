// file: internal/itunes/service/fs_regroup_folderref_test.go
// version: 1.0.0
// guid: d721dc75-fe57-443d-9b94-7ffd831a58c3
// last-edited: 2026-08-05

package itunesservice

import (
	"fmt"
	"testing"
)

// 🔴 THE REVIEW-TRUST BUG. A production group of 17 tracks — every one of them
// "Rysa Walker - The Delphi Effect ... (Unabridged)" — was labelled
// `/abooks/imported/Rysa Walker`, the AUTHOR folder.
//
// The grouping was correct. The label was not: the parent carried an edition
// marker, so folderKeyOf took the edition branch and returned the grandparent. A
// reviewer reading "Rysa Walker" would reasonably reject it as an author-folder
// merge and discard a good regroup — and with ~900 holds to work through, a label
// that misrepresents its group is a correctness problem, not a cosmetic one.
func TestClassify_LabelsTheBookFolderNotTheAuthorFolder(t *testing.T) {
	author := "/mnt/bigdata/books/abooks/imported/Rysa Walker"
	book := author + "/Rysa Walker - The Delphi Effect The Delphi Trilogy, Book 1 (Unabridged)"

	var books []ShatterBook
	for i := 3; i <= 19; i++ {
		books = append(books, ShatterBook{
			BookID:      fmt.Sprintf("b%03d", i),
			FilePath:    fmt.Sprintf("%s/The Delphi Effect The Delphi Trilogy, Book 1 (Unabridged) - %03d.mp3", book, i),
			FileCount:   1,
			IsPrimary:   true,
			Title:       "The Delphi Effect",
			DurationSec: 15 * 60,
		})
	}

	groups, _ := ClassifyShatteredFolders(books)
	if len(groups) == 0 {
		t.Fatal("expected a group")
	}
	for _, g := range groups {
		if g.FolderRef == author {
			t.Fatalf("group labelled with the AUTHOR folder %q; a reviewer would read this "+
				"as an author-folder merge and reject a correct regroup", g.FolderRef)
		}
		if g.FolderRef != book {
			t.Fatalf("FolderRef = %q, want the book folder %q", g.FolderRef, book)
		}
	}
}

// 🔑 The grandparent is STILL right when members genuinely span sibling shells —
// that is the shatter shape the grouping key climbs for, and collapsing the label
// to one member's parent would name a single chapter dir as the whole book.
func TestClassify_KeepsTheGrandparentWhenMembersSpanShells(t *testing.T) {
	bookDir := "/lib/Author/Metro 2034"
	var books []ShatterBook
	for i := 1; i <= 6; i++ {
		shell := fmt.Sprintf("%s/Metro 2034 - %02d", bookDir, i)
		books = append(books, ShatterBook{
			BookID:      fmt.Sprintf("b%02d", i),
			FilePath:    fmt.Sprintf("%s/%02d Chapter %d.mp3", shell, i, i),
			FileCount:   1,
			IsPrimary:   true,
			Title:       fmt.Sprintf("Chapter %d", i),
			DurationSec: 12 * 60,
		})
	}

	groups, _ := ClassifyShatteredFolders(books)
	if len(groups) == 0 {
		t.Fatal("expected a group")
	}
	for _, g := range groups {
		if g.FolderRef != bookDir {
			t.Fatalf("FolderRef = %q, want the book folder %q that holds the chapter shells",
				g.FolderRef, bookDir)
		}
	}
}

func TestDisplayFolderRef(t *testing.T) {
	same := []memberInfo{
		{parentDir: "/lib/A/Book"},
		{parentDir: "/lib/A/Book"},
	}
	if got := displayFolderRef(same, "/lib/A"); got != "/lib/A/Book" {
		t.Errorf("one shared parent → got %q, want the parent", got)
	}

	spanning := []memberInfo{
		{parentDir: "/lib/A/Book/Book - 01"},
		{parentDir: "/lib/A/Book/Book - 02"},
	}
	if got := displayFolderRef(spanning, "/lib/A/Book"); got != "/lib/A/Book" {
		t.Errorf("members spanning shells → got %q, want the group key", got)
	}

	if got := displayFolderRef(nil, "/fallback"); got != "/fallback" {
		t.Errorf("empty members → got %q, want the group key", got)
	}
	if got := displayFolderRef([]memberInfo{{parentDir: ""}}, "/fallback"); got != "/fallback" {
		t.Errorf("blank parentDir → got %q, want the group key", got)
	}
}
