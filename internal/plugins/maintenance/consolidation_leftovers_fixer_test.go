// file: internal/plugins/maintenance/consolidation_leftovers_fixer_test.go
// version: 1.2.1
// guid: 240c6560-a115-459f-a156-ce41853ac125
// last-edited: 2026-10-06

package maintenance

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"syscall"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/audiobooks"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/repairs"
)

// lfFixture is the fragment fixture (real PebbleStore, real files under a
// temp library root) with helpers for the leftovers shape.
type lfFixture struct{ *fragFixture }

func newLFFixture(t *testing.T) *lfFixture { return &lfFixture{newFragFixture(t)} }

// leftover seeds a live book whose one row (and book path) names rel, which
// is NOT on disk, with size bytes and hash.
func (f *lfFixture) leftover(t *testing.T, role, rel string, size int64, hash string) string {
	t.Helper()
	p := f.path(rel)
	b, err := f.s.CreateBook(&database.Book{Title: role, FilePath: p})
	require.NoError(t, err)
	f.ids[role] = b.ID
	bf := &database.BookFile{BookID: b.ID, FilePath: p, FileSize: size, FileHash: hash, Duration: 600, TrackNumber: 1}
	require.NoError(t, f.s.CreateBookFile(bf))
	f.rowIDs[role] = bf.ID
	return b.ID
}

// combined seeds a live book owning files (present on disk, of the given
// sizes) under dir.
func (f *lfFixture) combined(t *testing.T, role, dir string, files map[string]int, hashes map[string]string) string {
	t.Helper()
	b, err := f.s.CreateBook(&database.Book{Title: role, FilePath: f.path(dir)})
	require.NoError(t, err)
	f.ids[role] = b.ID
	f.organized(t, b.ID)
	names := make([]string, 0, len(files))
	for name := range files {
		names = append(names, name)
	}
	sort.Strings(names)
	for i, name := range names {
		size, track := files[name], i+1
		p := f.file(t, filepath.Join(dir, name), size)
		bf := &database.BookFile{BookID: b.ID, FilePath: p, FileSize: int64(size), FileHash: hashes[name], Duration: 600, TrackNumber: track}
		require.NoError(t, f.s.CreateBookFile(bf))
		f.rowIDs[role+"/"+name] = bf.ID
	}
	return b.ID
}

func (f *lfFixture) planLF(t *testing.T, opID string) *repairs.PlanResult {
	t.Helper()
	params, err := json.Marshal(repairs.PlanParams{FixerID: leftoverFixerID})
	require.NoError(t, err)
	rep := &repairsOpReporter{id: opID}
	require.NoError(t, f.p.runRepairsPlan(context.Background(), params, rep))
	res, ok := rep.result.(*repairs.PlanResult)
	require.True(t, ok)
	data, err := json.Marshal(res)
	require.NoError(t, err)
	s := string(data)
	f.ops.mu.Lock()
	f.ops.rows[opID] = &database.OperationV2Row{ID: opID, DefID: repairs.PlanOpID, Status: "completed", ResultData: &s}
	f.ops.mu.Unlock()
	return res
}

func (f *lfFixture) applyLF(t *testing.T, planOpID, opID string, rowIDs []string) *repairs.ApplyResult {
	t.Helper()
	f.applyOp(opID, leftoverFixerID)
	no := false
	params, err := json.Marshal(repairs.ApplyParams{FixerID: leftoverFixerID, PlanOpID: planOpID, RowIDs: rowIDs, DryRun: &no})
	require.NoError(t, err)
	rep := &repairsOpReporter{id: opID}
	require.NoError(t, f.p.runRepairsApply(context.Background(), params, rep))
	res, ok := rep.result.(*repairs.ApplyResult)
	require.True(t, ok)
	return res
}

func lfRow(res *repairs.PlanResult, bookID string) (repairs.Row, bool) {
	for _, r := range res.Rows {
		if r.RowID == "leftover:"+bookID {
			return r, true
		}
	}
	return repairs.Row{}, false
}

