// file: internal/plugins/maintenance/relink_pre_fix_imports_safety_test.go
// version: 1.2.0
// guid: 17b88502-dcdd-4723-9e9b-a13d2548dac6
// last-edited: 2026-10-11

package maintenance

// The relink pass's safety checks, each with a test that fails when the check
// is removed: the in-lock re-plan (R1), adopt-only (R2), the link write's
// compare-and-set (R3a/R3b), pfrSameBook's compare-and-set (R4), the
// same-book holder's status (M10), and the per-row link evidence (S1).
// R1-R4 come from the round-1 review of the branch; R3 is split in two because
// the compare-and-set refuses a row whose path moved (see R3b). The round-2
// review added RV4a/RV4b (adopt-only holds the row to the plan, not only to
// CreateIngestVersion's own read) and the empty version format (B2).

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/repairs"
	"github.com/falkcorp/audiobook-organizer/internal/versions"
)

// pfrFreshDecision plans the fixture and returns the fresh (re-planned) row
// for role and its decision.
func pfrFreshDecision(t *testing.T, f *fragFixture, role string) (repairs.Row, *pfrDecision) {
	t.Helper()
	fx := newRelinkPreFixFixer(f.p)
	res := f.fixerPlan(t, relinkPreFixFixerID, "op-plan", nil)
	fresh, err := fx.Replan(context.Background(), nil, findRow(t, res, f.rowIDs[role]), nil)
	require.NoError(t, err)
	d, ok := fresh.Detail.(*pfrDecision)
	require.True(t, ok)
	return fresh, d
}

// R1: a row planned to link V, whose book since had V trashed and a new
// unnamed V2 appear, reaches Apply with the stale fresh row. Apply's in-lock
// re-plan must refuse it rather than link V2, a version the owner never
// reviewed. Removing the in-lock fingerprint compare links V2.
func TestRelinkPreFixImports_InLockReplanRefusesNewTarget(t *testing.T) {
	t.Parallel()
	f := newPreFixFixture(t)
	book, v := f.preFixImport(t, "r1", "")
	fx := newRelinkPreFixFixer(f.p)
	fresh, d := pfrFreshDecision(t, f, "r1")
	require.Equal(t, pfrClassLink, d.class)

	v.Status = database.BookVersionStatusTrash
	require.NoError(t, f.s.UpdateBookVersion(v))
	v2 := f.version(t, book, "")

	w := repairs.NewWriter(f.s, f.s, relinkPreFixFixerID, "bulk_update", "repairs-").WithJournal(f.s, f.s, "op-apply")
	f.applyOp("op-apply", relinkPreFixFixerID)
	err := fx.Apply(context.Background(), w, fresh)
	require.True(t, errors.Is(err, repairs.ErrChangedSincePlan), "err=%v", err)
	require.Empty(t, f.bookFile(t, "r1").VersionID, "linked (v2=%s)", v2.ID)
}

// R2: pfrAdopt handed a decision whose target is no longer the book's
// stranded version must refuse and create nothing. Dropping AdoptVersionID
// (here, or its check in CreateIngestVersion) creates a second version.
func TestRelinkPreFixImports_AdoptNeverCreates(t *testing.T) {
	t.Parallel()
	f := newPreFixFixture(t)
	book, v := f.preFixImport(t, "r2", "")
	_, d := pfrFreshDecision(t, f, "r2")

	v.Status = database.BookVersionStatusTrash
	require.NoError(t, f.s.UpdateBookVersion(v))

	err := pfrAdopt(f.s, d, "")
	require.True(t, errors.Is(err, repairs.ErrChangedSincePlan), "err=%v", err)
	vers, err := f.s.GetBookVersionsByBookID(book)
	require.NoError(t, err)
	require.Len(t, vers, 1, "pfrAdopt created a version")
	require.Empty(t, f.bookFile(t, "r2").VersionID)
}

