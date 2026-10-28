// file: internal/versions/serve_guard_nothing_served_test.go
// version: 1.0.0
// guid: 5e0b7a93-41d6-4c28-9f1a-d8c36b72e054
// last-edited: 2026-10-28

package versions

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// servedPathPresent decides whether a path the book serves counts: a file that
// exists does, a folder only while it holds a regular file outside .versions/.
func TestServedPathPresent(t *testing.T) {
	mk := func(t *testing.T, files ...string) string {
		t.Helper()
		root := filepath.Join(t.TempDir(), "No Mans Land")
		if err := os.MkdirAll(root, 0o755); err != nil {
			t.Fatal(err)
		}
		for _, f := range files {
			if strings.HasSuffix(f, "/") {
				if err := os.MkdirAll(filepath.Join(root, f), 0o755); err != nil {
					t.Fatal(err)
				}
				continue
			}
			writeServed(t, filepath.Join(root, f))
		}
		return root
	}
	cases := []struct {
		name  string
		files []string
		want  bool
	}{
		{"empty folder", nil, false},
		{"folder of empty folders", []string{"Disc 1/", "Disc 2/"}, false},
		{"only version slots", []string{".versions/V1/Knightfall.m4b"}, false},
		{"a file", []string{"Knightfall.m4b"}, true},
		{"a file in a subfolder", []string{"Disc 1/Knightfall.m4b"}, true},
		{"a file beside the version slots", []string{".versions/V1/Knightfall.m4b", "Zero Year.m4b"}, true},
		{"a nested folder named like the versions dir still counts", []string{"Disc 1/.versions/V1/x.m4b", "Disc 1/y.m4b"}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := mk(t, tc.files...)
			got, why := servedPathPresent(dir)
			if got != tc.want {
				t.Fatalf("servedPathPresent = %v (%s), want %v", got, why, tc.want)
			}
			if !got && why == "" {
				t.Errorf("a path that does not count must say why")
			}
		})
	}

	t.Run("a path that does not exist", func(t *testing.T) {
		if got, why := servedPathPresent(filepath.Join(t.TempDir(), "gone.m4b")); got || why == "" {
			t.Fatalf("servedPathPresent = %v, %q, want false and a reason", got, why)
		}
	})
	t.Run("a symlink to a folder with a file", func(t *testing.T) {
		dir := mk(t, "Knightfall.m4b")
		link := filepath.Join(t.TempDir(), "link")
		if err := os.Symlink(dir, link); err != nil {
			t.Fatal(err)
		}
		if got, why := servedPathPresent(link); !got {
			t.Fatalf("servedPathPresent(link) = false (%s), want true", why)
		}
	})
}

// The folder form of the nothing-served refusal, end to end: a book whose
// FilePath is a folder holding nothing but version slots serves nothing, and the
// trashed version is left alone (os.Stat on the folder alone would have called
// it present and let the purge go on).
func TestPurgeRefusesWhenTheBookFolderHoldsOnlyVersionSlots(t *testing.T) {
	f := newServedFixture(t)
	v := f.version(database.BookVersionStatusAlt, 30*day)
	slotFile := filepath.Join(VersionSlotDir(f.dir, v.ID), "hush-alt.m4b")
	writeServed(t, slotFile)
	f.row(v.ID, slotFile, 1)
	if err := os.Remove(f.active); err != nil {
		t.Fatal(err)
	}
	f.book.FilePath = f.dir
	if _, err := f.store.UpdateBook(f.book.ID, f.book); err != nil {
		t.Fatal(err)
	}
	f.trashLikeHandler(v.ID)

	ver, _ := f.store.GetBookVersion(v.ID)
	err := PurgeVersion(f.store, ver, testGuard)
	var ns *NothingServedError
	if !errors.Is(err, ErrPurgeNothingServed) || !errors.As(err, &ns) {
		t.Fatalf("PurgeVersion err = %v, want a *NothingServedError", err)
	}
	if ns.BookPath != f.dir {
		t.Errorf("BookPath = %q, want the folder %s", ns.BookPath, f.dir)
	}
	if !onDisk(slotFile) {
		t.Fatal("the trashed version's file was deleted although the book's folder holds nothing it serves")
	}
	if got := f.status(v.ID); got != database.BookVersionStatusTrash {
		t.Fatalf("status = %s, want trash (restorable)", got)
	}
}

// The control: once the folder holds a file outside the slots the purge goes on.
func TestPurgeProceedsWhenTheBookFolderHoldsAFile(t *testing.T) {
	f := newServedFixture(t)
	v := f.version(database.BookVersionStatusAlt, 30*day)
	slotFile := filepath.Join(VersionSlotDir(f.dir, v.ID), "hush-alt.m4b")
	writeServed(t, slotFile)
	f.row(v.ID, slotFile, 1)
	f.book.FilePath = f.dir // f.active still sits in it
	if _, err := f.store.UpdateBook(f.book.ID, f.book); err != nil {
		t.Fatal(err)
	}
	f.trashLikeHandler(v.ID)

	ver, _ := f.store.GetBookVersion(v.ID)
	if err := PurgeVersion(f.store, ver, testGuard); err != nil {
		t.Fatalf("PurgeVersion = %v, want the purge to go on", err)
	}
	if onDisk(slotFile) || !onDisk(f.active) {
		t.Errorf("slot file on disk = %v, served file on disk = %v, want the slot gone and the served file kept", onDisk(slotFile), onDisk(f.active))
	}
}
