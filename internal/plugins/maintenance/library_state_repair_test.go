// file: internal/plugins/maintenance/library_state_repair_test.go
// version: 1.0.0
// guid: 3e8b1c47-90fd-4a26-b5e1-6c7a24f0d913
// last-edited: 2026-09-20

package maintenance

import (
	"errors"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

const (
	lsrTestRoot   = "/mnt/bigdata/books/audiobook-organizer"
	lsrTestITunes = "/mnt/bigdata/books/itunes"
)

func lsrBook(id, path, state string, hash bool) database.BookCore {
	b := database.BookCore{ID: id, FilePath: path}
	if state != "" {
		s := state
		b.LibraryState = &s
	}
	if hash {
		h := "deadbeef"
		b.OrganizedFileHash = &h
	}
	return b
}

func TestClassifyLibraryStateRepair(t *testing.T) {
	roots := []string{lsrTestITunes}
	organizedPath := lsrTestRoot + "/Some Author/Some Book/01.mp3"

	cases := []struct {
		name        string
		book        database.BookCore
		includeSusp bool
		wantKeep    bool
		wantOutcome string
	}{
		{
			name:     "stale imported under libroot with hash is repaired",
			book:     lsrBook("b1", organizedPath, libStateImported, true),
			wantKeep: true,
		},
		{
			// The evidence gate: under libroot is not by itself proof the book
			// was ever organized.
			name:        "imported under libroot WITHOUT hash is left alone",
			book:        lsrBook("b2", organizedPath, libStateImported, false),
			wantOutcome: lsrNoOrganizedHash,
		},
		{
			// The demoted source of an organized copy. Its state is correct and
			// repairing it would make the wrong copy visible.
			name:        "organized_source is never repaired even with hash under libroot",
			book:        lsrBook("b3", organizedPath, libStateOrganizedSource, true),
			wantOutcome: lsrOrganizedSource,
		},
		{
			name:        "suspicious is excluded by default",
			book:        lsrBook("b4", organizedPath, libStateSuspicious, true),
			wantOutcome: lsrSuspiciousExcluded,
		},
		{
			name:        "suspicious is repaired only when explicitly included",
			book:        lsrBook("b5", organizedPath, libStateSuspicious, true),
			includeSusp: true,
			wantKeep:    true,
		},
		{
			name:        "itunes path is hands off",
			book:        lsrBook("b6", lsrTestITunes+"/iTunes Media/x/01.m4b", libStateImported, true),
			wantOutcome: lsrITunes,
		},
		{
			name:        "outside libroot is not repaired",
			book:        lsrBook("b7", "/mnt/bigdata/books/newbooks/x/01.mp3", libStateImported, true),
			wantOutcome: lsrNotUnderLibroot,
		},
		{
			name:        "already organized is reported, not rewritten",
			book:        lsrBook("b8", organizedPath, libStateOrganized, true),
			wantOutcome: lsrAlreadyOrganized,
		},
		{
			// Allowlist, not blocklist.
			name:        "unknown state is never guessed",
			book:        lsrBook("b9", organizedPath, "quarantined", true),
			wantOutcome: lsrStateNotAllowed,
		},
		{
			name:        "nil state is never guessed",
			book:        lsrBook("b10", organizedPath, "", true),
			wantOutcome: lsrStateNotAllowed,
		},
		{
			name:        "empty path",
			book:        lsrBook("b11", "", libStateImported, true),
			wantOutcome: lsrEmptyPath,
		},
		{
			// A sibling directory whose name merely starts with the root's must
			// not be swept in: containment is a separator-boundary test.
			name:        "path sharing a prefix but not a component boundary",
			book:        lsrBook("b12", lsrTestRoot+"-backup/Author/01.mp3", libStateImported, true),
			wantOutcome: lsrNotUnderLibroot,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			params := libraryStateRepairParams{IncludeSuspicious: tc.includeSusp}
			ch, keep := classifyLibraryStateRepair(tc.book, params, lsrTestRoot, roots)
			if keep != tc.wantKeep {
				t.Fatalf("keep=%v want %v (outcome %q)", keep, tc.wantKeep, ch.Outcome)
			}
			if !tc.wantKeep && ch.Outcome != tc.wantOutcome {
				t.Fatalf("outcome=%q want %q", ch.Outcome, tc.wantOutcome)
			}
		})
	}
}