// whm seeds the main shape: "18 - WHM 8/18 - WHM 8.m4b" is gone, and the
// combined book owns "WHM 8/WHM 8 - 18.m4b" of the same size.
func (f *lfFixture) whm(t *testing.T) (leftover, combined string) {
	t.Helper()
	leftover = f.leftover(t, "L", "lib/Author/WHM Series/18 - WHM 8/18 - WHM 8.m4b", 1800, "")
	combined = f.combined(t, "C", "lib/Author/WHM Series/WHM 8",
		map[string]int{"WHM 8 - 17.m4b": 1700, "WHM 8 - 18.m4b": 1800}, nil)
	return leftover, combined
}

// TestLeftovers_PlanClassifiesEveryShape runs the plan through the engine
// (so the framework guard sees row paths whose folder is gone) and checks
// each class.
func TestLeftovers_PlanClassifiesEveryShape(t *testing.T) {
	f := newLFFixture(t)
	l, c := f.whm(t)
	// size+hash: both sides carry the same hash.
	lh := f.leftover(t, "LH", "lib/A2/S2/03 - Two/03 - Two.mp3", 2100, "hh")
	f.combined(t, "CH", "lib/A2/S2/Two", map[string]int{"Two - 03.mp3": 2100}, map[string]string{"Two - 03.mp3": "hh"})
	// hash disagreement: same size, different hashes.
	hd := f.leftover(t, "HD", "lib/A3/S3/04 - Three/04 - Three.mp3", 2200, "aa")
	f.combined(t, "CD", "lib/A3/S3/Three", map[string]int{"Three - 04.mp3": 2200}, map[string]string{"Three - 04.mp3": "bb"})
	// ambiguous: two live books own a present same-size file.
	am := f.leftover(t, "AM", "lib/A4/S4/05 - Four/05 - Four.mp3", 2300, "")
	f.combined(t, "CA1", "lib/A4/S4/Four", map[string]int{"Four - 05.mp3": 2300}, nil)
	f.combined(t, "CA2", "lib/A4/S4/Four copy", map[string]int{"Four - 05.mp3": 2300}, nil)
	// no match: nothing of that size anywhere in the series folder.
	nm := f.leftover(t, "NM", "lib/A5/S5/06 - Five/06 - Five.mp3", 2400, "")
	f.combined(t, "CN", "lib/A5/S5/Five", map[string]int{"Five - 06.mp3": 9999}, nil)
	// a same-size file outside the series folder does not count.
	out := f.leftover(t, "OUT", "lib/A6/S6/07 - Six/07 - Six.mp3", 2500, "")
	f.combined(t, "CO", "lib/A7/S7/Six", map[string]int{"Six - 07.mp3": 2500}, nil)
	// iTunes leftover.
	it := f.leftover(t, "IT", "lib/A8/S8/08 - Eight/08 - Eight.mp3", 2600, "")
	f.combined(t, "CI", "lib/A8/S8/Eight", map[string]int{"Eight - 08.mp3": 2600}, nil)
	pid := "ABCDEF0123456789"
	_, err := f.s.ModifyBook(it, func(b *database.Book) error { b.ITunesPersistentID = &pid; return nil })
	require.NoError(t, err)
	// book path present: a repoint candidate, held.
	bp := f.leftover(t, "BP", "lib/A9/S9/09 - Nine/09 - Nine.mp3", 2700, "")
	f.combined(t, "CB", "lib/A9/S9/Nine", map[string]int{"Nine - 09.mp3": 2700}, nil)
	bpPath := f.file(t, "lib/A9/S9/Nine book/09.mp3", 1)
	_, err = f.s.ModifyBook(bp, func(b *database.Book) error { b.FilePath = bpPath; return nil })
	require.NoError(t, err)
	// present row: not a leftover at all.
	pr := f.leftover(t, "PR", "lib/A10/S10/10 - Ten/10 - Ten.mp3", 2800, "")
	f.file(t, "lib/A10/S10/10 - Ten/10 - Ten.mp3", 2800)
	// row already Missing and nothing unmarked: not this population.
	mm := f.leftover(t, "MM", "lib/A11/S11/11 - El/11 - El.mp3", 2900, "")
	_, err = f.s.ModifyBookFile(mm, f.rowIDs["MM"], func(bf *database.BookFile) error { bf.Missing = true; return nil })
	require.NoError(t, err)
	// Doctor Who: the framework guard holds it.
	dw := f.leftover(t, "DW", "lib/Doctor Who/Shada/12 - Shada/12 - Shada.mp3", 3000, "")
	f.combined(t, "CW", "lib/Doctor Who/Shada/Shada", map[string]int{"Shada - 12.mp3": 3000}, nil)

	res := f.planLF(t, "op-plan")
	cases := []struct {
		name, id, class, skipped string
	}{
		{"size match", l, leftoverClassRetire, ""},
		{"size+hash match", lh, leftoverClassRetire, ""},
		{"hash disagree", hd, leftoverClassHeld, leftoverSkipHashDisagree},
		{"ambiguous", am, leftoverClassHeld, leftoverSkipAmbiguous},
		{"no match", nm, leftoverClassNoMatch, leftoverSkipNoMatch},
		{"outside series folder", out, leftoverClassNoMatch, leftoverSkipNoMatch},
		{"itunes", it, leftoverClassITunes, leftoverSkipITunes},
		{"book path present", bp, leftoverClassHeld, leftoverSkipBookPath},
		{"doctor who", dw, leftoverClassRetire, repairs.SkipOwnerManual},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r, ok := lfRow(res, tc.id)
			require.True(t, ok, "no row for %s", tc.name)
			require.Equal(t, tc.class, r.Class, r.Reason)
			require.Equal(t, tc.skipped, r.Skipped, r.SkipReason)
		})
	}
	r, _ := lfRow(res, l)
	require.ElementsMatch(t, []string{l, c}, r.BookIDs)
	require.Equal(t, leftoverBasisSizeChapter, r.Current["match_basis"])
	r, _ = lfRow(res, lh)
	require.Equal(t, leftoverBasisSizeHash, r.Current["match_basis"])
	for _, id := range []string{pr, mm} {
		_, ok := lfRow(res, id)
		require.False(t, ok, "%s is not a leftover", id)
	}
}

