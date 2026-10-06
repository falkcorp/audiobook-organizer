// file: internal/plugins/maintenance/duplicate_copies_fixer_test.go
// version: 1.2.2
// guid: c1cb262a-d405-4d1a-9eb7-a3c341190585
// last-edited: 2026-10-06

package maintenance

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/audiobooks"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/repairs"
	"github.com/falkcorp/audiobook-organizer/internal/undo"
	"github.com/falkcorp/audiobook-organizer/internal/versionprimary"
)

// fakeLabels is an in-memory dedup verdict store: labels, decided
// candidates, and an error each strict read returns when set.
type fakeLabels struct {
	ex    []database.LabeledExample
	cands []database.DedupCandidate
	err   error
}

func (l *fakeLabels) ListLabeledExamplesStrict(f database.LabeledExampleFilter) ([]database.LabeledExample, error) {
	if l.err != nil {
		return nil, l.err
	}
	var out []database.LabeledExample
	for _, e := range l.ex {
		if f.Label == "" || e.Label == f.Label {
			out = append(out, e)
		}
	}
	return out, nil
}

func (l *fakeLabels) ListLabeledExamplesForEntitiesStrict(ids []string, f database.LabeledExampleFilter) ([]database.LabeledExample, error) {
	if l.err != nil {
		return nil, l.err
	}
	want := map[string]bool{}
	for _, id := range ids {
		want[id] = true
	}
	var out []database.LabeledExample
	for _, e := range l.ex {
		if (want[e.EntityAID] || want[e.EntityBID]) && (f.Label == "" || e.Label == f.Label) {
			out = append(out, e)
		}
	}
	return out, nil
}

func (l *fakeLabels) ListCandidatesForEntityStrict(entityType, entityID, status string) ([]database.DedupCandidate, error) {
	if l.err != nil {
		return nil, l.err
	}
	var out []database.DedupCandidate
	for _, c := range l.cands {
		if c.EntityType == entityType && (c.EntityAID == entityID || c.EntityBID == entityID) && (status == "" || c.Status == status) {
			out = append(out, c)
		}
	}
	return out, nil
}

func (l *fakeLabels) TerminalCandidatesStrict(entityType string) ([]database.DedupCandidate, error) {
	if l.err != nil {
		return nil, l.err
	}
	var out []database.DedupCandidate
	for _, c := range l.cands {
		if c.EntityType == entityType && database.IsTerminalCandidateStatus(c.Status) {
			out = append(out, c)
		}
	}
	return out, nil
}

// dcFixture is the fragment fixture (real PebbleStore, real files) with a
// label store, so both fixers run against one library.
type dcFixture struct {
	*fragFixture
	labels *fakeLabels
}

func newDCFixture(t *testing.T) *dcFixture {
	t.Helper()
	f := newGlobalRootFragFixture(t)
	l := &fakeLabels{}
	f.p = &Plugin{deps: scanDeps{fakeDeps: fakeDeps{root: f.root, store: f.s, labels: l}, scan: &scriptedScan{renewsLeft: -1}, ops: f.ops}, standDownWait: noWait}
	return &dcFixture{fragFixture: f, labels: l}
}

// dcRow is one seeded book_file row: track, duration, hash; gone: no file on
// disk and flagged Missing; name overrides the file name.
type dcRow struct {
	track, dur int
	hash       string
	gone       bool
	name       string
}

// copyBook seeds an organized book under dir with rows; row roles are
// "<role>/<track>".
func (d *dcFixture) copyBook(t *testing.T, role, title, dir string, rows ...dcRow) string {
	t.Helper()
	b, err := d.s.CreateBook(&database.Book{Title: title, FilePath: d.path(dir)})
	require.NoError(t, err)
	d.ids[role] = b.ID
	d.organized(t, b.ID)
	for i, r := range rows {
		name := r.name
		if name == "" {
			name = fmt.Sprintf("%02d.mp3", r.track)
		}
		p := d.path(filepath.Join(dir, name))
		if !r.gone {
			d.file(t, filepath.Join(dir, name), 100+i)
		}
		bf := &database.BookFile{BookID: b.ID, FilePath: p, FileSize: int64(100 + i), Duration: r.dur, TrackNumber: r.track,
			FileHash: r.hash, Missing: r.gone}
		require.NoError(t, d.s.CreateBookFile(bf))
		d.rowIDs[fmt.Sprintf("%s/%d", role, r.track)] = bf.ID
	}
	return b.ID
}

func (d *dcFixture) planFor(t *testing.T, fixerID, opID string, params any) *repairs.PlanResult {
	t.Helper()
	var raw json.RawMessage
	if params != nil {
		b, err := json.Marshal(params)
		require.NoError(t, err)
		raw = b
	}
	pp, err := json.Marshal(repairs.PlanParams{FixerID: fixerID, Params: raw})
	require.NoError(t, err)
	rep := &repairsOpReporter{id: opID}
	require.NoError(t, d.p.runRepairsPlan(context.Background(), pp, rep))
	res, ok := rep.result.(*repairs.PlanResult)
	require.True(t, ok)
	data, err := json.Marshal(res)
	require.NoError(t, err)
	s := string(data)
	d.ops.mu.Lock()
	d.ops.rows[opID] = &database.OperationV2Row{ID: opID, DefID: repairs.PlanOpID, Status: "completed", ResultData: &s}
	d.ops.mu.Unlock()
	return res
}