// pfrInterleaveStore runs hook once, the first time CreateIngestVersion lists
// the book's versions: after it read the row (GetBookFiles) and before its
// link write. That is where any writer that takes neither the merge lock nor
// the torrent lock can land.
type pfrInterleaveStore struct {
	versions.IngestStore
	once sync.Once
	hook func()
}

func (s *pfrInterleaveStore) GetBookVersionsByBookID(id string) ([]database.BookVersion, error) {
	s.once.Do(s.hook)
	return s.IngestStore.GetBookVersionsByBookID(id)
}

// R3a: a field the link does not depend on, committed by another writer
// inside the adopt's read-to-write window, survives, and the link lands.
// The whole-row UpdateBookFile write-back the link used to be reverted it.
func TestRelinkPreFixImports_AdoptKeepsConcurrentRowEdit(t *testing.T) {
	t.Parallel()
	f := newPreFixFixture(t)
	book, v := f.preFixImport(t, "r3a", "")
	_, d := pfrFreshDecision(t, f, "r3a")

	is := &pfrInterleaveStore{IngestStore: f.s, hook: func() {
		_, merr := f.s.ModifyBookFile(book, f.rowIDs["r3a"], func(r *database.BookFile) error {
			r.TrackNumber, r.Duration = 7, 4242
			return nil
		})
		require.NoError(t, merr)
	}}
	require.NoError(t, pfrAdopt(is, d, "precomputed-hash"))
	row := f.bookFile(t, "r3a")
	require.Equal(t, v.ID, row.VersionID)
	require.Equal(t, "precomputed-hash", row.FileHash)
	require.Equal(t, 7, row.TrackNumber, "concurrent TrackNumber write was reverted by the adopt write")
	require.Equal(t, 4242, row.Duration, "concurrent Duration write was reverted by the adopt write")
}

// R3b: the row is repointed inside the window (with another field). The
// owner approved linking the file at the planned path, so the link is
// refused as changed, nothing is written, and both concurrent edits survive.
// (The review's R3 asserted the link lands here; with FilePath in the
// compare-and-set it must not.)
func TestRelinkPreFixImports_AdoptRefusesRowRepointedConcurrently(t *testing.T) {
	t.Parallel()
	f := newPreFixFixture(t)
	book, _ := f.preFixImport(t, "r3b", "")
	_, d := pfrFreshDecision(t, f, "r3b")

	moved := f.file(t, "library/r3b-moved.m4b", 999)
	is := &pfrInterleaveStore{IngestStore: f.s, hook: func() {
		_, merr := f.s.ModifyBookFile(book, f.rowIDs["r3b"], func(r *database.BookFile) error {
			r.FilePath, r.TrackNumber = moved, 7
			return nil
		})
		require.NoError(t, merr)
	}}
	err := pfrAdopt(is, d, "precomputed-hash")
	require.True(t, errors.Is(err, repairs.ErrChangedSincePlan), "err=%v", err)
	require.True(t, errors.Is(err, versions.ErrBookFileChanged), "err=%v", err)
	row := f.bookFile(t, "r3b")
	require.Empty(t, row.VersionID)
	require.Empty(t, row.FileHash)
	require.Equal(t, moved, row.FilePath, "concurrent FilePath write was reverted")
	require.Equal(t, 7, row.TrackNumber, "concurrent TrackNumber write was reverted")
}

// R4: pfrSameBook handed a decision for a row that has since moved (another
// writer changed its path) must refuse, not link.
func TestRelinkPreFixImports_SameBookRefusesMovedRow(t *testing.T) {
	t.Parallel()
	f := newPreFixFixture(t)
	same := f.book(t, "s1", "Placeholder Two Files", f.path("imports/s1.m4b"), nil)
	sv := f.version(t, same, pfrHashSame)
	f.importRow(t, "s1", same, pfrHashSame, sv.ID)
	f.importRow(t, "s2", same, pfrHashSame, "")
	f.ids["s2"] = same
	_, d := pfrFreshDecision(t, f, "s2")
	require.Equal(t, pfrClassSameBook, d.class)

	moved := f.file(t, "library/s2-moved.m4b", 999)
	_, err := f.s.ModifyBookFile(same, f.rowIDs["s2"], func(r *database.BookFile) error { r.FilePath = moved; return nil })
	require.NoError(t, err)
	err = pfrSameBook(f.s, d, "")
	require.True(t, errors.Is(err, repairs.ErrChangedSincePlan), "err=%v", err)
	require.Empty(t, f.bookFile(t, "s2").VersionID)
}

