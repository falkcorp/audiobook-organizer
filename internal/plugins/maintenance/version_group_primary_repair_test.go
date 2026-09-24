// file: internal/plugins/maintenance/version_group_primary_repair_test.go
// version: 1.2.0
// guid: b6c88f6f-930b-4ea7-bded-52290d5a52aa
// last-edited: 2026-09-24

package maintenance

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/metafetch"
	"github.com/falkcorp/audiobook-organizer/internal/versionprimary"
)

type vgRepairFixture struct {
	s        *database.PebbleStore
	root     string
	chapters map[string]int // probe answer by path
	mu       sync.Mutex
}

func newVGRepairFixture(t *testing.T) *vgRepairFixture {
	return &vgRepairFixture{s: newSeriesPhantomStore(t), root: t.TempDir(), chapters: map[string]int{}}
}

func (f *vgRepairFixture) probe(_ context.Context, path string) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	n, ok := f.chapters[path]
	if !ok {
		return 0, errors.New("no such file in the stub")
	}
	return n, nil
}

// book creates a book in gid with one .m4b under the root (unless noFile)
// whose probe reports chapters. primary is "true", "false" or "nil".
func (f *vgRepairFixture) book(t *testing.T, id, gid, state, primary string, chapters int, noFile bool, created time.Time, mut func(*database.Book)) string {
	t.Helper()
	g, st, c := gid, state, created
	b := &database.Book{ID: id, Title: "Book " + id, VersionGroupID: &g, LibraryState: &st, CreatedAt: &c,
		FilePath: filepath.Join(f.root, "stale", id+".m4b")}
	switch primary {
	case "true":
		v := true
		b.IsPrimaryVersion = &v
	case "false":
		v := false
		b.IsPrimaryVersion = &v
	}
	if mut != nil {
		mut(b)
	}
	created2, err := f.s.CreateBook(b)
	require.NoError(t, err)
	// CreateBook may default the flag; pin the stored tri-state the test
	// asked for.
	_, err = f.s.ModifyBook(created2.ID, func(row *database.Book) error {
		row.IsPrimaryVersion = b.IsPrimaryVersion
		return nil
	})
	require.NoError(t, err)
	if !noFile {
		path := filepath.Join(f.root, "Author", id, id+".m4b")
		require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
		require.NoError(t, os.WriteFile(path, []byte("m4b"), 0o644))
		require.NoError(t, f.s.CreateBookFile(&database.BookFile{ID: "bf-" + id, BookID: created2.ID, FilePath: path}))
		f.mu.Lock()
		f.chapters[path] = chapters
		f.mu.Unlock()
	}
	return created2.ID
}

func (f *vgRepairFixture) flag(t *testing.T, id string) string {
	t.Helper()
	b, err := f.s.GetBookByID(id)
	require.NoError(t, err)
	require.NotNil(t, b)
	return storedPrimaryFlag(b.IsPrimaryVersion)
}

func strPtr(s string) *string { return &s }

// seed builds three groups:
//   - vg-double: A (10 chapters, true, thin metadata, narrator "Keep"),
//     B (1 chapter, true, rich metadata, narrator "Other"), C (organized but
//     no book_file row, nil flag). A must win on content; B donates.
//   - vg-held: S (organized_source, 10 chapters, true) and L (organized,
//     no chapters, nil). The better copy is outside the library: held.
//   - vg-ok: one explicit true, one explicit false. Not a candidate.
func (f *vgRepairFixture) seed(t *testing.T) {
	base := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	f.book(t, "A", "vg-double", "organized", "true", 10, false, base.Add(2*time.Hour), func(b *database.Book) {
		b.Narrator = strPtr("Keep")
	})
	f.book(t, "B", "vg-double", "organized", "true", 1, false, base, func(b *database.Book) {
		b.Narrator = strPtr("Other")
		b.Description = strPtr("donor description")
		b.Publisher = strPtr("Donor House")
		b.ASIN = strPtr("B0TESTASIN")
	})
	f.book(t, "C", "vg-double", "organized", "nil", 0, true, base.Add(time.Hour), nil)
	f.book(t, "S", "vg-held", "organized_source", "true", 10, false, base, nil)
	f.book(t, "L", "vg-held", "organized", "nil", 0, false, base.Add(time.Hour), nil)
	f.book(t, "OK1", "vg-ok", "organized", "true", 5, false, base, nil)
	f.book(t, "OK2", "vg-ok", "organized", "false", 5, false, base.Add(time.Hour), nil)
}