// TestLeftovers_StatErrorHoldsNeverRetires: only ENOENT counts as gone; a
// permission error on the row path holds the row.
func TestLeftovers_StatErrorHoldsNeverRetires(t *testing.T) {
	f := newLFFixture(t)
	l, _ := f.whm(t)
	fx := newConsolidationLeftoversFixer(f.p)
	dead := f.path("lib/Author/WHM Series/18 - WHM 8/18 - WHM 8.m4b")
	fx.statFn = func(p string) (os.FileInfo, error) {
		if p == dead {
			return nil, &os.PathError{Op: "stat", Path: p, Err: syscall.EACCES}
		}
		return os.Stat(p)
	}
	rows, err := fx.Plan(context.Background(), nil, &repairsOpReporter{id: "op-plan"})
	require.NoError(t, err)
	var got *repairs.Row
	for i := range rows {
		if rows[i].RowID == "leftover:"+l {
			got = &rows[i]
		}
	}
	require.NotNil(t, got)
	require.Equal(t, leftoverSkipStatError, got.Skipped, got.Reason)
	require.False(t, got.Applicable())
}

// TestLeftovers_ApplyRetiresMarksMissingAndCarriesState: the dead row is
// kept and marked Missing, the leftover is retired into the combined book,
// the user's position and external ids follow; the revert undoes all of it.
func TestLeftovers_ApplyRetiresMarksMissingAndCarriesState(t *testing.T) {
	f := newLFFixture(t)
	l, c := f.whm(t)
	deadPath := f.path("lib/Author/WHM Series/18 - WHM 8/18 - WHM 8.m4b")
	require.NoError(t, f.s.CreateExternalIDMapping(&database.ExternalIDMapping{Source: "audible", ExternalID: "B0LEFT", BookID: l}))
	u, err := f.s.CreateUser("reader", "reader@example.com", "bcrypt", "x", []string{"user"}, "active")
	require.NoError(t, err)
	require.NoError(t, f.s.SetUserPosition(u.ID, l, f.rowIDs["L"], 100))
	require.NoError(t, f.s.SetUserBookState(&database.UserBookState{UserID: u.ID, BookID: l,
		Status: database.UserBookStatusFinished, ProgressPct: 100}))
	before, err := f.s.GetBookFiles(l)
	require.NoError(t, err)
	require.Len(t, before, 1)

	f.planLF(t, "op-plan")
	out := f.applyLF(t, "op-plan", "op-apply", []string{"leftover:" + l})
	require.Equal(t, 1, out.Applied, "%+v", out.Rows)

	after, err := f.s.GetBookFiles(l)
	require.NoError(t, err)
	require.Len(t, after, 1, "the dead row is kept, never deleted")
	require.Equal(t, before[0].ID, after[0].ID)
	require.Equal(t, deadPath, after[0].FilePath)
	require.True(t, after[0].Missing, "the dead row is marked Missing")
	b, err := f.s.GetBookByID(l)
	require.NoError(t, err)
	require.True(t, b.IsSoftDeleted())
	require.NotNil(t, b.MergedIntoBookID)
	require.Equal(t, c, *b.MergedIntoBookID)
	owner, err := f.s.GetBookByExternalID("audible", "B0LEFT")
	require.NoError(t, err)
	require.Equal(t, c, owner)
	pos, err := f.s.ListUserPositionsForBook(u.ID, l)
	require.NoError(t, err)
	require.Empty(t, pos, "the leftover's position followed")
	pos, err = f.s.ListUserPositionsForBook(u.ID, c)
	require.NoError(t, err)
	require.Len(t, pos, 1, "the combined book holds the position")
	require.InDelta(t, 700, pos[0].PositionSeconds, 0.01,
		"chapter 18 starts after the combined book's 600 s chapter 17: 600 + 100")
	cst, err := f.s.GetUserBookState(u.ID, c)
	require.NoError(t, err)
	require.NotNil(t, cst)
	require.NotEqual(t, database.UserBookStatusFinished, cst.Status, "a finished chapter never finishes the combined book")
	cfiles, err := f.s.GetBookFiles(c)
	require.NoError(t, err)
	require.Len(t, cfiles, 2, "the combined book's rows are untouched")

	rr, err := audiobooks.NewRevertService(f.s).RevertOperation("op-apply")
	require.NoError(t, err)
	require.Zero(t, rr.Failed, "%+v", rr)
	b, err = f.s.GetBookByID(l)
	require.NoError(t, err)
	require.False(t, b.IsSoftDeleted())
	require.Equal(t, deadPath, b.FilePath)
	after, err = f.s.GetBookFiles(l)
	require.NoError(t, err)
	require.Len(t, after, 1)
	require.False(t, after[0].Missing, "the revert restores the Missing flag")
	pos, err = f.s.ListUserPositionsForBook(u.ID, l)
	require.NoError(t, err)
	require.Len(t, pos, 1, "the revert puts the position back")
}

