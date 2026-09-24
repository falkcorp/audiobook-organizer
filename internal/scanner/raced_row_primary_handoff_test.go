// file: internal/scanner/raced_row_primary_handoff_test.go
// version: 1.0.0
// guid: 00e57d28-b1fc-410b-8ab8-557e1e5fe233
// last-edited: 2026-09-24

package scanner

import (
	"sync"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/versionprimary/vptest"
)

// racedRowStore makes the create of target lose a race, deterministically: the
// FIRST lookup of target reports "no row" (which sends saveBookToDatabase down
// the create path) and runs create as it returns, so the re-read under the
// path stripe finds the other writer's row and takes the raced-row branch.
type racedRowStore struct {
	scannerStore
	target string
	create func() string
	once   sync.Once
	rowID  string
}

func (s *racedRowStore) GetBookByFilePath(path string) (*database.Book, error) {
	if path == s.target {
		fired := false
		s.once.Do(func() {
			s.rowID = s.create()
			fired = true
		})
		if fired {
			return nil, nil
		}
	}
	return s.scannerStore.GetBookByFilePath(path)
}

// moveIntoPrimarylessGroup is the other writer that groups the hash partner
// between the scanner's hash lookup and its link write: the partner lands in
// gid as an organized, explicit-false member with its file row, so gid has no
// primary and the partner is the only member eligible to take it.
func moveIntoPrimarylessGroup(t *testing.T, gid string) func(base *database.PebbleStore, bookID string) {
	return func(base *database.PebbleStore, bookID string) {
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
			cur.VersionGroupID = new(gid)
			cur.IsPrimaryVersion = new(false)
			cur.LibraryState = new("organized")
			return nil
		}); err != nil {
			t.Errorf("concurrent version-link write failed: %v", err)
		}
	}
}

// installRacedRow wraps the current scanner store so the second copy's create
// loses the race to a row built by create.
func installRacedRow(t *testing.T, target string, create func() string) *racedRowStore {
	t.Helper()
	r := &racedRowStore{scannerStore: getStore(), target: target, create: create}
	SetStore(r)
	return r
}

// The raced-row branch, joined: the hash-duplicate branch linked the partner
// into a group another writer established (no primary yet), then the new
// row's create lost the race. The row that won joins that group as explicit
// false, and the branch hands the group's primary on so it ends with one --
// the partner, the only organized member. Without the hand-off the group is
// left with none, which hides the book from ABS.
func TestRacedRow_JoinsAsNonPrimaryAndHandsGroupPrimaryOn(t *testing.T) {
	const otherGroup = "vg-other-writer"
	base, firstID, secondPath := seedHashDupRace(t, moveIntoPrimarylessGroup(t, otherGroup))
	raced := installRacedRow(t, secondPath, func() string {
		row, err := base.CreateBook(&database.Book{FilePath: secondPath, Title: "Shared Title"})
		if err != nil {
			t.Errorf("create raced row: %v", err)
			return ""
		}
		return row.ID
	})

	importSecondCopy(t, secondPath)
	if raced.rowID == "" {
		t.Fatal("the fixture never created the racing row; the raced-row branch was not exercised")
	}

	f := &vptest.Fixture{S: base, Root: config.AppConfig.RootDir}
	racedRow, err := base.GetBookByID(raced.rowID)
	if err != nil || racedRow == nil {
		t.Fatalf("read raced row: %v", err)
	}
	if racedRow.VersionGroupID == nil || *racedRow.VersionGroupID != otherGroup {
		t.Fatalf("raced row group = %v, want %q (it must join the partner's group)", racedRow.VersionGroupID, otherGroup)
	}
	if got := f.Flag(t, raced.rowID); got != "false" {
		t.Fatalf("raced row flag = %s, want explicit false", got)
	}
	f.RequireSinglePrimary(t, otherGroup, firstID)
}

// The raced-row branch, refused: the row that won the race was already in a
// group of its own (heldGroup), so it keeps that group and does not join the
// partner's. Both groups are handed on: the partner's (now holding only the
// partner) and the held one. Each starts with no primary and exactly one
// organized member, so each must end with that member crowned.
func TestRacedRow_HeldGroupAndPartnerGroupAreBothHandedOn(t *testing.T) {
	const (
		otherGroup = "vg-other-writer"
		heldGroup  = "vg-held"
	)
	base, firstID, secondPath := seedHashDupRace(t, moveIntoPrimarylessGroup(t, otherGroup))
	raced := installRacedRow(t, secondPath, func() string {
		row, err := base.CreateBook(&database.Book{
			FilePath:         secondPath,
			Title:            "Shared Title",
			VersionGroupID:   new(heldGroup),
			IsPrimaryVersion: new(false),
			LibraryState:     new("organized"),
		})
		if err != nil {
			t.Errorf("create raced row: %v", err)
			return ""
		}
		if _, err := base.ModifyBook(row.ID, func(cur *database.Book) error {
			cur.IsPrimaryVersion = new(false)
			return nil
		}); err != nil {
			t.Errorf("pin raced row flag: %v", err)
		}
		if err := base.CreateBookFile(&database.BookFile{ID: "bf-raced", BookID: row.ID, FilePath: secondPath}); err != nil {
			t.Errorf("seed raced book file: %v", err)
		}
		return row.ID
	})

	importSecondCopy(t, secondPath)
	if raced.rowID == "" {
		t.Fatal("the fixture never created the racing row; the raced-row branch was not exercised")
	}

	f := &vptest.Fixture{S: base, Root: config.AppConfig.RootDir}
	racedRow, err := base.GetBookByID(raced.rowID)
	if err != nil || racedRow == nil {
		t.Fatalf("read raced row: %v", err)
	}
	if racedRow.VersionGroupID == nil || *racedRow.VersionGroupID != heldGroup {
		t.Fatalf("raced row group = %v, want it to keep %q", racedRow.VersionGroupID, heldGroup)
	}
	f.RequireSinglePrimary(t, heldGroup, raced.rowID)
	f.RequireSinglePrimary(t, otherGroup, firstID)
}