func (f *vgRepairFixture) run(t *testing.T, deps fakeDeps, params vgPrimaryRepairParams) (*vgRepairReport, error) {
	t.Helper()
	p := &Plugin{deps: deps}
	return p.versionGroupPrimaryRepair(context.Background(), params, f.root, f.probe, &opIDReporter{id: "op-vgpr-test"})
}

func TestVGPrimaryRepair_DryRunReportsAndWritesNothing(t *testing.T) {
	f := newVGRepairFixture(t)
	f.seed(t)
	rep, err := f.run(t, fakeDeps{store: f.s}, vgPrimaryRepairParams{})
	require.NoError(t, err)
	require.True(t, rep.DryRun)
	require.Equal(t, 2, rep.Candidates, "vg-ok is correct and must not be a candidate")
	require.Equal(t, 1, rep.ByDecision[versionprimary.DecisionElect])
	require.Equal(t, 1, rep.ByHoldReason[versionprimary.HoldBetterCopyNotInLibrary])
	require.Equal(t, 1, rep.ContentNotMetadataBest)
	require.Len(t, rep.Groups, 2)

	var double *vgRepairGroupReport
	for i := range rep.Groups {
		if rep.Groups[i].GroupID == "vg-double" {
			double = &rep.Groups[i]
		}
	}
	require.NotNil(t, double)
	require.Equal(t, "A", double.WinnerID)
	require.Equal(t, "B", double.MetadataBestID)
	require.NotNil(t, double.CarryOver)
	fills := map[string]string{}
	for _, fl := range double.CarryOver.Fills {
		fills[fl.Field] = fl.Value
	}
	require.Equal(t, map[string]string{"description": "donor description", "publisher": "Donor House", "asin": "B0TESTASIN"}, fills)
	require.Len(t, double.CarryOver.Conflicts, 1)
	require.Equal(t, "narrator", double.CarryOver.Conflicts[0].Field)
	for _, m := range double.Members {
		if m.BookID == "A" {
			require.Equal(t, versionprimary.ChapterSourceProbe, m.ChapterSource)
			require.Equal(t, versionprimary.TierM4BChapters, m.Tier)
		}
		if m.BookID == "C" {
			require.Equal(t, versionprimary.IneligibleNoActiveFiles, m.Ineligible)
		}
	}

	for id, want := range map[string]string{"A": "true", "B": "true", "C": "nil", "S": "true", "L": "nil"} {
		require.Equal(t, want, f.flag(t, id), "dry run changed %s", id)
	}
	hist, err := f.s.GetBookChangeHistory("A", 100)
	require.NoError(t, err)
	require.Empty(t, hist)
}

func TestVGPrimaryRepair_ApplyNeedsGroupIDs(t *testing.T) {
	f := newVGRepairFixture(t)
	f.seed(t)
	_, err := f.run(t, fakeDeps{store: f.s}, vgPrimaryRepairParams{Apply: true})
	require.ErrorContains(t, err, "group_ids")
	require.Equal(t, "true", f.flag(t, "B"))
}