// TestLeftovers_ApplyRechecksEveryCondition: a file back on disk, a second
// owner appearing after the plan, or a hash that now disagrees refuses the
// row at apply with nothing written.
func TestLeftovers_ApplyRechecksEveryCondition(t *testing.T) {
	cases := map[string]func(t *testing.T, f *lfFixture){
		"leftover file back on disk": func(t *testing.T, f *lfFixture) {
			f.file(t, "lib/Author/WHM Series/18 - WHM 8/18 - WHM 8.m4b", 1800)
		},
		"second owner after the plan": func(t *testing.T, f *lfFixture) {
			f.combined(t, "C2", "lib/Author/WHM Series/WHM 8 copy", map[string]int{"WHM 8 - 18.m4b": 1800}, nil)
		},
		"combined file gone": func(t *testing.T, f *lfFixture) {
			require.NoError(t, os.Remove(f.path("lib/Author/WHM Series/WHM 8/WHM 8 - 18.m4b")))
		},
		"hashes now disagree": func(t *testing.T, f *lfFixture) {
			require.NoError(t, f.s.SetBookFileHash(f.rowIDs["L"], "aa"))
			require.NoError(t, f.s.SetBookFileHash(f.rowIDs["C/WHM 8 - 18.m4b"], "bb"))
		},
	}
	for name, change := range cases {
		t.Run(name, func(t *testing.T) {
			f := newLFFixture(t)
			l, _ := f.whm(t)
			f.planLF(t, "op-plan")
			change(t, f)
			out := f.applyLF(t, "op-plan", "op-apply", []string{"leftover:" + l})
			require.Zero(t, out.Applied, "%+v", out.Rows)
			b, err := f.s.GetBookByID(l)
			require.NoError(t, err)
			require.False(t, b.IsSoftDeleted(), "nothing written")
			rows, err := f.s.GetBookFiles(l)
			require.NoError(t, err)
			require.Len(t, rows, 1)
			require.False(t, rows[0].Missing, "nothing written")
		})
	}
}

