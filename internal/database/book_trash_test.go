// file: internal/database/book_trash_test.go
// version: 1.2.0
// guid: 7e0be750-b46f-4059-ac27-c22ce7676874
// last-edited: 2026-10-01

package database

import (
	"testing"
	"time"
)

func stateOf(b *Book) string {
	if b.LibraryState == nil {
		return "<nil>"
	}
	return *b.LibraryState
}

func TestRememberLibraryStateBeforeTrash(t *testing.T) {
	b := &Book{LibraryState: strp("organized")}
	RememberLibraryStateBeforeTrash(b)
	if b.PreTrashLibraryState == nil || *b.PreTrashLibraryState != "organized" {
		t.Fatalf("recorded %v, want organized", b.PreTrashLibraryState)
	}
	// Relabelling an already-"deleted" row never records "deleted" over it.
	b.LibraryState = strp("deleted")
	RememberLibraryStateBeforeTrash(b)
	if *b.PreTrashLibraryState != "organized" {
		t.Fatalf("recorded %q over organized", *b.PreTrashLibraryState)
	}
	for _, s := range []*string{nil, strp(""), strp("  "), strp("DELETED")} {
		c := &Book{LibraryState: s}
		RememberLibraryStateBeforeTrash(c)
		if c.PreTrashLibraryState != nil {
			t.Errorf("state %v recorded %q", s, *c.PreTrashLibraryState)
		}
	}
	RememberLibraryStateBeforeTrash(nil) // no panic
}

func TestRestoreLibraryStateFromTrash(t *testing.T) {
	root := "/lib"
	env := TrashRestoreEnv{RootDir: root, ITunesRoots: []string{"/media/iTunes"}}
	in := []BookFile{{FilePath: "/lib/A/B/b.m4b"}}
	cases := []struct {
		name     string
		state    *string
		recorded *string
		files    []BookFile
		env      TrashRestoreEnv
		want     string
	}{
		{"kept state is the pre-trash state", strp("organized_source"), strp("organized"), in, env, "organized_source"},
		{"recorded state wins over the label", strp("deleted"), strp("organized"), in, env, "organized"},
		{"recorded imported stays imported", strp("deleted"), strp("imported"), in, env, "imported"},
		{"label is case-insensitive", strp("Deleted"), strp("organized"), in, env, "organized"},
		{"nil state uses the record", nil, strp("organized"), in, env, "organized"},
		// "organized" is what ABS lists, so it is never restored on a row
		// whose present files do not prove it: kept, recorded or legacy.
		{"recorded organized with no files is imported", strp("deleted"), strp("organized"), nil, env, "imported"},
		{"kept organized with no files is imported (combine shell)", strp("organized"), nil, nil, env, "imported"},
		// Missing does not demote (re-review of #3649, finding 3): an
		// organized book whose files are all marked Missing stays in
		// repoint-missing-to-folder-audio's ABSLibraryFilter scope.
		{"kept organized with only missing files under the root stays", strp("organized"), nil, []BookFile{{FilePath: "/lib/x.m4b", Missing: true}}, env, "organized"},
		{"kept organized with a missing file in the iTunes library is imported", strp("organized"), nil, append(in, BookFile{FilePath: "/media/iTunes/a.m4b", Missing: true}), TrashRestoreEnv{RootDir: "/", ITunesRoots: []string{"/media/iTunes"}}, "imported"},
		{"kept organized with a missing file with no path is imported", strp("organized"), nil, append(in, BookFile{FilePath: "", Missing: true}), env, "imported"},
		{"kept organized with a file outside the root is imported", strp("organized"), nil, append(in, BookFile{FilePath: "/elsewhere/c.m4b"}), env, "imported"},
		{"kept organized in the iTunes library is imported", strp("organized"), nil, []BookFile{{FilePath: "/media/iTunes/a.m4b"}}, TrashRestoreEnv{RootDir: "/media", ITunesRoots: []string{"/media/iTunes"}}, "imported"},
		{"kept organized with present files inside the root stays", strp("organized"), nil, in, env, "organized"},
		{"kept organized wins over a stale record", strp("organized"), strp("imported"), in, env, "organized"},
		{"recorded organized with a file outside the root is imported", strp("deleted"), strp("organized"), []BookFile{{FilePath: "/elsewhere/c.m4b"}}, env, "imported"},
		{"a recorded deleted is ignored", strp("deleted"), strp("deleted"), nil, env, "imported"},
		{"legacy: present files inside the root", strp("deleted"), nil, in, env, "organized"},
		{"legacy: a file outside the root", strp("deleted"), nil, append(in, BookFile{FilePath: "/elsewhere/c.m4b"}), env, "imported"},
		{"legacy: a missing file outside the root is imported", strp("deleted"), nil, append(in, BookFile{FilePath: "/elsewhere/c.m4b", Missing: true}), env, "imported"},
		{"legacy: only missing files under the root", strp("deleted"), nil, []BookFile{{FilePath: "/lib/x.m4b", Missing: true}}, env, "organized"},
		{"legacy: no files", strp("deleted"), nil, nil, env, "imported"},
		{"legacy: an empty file path", strp("deleted"), nil, []BookFile{{FilePath: ""}}, env, "imported"},
		{"legacy: frozen iTunes tree under the root", strp("deleted"), nil, []BookFile{{FilePath: "/lib/books/itunes/A/a.m4b"}}, TrashRestoreEnv{RootDir: "/lib"}, "imported"},
		{"legacy: configured iTunes root under the root", strp("deleted"), nil, []BookFile{{FilePath: "/lib/iTunes/a.m4b"}}, TrashRestoreEnv{RootDir: "/lib", ITunesRoots: []string{"/lib/iTunes"}}, "imported"},
		{"legacy: unresolved iTunes roots fail closed", strp("deleted"), nil, in, TrashRestoreEnv{RootDir: root, ITunesRootsUnknown: true}, "imported"},
		{"legacy: no library root", strp("deleted"), nil, in, TrashRestoreEnv{}, "imported"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			b := &Book{LibraryState: tc.state, PreTrashLibraryState: tc.recorded}
			RestoreLibraryStateFromTrash(b, tc.files, tc.env)
			if got := stateOf(b); got != tc.want {
				t.Errorf("state = %q, want %q", got, tc.want)
			}
			if b.PreTrashLibraryState != nil {
				t.Errorf("record not cleared")
			}
		})
	}
	RestoreLibraryStateFromTrash(nil, nil, env) // no panic
}