func TestVGPrimaryRepair_ApplyElectsOneAndFillsOnlyEmptyFields(t *testing.T) {
	f := newVGRepairFixture(t)
	f.seed(t)
	rep, err := f.run(t, fakeDeps{store: f.s}, vgPrimaryRepairParams{
		Apply: true, GroupIDs: []string{"vg-double", "vg-held", "vg-ok", "vg-typo"},
	})
	require.NoError(t, err)
	require.Equal(t, 1, rep.Applied)
	require.Equal(t, []string{"vg-typo"}, rep.RequestedUnmatched)
	require.Equal(t, []string{"vg-ok"}, rep.RequestedNotCandidate)
	require.Equal(t, 3, rep.FieldsFilled)
	require.Zero(t, rep.HistoryFailed)

	// Double-primary group ends with exactly one explicit true; the
	// nil-flagged member ends explicit false.
	require.Equal(t, "true", f.flag(t, "A"))
	require.Equal(t, "false", f.flag(t, "B"))
	require.Equal(t, "false", f.flag(t, "C"))

	a, err := f.s.GetBookByID("A")
	require.NoError(t, err)
	require.Equal(t, "Keep", *a.Narrator, "a non-empty winner field is never overwritten")
	require.Equal(t, "donor description", *a.Description)
	require.Equal(t, "Donor House", *a.Publisher)
	b, err := f.s.GetBookByID("B")
	require.NoError(t, err)
	require.Equal(t, "Other", *b.Narrator, "the donor is untouched")

	// History rows exist after the write, one batch per book.
	hist, err := f.s.GetBookChangeHistory("A", 100)
	require.NoError(t, err)
	fields := map[string]bool{}
	batch := ""
	for _, r := range hist {
		fields[r.Field] = true
		require.Equal(t, vgRepairSource, r.Source)
		require.NotEmpty(t, r.BatchID)
		if batch == "" {
			batch = r.BatchID
		}
		require.Equal(t, batch, r.BatchID)
	}
	// A was already explicit true, so its flag has no history row.
	require.Equal(t, map[string]bool{"description": true, "publisher": true, "asin": true}, fields)
	histB, err := f.s.GetBookChangeHistory("B", 100)
	require.NoError(t, err)
	require.Len(t, histB, 1)
	require.Equal(t, "is_primary_version", histB[0].Field)
	require.Equal(t, `"true"`, *histB[0].PreviousValue)
	require.Equal(t, `"false"`, *histB[0].NewValue)

	// Held group unchanged.
	require.Equal(t, "true", f.flag(t, "S"))
	require.Equal(t, "nil", f.flag(t, "L"))
	histL, err := f.s.GetBookChangeHistory("L", 100)
	require.NoError(t, err)
	require.Empty(t, histL)

	// A re-run finds nothing left to do in the repaired group.
	rep2, err := f.run(t, fakeDeps{store: f.s}, vgPrimaryRepairParams{})
	require.NoError(t, err)
	require.Equal(t, 1, rep2.Candidates, "only the held group remains a candidate")
}

// vgChangingStore flips B's flag the second time vg-double is listed: after
// the plan, before the writes.
type vgChangingStore struct {
	*database.PebbleStore
	mu    sync.Mutex
	reads int
}

func (s *vgChangingStore) GetBooksByVersionGroup(gid string) ([]database.Book, error) {
	if gid == "vg-double" {
		s.mu.Lock()
		s.reads++
		n := s.reads
		s.mu.Unlock()
		if n == 2 {
			if _, err := s.ModifyBook("B", func(b *database.Book) error {
				f := false
				b.IsPrimaryVersion = &f
				return nil
			}); err != nil {
				return nil, err
			}
		}
	}
	return s.PebbleStore.GetBooksByVersionGroup(gid)
}

func TestVGPrimaryRepair_ChangedSincePlanIsSkipped(t *testing.T) {
	f := newVGRepairFixture(t)
	f.seed(t)
	store := &vgChangingStore{PebbleStore: f.s}
	rep, err := f.run(t, fakeDeps{store: store}, vgPrimaryRepairParams{Apply: true, GroupIDs: []string{"vg-double"}})
	require.NoError(t, err)
	require.Equal(t, 1, rep.ChangedSincePlan)
	require.Zero(t, rep.Applied)
	require.Equal(t, "true", f.flag(t, "A"))
	require.Equal(t, "false", f.flag(t, "B"), "only the concurrent writer's change")
	require.Equal(t, "nil", f.flag(t, "C"))
	hist, err := f.s.GetBookChangeHistory("A", 100)
	require.NoError(t, err)
	require.Empty(t, hist)
}