// pfrSameBook re-reads its target inside the write: a holder trashed after
// the decision (the trash handler takes no lock) is refused, not linked --
// once linked, purging the trashed version would delete this file too.
func TestRelinkPreFixImports_SameBookRefusesTrashedHolder(t *testing.T) {
	t.Parallel()
	f := newPreFixFixture(t)
	same := f.book(t, "t1", "Placeholder Trashed Holder", f.path("imports/t1.m4b"), nil)
	sv := f.version(t, same, pfrHashSame)
	f.importRow(t, "t1", same, pfrHashSame, sv.ID)
	f.importRow(t, "t2", same, pfrHashSame, "")
	f.ids["t2"] = same
	_, d := pfrFreshDecision(t, f, "t2")
	require.Equal(t, pfrClassSameBook, d.class)

	sv.Status = database.BookVersionStatusTrash
	require.NoError(t, f.s.UpdateBookVersion(sv))
	err := pfrSameBook(f.s, d, "")
	require.True(t, errors.Is(err, repairs.ErrChangedSincePlan), "err=%v", err)
	require.Contains(t, err.Error(), database.BookVersionStatusTrash)
	require.Empty(t, f.bookFile(t, "t2").VersionID)
}

// N5: a hashed row whose book's version of that torrent is already linked to
// a sibling row, while the torrent index names no live holder, is
// no_candidate with a reason naming that version, not "every live version is
// already linked".
func TestRelinkPreFixImports_NoCandidateNamesLinkedSameTorrentVersion(t *testing.T) {
	t.Parallel()
	f := newPreFixFixture(t)
	book := f.book(t, "n1", "Placeholder Index Elsewhere", f.path("imports/n1.m4b"), nil)
	v := f.version(t, book, pfrHashMulti)
	f.importRow(t, "n1", book, pfrHashMulti, v.ID)
	f.importRow(t, "n2", book, pfrHashMulti, "")
	f.ids["n2"] = book
	// Another book's version took the torrent index and was then trashed, so
	// the index names no live holder.
	other := f.book(t, "other", "Placeholder Other", f.path("imports/other.m4b"), nil)
	ov := f.version(t, other, pfrHashMulti)
	ov.Status = database.BookVersionStatusTrash
	require.NoError(t, f.s.UpdateBookVersion(ov))

	res := f.fixerPlan(t, relinkPreFixFixerID, "op-plan", nil)
	r := findRow(t, res, f.rowIDs["n2"])
	require.Equal(t, pfrClassNoCandidate, r.Class, r.Reason)
	require.Contains(t, r.SkipReason, v.ID)
	require.Contains(t, r.SkipReason, "torrent index does not name it")
}

// M10: a same-book torrent holder that is not live for files (a version
// mid-swap) is not linked to: the row is no_candidate, not same_book_version.
func TestRelinkPreFixImports_SameBookHolderMidSwapIsNotLinked(t *testing.T) {
	t.Parallel()
	f := newPreFixFixture(t)
	book := f.book(t, "w1", "Placeholder Swapping", f.path("imports/w1.m4b"), nil)
	v, err := f.s.CreateBookVersion(&database.BookVersion{BookID: book, Status: database.BookVersionStatusSwappingIn,
		Format: "m4b", Source: "deluge", TorrentHash: pfrHashSame})
	require.NoError(t, err)
	f.importRow(t, "w1", book, pfrHashSame, v.ID)
	f.importRow(t, "w2", book, pfrHashSame, "")
	f.ids["w2"] = book

	res := f.fixerPlan(t, relinkPreFixFixerID, "op-plan", nil)
	r := findRow(t, res, f.rowIDs["w2"])
	require.Equal(t, pfrClassNoCandidate, r.Class, r.Reason)
	require.Contains(t, r.SkipReason, database.BookVersionStatusSwappingIn)
	require.Zero(t, res.Applicable)
}