func TestRestoreBookFromTrash(t *testing.T) {
	env := TrashRestoreEnv{RootDir: "/lib"}
	now := time.Now()
	in := []BookFile{{FilePath: "/lib/a.m4b"}}

	// A trashed merge loser: bits cleared, unlinked, state restored.
	b := &Book{MarkedForDeletion: new(true), MarkedForDeletionAt: &now, LibraryState: strp("deleted"),
		PreTrashLibraryState: strp("organized"), MergedIntoBookID: strp("survivor")}
	if !RestoreBookFromTrash(b, in, env) {
		t.Errorf("a trashed row reported not restored")
	}
	if b.MarkedForDeletion == nil || *b.MarkedForDeletion || b.MarkedForDeletionAt != nil {
		t.Errorf("trash bits not cleared: %v %v", b.MarkedForDeletion, b.MarkedForDeletionAt)
	}
	if b.MergedIntoBookID != nil || stateOf(b) != "organized" {
		t.Errorf("merged=%v state=%q", b.MergedIntoBookID, stateOf(b))
	}

	// Only the label marks it trashed: still a restore.
	l := &Book{LibraryState: strp("deleted"), MergedIntoBookID: strp("s")}
	if !RestoreBookFromTrash(l, in, env) {
		t.Errorf("a label-only trashed row reported not restored")
	}
	if l.MergedIntoBookID != nil || stateOf(l) != "organized" {
		t.Errorf("label-only: merged=%v state=%q", l.MergedIntoBookID, stateOf(l))
	}
	RestoreBookFromTrash(nil, nil, env) // no panic
}