func (d *dcFixture) applyFor(t *testing.T, fixerID, planOpID, opID string, rowIDs []string) *repairs.ApplyResult {
	t.Helper()
	no := false
	params, err := json.Marshal(repairs.ApplyParams{FixerID: fixerID, PlanOpID: planOpID, RowIDs: rowIDs, DryRun: &no})
	require.NoError(t, err)
	rep := &repairsOpReporter{id: opID}
	require.NoError(t, d.p.runRepairsApply(context.Background(), params, rep))
	res, ok := rep.result.(*repairs.ApplyResult)
	require.True(t, ok)
	return res
}

func dupRowID(a, b string) string {
	if b < a {
		a = b
	}
	return "dup:" + a
}

// rowOf finds the row whose books include id.
func rowOf(t *testing.T, res *repairs.PlanResult, id string) repairs.Row {
	t.Helper()
	for _, r := range res.Rows {
		if contains(r.BookIDs, id) {
			return r
		}
	}
	t.Fatalf("no row includes %s", id)
	return repairs.Row{}
}

// dune seeds the main shape: the survivor S {1,2,4,5(Missing),6} and the
// copy L {1,2,3,5}. L's 5 repoints S's Missing 5, L's 3 folds into the gap.
func (d *dcFixture) dune(t *testing.T) (s, l string) {
	t.Helper()
	s = d.copyBook(t, "S", "Dune", "lib/Frank Herbert/Dune",
		dcRow{track: 1, dur: 600, hash: "h1"}, dcRow{track: 2, dur: 600, hash: "h2"}, dcRow{track: 4, dur: 600, hash: "h4"},
		dcRow{track: 5, dur: 600, hash: "h5", gone: true}, dcRow{track: 6, dur: 600, hash: "h6"})
	l = d.copyBook(t, "L", "44 - Dune", "lib/Dune copy",
		dcRow{track: 1, dur: 600, hash: "h1"}, dcRow{track: 2, dur: 600, hash: "h2"}, dcRow{track: 3, dur: 100, hash: "h3"},
		dcRow{track: 5, dur: 600, hash: "h5"})
	return s, l
}