// S1: a would_link row whose version and file agree on every check is low
// risk, with one "agrees" evidence line per check.
func TestRelinkPreFixImports_EvidenceAgreeIsLowRisk(t *testing.T) {
	t.Parallel()
	f := newPreFixFixture(t)
	f.preFixImport(t, "agree", "")
	res := f.fixerPlan(t, relinkPreFixFixerID, "op-plan", nil)
	r := findRow(t, res, f.rowIDs["agree"])
	require.Equal(t, pfrClassLink, r.Class)
	require.Equal(t, repairs.RiskLow, r.Risk, "%v", r.Evidence)
	ev := strings.Join(r.Evidence, "\n")
	for _, check := range []string{pfrCheckDates, pfrCheckFormat, pfrCheckLocation, pfrCheckMissing} {
		require.Contains(t, ev, "agrees ("+check+")")
	}
	require.NotContains(t, ev, "REVIEW")
	require.Contains(t, ev, "import path "+f.root)
	// The fixture's version has no format, as every imported version: that is
	// neutral, not a disagreement.
	require.Contains(t, ev, "version format not recorded")
}

// S1: each disagreement alone makes a would_link row Review risk, names the
// check in its evidence, and leaves the row applicable (the owner decides).
func TestRelinkPreFixImports_EvidenceDisagreeIsReview(t *testing.T) {
	t.Parallel()
	cases := []struct {
		check string
		seed  func(t *testing.T, f *fragFixture)
	}{
		{pfrCheckDates, func(t *testing.T, f *fragFixture) {
			// A version ingested 30 days before its row: the row came from a
			// later backfill, scan or fixer, not from the import.
			book := f.book(t, "x", "Placeholder Dates", f.path("imports/x.m4b"), nil)
			_, err := f.s.CreateBookVersion(&database.BookVersion{BookID: book, Status: database.BookVersionStatusActive,
				Source: "imported", IngestDate: time.Now().Add(-30 * 24 * time.Hour)})
			require.NoError(t, err)
			f.importRow(t, "x", book, "", "")
		}},
		{pfrCheckFormat + " (row vs extension)", func(t *testing.T, f *fragFixture) {
			// No version format (as on every import): the row's format is
			// compared with its file's extension.
			book := f.book(t, "x", "Placeholder Format Ext", f.path("imports/x.m4b"), nil)
			f.version(t, book, "")
			p := f.file(t, "imports/x.m4b", 333)
			bf := &database.BookFile{BookID: book, FilePath: p, Format: "mp3", FileSize: 333, TrackNumber: 1}
			require.NoError(t, f.s.CreateBookFile(bf))
			f.rowIDs["x"] = bf.ID
		}},
		{pfrCheckFormat + " (version set)", func(t *testing.T, f *fragFixture) {
			// A version that does record a format must agree with the row.
			book := f.book(t, "x", "Placeholder Format", f.path("imports/x.m4b"), nil)
			_, err := f.s.CreateBookVersion(&database.BookVersion{BookID: book, Status: database.BookVersionStatusActive,
				Format: "mp3", Source: "imported"})
			require.NoError(t, err)
			f.importRow(t, "x", book, "", "")
		}},
		{pfrCheckLocation, func(t *testing.T, f *fragFixture) {
			// The row names a file under the library root, not an import folder.
			book := f.book(t, "x", "Placeholder Library", f.path("library/x.m4b"), nil)
			f.version(t, book, "")
			p := f.file(t, "library/x.m4b", 333)
			bf := &database.BookFile{BookID: book, FilePath: p, Format: "m4b", FileSize: 333, TrackNumber: 1}
			require.NoError(t, f.s.CreateBookFile(bf))
			f.rowIDs["x"] = bf.ID
		}},
		{pfrCheckMissing, func(t *testing.T, f *fragFixture) {
			book, _ := f.preFixImport(t, "x", "")
			_, err := f.s.ModifyBookFile(book, f.rowIDs["x"], func(r *database.BookFile) error { r.Missing = true; return nil })
			require.NoError(t, err)
		}},
	}
	for _, tc := range cases {
		t.Run(tc.check, func(t *testing.T) {
			t.Parallel()
			f := newPreFixFixture(t)
			tc.seed(t, f)
			res := f.fixerPlan(t, relinkPreFixFixerID, "op-plan", nil)
			r := findRow(t, res, f.rowIDs["x"])
			require.Equal(t, pfrClassLink, r.Class, r.Reason)
			require.True(t, r.Applicable())
			require.Equal(t, repairs.RiskReview, r.Risk, "%v", r.Evidence)
			ev := strings.Join(r.Evidence, "\n")
			check, _, _ := strings.Cut(tc.check, " ")
			require.Contains(t, ev, "REVIEW ("+check+")")
			require.Equal(t, 1, strings.Count(ev, "REVIEW"), "only %s should disagree: %s", check, ev)
		})
	}
}

