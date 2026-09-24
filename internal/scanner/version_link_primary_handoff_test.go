// file: internal/scanner/version_link_primary_handoff_test.go
// version: 1.0.0
// guid: 4c1d8e27-5a93-4b6f-9e02-7f3a1b6c8d54
// last-edited: 2026-09-24

package scanner

import (
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/versionprimary/vptest"
)

// A scan that links a hash-duplicate into a group another writer established
// hands that group's primary on: here the group had no primary, its only
// other member is an organized library copy, and the scan crowns it instead
// of leaving the group with none. The new row joins as explicit false.
func TestVersionLink_JoiningAnotherWritersGroupHandsPrimaryOn(t *testing.T) {
	const otherGroup = "vg-no-primary"
	base, firstID, secondPath := seedHashDupRace(t, func(base *database.PebbleStore, bookID string) {
		b, err := base.GetBookByID(bookID)
		if err != nil || b == nil {
			t.Errorf("read seeded book: %v", err)
			return
		}
		files, err := base.GetBookFiles(bookID)
		if err != nil {
			t.Errorf("read seeded files: %v", err)
			return
		}
		if len(files) == 0 {
			if err := base.CreateBookFile(&database.BookFile{ID: "bf-first", BookID: bookID, FilePath: b.FilePath}); err != nil {
				t.Errorf("seed book file: %v", err)
			}
		}
		if _, err := base.ModifyBook(bookID, func(cur *database.Book) error {
			cur.VersionGroupID = new(otherGroup)
			cur.IsPrimaryVersion = new(false)
			cur.LibraryState = new("organized")
			return nil
		}); err != nil {
			t.Errorf("concurrent version-link write failed: %v", err)
		}
	})

	importSecondCopy(t, secondPath)

	f := &vptest.Fixture{S: base, Root: config.AppConfig.RootDir}
	f.RequireSinglePrimary(t, otherGroup, firstID)
	second, err := base.GetBookByFilePath(secondPath)
	if err != nil || second == nil {
		t.Fatalf("expected second book, err=%v", err)
	}
	if got := f.Flag(t, second.ID); got != "false" {
		t.Fatalf("second copy flag = %s, want explicit false", got)
	}
}

// joinPrimaryFlag: a row joining a group the scanner did not mint never
// arrives as primary.
func TestJoinPrimaryFlag(t *testing.T) {
	if v := *joinPrimaryFlag("vg-a", "vg-a", true); !v {
		t.Fatal("minted group: want the pair's primary half kept")
	}
	if v := *joinPrimaryFlag("vg-theirs", "vg-a", true); v {
		t.Fatal("joined group: want explicit false")
	}
}