func TestDuplicateCopies_PlanClassifiesEveryClass(t *testing.T) {
	d := newDCFixture(t)
	s, l := d.dune(t)
	// unproven: same title, no shared hash.
	u1 := d.copyBook(t, "u1", "Hyperion", "lib/Hyperion", dcRow{track: 1, dur: 600, hash: "x1"}, dcRow{track: 2, dur: 600, hash: "x2"})
	u2 := d.copyBook(t, "u2", "Hyperion", "lib/Hyperion 2", dcRow{track: 1, dur: 600, hash: "y1"}, dcRow{track: 2, dur: 600, hash: "y2"})
	// conflicting ASIN.
	a1 := d.copyBook(t, "a1", "Emma", "lib/Emma", dcRow{track: 1, dur: 600, hash: "e1"}, dcRow{track: 2, dur: 600, hash: "e2"})
	a2 := d.copyBook(t, "a2", "Emma", "lib/Emma 2", dcRow{track: 1, dur: 600, hash: "e1"}, dcRow{track: 2, dur: 600, hash: "e2"})
	for id, asin := range map[string]string{a1: "B000000001", a2: "B000000002"} {
		asin := asin
		_, err := d.s.ModifyBook(id, func(b *database.Book) error { b.ASIN = &asin; return nil })
		require.NoError(t, err)
	}
	// not_dup label.
	n1 := d.copyBook(t, "n1", "Ubik", "lib/Ubik", dcRow{track: 1, dur: 600, hash: "u1"}, dcRow{track: 2, dur: 600, hash: "u2"})
	n2 := d.copyBook(t, "n2", "Ubik", "lib/Ubik 2", dcRow{track: 1, dur: 600, hash: "u1"}, dcRow{track: 2, dur: 600, hash: "u2"})
	d.labels.ex = append(d.labels.ex, database.LabeledExample{EntityAID: n2, EntityBID: n1, Label: "not_dup"})
	// Doctor Who.
	w1 := d.copyBook(t, "w1", "Doctor Who - Shada", "lib/Doctor Who/Shada", dcRow{track: 1, dur: 600, hash: "w1"}, dcRow{track: 2, dur: 600, hash: "w2"})
	w2 := d.copyBook(t, "w2", "Doctor Who - Shada", "lib/Doctor Who/Shada 2", dcRow{track: 1, dur: 600, hash: "w1"}, dcRow{track: 2, dur: 600, hash: "w2"})
	// no eligible survivor: neither copy is under the library root.
	o1 := d.copyBook(t, "o1", "Solaris", "../outside/Solaris", dcRow{track: 1, dur: 600, hash: "o1"}, dcRow{track: 2, dur: 600, hash: "o2"})
	o2 := d.copyBook(t, "o2", "Solaris", "../outside/Solaris 2", dcRow{track: 1, dur: 600, hash: "o1"}, dcRow{track: 2, dur: 600, hash: "o2"})
	// track order: the copy's unmatched file is not on disk.
	t1 := d.copyBook(t, "t1", "Neuromancer", "lib/Neuromancer", dcRow{track: 1, dur: 600, hash: "n1"}, dcRow{track: 2, dur: 600, hash: "n2"}, dcRow{track: 3, dur: 600, hash: "n3"})
	t2 := d.copyBook(t, "t2", "Neuromancer", "lib/Neuromancer 2", dcRow{track: 1, dur: 600, hash: "n1"}, dcRow{track: 2, dur: 600, hash: "n2"}, dcRow{track: 9, dur: 30, hash: "n9", gone: true})
	// user tags.
	g1 := d.copyBook(t, "g1", "Lolita", "lib/Lolita", dcRow{track: 1, dur: 600, hash: "l1"}, dcRow{track: 2, dur: 600, hash: "l2"})
	g2 := d.copyBook(t, "g2", "Lolita", "lib/Lolita 2", dcRow{track: 1, dur: 600, hash: "l1"}, dcRow{track: 2, dur: 600, hash: "l2"})
	require.NoError(t, d.s.AddBookTagWithSource(g1, "favourite", "user"))
	require.NoError(t, d.s.AddBookTagWithSource(g2, "favourite", "user"))
	require.NoError(t, d.s.AddBookTagWithSource(maxID(g1, g2), "reread", "user"))

	res := d.planFor(t, dcFixerID, "op-plan", nil)
	cases := []struct {
		name, class, skipped string
		books                []string
	}{
		{"merge", dcClassMerge, "", []string{s, l}},
		{"unproven", dcClassUnproven, dcSkipUnproven, []string{u1, u2}},
		{"conflicting asin", dcClassMerge, dcSkipASIN, []string{a1, a2}},
		{"owner not_dup", dcClassMerge, dcSkipNotDup, []string{n1, n2}},
		{"doctor who", dcClassManual, repairs.SkipOwnerManual, []string{w1, w2}},
		{"no eligible survivor", dcClassMerge, dcSkipNoSurvivor, []string{o1, o2}},
		{"track order", dcClassMerge, dcSkipTrackOrder, []string{t1, t2}},
		{"user tags", dcClassMerge, dcSkipTags, []string{g1, g2}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := rowOf(t, res, tc.books[0])
			require.Equal(t, tc.class, r.Class, r.SkipReason)
			require.Equal(t, tc.skipped, r.Skipped, r.SkipReason)
			require.ElementsMatch(t, tc.books, r.BookIDs)
		})
	}
	r := rowOf(t, res, s)
	require.Equal(t, s, r.Proposed["survivor"])
	plan := r.Detail.(*dcPlan)
	require.Len(t, plan.Repoints, 1)
	require.Equal(t, d.rowIDs["S/5"], plan.Repoints[0].Row)
	require.Len(t, plan.Folds, 1)
	require.Equal(t, d.rowIDs["L/3"], plan.Folds[0].Row)
}

func maxID(a, b string) string {
	if a > b {
		return a
	}
	return b
}

// TestDuplicateCopies_ApplyThenRevertRoundTrip: repoint, fold, retire with
// external ids and back.
func TestDuplicateCopies_ApplyThenRevertRoundTrip(t *testing.T) {
	d := newDCFixture(t)
	s, l := d.dune(t)
	require.NoError(t, d.s.CreateExternalIDMapping(&database.ExternalIDMapping{Source: "audible", ExternalID: "B0COPY", BookID: l}))
	beforeS5 := *d.fileRow(t, "S", "S/5")
	lPath := d.path("lib/Dune copy/05.mp3")

	res := d.planFor(t, dcFixerID, "op-plan", nil)
	id := dupRowID(s, l)
	require.True(t, findRow(t, res, id).Applicable(), findRow(t, res, id).SkipReason)
	out := d.applyFor(t, dcFixerID, "op-plan", "op-apply", []string{id})
	require.Equal(t, 1, out.Applied, "%+v", out.Rows)

	s5 := d.fileRow(t, "S", "S/5")
	require.Equal(t, lPath, s5.FilePath)
	require.False(t, s5.Missing)
	folded, err := d.s.GetBookFileByID(s, d.rowIDs["L/3"])
	require.NoError(t, err)
	require.NotNil(t, folded, "L's 03 folded onto S")
	lb, err := d.s.GetBookByID(l)
	require.NoError(t, err)
	require.True(t, lb.IsSoftDeleted())
	require.Equal(t, s, *lb.MergedIntoBookID)
	lrows, err := d.s.GetBookFiles(l)
	require.NoError(t, err)
	require.Len(t, lrows, 3, "the loser keeps its hash-matched rows: no row deleted, never emptied")
	owner, err := d.s.GetBookByExternalID("audible", "B0COPY")
	require.NoError(t, err)
	require.Equal(t, s, owner)

	changes, err := d.s.GetOperationChanges("op-apply")
	require.NoError(t, err)
	for i := range changes {
		require.True(t, undo.IsRestorable(changes[i]), "%s %s", changes[i].ChangeType, changes[i].FieldName)
	}
	rr, err := audiobooks.NewRevertService(d.s).RevertOperation("op-apply")
	require.NoError(t, err)
	require.Zero(t, rr.Failed, "%+v", rr)

	after := d.fileRow(t, "S", "S/5")
	require.Equal(t, beforeS5.FilePath, after.FilePath)
	require.Equal(t, beforeS5.Missing, after.Missing)
	back, err := d.s.GetBookFileByID(l, d.rowIDs["L/3"])
	require.NoError(t, err)
	require.NotNil(t, back, "L's 03 is back on L")
	lb, err = d.s.GetBookByID(l)
	require.NoError(t, err)
	require.False(t, lb.IsSoftDeleted())
	require.Nil(t, lb.MergedIntoBookID)
	require.Equal(t, d.path("lib/Dune copy"), lb.FilePath)
	owner, err = d.s.GetBookByExternalID("audible", "B0COPY")
	require.NoError(t, err)
	require.Equal(t, l, owner)
	byPath, err := d.s.GetBookFileByPath(lPath)
	require.NoError(t, err)
	require.NotNil(t, byPath, "the repointed path still resolves to a row after the revert")
	require.Equal(t, lPath, byPath.FilePath)
}