// lsrFakeStore emulates ModifyBook's three nil-error answers: it wrote, it hit
// ErrSkipBookWrite (returning the unmodified row), or the row does not exist
// (returning nil, nil).
type lsrFakeStore struct {
	rows    map[string]*database.Book
	failErr error
}

func (s *lsrFakeStore) GetAllBooksCoreComplete(int, int) ([]database.BookCore, error) {
	return nil, nil
}

func (s *lsrFakeStore) ModifyBook(id string, fn func(*database.Book) error) (*database.Book, error) {
	if s.failErr != nil {
		return nil, s.failErr
	}
	row, ok := s.rows[id]
	if !ok {
		return nil, nil
	}
	if err := fn(row); err != nil {
		if errors.Is(err, database.ErrSkipBookWrite) {
			return row, nil
		}
		return nil, err
	}
	return row, nil
}

func lsrRow(state string, hash bool) *database.Book {
	b := &database.Book{}
	s := state
	b.LibraryState = &s
	if hash {
		h := "deadbeef"
		b.OrganizedFileHash = &h
	}
	return b
}

func TestApplyLibraryStateRepair_WritesOrganized(t *testing.T) {
	store := &lsrFakeStore{rows: map[string]*database.Book{"b1": lsrRow(libStateImported, true)}}
	got := applyLibraryStateRepair(store, libraryStateRepairChange{BookID: "b1", OldState: libStateImported})
	if got.Outcome != lsrRepaired {
		t.Fatalf("outcome=%q want %q", got.Outcome, lsrRepaired)
	}
	if s := store.rows["b1"].LibraryState; s == nil || *s != libStateOrganized {
		t.Fatalf("row not written: %v", s)
	}
}

// The scope snapshot is minutes old by the time the last row is written, and
// this op is not serialized against the scan or organize paths that also write
// this column. A row whose state moved must be reported, never clobbered.
func TestApplyLibraryStateRepair_DoesNotClobberAChangedState(t *testing.T) {
	store := &lsrFakeStore{rows: map[string]*database.Book{"b1": lsrRow(libStateOrganizedSource, true)}}
	got := applyLibraryStateRepair(store, libraryStateRepairChange{BookID: "b1", OldState: libStateImported})
	if got.Outcome != lsrChangedSinceScan {
		t.Fatalf("outcome=%q want %q", got.Outcome, lsrChangedSinceScan)
	}
	if s := store.rows["b1"].LibraryState; s == nil || *s != libStateOrganizedSource {
		t.Fatalf("a changed row was clobbered: %v", s)
	}
}

// The hash is the evidence the whole repair rests on; losing it between scan
// and write disqualifies the row.
func TestApplyLibraryStateRepair_RefusesWhenHashVanished(t *testing.T) {
	store := &lsrFakeStore{rows: map[string]*database.Book{"b1": lsrRow(libStateImported, false)}}
	got := applyLibraryStateRepair(store, libraryStateRepairChange{BookID: "b1", OldState: libStateImported})
	if got.Outcome != lsrChangedSinceScan {
		t.Fatalf("outcome=%q want %q", got.Outcome, lsrChangedSinceScan)
	}
	if s := store.rows["b1"].LibraryState; s == nil || *s != libStateImported {
		t.Fatalf("row was written without its evidence: %v", s)
	}
}

func TestApplyLibraryStateRepair_RowGone(t *testing.T) {
	store := &lsrFakeStore{rows: map[string]*database.Book{}}
	got := applyLibraryStateRepair(store, libraryStateRepairChange{BookID: "missing", OldState: libStateImported})
	if got.Outcome != lsrRowGone {
		t.Fatalf("outcome=%q want %q", got.Outcome, lsrRowGone)
	}
}

func TestApplyLibraryStateRepair_WriteFailedIsNotSuccess(t *testing.T) {
	store := &lsrFakeStore{failErr: errors.New("boom")}
	got := applyLibraryStateRepair(store, libraryStateRepairChange{BookID: "b1", OldState: libStateImported})
	if got.Outcome != lsrWriteFailed {
		t.Fatalf("outcome=%q want %q", got.Outcome, lsrWriteFailed)
	}
}

func TestLibraryStateRepairParams_DryRunDefaultsTrue(t *testing.T) {
	var p libraryStateRepairParams
	if !p.dryRun() {
		t.Fatal("an omitted dry_run must read as a dry run")
	}
	f := false
	p.DryRun = &f
	if p.dryRun() {
		t.Fatal("explicit dry_run:false must write")
	}
}
