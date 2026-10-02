// file: internal/database/book_trash_test.go
// version: 1.0.0
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
		{"recorded state wins over the label", strp("deleted"), strp("organized"), nil, env, "organized"},
		{"recorded imported stays imported", strp("deleted"), strp("imported"), in, env, "imported"},
		{"label is case-insensitive", strp("Deleted"), strp("organized"), nil, env, "organized"},
		{"nil state uses the record", nil, strp("organized"), nil, env, "organized"},
		{"a recorded deleted is ignored", strp("deleted"), strp("deleted"), nil, env, "imported"},
		{"legacy: present files inside the root", strp("deleted"), nil, in, env, "organized"},
		{"legacy: a file outside the root", strp("deleted"), nil, append(in, BookFile{FilePath: "/elsewhere/c.m4b"}), env, "imported"},
		{"legacy: a missing file outside the root is ignored", strp("deleted"), nil, append(in, BookFile{FilePath: "/elsewhere/c.m4b", Missing: true}), env, "organized"},
		{"legacy: no present files", strp("deleted"), nil, []BookFile{{FilePath: "/lib/x.m4b", Missing: true}}, env, "imported"},
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

	// A trashed merge loser: bits cleared, unlinked, state restored.
	b := &Book{MarkedForDeletion: new(true), MarkedForDeletionAt: &now, LibraryState: strp("deleted"),
		PreTrashLibraryState: strp("organized"), MergedIntoBookID: strp("survivor")}
	RestoreBookFromTrash(b, nil, env)
	if b.MarkedForDeletion == nil || *b.MarkedForDeletion || b.MarkedForDeletionAt != nil {
		t.Errorf("trash bits not cleared: %v %v", b.MarkedForDeletion, b.MarkedForDeletionAt)
	}
	if b.MergedIntoBookID != nil || stateOf(b) != "organized" {
		t.Errorf("merged=%v state=%q", b.MergedIntoBookID, stateOf(b))
	}

	// Only the label marks it trashed: still a restore.
	l := &Book{LibraryState: strp("deleted"), MergedIntoBookID: strp("s")}
	RestoreBookFromTrash(l, []BookFile{{FilePath: "/lib/a.m4b"}}, env)
	if l.MergedIntoBookID != nil || stateOf(l) != "organized" {
		t.Errorf("label-only: merged=%v state=%q", l.MergedIntoBookID, stateOf(l))
	}

	// A live merge loser is not in the trash: only the explicit false is
	// written; it stays linked and keeps its state.
	live := &Book{LibraryState: strp("imported"), MergedIntoBookID: strp("survivor")}
	RestoreBookFromTrash(live, []BookFile{{FilePath: "/lib/a.m4b"}}, env)
	if live.MarkedForDeletion == nil || *live.MarkedForDeletion {
		t.Errorf("explicit false not written")
	}
	if live.MergedIntoBookID == nil || stateOf(live) != "imported" {
		t.Errorf("live row changed: merged=%v state=%q", live.MergedIntoBookID, stateOf(live))
	}
	RestoreBookFromTrash(nil, nil, env) // no panic
}
