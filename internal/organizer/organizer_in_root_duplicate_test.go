// file: internal/organizer/organizer_in_root_duplicate_test.go
// version: 1.0.0
// guid: 0e6f2b73-5a91-4d48-b3c7-9a14d8e0f625
// last-edited: 2026-10-23

package organizer

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// Two live books in different version groups, both already inside the library
// root, with the same file hash. The same-hash check lists every holder
// (round 4), so each book now sees the other. A book that is already in the
// library adds no second copy by being re-laid-out, so the check must not
// block its organize (a rename after a metadata edit) on every run until dedup
// resolves the pair; it reports the pair to dedup instead. Only a twin that
// occupies the target itself still refuses: the organize would write a _copyN
// of identical bytes next to it.

type inRootPair struct {
	f        *libraryCopyFixture
	a, b     *database.Book
	spy      *collisionSpy
	targetOf string
}

// newInRootPair builds A and B in the root at paths that are NOT the organize
// target (root/Some Author/Title/Title.mp3), same bytes, same hash, groups g-a
// and g-b. samePath puts B's row on A's file (two rows, one path).
func newInRootPair(t *testing.T, order idOrder, samePath bool, bAtTarget bool) *inRootPair {
	t.Helper()
	aID, bID := mintBookIDs(t, order.originalOlder)
	svc, store, root := setupInPlace(t)
	// setupInPlace leaves the strategy empty; the organize that proceeds needs one.
	config.AppConfig.OrganizationStrategy = "copy"
	author, err := store.CreateAuthor("Some Author")
	if err != nil {
		t.Fatal(err)
	}
	hash := "in-root-pair-hash"
	content := filled(512, 7)
	target := filepath.Join(root, "Some Author", "Title", "Title.mp3")

	mk := func(id, path, group string, writeFile bool) *database.Book {
		var c []byte
		if writeFile {
			c = content
		}
		b := addInPlaceBook(t, store, id, "Title", path, c, nil, 0)
		b.Author = nil
		b.AuthorID = &author.ID
		b.FileHash = &hash
		b.VersionGroupID = &group
		organized := "organized"
		b.LibraryState = &organized
		if _, err := store.UpdateBook(b.ID, b); err != nil {
			t.Fatal(err)
		}
		stored, _ := store.GetBookByID(id)
		return stored
	}
	aPath := filepath.Join(root, "Old Layout", "Title.mp3")
	a := mk(aID, aPath, "g-a", true)
	var b *database.Book
	switch {
	case samePath:
		b = mk(bID, aPath, "g-b", false)
	case bAtTarget:
		b = mk(bID, target, "g-b", true)
	default:
		b = mk(bID, filepath.Join(root, "Other Layout", "Title.mp3"), "g-b", true)
	}
	return &inRootPair{f: &libraryCopyFixture{svc: svc, store: store, root: root, original: a, libCopy: b, hash: hash}, a: a, b: b, spy: &collisionSpy{}, targetOf: target}
}

func TestOrganizeBook_InRootBookIsNotBlockedByAnInRootTwinElsewhere(t *testing.T) {
	eachIDOrder(t, func(t *testing.T, order idOrder) {
		for _, which := range []string{"a", "b"} {
			t.Run("organize-"+which, func(t *testing.T) {
				p := newInRootPair(t, order, false, false)
				book, twin := p.a, p.b
				if which == "b" {
					book, twin = p.b, p.a
				}

				target, method, err := p.f.organizer(p.spy).OrganizeBook(book)

				if err != nil {
					t.Fatalf("OrganizeBook(%s) = %v; an in-root book must not be refused for a twin that is elsewhere", which, err)
				}
				if target != p.targetOf || method == "" {
					t.Errorf("OrganizeBook = (%q, %q); want a write at %q", target, method, p.targetOf)
				}
				if got := p.spy.calls; len(got) != 1 || got[0][0] != book.ID || got[0][1] != twin.FilePath {
					t.Errorf("OnCollision calls = %v; want the pair reported once (%s, %s)", got, book.ID, twin.FilePath)
				}
			})
		}
	})
}

// A twin that occupies the target is still a duplicate refusal: organizing would
// write a _copy1 of identical bytes beside it.
func TestOrganizeBook_InRootBookIsStillRefusedWhenTheTwinHoldsTheTarget(t *testing.T) {
	eachIDOrder(t, func(t *testing.T, order idOrder) {
		p := newInRootPair(t, order, false, true)

		_, _, err := p.f.organizer(p.spy).OrganizeBook(p.a)

		want := "duplicate file already organized at: " + p.b.FilePath
		if err == nil || err.Error() != want {
			t.Fatalf("OrganizeBook(a) = %v; want %q", err, want)
		}
		if got := p.spy.calls; len(got) != 1 || got[0][1] != p.b.FilePath {
			t.Errorf("OnCollision calls = %v; want one for the twin that holds the target", got)
		}
		if m, _ := filepath.Glob(filepath.Join(filepath.Dir(p.targetOf), "Title_copy*")); len(m) != 0 {
			t.Errorf("a _copyN was written: %v", m)
		}
	})
}

// Two rows over ONE file (the commonest dedup merge) are not a second copy
// anywhere: the check used to refuse with "duplicate file already organized at"
// the book's own path.
func TestOrganizeBook_AnotherRowOverTheSameFileIsNotADuplicate(t *testing.T) {
	eachIDOrder(t, func(t *testing.T, order idOrder) {
		p := newInRootPair(t, order, true, false)

		_, _, err := p.f.organizer(p.spy).OrganizeBook(p.a)

		if err != nil && strings.Contains(err.Error(), "duplicate file already organized") {
			t.Fatalf("a second row over the book's own file was reported as a duplicate: %v", err)
		}
		if err != nil {
			t.Fatalf("OrganizeBook(a) = %v", err)
		}
		if n := p.spy.count(); n != 0 {
			t.Errorf("OnCollision fired %d time(s) for a row over the same file; want 0", n)
		}
	})
}