// Rollback: the history rows this op writes are what "undo last apply"
// reverts. Undo is per book: the winner's batch restores its filled fields
// (and flag), each loser's batch restores its flag.
func TestVGPrimaryRepair_UndoLastApplyRestoresFieldsAndFlags(t *testing.T) {
	f := newVGRepairFixture(t)
	f.seed(t)
	_, err := f.run(t, fakeDeps{store: f.s}, vgPrimaryRepairParams{Apply: true, GroupIDs: []string{"vg-double"}})
	require.NoError(t, err)
	svc := metafetch.NewService(f.s)

	res, err := svc.UndoLastApply("A")
	require.NoError(t, err)
	require.ElementsMatch(t, []string{"description", "publisher", "asin"}, res.Reverted)
	a, err := f.s.GetBookByID("A")
	require.NoError(t, err)
	require.True(t, a.Description == nil || *a.Description == "", "description: %v", a.Description)
	require.True(t, a.Publisher == nil || *a.Publisher == "")
	require.Equal(t, "Keep", *a.Narrator)

	_, err = svc.UndoLastApply("B")
	require.NoError(t, err)
	require.Equal(t, "true", f.flag(t, "B"))
	_, err = svc.UndoLastApply("C")
	require.NoError(t, err)
	require.Equal(t, "nil", f.flag(t, "C"))
}

// A merge loser still flagged primary is not live, so this op neither counts
// it as a primary nor demotes it; the dry run reports it so the totals
// reconcile with version-group-primary-report.
func TestVGPrimaryRepair_ReportsNonLiveEffectivePrimaries(t *testing.T) {
	f := newVGRepairFixture(t)
	f.seed(t)
	base := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	f.book(t, "LOSER", "vg-ok", "organized", "true", 5, false, base.Add(3*time.Hour), func(b *database.Book) {
		b.MergedIntoBookID = strPtr("OK1")
	})
	rep, err := f.run(t, fakeDeps{store: f.s}, vgPrimaryRepairParams{})
	require.NoError(t, err)
	require.Equal(t, 1, rep.NonLivePrimaryGroups)
	require.Equal(t, 1, rep.NonLivePrimaryBooks)
	require.Equal(t, []string{"vg-ok"}, rep.NonLivePrimarySample)
	// vg-ok's live members are exactly one true, one false, but the loser
	// is still counted primary and ABS lists it, so vg-ok is now a
	// candidate (2026-09-24) whose apply demotes only the loser.
	require.Equal(t, 3, rep.Candidates)
	var ok *vgRepairGroupReport
	for i := range rep.Groups {
		if rep.Groups[i].GroupID == "vg-ok" {
			ok = &rep.Groups[i]
		}
	}
	require.NotNil(t, ok)
	require.Equal(t, vgDecisionDemoteNonLive, ok.Kind)
	require.Equal(t, []string{"LOSER"}, ok.DemoteNonLive)

	rep, err = f.run(t, fakeDeps{store: f.s}, vgPrimaryRepairParams{Apply: true, GroupIDs: []string{"vg-ok"}})
	require.NoError(t, err)
	require.Equal(t, 1, rep.Applied)
	require.Equal(t, "false", f.flag(t, "LOSER"))
	require.Equal(t, "true", f.flag(t, "OK1"))
	require.Equal(t, "false", f.flag(t, "OK2"))
}

// seedBackwardsMerge builds prod's backwards MATCH-4 shape through the real
// store path: SRC (organized_source, older) and ORG (organized, the library
// copy, same tier) in vg-rev, then FlagMetadataHashDuplicate(SRC, ORG), which
// writes ORG merged_into=SRC and is_primary_version=false exactly as MATCH-4
// did. SRC keeps its explicit true, so the live members look correct.
func (f *vgRepairFixture) seedBackwardsMerge(t *testing.T, gid, src, org string) {
	t.Helper()
	base := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	f.book(t, src, gid, "organized_source", "true", 10, false, base, func(b *database.Book) {
		b.Description = strPtr("source description")
	})
	f.book(t, org, gid, "organized", "true", 10, false, base.Add(time.Hour), nil)
	require.NoError(t, f.s.FlagMetadataHashDuplicate(src, org))
	require.Equal(t, "false", f.flag(t, org))
}