// TestLeftovers_ResumeAfterMarkingMissing: a run cut off after marking the
// dead row Missing re-plans to the same fingerprint and finishes the retire.
func TestLeftovers_ResumeAfterMarkingMissing(t *testing.T) {
	f := newLFFixture(t)
	l, c := f.whm(t)
	f.planLF(t, "op-plan")
	_, err := f.s.ModifyBookFile(l, f.rowIDs["L"], func(bf *database.BookFile) error { bf.Missing = true; return nil })
	require.NoError(t, err)
	out := f.applyLF(t, "op-plan", "op-apply", []string{"leftover:" + l})
	require.Equal(t, 1, out.Applied, "%+v", out.Rows)
	b, err := f.s.GetBookByID(l)
	require.NoError(t, err)
	require.True(t, b.IsSoftDeleted())
	require.Equal(t, c, *b.MergedIntoBookID)
}

// TestLeftovers_ScopeRefusesLibraryRoot: a row whose grandparent is the
// library root is held, never matched across the whole library.
func TestLeftovers_ScopeRefusesLibraryRoot(t *testing.T) {
	scope, ok := leftoverScope("/lib/Book/file.mp3", []string{"/lib"})
	require.False(t, ok, scope)
	_, ok = leftoverScope("/lib/Author/Series/Book/file.mp3", []string{"/lib"})
	require.True(t, ok)
	_, ok = leftoverScope("/x/file.mp3", nil)
	require.False(t, ok)
	require.True(t, errors.Is(&os.PathError{Err: syscall.ENOENT}, os.ErrNotExist))
}

// TestLeftovers_ITunesOwnership (owner decision 2026-10-06): a row's bare
// iTunes path reference does not make a leftover iTunes-owned (it plans and
// applies); a persistent id or a file inside an iTunes Media folder does.
func TestLeftovers_ITunesOwnership(t *testing.T) {
	f := newLFFixture(t)
	po := f.leftover(t, "PO", "lib/B1/S1/01 - One/01 - One.m4b", 3100, "")
	f.combined(t, "CPO", "lib/B1/S1/One", map[string]int{"One - 01.m4b": 3100}, nil)
	_, err := f.s.ModifyBookFile(po, f.rowIDs["PO"], func(bf *database.BookFile) error {
		bf.ITunesPath = "file://localhost/W:/audiobook-organizer/B1/S1/01%20-%20One/01%20-%20One.m4b"
		return nil
	})
	require.NoError(t, err)
	pid := f.leftover(t, "PID", "lib/B2/S2/02 - Two/02 - Two.m4b", 3200, "")
	f.combined(t, "CPID", "lib/B2/S2/Two", map[string]int{"Two - 02.m4b": 3200}, nil)
	_, err = f.s.ModifyBookFile(pid, f.rowIDs["PID"], func(bf *database.BookFile) error {
		bf.ITunesPersistentID = "0123456789ABCDEF"
		return nil
	})
	require.NoError(t, err)
	med := f.leftover(t, "MED", "lib/B3/iTunes Media/Audiobooks/03 - Three/03.m4b", 3300, "")
	f.combined(t, "CMED", "lib/B3/iTunes Media/Audiobooks/Three", map[string]int{"Three - 03.m4b": 3300}, nil)

	res := f.planLF(t, "op-plan")
	r, ok := lfRow(res, po)
	require.True(t, ok)
	require.Equal(t, leftoverClassRetire, r.Class, r.Reason)
	require.True(t, r.Applicable(), r.SkipReason)
	for _, id := range []string{pid, med} {
		r, ok := lfRow(res, id)
		require.True(t, ok)
		require.Equal(t, leftoverClassITunes, r.Class, r.Reason)
		require.Equal(t, leftoverSkipITunes, r.Skipped, r.SkipReason)
	}
	out := f.applyLF(t, "op-plan", "op-apply", []string{"leftover:" + po})
	require.Equal(t, 1, out.Applied, "%+v", out.Rows)
	b, err := f.s.GetBookByID(po)
	require.NoError(t, err)
	require.True(t, b.IsSoftDeleted())
	rows, err := f.s.GetBookFiles(po)
	require.NoError(t, err)
	require.Len(t, rows, 1)
	require.True(t, rows[0].Missing)
	require.NotEmpty(t, rows[0].ITunesPath, "the iTunes reference on the row is left as it was")
}