// S1: the disagreeing checks are fingerprinted, so a row whose risk changed
// after the owner reviewed it (here: marked missing) is refused at apply.
func TestRelinkPreFixImports_RiskChangeSincePlanIsRefused(t *testing.T) {
	t.Parallel()
	f := newPreFixFixture(t)
	book, _ := f.preFixImport(t, "risk", "")
	res := f.fixerPlan(t, relinkPreFixFixerID, "op-plan", nil)
	require.Equal(t, repairs.RiskLow, findRow(t, res, f.rowIDs["risk"]).Risk)
	_, err := f.s.ModifyBookFile(book, f.rowIDs["risk"], func(r *database.BookFile) error { r.Missing = true; return nil })
	require.NoError(t, err)
	out := f.fixerApply(t, relinkPreFixFixerID, "op-plan", "op-apply", applicableIDs(res))
	require.Zero(t, out.Applied)
	require.Equal(t, 1, out.ChangedSincePlan, "%+v", out.Rows)
	require.Empty(t, f.bookFile(t, "risk").VersionID)
}

// pfrLocation checks the library root first: a file under the root is a
// library file even when an import path contains the root or sits inside it.
func TestRelinkPreFixImports_LocationChecksRootFirst(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name, path string
		env        pfrEnvironment
		want       bool
		wantIn     string
	}{
		{"import folder", "/data/imports/a.m4b",
			pfrEnvironment{rootDir: "/data/lib", importPaths: []database.ImportPath{{Path: "/data/imports"}}}, true, "import path /data/imports"},
		{"import path contains the root", "/data/lib/a.m4b",
			pfrEnvironment{rootDir: "/data/lib", importPaths: []database.ImportPath{{Path: "/data"}}}, false, "library root"},
		{"import path inside the root", "/data/lib/incoming/a.m4b",
			pfrEnvironment{rootDir: "/data/lib", importPaths: []database.ImportPath{{Path: "/data/lib/incoming"}}}, false, "library root"},
		{"neither", "/mnt/downloads/a.m4b",
			pfrEnvironment{rootDir: "/data/lib", importPaths: []database.ImportPath{{Path: "/data/imports"}}}, false, "no registered import path"},
		{"sibling prefix is not under", "/data/imports-old/a.m4b",
			pfrEnvironment{rootDir: "/data/lib", importPaths: []database.ImportPath{{Path: "/data/imports"}}}, false, "no registered import path"},
	}
	for _, tc := range cases {
		where, ok := pfrLocation(tc.path, tc.env)
		require.Equal(t, tc.want, ok, "%s: %s", tc.name, where)
		require.Contains(t, where, tc.wantIn, tc.name)
	}
}

// preReadStore runs hook once, BEFORE CreateIngestVersion's own row read
// (GetBookFiles): after Apply's locked re-plan and before the read the link
// write's compare-and-set compares against. A writer that takes neither lock
// can land there (Apply's Beat and journal write sit in that window).
type preReadStore struct {
	versions.IngestStore
	once sync.Once
	hook func()
}

