// file: internal/organizer/landing.go
// version: 1.1.0
// guid: 5c1e9a3b-7d42-4f6e-9b8a-2e0c4d7f1a35
// last-edited: 2026-09-02

package organizer

// Landing is what an organize physically produced for one book: the answer to
// "where DID the files go", as distinct from a plan's "where WOULD they go".
//
// It exists because the two used to be conflated. OrganizeBookDirectory copied
// files and returned a source->destination map, OrganizeDirectoryBook threw
// the map away and returned only the directory, and CreateOrganizedVersion then
// RECOMPUTED the plan and adopted whatever file existed at each planned target.
// When two books planned the same target — same title, same author pattern —
// the loser's organize skipped the occupied path, and CreateOrganizedVersion
// pointed the loser's book_file row at the winner's audio, counted as success.
// Threading the landing through instead of re-deriving it removes the seam.
type Landing struct {
	// Path is the book's new path: the target directory for a multi-file book,
	// the organized file otherwise.
	Path string

	// Files maps each source path that LANDED to its organized path, for a
	// directory book. A landed file was either written by this organize or
	// adopted because the occupant was proven (by hash) to be this file. nil
	// for a single-file book; IsDir reports on that.
	Files map[string]string

	// Created lists the paths this organize WROTE — the subset of Files values
	// (or Path, for a single file) that did not exist before it ran. A rollback
	// may remove exactly these and nothing else: an adopted file was there
	// first and some other row may name it.
	Created []string

	// InPlace reports that the book was already under the library root and
	// was RENAMED there (ReOrganizeInPlace rewrote its existing rows) rather
	// than copied into a new organized version. Callers branch on this to
	// decide whether to stamp the existing book or create a version row for
	// it; they must never re-derive it from a RootDir prefix test of their
	// own. The HTTP handler did exactly that against a RootDir snapshot taken
	// at startup while OrganizeOneBook read the live value, and after a
	// runtime root_dir change the two disagreed: the file was moved in place
	// and then a second book row was created at the same path.
	InPlace bool
}

// IsDir reports whether the landing describes a multi-file (directory) book.
func (l *Landing) IsDir() bool { return l != nil && l.Files != nil }
