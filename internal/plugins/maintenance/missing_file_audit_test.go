// file: internal/plugins/maintenance/missing_file_audit_test.go
// version: 1.0.0
// guid: 5c8e2a17-96d4-4b3f-a70e-1d92f4c6b085
// last-edited: 2026-08-17

package maintenance

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// auditFixture builds REAL files on disk and REAL absent paths beside them, and
// returns rows pointing at both.
//
// 🔴 THE FILES ARE ACTUALLY CREATED. This op's entire output is the result of
// os.Stat, so a fixture that faked the filesystem would be testing the fake and
// would pass just as happily with the stat inverted. A tmpdir costs nothing and
// makes the assertions mean what they say.
func auditFixture(t *testing.T) (dir string, rows []database.BookFileCore) {
	t.Helper()
	dir = t.TempDir()
	write := func(name string) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte("x"), 0o600); err != nil {
			t.Fatalf("write fixture %s: %v", name, err)
		}
		return p
	}
	return dir, []database.BookFileCore{
		// "intact" — every file present.
		{ID: "f1", BookID: "intact", FilePath: write("intact-1.m4b")},
		{ID: "f2", BookID: "intact", FilePath: write("intact-2.m4b")},
		// "partial" — the live shape: a phantom row beside a surviving real file.
		{ID: "f3", BookID: "partial", FilePath: filepath.Join(dir, "gone.m4b")},
		{ID: "f4", BookID: "partial", FilePath: write("partial-real.m4b")},
		// "allgone" — nothing survives.
		{ID: "f5", BookID: "allgone", FilePath: filepath.Join(dir, "vanished-1.m4b")},
		{ID: "f6", BookID: "allgone", FilePath: filepath.Join(dir, "vanished-2.m4b")},
	}
}

func runAudit(t *testing.T, rows []database.BookFileCore, params missingFileAuditParams) missingFileReport {
	t.Helper()
	store := &database.MockStore{
		GetAllBookFilesCoreFunc: func() ([]database.BookFileCore, error) { return rows, nil },
	}
	rep, err := auditMissingFiles(context.Background(), store, params, &fakeReporter{})
	if err != nil {
		t.Fatalf("auditMissingFiles: %v", err)
	}
	return rep
}

// The counts are the whole product of this op and a destructive repair will be
// sized from them, so they are pinned exactly rather than loosely.
func TestMissingFileAudit_CountsMissingAndPresent(t *testing.T) {
	_, rows := auditFixture(t)
	got := runAudit(t, rows, missingFileAuditParams{})

	for _, c := range []struct {
		name      string
		got, want int
	}{
		{"TotalRows", got.TotalRows, 6},
		{"Missing", got.Missing, 3},
		{"Present", got.Present, 3},
		{"Unreadable", got.Unreadable, 0},
		{"BooksTotal", got.BooksTotal, 3},
	} {
		if c.got != c.want {
			t.Errorf("%s = %d, want %d", c.name, c.got, c.want)
		}
	}
}

// 🔴 A BOOK WITH ONE DEAD FILE IS NOT A BOOK WITH NO FILES LEFT.
//
// The partially-broken books can be repaired by dropping the phantom row; the
// fully-broken ones cannot, because dropping every row leaves a book with nothing
// at all. Collapsing the two categories would let the report recommend a repair
// that destroys the second group — which is the only reason the distinction is
// computed.
func TestMissingFileAudit_SeparatesFullyBrokenFromPartiallyBroken(t *testing.T) {
	_, rows := auditFixture(t)
	got := runAudit(t, rows, missingFileAuditParams{})

	if got.BooksAllGone != 1 {
		t.Errorf("BooksAllGone = %d, want 1 (the book whose every file is missing)", got.BooksAllGone)
	}
	if got.BooksPartial != 1 {
		t.Errorf("BooksPartial = %d, want 1 (the book with a phantom row beside a real file)", got.BooksPartial)
	}
	if got.BooksIntact != 1 {
		t.Errorf("BooksIntact = %d, want 1", got.BooksIntact)
	}
}

// path_prefix is what makes the op usable one tree at a time — the live finding
// was that missing rows are confined to a single tree, and confirming that needs a
// restricted sweep.
func TestMissingFileAudit_PathPrefixRestrictsTheSweep(t *testing.T) {
	dir, rows := auditFixture(t)

	none := runAudit(t, rows, missingFileAuditParams{PathPrefix: filepath.Join(dir, "no-such-subtree")})
	if none.TotalRows != 0 {
		t.Errorf("a prefix matching nothing swept %d rows, want 0", none.TotalRows)
	}
	// 🔴 ANTI-VACUOUS: the assertion above would also pass if the sweep were simply
	// broken, so prove the same fixture is fully visible without the prefix.
	all := runAudit(t, rows, missingFileAuditParams{PathPrefix: dir})
	if all.TotalRows != 6 {
		t.Errorf("prefix matching the fixture dir swept %d rows, want 6", all.TotalRows)
	}
}

// A row with no path at all is a different defect. Counting it as missing bytes
// would inflate the number a bulk repair gets sized from.
func TestMissingFileAudit_SkipsRowsWithNoPath(t *testing.T) {
	_, rows := auditFixture(t)
	rows = append(rows, database.BookFileCore{ID: "f7", BookID: "nopath", FilePath: "   "})
	got := runAudit(t, rows, missingFileAuditParams{})

	if got.TotalRows != 6 {
		t.Errorf("TotalRows = %d, want 6 — the blank-path row must not be swept", got.TotalRows)
	}
	if got.Missing != 3 {
		t.Errorf("Missing = %d, want 3 — a blank path is not missing bytes", got.Missing)
	}
}

// The sample is what lets a human sanity-check the number before acting on it, so
// it must actually name the missing paths and not, say, the present ones.
func TestMissingFileAudit_SampleNamesMissingPathsOnly(t *testing.T) {
	dir, rows := auditFixture(t)
	got := runAudit(t, rows, missingFileAuditParams{})

	if len(got.Sample) != 3 {
		t.Fatalf("Sample has %d entries, want 3", len(got.Sample))
	}
	present := map[string]bool{
		filepath.Join(dir, "intact-1.m4b"):     true,
		filepath.Join(dir, "intact-2.m4b"):     true,
		filepath.Join(dir, "partial-real.m4b"): true,
	}
	for _, s := range got.Sample {
		if present[s] {
			t.Errorf("Sample names %q, which EXISTS — the sample must list missing paths", s)
		}
	}
}

// missingPathRoot is what turned "41% of files are gone" into "every one of them is
// in the organizer's own tree and none are in the iTunes tree" — the same number,
// but the second one names a cause.
func TestMissingPathRoot_GroupsByLibraryTree(t *testing.T) {
	cases := map[string]string{
		"/mnt/bigdata/books/audiobook-organizer/Morgan Rice/x/y.m4b": "/mnt/bigdata/books/audiobook-organizer",
		"/mnt/bigdata/books/itunes/iTunes Media/Audiobooks/z.m4b":    "/mnt/bigdata/books/itunes",
		"/short/path.m4b": "/short/path.m4b",
	}
	for in, want := range cases {
		if got := missingPathRoot(in); got != want {
			t.Errorf("missingPathRoot(%q) = %q, want %q", in, got, want)
		}
	}
}