func findGroup(t *testing.T, rep *vgRepairReport, gid string) *vgRepairGroupReport {
	t.Helper()
	for i := range rep.Groups {
		if rep.Groups[i].GroupID == gid {
			return &rep.Groups[i]
		}
	}
	t.Fatalf("group %s not in report", gid)
	return nil
}

func TestVGPrimaryRepair_ReviveBackwardsMerge(t *testing.T) {
	f := newVGRepairFixture(t)
	f.seedBackwardsMerge(t, "vg-rev", "SRC", "ORG")

	rep, err := f.run(t, fakeDeps{store: f.s}, vgPrimaryRepairParams{})
	require.NoError(t, err)
	require.Equal(t, 1, rep.Candidates, "a same-group merge loser makes the group a candidate")
	g := findGroup(t, rep, "vg-rev")
	require.Equal(t, vgDecisionRevive, g.Kind)
	require.Equal(t, "ORG", g.WinnerID)
	require.Equal(t, "ORG", g.RevivedID)
	require.Equal(t, "SRC", g.RevivedFrom)
	require.Equal(t, 1, rep.ByDecision[vgDecisionRevive])
	b, err := f.s.GetBookByID("ORG")
	require.NoError(t, err)
	require.Equal(t, "SRC", *b.MergedIntoBookID, "dry run wrote")

	rep, err = f.run(t, fakeDeps{store: f.s}, vgPrimaryRepairParams{Apply: true, GroupIDs: []string{"vg-rev"}})
	require.NoError(t, err)
	require.Equal(t, 1, rep.Applied)
	require.Equal(t, "true", f.flag(t, "ORG"))
	require.Equal(t, "false", f.flag(t, "SRC"))
	b, err = f.s.GetBookByID("ORG")
	require.NoError(t, err)
	require.Nil(t, b.MergedIntoBookID)
	// Carry-over donors must be eligible, so the organized_source row
	// donates nothing.
	require.True(t, b.Description == nil || *b.Description == "")
	// The memdb/core projection agrees: the merge is gone, not put back.
	cores, err := f.s.GetAllBooksCoreComplete(0, 0)
	require.NoError(t, err)
	for _, c := range cores {
		if c.ID == "ORG" {
			require.Nil(t, c.MergedIntoBookID)
		}
	}

	// Rerun: nothing left to do.
	rep, err = f.run(t, fakeDeps{store: f.s}, vgPrimaryRepairParams{})
	require.NoError(t, err)
	require.Zero(t, rep.Candidates)

	// Undo on the revived book restores the merge and the flag.
	svc := metafetch.NewService(f.s)
	res, err := svc.UndoLastApply("ORG")
	require.NoError(t, err)
	require.Contains(t, res.Reverted, "merged_into_book_id")
	require.Contains(t, res.Reverted, "is_primary_version")
	b, err = f.s.GetBookByID("ORG")
	require.NoError(t, err)
	require.NotNil(t, b.MergedIntoBookID)
	require.Equal(t, "SRC", *b.MergedIntoBookID)
	require.Equal(t, "false", f.flag(t, "ORG"))
}

// A better-tier source keeps the group held: reviving would crown the worse
// copy.
func TestVGPrimaryRepair_ReviveRefusedWhenSourceIsBetter(t *testing.T) {
	f := newVGRepairFixture(t)
	base := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	f.book(t, "SRC", "vg-rev", "organized_source", "true", 10, false, base, nil)
	f.book(t, "ORG", "vg-rev", "organized", "true", 1, false, base.Add(time.Hour), nil)
	require.NoError(t, f.s.FlagMetadataHashDuplicate("SRC", "ORG"))
	rep, err := f.run(t, fakeDeps{store: f.s}, vgPrimaryRepairParams{Apply: true, GroupIDs: []string{"vg-rev"}})
	require.NoError(t, err)
	require.Equal(t, versionprimary.DecisionHeld, findGroup(t, rep, "vg-rev").Kind)
	require.Zero(t, rep.Applied)
	b, err := f.s.GetBookByID("ORG")
	require.NoError(t, err)
	require.Equal(t, "SRC", *b.MergedIntoBookID)
}