// TestDuplicateCopies_ProgressFollowsOnlyWithTheSameLayout.
func TestDuplicateCopies_ProgressFollowsOnlyWithTheSameLayout(t *testing.T) {
	d := newDCFixture(t)
	a := d.copyBook(t, "A", "Dune", "lib/Dune", dcRow{track: 1, dur: 600, hash: "h1"}, dcRow{track: 2, dur: 600, hash: "h2"})
	b := d.copyBook(t, "B", "Dune", "lib/Dune 2", dcRow{track: 1, dur: 600, hash: "h1"}, dcRow{track: 2, dur: 600, hash: "h2"})
	loser, keep := maxID(a, b), a
	if keep == loser {
		keep = b
	}
	u, err := d.s.CreateUser("reader", "reader@example.com", "bcrypt", "x", []string{"user"}, "active")
	require.NoError(t, err)
	seg := d.rowIDs["A/1"]
	if loser == b {
		seg = d.rowIDs["B/1"]
	}
	require.NoError(t, d.s.SetUserPosition(u.ID, loser, seg, 700))
	res := d.planFor(t, dcFixerID, "op-plan", nil)
	r := rowOf(t, res, a)
	require.True(t, r.Applicable(), r.SkipReason)
	require.Equal(t, keep, r.Proposed["survivor"])
	out := d.applyFor(t, dcFixerID, "op-plan", "op-apply", []string{r.RowID})
	require.Equal(t, 1, out.Applied, "%+v", out.Rows)
	pos, err := d.s.ListUserPositionsForBook(u.ID, keep)
	require.NoError(t, err)
	require.Len(t, pos, 1, "the position followed onto the survivor")

	// A different layout: skipped.
	d2 := newDCFixture(t)
	s, l := d2.dune(t)
	u2, err := d2.s.CreateUser("reader", "reader@example.com", "bcrypt", "x", []string{"user"}, "active")
	require.NoError(t, err)
	require.NoError(t, d2.s.SetUserPosition(u2.ID, l, d2.rowIDs["L/1"], 700))
	r = rowOf(t, d2.planFor(t, dcFixerID, "op-plan", nil), s)
	require.Equal(t, dcSkipProgress, r.Skipped, r.SkipReason)
}

// TestDuplicateCopies_ResumesACutOffRow: the repoint and fold already done,
// the next apply finishes the row without writing them twice.
func TestDuplicateCopies_ResumesACutOffRow(t *testing.T) {
	d := newDCFixture(t)
	s, l := d.dune(t)
	res := d.planFor(t, dcFixerID, "op-plan", nil)
	id := dupRowID(s, l)
	plan := findRow(t, res, id).Detail.(*dcPlan)
	w := repairs.NewWriter(d.s, d.s, dcFixerID, "bulk_update", "repairs-").WithJournal(d.s, d.s, "op-cut")
	rp := plan.Repoints[0]
	require.NoError(t, w.RepointBookFile(s, rp.Row, rp.Was, rp.To))
	require.NoError(t, w.MoveBookFiles([]string{d.rowIDs["L/3"]}, l, s))

	out := d.applyFor(t, dcFixerID, "op-plan", "op-apply", []string{id})
	require.Equal(t, 1, out.Applied, "%+v", out.Rows)
	require.False(t, d.live(t, "L"))
	changes, err := d.s.GetOperationChanges("op-apply")
	require.NoError(t, err)
	for _, c := range changes {
		require.NotEqual(t, undo.ChangeTypeBookFileRepoint, c.ChangeType, "the finished repoint is not written twice")
		require.NotEqual(t, undo.ChangeTypeBookFileReassign, c.ChangeType, "the finished fold is not written twice")
	}
}

// TestDuplicateCopies_ChangedSincePlanIsRefused: a copy retitled after the
// plan fails T under the lock; nothing is written.
func TestDuplicateCopies_ChangedSincePlanIsRefused(t *testing.T) {
	d := newDCFixture(t)
	s, l := d.dune(t)
	d.planFor(t, dcFixerID, "op-plan", nil)
	_, err := d.s.ModifyBook(l, func(b *database.Book) error { b.Title = "Children of Dune"; return nil })
	require.NoError(t, err)
	out := d.applyFor(t, dcFixerID, "op-plan", "op-apply", []string{dupRowID(s, l)})
	require.Equal(t, 1, out.ChangedSincePlan, "%+v", out.Rows)
	require.True(t, d.live(t, "L"))
	require.True(t, d.fileRow(t, "S", "S/5").Missing, "nothing written")
}