// A row that is not in the trash is left exactly as it is: "restoring" a live
// row must not write its deletion columns, unlink a live merge loser or
// relabel it (adversarial review of #3649, finding 1).
func TestRestoreBookFromTrash_LiveRowIsANoOp(t *testing.T) {
	env := TrashRestoreEnv{RootDir: "/lib"}
	for _, flag := range []*bool{nil, new(false)} {
		live := &Book{LibraryState: strp("imported"), MergedIntoBookID: strp("survivor"), MarkedForDeletion: flag,
			IsPrimaryVersion: new(true)}
		if RestoreBookFromTrash(live, []BookFile{{FilePath: "/lib/a.m4b"}}, env) {
			t.Errorf("flag %v: a live row reported restored", flag)
		}
		if live.MarkedForDeletion != flag {
			t.Errorf("flag %v: deletion flag rewritten to %v", flag, live.MarkedForDeletion)
		}
		if live.MergedIntoBookID == nil || stateOf(live) != "imported" {
			t.Errorf("flag %v: live row changed: merged=%v state=%q", flag, live.MergedIntoBookID, stateOf(live))
		}
	}
	if IsInTrash(&Book{MarkedForDeletion: new(false)}) || IsInTrash(&Book{}) || IsInTrash(nil) {
		t.Errorf("a live row (nil or false flag, no label) reads as in the trash")
	}
	if !IsInTrash(&Book{MarkedForDeletion: new(true)}) || !IsInTrash(&Book{LibraryState: strp(" Deleted ")}) {
		t.Errorf("a trashed row (flag or label) reads as live")
	}
}

// Re-review of #3649, finding 3: an organized book whose file rows are all
// marked Missing, under the library root, comes back "organized", so
// repoint-missing-to-folder-audio (scoped by ABSLibraryFilter) still repairs
// it. It is not ABS-listable as its own item: it has no audio to play.
func TestRestoreLibraryStateFromTrash_AllMissingUnderRootStaysOrganized(t *testing.T) {
	env := TrashRestoreEnv{RootDir: "/lib"}
	files := []BookFile{{FilePath: "/lib/A/a.m4b", Missing: true}, {FilePath: "/lib/A/b.m4b", Missing: true}}
	b := &Book{MarkedForDeletion: new(true), LibraryState: strp("organized")}
	if !RestoreBookFromTrash(b, files, env) {
		t.Fatal("a trashed row reported not restored")
	}
	if got := stateOf(b); got != "organized" {
		t.Fatalf("state = %q, want organized", got)
	}
	if !ABSLibraryFilter().Matches(b) {
		t.Fatal("the restored row must be back in ABSLibraryFilter scope (repoint-missing-to-folder-audio)")
	}
	if RestoredRowIsABSListable(b, files, env) {
		t.Fatal("a row with no present file is not listable as an item of its own")
	}
}

func TestRestoredRowIsABSListable(t *testing.T) {
	env := TrashRestoreEnv{RootDir: "/lib", ITunesRoots: []string{"/lib/iTunes"}}
	in := []BookFile{{FilePath: "/lib/A/a.m4b"}}
	live := func(state string, primary *bool) *Book {
		return &Book{MarkedForDeletion: new(false), LibraryState: strp(state), IsPrimaryVersion: primary}
	}
	cases := []struct {
		name  string
		b     *Book
		files []BookFile
		want  bool
	}{
		{"primary organized with a present file", live("organized", nil), in, true},
		{"explicit primary", live("organized", new(true)), in, true},
		{"non-primary (a MergeBooks loser)", live("organized", new(false)), in, false},
		{"imported", live("imported", nil), in, false},
		{"no files (a combine shell)", live("organized", nil), nil, false},
		{"only missing files", live("organized", nil), []BookFile{{FilePath: "/lib/A/a.m4b", Missing: true}}, false},
		{"a present file in the iTunes library", live("organized", nil), append(in, BookFile{FilePath: "/lib/iTunes/a.m4b"}), false},
		{"still in the trash", &Book{MarkedForDeletion: new(true), LibraryState: strp("organized")}, in, false},
		{"nil", nil, in, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := RestoredRowIsABSListable(tc.b, tc.files, env); got != tc.want {
				t.Errorf("RestoredRowIsABSListable = %v, want %v", got, tc.want)
			}
		})
	}
}