// A loser whose file is in the iTunes library is hands-off.
func TestVGPrimaryRepair_ReviveRefusesITunes(t *testing.T) {
	f := newVGRepairFixture(t)
	base := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	f.book(t, "SRC", "vg-rev", "organized_source", "true", 10, false, base, nil)
	f.book(t, "ORG", "vg-rev", "organized", "true", 10, true, base.Add(time.Hour), nil)
	path := filepath.Join(f.root, "books", "itunes", "ORG", "ORG.m4b")
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
	require.NoError(t, os.WriteFile(path, []byte("m4b"), 0o644))
	require.NoError(t, f.s.CreateBookFile(&database.BookFile{ID: "bf-ORG", BookID: "ORG", FilePath: path}))
	f.chapters[path] = 10
	require.NoError(t, f.s.FlagMetadataHashDuplicate("SRC", "ORG"))
	rep, err := f.run(t, fakeDeps{store: f.s}, vgPrimaryRepairParams{Apply: true, GroupIDs: []string{"vg-rev"}})
	require.NoError(t, err)
	require.Equal(t, versionprimary.DecisionHeld, findGroup(t, rep, "vg-rev").Kind)
	require.Zero(t, rep.Applied)
	require.Equal(t, "false", f.flag(t, "ORG"))
}

// The organized copy was merged into a healthy book of ANOTHER group: the
// held group is labelled, and nothing is written even on apply.
func TestVGPrimaryRepair_LeftoverMergedElsewhereIsReportOnly(t *testing.T) {
	f := newVGRepairFixture(t)
	base := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	f.book(t, "SRC", "vg-left", "organized_source", "nil", 10, false, base, nil)
	f.book(t, "ORG", "vg-left", "organized", "true", 10, false, base.Add(time.Hour), nil)
	f.book(t, "OTHER", "vg-other", "organized", "true", 10, false, base, nil)
	require.NoError(t, f.s.FlagMetadataHashDuplicate("OTHER", "ORG"))
	rep, err := f.run(t, fakeDeps{store: f.s}, vgPrimaryRepairParams{Apply: true, GroupIDs: []string{"vg-left"}})
	require.NoError(t, err)
	g := findGroup(t, rep, "vg-left")
	require.Equal(t, vgDecisionLeftover, g.Kind)
	require.Equal(t, "ORG", g.LeftoverBookID)
	require.Equal(t, "OTHER", g.LeftoverTargetBookID)
	require.Equal(t, "vg-other", g.LeftoverTargetGroupID)
	require.Zero(t, rep.Applied)
	require.Equal(t, "nil", f.flag(t, "SRC"))
	b, err := f.s.GetBookByID("ORG")
	require.NoError(t, err)
	require.Equal(t, "OTHER", *b.MergedIntoBookID)
}

// A non-live primary in a group with no live primary to hand over to is
// kept: ABS lists merge losers, so demoting it could hide the only copy.
func TestVGPrimaryRepair_NonLiveKeptWhenGroupHeld(t *testing.T) {
	f := newVGRepairFixture(t)
	base := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	f.book(t, "SRC", "vg-h", "organized_source", "nil", 10, false, base, nil)
	f.book(t, "LOSER", "vg-h", "imported", "true", 10, false, base.Add(time.Hour), func(b *database.Book) {
		b.MergedIntoBookID = strPtr("SRC")
	})
	rep, err := f.run(t, fakeDeps{store: f.s}, vgPrimaryRepairParams{Apply: true, GroupIDs: []string{"vg-h"}})
	require.NoError(t, err)
	g := findGroup(t, rep, "vg-h")
	require.Equal(t, versionprimary.DecisionHeld, g.Kind)
	require.Equal(t, []string{"LOSER"}, g.NonLiveKept)
	require.Equal(t, 1, rep.NonLiveKeptBooks)
	require.Equal(t, "true", f.flag(t, "LOSER"))
}