// TestDuplicateCopies_LoserJoinedAGroupSincePlan: a loser that joined a
// version group after the plan would hand that group's primacy to the
// survivor, a write nobody reviewed, so the row is refused.
func TestDuplicateCopies_LoserJoinedAGroupSincePlan(t *testing.T) {
	d := newDCFixture(t)
	s, l := d.dune(t)
	d.planFor(t, dcFixerID, "op-plan", nil)
	gid, yes := "vg-later", true
	_, err := d.s.ModifyBook(l, func(b *database.Book) error { b.VersionGroupID, b.IsPrimaryVersion = &gid, &yes; return nil })
	require.NoError(t, err)
	out := d.applyFor(t, dcFixerID, "op-plan", "op-apply", []string{dupRowID(s, l)})
	require.Equal(t, 1, out.ChangedSincePlan, "%+v", out.Rows)
	require.True(t, d.live(t, "L"))
}

// TestDuplicateCopies_ITunesCopyIsIgnoredNeverWritten: a third copy with a
// book PID (and one under books/itunes/**) is excluded: never elected, never
// in the row's books, untouched by the apply; the other two still merge.
func TestDuplicateCopies_ITunesCopyIsIgnoredNeverWritten(t *testing.T) {
	d := newDCFixture(t)
	s, l := d.dune(t)
	pid := d.copyBook(t, "P", "Dune", "lib/Dune pid",
		dcRow{track: 1, dur: 600, hash: "h1"}, dcRow{track: 2, dur: 600, hash: "h2"}, dcRow{track: 4, dur: 600, hash: "h4"},
		dcRow{track: 5, dur: 600, hash: "h5"}, dcRow{track: 6, dur: 600, hash: "h6"}, dcRow{track: 7, dur: 600, hash: "h7"})
	p := "PIDDUNE"
	_, err := d.s.ModifyBook(pid, func(b *database.Book) error { b.ITunesPersistentID = &p; return nil })
	require.NoError(t, err)
	it := d.copyBook(t, "I", "Dune", "books/itunes/Dune",
		dcRow{track: 1, dur: 600, hash: "h1"}, dcRow{track: 2, dur: 600, hash: "h2"})
	beforeP, err := d.s.GetBookByID(pid)
	require.NoError(t, err)
	beforeI, err := d.s.GetBookByID(it)
	require.NoError(t, err)

	res := d.planFor(t, dcFixerID, "op-plan", nil)
	r := rowOf(t, res, s)
	require.True(t, r.Applicable(), r.SkipReason)
	require.ElementsMatch(t, []string{s, l}, r.BookIDs, "the iTunes copies are not in the row's books")
	require.Equal(t, s, r.Proposed["survivor"], "the PID copy, though it has the most files, is never elected")
	var listed []string
	for _, m := range r.Members {
		listed = append(listed, m.BookID)
	}
	require.Subset(t, listed, []string{pid, it}, "the iTunes copies are listed")
	out := d.applyFor(t, dcFixerID, "op-plan", "op-apply", []string{r.RowID})
	require.Equal(t, 1, out.Applied, "%+v", out.Rows)
	afterP, err := d.s.GetBookByID(pid)
	require.NoError(t, err)
	afterI, err := d.s.GetBookByID(it)
	require.NoError(t, err)
	require.Equal(t, beforeP.UpdatedAt, afterP.UpdatedAt, "the PID copy was never written")
	require.Equal(t, beforeI.UpdatedAt, afterI.UpdatedAt, "the books/itunes copy was never written")
	require.False(t, afterP.IsSoftDeleted())
	require.False(t, afterI.IsSoftDeleted())
}

// TestDuplicateCopies_HandOffWouldWriteAnITunesCopy: a primary loser whose
// version group holds an iTunes copy not explicitly non-primary is skipped.
func TestDuplicateCopies_HandOffWouldWriteAnITunesCopy(t *testing.T) {
	d := newDCFixture(t)
	s, l := d.dune(t)
	it := d.copyBook(t, "I", "Dune (iTunes)", "books/itunes/Dune", dcRow{track: 1, dur: 600, hash: "zz"}, dcRow{track: 2, dur: 600, hash: "zy"})
	gid := "vg-dune"
	f := false
	for _, id := range []string{l, it} {
		_, err := d.s.ModifyBook(id, func(b *database.Book) error { b.VersionGroupID = &gid; return nil })
		require.NoError(t, err)
	}
	r := rowOf(t, d.planFor(t, dcFixerID, "op-plan", nil), s)
	require.Equal(t, dcSkipITunesVG, r.Skipped, r.SkipReason)
	_, err := d.s.ModifyBook(it, func(b *database.Book) error { b.IsPrimaryVersion = &f; return nil })
	require.NoError(t, err)
	r = rowOf(t, d.planFor(t, dcFixerID, "op-plan2", nil), s)
	require.Equal(t, dcSkipNoHeir, r.Skipped, "an explicit-false iTunes copy is never the heir: %s", r.SkipReason)
}