func (s *preReadStore) GetBookFiles(id string) ([]database.BookFile, error) {
	s.once.Do(s.hook)
	return s.IngestStore.GetBookFiles(id)
}

// RV4a: a repoint that lands between the locked re-plan and
// CreateIngestVersion's row read is refused, not linked at a path the owner
// never reviewed. Before round 2 the compare-and-set held the row only to
// CreateIngestVersion's own read, which already saw the moved path.
func TestRelinkPreFixImports_AdoptRefusesRepointBeforeIngestRead(t *testing.T) {
	t.Parallel()
	f := newPreFixFixture(t)
	book, _ := f.preFixImport(t, "p1", "")
	_, d := pfrFreshDecision(t, f, "p1")
	moved := f.file(t, "library/p1-moved.m4b", 999)
	is := &preReadStore{IngestStore: f.s, hook: func() {
		_, err := f.s.ModifyBookFile(book, f.rowIDs["p1"], func(r *database.BookFile) error { r.FilePath = moved; return nil })
		require.NoError(t, err)
	}}
	err := pfrAdopt(is, d, "")
	require.ErrorIs(t, err, repairs.ErrChangedSincePlan)
	require.ErrorIs(t, err, versions.ErrBookFileChanged)
	row := f.bookFile(t, "p1")
	require.Empty(t, row.VersionID)
	require.Equal(t, moved, row.FilePath)
}

// RV4b: a hashless row that gains a torrent hash in the same window is
// refused, not linked to the hashless version while claiming the torrent.
func TestRelinkPreFixImports_AdoptRefusesHashGainedBeforeIngestRead(t *testing.T) {
	t.Parallel()
	f := newPreFixFixture(t)
	book, _ := f.preFixImport(t, "p2", "")
	_, d := pfrFreshDecision(t, f, "p2")
	is := &preReadStore{IngestStore: f.s, hook: func() {
		_, err := f.s.ModifyBookFile(book, f.rowIDs["p2"], func(r *database.BookFile) error { r.DelugeHash = pfrHashA; return nil })
		require.NoError(t, err)
	}}
	err := pfrAdopt(is, d, "")
	require.ErrorIs(t, err, repairs.ErrChangedSincePlan)
	require.ErrorIs(t, err, versions.ErrBookFileChanged)
	row := f.bookFile(t, "p2")
	require.Empty(t, row.VersionID)
	require.Equal(t, pfrHashA, row.DelugeHash)
}

// The plan logs, to the repairs.plan op log, how many link rows each evidence
// check flags, so the owner can size each Review group before applying.
func TestRelinkPreFixImports_PlanLogsEvidenceCounts(t *testing.T) {
	t.Parallel()
	f := newPreFixFixture(t)
	f.preFixImport(t, "agree", "")
	old := time.Now().Add(-30 * 24 * time.Hour)
	for _, role := range []string{"dates", "both"} {
		book := f.book(t, role, "Placeholder "+role, f.path("imports/"+role+".m4b"), nil)
		_, err := f.s.CreateBookVersion(&database.BookVersion{BookID: book, Status: database.BookVersionStatusActive,
			Source: "imported", IngestDate: old})
		require.NoError(t, err)
		f.importRow(t, role, book, "", "")
		if role == "both" {
			_, err := f.s.ModifyBookFile(book, f.rowIDs[role], func(r *database.BookFile) error { r.Missing = true; return nil })
			require.NoError(t, err)
		}
	}
	rep := &fakeReporter{}
	_, err := newRelinkPreFixFixer(f.p).Plan(context.Background(), nil, rep)
	require.NoError(t, err)
	rep.mu.Lock()
	logs := strings.Join(rep.logs, "\n")
	rep.mu.Unlock()
	require.Contains(t, logs, "evidence over 3 link rows (3 would_link, 0 same_book_version): low risk 1, review 2;")
	require.Contains(t, logs, "dates 2, format 0, location 0, missing 1")
}
