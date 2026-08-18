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

// 🔴 "I COULD NOT TELL" IS NOT "IT IS NOT THERE".
//
// This is the op's most important safety property and the one a reader is most
// likely to simplify away, because folding unreadable into missing makes the code
// shorter and every other test still passes. It must not be folded: the Missing
// count is what a bulk repair gets sized from, so a single unmounted share or a
// permissions fault would otherwise report the ENTIRE library as lost and invite a
// mass deletion of rows whose files are fine.
//
// The lever is a path containing a NUL byte. Go rejects it before the syscall, so
// os.Stat returns "invalid argument" identically on every platform — no dependence
// on filesystem permissions or on name-length limits that differ between a macOS
// dev box and a Linux CI container.
func TestMissingFileAudit_UnreadableIsNotCountedAsMissing(t *testing.T) {
	_, rows := auditFixture(t)
	rows = append(rows, database.BookFileCore{
		ID: "f8", BookID: "unreadable", FilePath: "/tmp/cannot\x00stat.m4b",
	})
	got := runAudit(t, rows, missingFileAuditParams{})

	if got.Unreadable != 1 {
		t.Errorf("Unreadable = %d, want 1 — a stat that failed for any reason other than "+
			"absence must be reported as undetermined", got.Unreadable)
	}
	if got.Missing != 3 {
		t.Errorf("Missing = %d, want 3 — the unreadable row must NOT inflate the missing "+
			"count that a repair is sized from", got.Missing)
	}
	// And it must not be silently counted as present either, which would hide it.
	if got.Present != 3 {
		t.Errorf("Present = %d, want 3", got.Present)
	}
	// A book whose only row is undetermined is not a book with no files left.
	if got.BooksAllGone != 1 {
		t.Errorf("BooksAllGone = %d, want 1 — the undetermined book must not be declared "+
			"fully broken", got.BooksAllGone)
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

// The identity-signal census decides which repairs are even possible, so it is
// pinned per-arm and per-field rather than in aggregate.
//
// 🔴 THE TWO ARMS CARRY DELIBERATELY DIFFERENT SIGNAL MIXES. The present rows are
// not filler: they are decoys. If the missing and present tallies were ever
// swapped, or folded into one counter, every assertion below moves — which is the
// only reason this test can detect that class of mistake at all. A fixture where
// both arms looked alike would pass just as happily with the arms crossed.
func TestMissingFileAudit_SignalCensusSeparatesMissingFromPresent(t *testing.T) {
	dir := t.TempDir()
	write := func(name string) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte("x"), 0o600); err != nil {
			t.Fatalf("write fixture %s: %v", name, err)
		}
		return p
	}
	gone := func(name string) string { return filepath.Join(dir, name) }

	fp := 120.0
	rows := []database.BookFileCore{
		// PRESENT arm — both hashes, no fingerprint.
		{ID: "p1", BookID: "b-present", FilePath: write("p1.m4b"),
			FileHash: "h1", OriginalFileHash: "o1", Duration: 100, FileSize: 10},
		{ID: "p2", BookID: "b-present", FilePath: write("p2.m4b"),
			FileHash: "h2", OriginalFileHash: "o2", Duration: 200, FileSize: 20},

		// MISSING arm — a different mix, so a swap cannot go unnoticed.
		{ID: "m1", BookID: "b-missing", FilePath: gone("m1.m4b"), FileHash: "mh1"},
		{ID: "m2", BookID: "b-missing", FilePath: gone("m2.m4b"), AcoustIDFingerprintDurationSec: fp},
		// Carries BOTH kinds of decisive signal — AnyDecisive must count it ONCE.
		{ID: "m3", BookID: "b-missing", FilePath: gone("m3.m4b"),
			FileHash: "mh3", AcoustIDFingerprintDurationSec: fp},
		// Carries NOTHING. This is the row that makes AnyDecisive a real measurement
		// rather than a restatement of the row count.
		{ID: "m4", BookID: "b-missing", FilePath: gone("m4.m4b")},
	}

	rep := runAudit(t, rows, missingFileAuditParams{})

	for _, tc := range []struct {
		name string
		got  int
		want int
	}{
		{"missing.Rows", rep.SignalsMissing.Rows, 4},
		{"missing.FileHash", rep.SignalsMissing.FileHash, 2},
		{"missing.Fingerprint", rep.SignalsMissing.Fingerprint, 2},
		{"missing.OriginalFileHash", rep.SignalsMissing.OriginalFileHash, 0},
		{"missing.Duration", rep.SignalsMissing.Duration, 0},
		// 3, not 4: m4 has no decisive signal. And 3, not 5: m3 has two and counts once.
		{"missing.AnyDecisive", rep.SignalsMissing.AnyDecisive, 3},

		{"present.Rows", rep.SignalsPresent.Rows, 2},
		{"present.FileHash", rep.SignalsPresent.FileHash, 2},
		{"present.OriginalFileHash", rep.SignalsPresent.OriginalFileHash, 2},
		{"present.Fingerprint", rep.SignalsPresent.Fingerprint, 0},
		{"present.Duration", rep.SignalsPresent.Duration, 2},
		{"present.AnyDecisive", rep.SignalsPresent.AnyDecisive, 2},
	} {
		if tc.got != tc.want {
			t.Errorf("%s = %d, want %d", tc.name, tc.got, tc.want)
		}
	}
}

// pct must not divide by zero on an empty arm — an audit scoped by path_prefix to
// a tree with no missing rows is a normal, expected outcome, not an error.
func TestMissingFileSignals_PctHandlesEmptyArm(t *testing.T) {
	var s missingFileSignals
	if got := s.pct(0); got != "n/a" {
		t.Errorf("pct on empty arm = %q, want %q", got, "n/a")
	}
	s.Rows, s.FileHash = 4, 1
	if got := s.pct(s.FileHash); got != "25.0%" {
		t.Errorf("pct(1 of 4) = %q, want %q", got, "25.0%")
	}
}