// TestDuplicateCopies_UnlocksAnAmbiguousFragment is the end-to-end case: a
// fragment matching both copies is ambiguous until duplicate-copies applies.
func TestDuplicateCopies_UnlocksAnAmbiguousFragment(t *testing.T) {
	d := newDCFixture(t)
	s, l := d.dune(t)
	fp := d.file(t, "lib/Dune frag/02.mp3", 777)
	frag := d.book(t, "frag", "02", fp, nil)
	require.NoError(t, d.s.CreateBookFile(&database.BookFile{BookID: frag, FilePath: fp, OriginalFilename: "02.mp3", FileSize: 777, Duration: 600, FileHash: "h2"}))

	before := d.planFor(t, fragFixerID, "op-frag-1", nil)
	require.Equal(t, fragClassAmbiguous, findRow(t, before, "ambiguous:"+frag).Class)

	// The what-if plan predicts the unlock and applies nothing.
	whatIf := d.planFor(t, fragFixerID, "op-frag-whatif", fragParams{AssumeRetired: &fragAssumeRetired{
		Retire: map[string]string{l: s}, Fold: []string{d.rowIDs["L/3"]}}})
	for _, r := range whatIf.Rows {
		require.Equal(t, fragSkipWhatIf, r.Skipped)
	}
	require.Equal(t, "applicable", findRow(t, whatIf, "copy:"+s).Current[fragWhatIfKey])

	res := d.planFor(t, dcFixerID, "op-plan", nil)
	out := d.applyFor(t, dcFixerID, "op-plan", "op-apply", []string{dupRowID(s, l)})
	require.Equal(t, 1, out.Applied, "%+v", out.Rows)
	_ = res

	after := d.planFor(t, fragFixerID, "op-frag-2", nil)
	r := findRow(t, after, "copy:"+s)
	require.True(t, r.Applicable(), r.SkipReason)
	require.Contains(t, r.BookIDs, frag)
	fout := d.applyFor(t, fragFixerID, "op-frag-2", "op-frag-apply", []string{r.RowID})
	require.Equal(t, 1, fout.Applied, "%+v", fout.Rows)
	require.False(t, d.live(t, "frag"))
}

// TestFragmentFixer_DisregardsITunesParents: a non-iTunes fragment matching
// one non-iTunes parent plus an iTunes copy of it takes the non-iTunes one as
// its parent; the iTunes copy is never written. An iTunes fragment with the
// same matches stays manual-only.
func TestFragmentFixer_DisregardsITunesParents(t *testing.T) {
	d := newDCFixture(t)
	parent := d.copyBook(t, "P", "Dune", "lib/Dune", dcRow{track: 1, dur: 600, hash: "h1"}, dcRow{track: 2, dur: 600, hash: "h2"})
	it := d.copyBook(t, "I", "Dune", "books/itunes/Dune", dcRow{track: 1, dur: 600, hash: "h1"}, dcRow{track: 2, dur: 600, hash: "h2"})
	fp := d.file(t, "lib/Dune frag/02.mp3", 777)
	frag := d.book(t, "frag", "02", fp, nil)
	require.NoError(t, d.s.CreateBookFile(&database.BookFile{BookID: frag, FilePath: fp, OriginalFilename: "02.mp3", FileSize: 777, Duration: 600, FileHash: "h2"}))
	ip := d.file(t, "books/itunes/frags/02.mp3", 778)
	itFrag := d.book(t, "itFrag", "02", ip, nil)
	require.NoError(t, d.s.CreateBookFile(&database.BookFile{BookID: itFrag, FilePath: ip, OriginalFilename: "02.mp3", FileSize: 778, Duration: 600, FileHash: "h1"}))
	beforeI, err := d.s.GetBookByID(it)
	require.NoError(t, err)
	beforeIRows, err := d.s.GetBookFiles(it)
	require.NoError(t, err)

	res := d.planFor(t, fragFixerID, "op-plan", nil)
	r := findRow(t, res, "copy:"+parent)
	require.True(t, r.Applicable(), r.SkipReason)
	require.ElementsMatch(t, []string{parent, frag}, r.BookIDs, "the iTunes parent is not in the row's books")
	require.Contains(t, fmt.Sprint(r.Evidence), it)
	m := findRow(t, res, "ambiguous:"+itFrag)
	require.Equal(t, fragClassManual, m.Class, "an iTunes fragment stays manual-only")
	require.NotEmpty(t, m.Skipped)

	out := d.applyFor(t, fragFixerID, "op-plan", "op-apply", []string{r.RowID})
	require.Equal(t, 1, out.Applied, "%+v", out.Rows)
	require.False(t, d.live(t, "frag"))
	afterI, err := d.s.GetBookByID(it)
	require.NoError(t, err)
	require.Equal(t, beforeI.UpdatedAt, afterI.UpdatedAt, "the iTunes parent was never written")
	afterIRows, err := d.s.GetBookFiles(it)
	require.NoError(t, err)
	require.Equal(t, beforeIRows, afterIRows)
	changes, err := d.s.GetOperationChanges("op-apply")
	require.NoError(t, err)
	for _, c := range changes {
		require.NotEqual(t, it, c.BookID, "nothing journaled against the iTunes parent")
	}
}