// TestLeftovers_TwinNeedsEvidence: a same-size file alone does not pick the
// twin. No agreeing chapter number, hash or duration holds the row; two
// candidates with equal evidence (a chapter and its _copyN) hold it too.
func TestLeftovers_TwinNeedsEvidence(t *testing.T) {
	f := newLFFixture(t)
	// size only: the owner's file has another chapter number and duration.
	ne := f.leftover(t, "NE", "lib/D1/S1/04 - Four/04 - Four.mp3", 4100, "")
	cne := f.combined(t, "CNE", "lib/D1/S1/Four", map[string]int{"Four - 09.mp3": 4100}, nil)
	_, err := f.s.ModifyBookFile(cne, f.rowIDs["CNE/Four - 09.mp3"], func(bf *database.BookFile) error { bf.Duration = 1234; return nil })
	require.NoError(t, err)
	// two candidates on one owner, both chapter 5 with the same duration.
	ta := f.leftover(t, "TA", "lib/D2/S2/05 - Five/05 - Five.mp3", 4200, "")
	f.combined(t, "CTA", "lib/D2/S2/Five", map[string]int{"Five - 05.mp3": 4200, "Five - 05_copy1.mp3": 4200}, nil)
	res := f.planLF(t, "op-plan")
	r, ok := lfRow(res, ne)
	require.True(t, ok)
	require.Equal(t, leftoverSkipNoEvidence, r.Skipped, r.SkipReason)
	r, ok = lfRow(res, ta)
	require.True(t, ok)
	require.Equal(t, leftoverSkipTwinAmbiguous, r.Skipped, r.SkipReason)
	require.Equal(t, 5, mustChapter(t, "Five - 05_copy1.mp3", false))
	require.Equal(t, 18, mustChapter(t, "18 - We Hunt Monsters 8.m4b", true))
	require.Equal(t, 18, mustChapter(t, "We Hunt Monsters 8 - 18.m4b", false))
}

func mustChapter(t *testing.T, name string, lead bool) int {
	t.Helper()
	n, ok := leftoverChapter(name, lead)
	require.True(t, ok, name)
	return n
}

// TestLeftovers_CombinedMustBeListed: a combined book Audiobookshelf does not
// list (not organized, or quarantined after the plan) never receives the
// retire; the re-plan under the lock re-checks it.
func TestLeftovers_CombinedMustBeListed(t *testing.T) {
	f := newLFFixture(t)
	l, c := f.whm(t)
	imported := "imported"
	_, err := f.s.ModifyBook(c, func(b *database.Book) error { b.LibraryState = &imported; return nil })
	require.NoError(t, err)
	res := f.planLF(t, "op-plan")
	r, ok := lfRow(res, l)
	require.True(t, ok)
	require.Equal(t, leftoverSkipNotListed, r.Skipped, r.SkipReason)

	f.organized(t, c)
	f.planLF(t, "op-plan2")
	notPrimary := false
	_, err = f.s.ModifyBook(c, func(b *database.Book) error { b.IsPrimaryVersion = &notPrimary; return nil })
	require.NoError(t, err)
	out := f.applyLF(t, "op-plan2", "op-apply", []string{"leftover:" + l})
	require.Zero(t, out.Applied, "%+v", out.Rows)
	b, err := f.s.GetBookByID(l)
	require.NoError(t, err)
	require.False(t, b.IsSoftDeleted())
}

// TestLeftovers_RefusesAnUnmountedRoot: an empty or missing library root
// fails the plan instead of reading every row as gone.
func TestLeftovers_RefusesAnUnmountedRoot(t *testing.T) {
	f := newLFFixture(t)
	f.whm(t)
	f.setDepsRoot(t, t.TempDir())
	params, err := json.Marshal(repairs.PlanParams{FixerID: leftoverFixerID})
	require.NoError(t, err)
	require.Error(t, f.p.runRepairsPlan(context.Background(), params, &repairsOpReporter{id: "op-plan"}))
	f.setDepsRoot(t, filepath.Join(f.root, "no-such-dir"))
	require.Error(t, f.p.runRepairsPlan(context.Background(), params, &repairsOpReporter{id: "op-plan2"}))
}