// TestDuplicateCopies_IdentityGate is the per-clause table of dcJudge. Each
// case flips one clause from the proven baseline.
func TestDuplicateCopies_IdentityGate(t *testing.T) {
	str := func(s string) *string { return &s }
	author := 7
	mk := func(id, title string, asin *string, rows ...database.BookFileCore) *dcBook {
		return &dcBook{Core: database.BookCore{ID: id, Title: title, ASIN: asin, AuthorID: &author}, Rows: rows, Title: dcTitleKey(title), Author: "frankherbert"}
	}
	row := func(dur int, hash string) database.BookFileCore {
		return database.BookFileCore{ID: hash + fmt.Sprint(dur), Duration: dur, FileHash: hash, FilePath: "/x/" + hash + ".mp3"}
	}
	base := func() (*dcBook, *dcBook) {
		return mk("a", "Dune", nil, row(900, "h1"), row(100, "h2")),
			mk("b", "44 - Dune", nil, row(900, "h1"), row(100, "zz"), row(600, "h3"))
	}
	none := dcRejections{}
	a, b := base()
	require.Equal(t, dcEdgeProven, dcJudge(a, b, none).Kind, "90%% of the smaller copy is hash-matched")

	cases := []struct {
		name string
		mut  func(a, b *dcBook) dcRejections
		want string
	}{
		{"coverage 89%", func(a, _ *dcBook) dcRejections {
			a.Rows = []database.BookFileCore{row(890, "h1"), row(110, "h2")}
			return none
		}, dcEdgeUnproven},
		{"short shared intro is the only overlap", func(a, b *dcBook) dcRejections {
			intro := database.BookFileCore{ID: "intro", Duration: 59, FileHash: "intro", FilePath: "/x/track.mp3"}
			a.Rows = []database.BookFileCore{intro}
			b.Rows = []database.BookFileCore{intro, row(900, "other")}
			return none
		}, dcEdgeUnproven},
		{"boilerplate credits are the only overlap", func(a, b *dcBook) dcRejections {
			credits := database.BookFileCore{ID: "cr", Duration: 300, FileHash: "cr", FilePath: "/x/x.mp3", Title: "Opening Credits"}
			a.Rows = []database.BookFileCore{credits}
			b.Rows = []database.BookFileCore{credits, row(900, "other")}
			return none
		}, dcEdgeUnproven},
		{"titles differ", func(_, b *dcBook) dcRejections {
			b.Title = dcTitleKey("Dune Messiah")
			return none
		}, ""},
		{"authors differ", func(_, b *dcBook) dcRejections {
			b.Author = "brianherbert"
			return none
		}, ""},
		{"conflicting asin", func(a, b *dcBook) dcRejections {
			a.Core.ASIN, b.Core.ASIN = str("B01"), str("B02")
			return none
		}, dcEdgeASIN},
		{"not_dup label", func(_, _ *dcBook) dcRejections {
			return dcRejections{dcPairKey("b", "a"): {Kind: dcEdgeNotDup, Why: "labeled not_dup"}}
		}, dcEdgeNotDup},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			a, b := base()
			nd := tc.mut(a, b)
			require.Equal(t, tc.want, dcJudge(a, b, nd).Kind)
		})
	}
	// Unknown author and the same ASIN do not block.
	a, b = base()
	b.Author = ""
	a.Core.ASIN, b.Core.ASIN = str("B01"), str("b01")
	require.Equal(t, dcEdgeProven, dcJudge(a, b, none).Kind)
}

// TestDuplicateCopies_OnlyUserTagsBlock: a system tag the survivor lacks
// does not hold the group back; a user tag does.
func TestDuplicateCopies_OnlyUserTagsBlock(t *testing.T) {
	d := newDCFixture(t)
	a := d.copyBook(t, "A", "Dune", "lib/Dune", dcRow{track: 1, dur: 600, hash: "h1"}, dcRow{track: 2, dur: 600, hash: "h2"})
	b := d.copyBook(t, "B", "Dune", "lib/Dune 2", dcRow{track: 1, dur: 600, hash: "h1"}, dcRow{track: 2, dur: 600, hash: "h2"})
	loser := maxID(a, b)
	require.NoError(t, d.s.AddBookTagWithSource(loser, "genre:scifi", "system"))
	r := rowOf(t, d.planFor(t, dcFixerID, "op-plan", nil), a)
	require.True(t, r.Applicable(), r.SkipReason)
	require.NoError(t, d.s.AddBookUserTag(loser, "keep me"))
	r = rowOf(t, d.planFor(t, dcFixerID, "op-plan2", nil), a)
	require.Equal(t, dcSkipTags, r.Skipped, r.SkipReason)
}

// TestDuplicateCopies_FailsClosedWithoutLabels: no label store, no plan.
func TestDuplicateCopies_FailsClosedWithoutLabels(t *testing.T) {
	d := newDCFixture(t)
	d.p = &Plugin{deps: scanDeps{fakeDeps: fakeDeps{root: d.root, store: d.s}, scan: &scriptedScan{renewsLeft: -1}, ops: d.ops}, standDownWait: noWait}
	_, err := newDuplicateCopiesFixer(d.p).Plan(context.Background(), nil, nil)
	require.ErrorContains(t, err, "not_dup")
}

// TestDuplicateCopies_FoldGatesAndClique: a fold that repeats a track, a fold
// over 25% of the matched audio, and a group that is not all proven copies of
// each other are each skipped.
func TestDuplicateCopies_FoldGatesAndClique(t *testing.T) {
	d := newDCFixture(t)
	// The copy's unmatched file is track 4, which the survivor already has.
	r1 := d.copyBook(t, "r1", "Dune", "lib/Dune", dcRow{track: 1, dur: 600, hash: "h1"}, dcRow{track: 2, dur: 600, hash: "h2"}, dcRow{track: 4, dur: 600, hash: "h4"})
	d.copyBook(t, "r2", "Dune", "lib/Dune 2", dcRow{track: 1, dur: 600, hash: "h1"}, dcRow{track: 2, dur: 600, hash: "h2"}, dcRow{track: 4, dur: 100, hash: "hx"})
	// The survivor is the smaller copy (the other is not organized): its
	// fold would add 600 s to 1,200 s of matched audio.
	c1 := d.copyBook(t, "c1", "Emma", "lib/Emma", dcRow{track: 1, dur: 600, hash: "e1"}, dcRow{track: 2, dur: 600, hash: "e2"})
	c2 := d.copyBook(t, "c2", "Emma", "lib/Emma 2", dcRow{track: 1, dur: 600, hash: "e1"}, dcRow{track: 2, dur: 600, hash: "e2"}, dcRow{track: 3, dur: 600, hash: "e3"})
	imported := "imported"
	_, err := d.s.ModifyBook(c2, func(b *database.Book) error { b.LibraryState = &imported; return nil })
	require.NoError(t, err)
	// A and C are each a proven copy of B but share no audio with each other.
	a := d.copyBook(t, "A", "Ubik", "lib/Ubik A", dcRow{track: 1, dur: 600, hash: "u1"}, dcRow{track: 2, dur: 600, hash: "u2"})
	d.copyBook(t, "B", "Ubik", "lib/Ubik B", dcRow{track: 1, dur: 600, hash: "u1"}, dcRow{track: 2, dur: 600, hash: "u2"},
		dcRow{track: 3, dur: 600, hash: "u3"}, dcRow{track: 4, dur: 600, hash: "u4"})
	d.copyBook(t, "C", "Ubik", "lib/Ubik C", dcRow{track: 3, dur: 600, hash: "u3"}, dcRow{track: 4, dur: 600, hash: "u4"})

	res := d.planFor(t, dcFixerID, "op-plan", nil)
	r := rowOf(t, res, r1)
	require.Equal(t, dcSkipTrackOrder, r.Skipped, r.SkipReason)
	r = rowOf(t, res, c1)
	require.Equal(t, c1, rowOf(t, res, c1).BookIDs[0])
	require.Equal(t, dcSkipFoldCap, r.Skipped, r.SkipReason)
	r = rowOf(t, res, a)
	require.Equal(t, dcSkipNotClique, r.Skipped, r.SkipReason)
	require.Len(t, r.BookIDs, 3)
}

// TestDuplicateCopies_ResumesAfterTheHandOff: a run cut off after the
// primary loser handed its version group to the survivor re-plans to the
// same decision and finishes.
func TestDuplicateCopies_ResumesAfterTheHandOff(t *testing.T) {
	d := newDCFixture(t)
	s, l := d.dune(t)
	gid := "vg-dune"
	no := false
	_, err := d.s.ModifyBook(s, func(b *database.Book) error { b.VersionGroupID, b.IsPrimaryVersion = &gid, &no; return nil })
	require.NoError(t, err)
	_, err = d.s.ModifyBook(l, func(b *database.Book) error { b.VersionGroupID = &gid; return nil })
	require.NoError(t, err)
	res := d.planFor(t, dcFixerID, "op-plan", nil)
	id := dupRowID(s, l)
	row := findRow(t, res, id)
	require.True(t, row.Applicable(), row.SkipReason)
	require.Equal(t, []string{s}, row.Detail.(*dcPlan).HandOff[gid])

	// The cut-off run: the loser's demote journaled, the survivor crowned.
	w := repairs.NewWriter(d.s, d.s, dcFixerID, "bulk_update", "repairs-").WithJournal(d.s, d.s, "op-cut")
	require.NoError(t, w.Journal(l, undo.ChangeTypeBookPrimaryDemote, "is_primary_version", "true", "false"))
	cr, err := versionprimary.Crown(fragEnsureStore{OpsStore: d.s, chapters: d.s}, gid, s)
	require.NoError(t, err)
	require.Equal(t, s, cr.PrimaryID)

	out := d.applyFor(t, dcFixerID, "op-plan", "op-apply", []string{id})
	require.Equal(t, 1, out.Applied, "%+v", out.Rows)
	require.False(t, d.live(t, "L"))
	sb, err := d.s.GetBookByID(s)
	require.NoError(t, err)
	require.True(t, sb.IsPrimaryVersion != nil && *sb.IsPrimaryVersion)
}
