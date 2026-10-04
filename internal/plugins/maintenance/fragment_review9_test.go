// file: internal/plugins/maintenance/fragment_review9_test.go
// version: 1.0.0
// guid: 3bc1296c-6b8c-481b-b190-a196671cd4a8
// last-edited: 2026-10-04

package maintenance

import (
	"encoding/json"
	"fmt"
	"os"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/audiobooks"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/merge"
	"github.com/falkcorp/audiobook-organizer/internal/repairs"
)

// heldActions are the actions a held row offers ("To clear it: a, or b").
func heldActions(reason string) []string {
	_, acts, ok := strings.Cut(reason, "To clear it: ")
	if !ok {
		return nil
	}
	return strings.Split(acts, ", or ")
}

// heldOver lists res's held interrupted rows over any of ids.
func heldOver(res *repairs.PlanResult, ids []string) []repairs.Row {
	var out []repairs.Row
	for _, r := range res.Rows {
		if r.Skipped == fragSkipInterrupted && slices.ContainsFunc(r.BookIDs, func(id string) bool { return slices.Contains(ids, id) }) {
			out = append(out, r)
		}
	}
	return out
}

// hiddenFiles lists r's planned member files (copies keep theirs by design)
// that sit on a soft-deleted book, out of every view. keepOwn, when set, is
// a dedup merge's loser: it keeps its OWN planned file, as a dedup loser
// keeps its files (owner decision 2026-10-04); every other planned file on
// it, the ones the run moved there, counts. also are rows applied over r's
// books since (a fresh plan after a revert may keep a different copy of a
// chapter): a file counts only when it is a member's under r AND under
// every one of them.
func (f *fragFixture) hiddenFiles(t *testing.T, r repairs.Row, keepOwn string, also ...repairs.Row) []string {
	t.Helper()
	member := func(row repairs.Row) map[string]string { // file -> its member
		var st fragGroupState
		require.NoError(t, json.Unmarshal(row.State, &st))
		out := map[string]string{}
		for id, fid := range st.Files {
			if !st.Roles[id].Copy {
				out[fid] = id
			}
		}
		return out
	}
	planned := member(r)
	var others []map[string]string
	for _, row := range also {
		if row.Class == fragClassNoParent && len(row.State) > 0 {
			others = append(others, member(row))
		}
	}
	var out []string
	for _, id := range r.BookIDs {
		b, err := f.s.GetBookByID(id)
		require.NoError(t, err)
		if b == nil || !b.IsSoftDeleted() {
			continue
		}
		rows, err := f.s.GetBookFiles(id)
		require.NoError(t, err)
	file:
		for _, br := range rows {
			m, ok := planned[br.ID]
			if !ok || (id == keepOwn && m == keepOwn) {
				continue
			}
			for _, o := range others {
				if _, ok := o[br.ID]; !ok {
					continue file
				}
			}
			out = append(out, id+"/"+br.ID)
		}
	}
	slices.Sort(out)
	return out
}

// The action texts a held row offers (fragment_consolidation_fixer.go's act*
// helpers and the conflict and legacy texts).
var (
	actReRevert    = regexp.MustCompile(`^revert apply operation\(s\) (.+)$`)
	actReFinish    = regexp.MustCompile(`^merge every remaining book of the folder into (\S+) by hand$`)
	actReOutside   = regexp.MustCompile(`^move the planned file onto (\S+), then merge every remaining book of the folder into (\S+) by hand$`)
	actReRestore   = regexp.MustCompile(`^restore book (\S+) \(undo its deletion\), then plan again$`)
	actReRowInto   = regexp.MustCompile(`merge this row's books into (\S+) by hand$`)
	actRePick      = regexp.MustCompile(`^pick one of (\S+?)[, ]`)
	actReAll       = regexp.MustCompile(`^merge (.+) into (\S+) by hand, then merge every remaining book of the folder into (\S+) by hand$`)
	actReOutFile   = regexp.MustCompile(`planned file (\S+) of book (\S+) is now on book (\S+), outside the run`)
	actReAllOthers = regexp.MustCompile(` and `)
)

// doHeldAction carries out one offered action the way an owner would; ""
// when it went through, else what failed.
func (f *fragFixture) doHeldAction(t *testing.T, row repairs.Row, act string) string {
	t.Helper()
	act = strings.TrimSpace(act)
	switch {
	case actReRevert.MatchString(act):
		var errs []string
		for _, op := range strings.Split(actReRevert.FindStringSubmatch(act)[1], ", ") {
			if _, err := audiobooks.NewRevertService(f.s).RevertOperation(op); err != nil {
				errs = append(errs, op+": "+err.Error())
			}
		}
		return strings.Join(errs, " | ")
	case actReOutside.MatchString(act):
		m := actReOutside.FindStringSubmatch(act)
		o := actReOutFile.FindStringSubmatch(row.SkipReason)
		if o == nil {
			return "no outside file in the reason"
		}
		if err := f.s.MoveBookFilesToBook([]string{o[1]}, o[3], m[1]); err != nil {
			return err.Error()
		}
		f.finishInto(t, row.BookIDs, m[2])
	case actReAll.MatchString(act):
		m := actReAll.FindStringSubmatch(act)
		f.finishInto(t, actReAllOthers.Split(m[1], -1), m[2])
		f.finishInto(t, row.BookIDs, m[3])
	case actReFinish.MatchString(act):
		f.finishInto(t, row.BookIDs, actReFinish.FindStringSubmatch(act)[1])
	case actReRestore.MatchString(act):
		id := actReRestore.FindStringSubmatch(act)[1]
		if _, err := f.s.ModifyBook(id, func(b *database.Book) error {
			b.MarkedForDeletion, b.MarkedForDeletionAt = nil, nil
			return nil
		}); err != nil {
			return err.Error()
		}
	case actReRowInto.MatchString(act):
		f.finishInto(t, row.BookIDs, actReRowInto.FindStringSubmatch(act)[1])
	case actRePick.MatchString(act):
		f.finishInto(t, row.BookIDs, actRePick.FindStringSubmatch(act)[1])
	default:
		return "an action no owner can carry out: " + act
	}
	return ""
}

// heldScenario is something that happens to a cut run's books before the
// next plan. It returns the dedup loser that keeps its own file (or ""),
// and skip when the scenario does not apply at this cut point.
type heldScenario struct {
	name string
	run  func(t *testing.T, f *fragFixture, r repairs.Row, at int) (keepOwn string, skip bool)
}

// changeLiveMember flips the library state of one live non-survivor member
// (an outside edit the run did not make); false when there is none.
func changeLiveMember(t *testing.T, f *fragFixture, r repairs.Row) bool {
	t.Helper()
	for _, id := range r.BookIDs {
		b, err := f.s.GetBookByID(id)
		require.NoError(t, err)
		if id == r.Proposed["survivor"] || b.IsSoftDeleted() {
			continue
		}
		st := "organized"
		if b.LibraryState != nil && *b.LibraryState == "organized" {
			st = "imported"
		}
		_, err = f.s.ModifyBook(id, func(bk *database.Book) error { bk.LibraryState = &st; return nil })
		require.NoError(t, err)
		return true
	}
	return false
}

func pruneAll(t *testing.T, f *fragFixture) {
	t.Helper()
	_, err := f.s.PruneOperationChanges(time.Now().Add(time.Hour))
	require.NoError(t, err)
}

// heldScenarios are review 9's: the survivor retired by another fixer, by a
// dedup merge, or deleted outright; a planned file moved to a live book
// outside the run; a live member changed (with and without a prune); and
// reverts tried anyway after a retire or a prune.
func heldScenarios() []heldScenario {
	return []heldScenario{
		{"other-fixer", func(t *testing.T, f *fragFixture, r repairs.Row, at int) (string, bool) {
			f.otherFixerRetiresSurvivor(t, r.Proposed["survivor"], f.newLiveBook(t, fmt.Sprintf("x%d", at)))
			return "", false
		}},
		{"dedup", func(t *testing.T, f *fragFixture, r repairs.Row, at int) (string, bool) {
			x := f.newLiveBook(t, fmt.Sprintf("x%d", at))
			f.organized(t, x)
			s := r.Proposed["survivor"]
			if _, err := merge.NewService(f.s).MergeBooks([]string{s, x}, x); err != nil {
				return "", true
			}
			return s, false
		}},
		{"delete", func(t *testing.T, f *fragFixture, r repairs.Row, at int) (string, bool) {
			yes, now := true, time.Now().UTC()
			_, err := f.s.ModifyBook(r.Proposed["survivor"], func(b *database.Book) error {
				b.MarkedForDeletion, b.MarkedForDeletionAt = &yes, &now
				return nil
			})
			require.NoError(t, err)
			return "", false
		}},
		{"outside", func(t *testing.T, f *fragFixture, r repairs.Row, at int) (string, bool) {
			s := r.Proposed["survivor"]
			z := f.newLiveBook(t, fmt.Sprintf("z%d", at))
			var st fragGroupState
			require.NoError(t, json.Unmarshal(r.State, &st))
			moved := map[string]bool{}
			for id, fid := range st.Files {
				if !st.Roles[id].Copy && id != s {
					moved[fid] = true
				}
			}
			rows, err := f.s.GetBookFiles(s)
			require.NoError(t, err)
			for _, br := range rows {
				if moved[br.ID] {
					require.NoError(t, f.s.MoveBookFilesToBook([]string{br.ID}, s, z))
					return "", false
				}
			}
			return "", true
		}},
		{"changed", func(t *testing.T, f *fragFixture, r repairs.Row, at int) (string, bool) {
			return "", !changeLiveMember(t, f, r)
		}},
		{"changed+prune", func(t *testing.T, f *fragFixture, r repairs.Row, at int) (string, bool) {
			if !changeLiveMember(t, f, r) {
				return "", true
			}
			pruneAll(t, f)
			return "", false
		}},
		{"other-fixer+revert-anyway", func(t *testing.T, f *fragFixture, r repairs.Row, at int) (string, bool) {
			f.otherFixerRetiresSurvivor(t, r.Proposed["survivor"], f.newLiveBook(t, fmt.Sprintf("x%d", at)))
			_, _ = audiobooks.NewRevertService(f.s).RevertOperation("op-cut") // refused or partial: the point is the hold after it
			return "", false
		}},
		{"prune+revert-anyway", func(t *testing.T, f *fragFixture, r repairs.Row, at int) (string, bool) {
			pruneAll(t, f)
			_, err := audiobooks.NewRevertService(f.s).RevertOperation("op-cut")
			require.Error(t, err, "a revert of an op whose only row left is its plan record is refused")
			return "", false
		}},
	}
}

// cutShapes are the four shapes review 9 ran: the prod shape, no version
// group, the survivor inside the originals' group, and the hand-off to the
// survivor.
var cutShapes = [][2]string{{"all", cutVGOrig}, {"none", cutVGNone}, {"all", cutVGSurvivor}, {"all", cutVGHandOffToSurvivor}}

// actionMatrixTally counts one shape x scenario run.
type actionMatrixTally struct {
	cases, held, actions                         int
	actErr, stillHeld, splits, hidden, noActions int
	notes                                        []string
}

func (tl *actionMatrixTally) note(format string, a ...any) {
	if len(tl.notes) < 20 {
		tl.notes = append(tl.notes, fmt.Sprintf(format, a...))
	}
}

func (tl *actionMatrixTally) bad() int {
	return tl.actErr + tl.stillHeld + tl.splits + tl.hidden + tl.noActions
}

// settle applies every applicable row over r's books (the continuation, or
// the carry row) and checks the run ends as one live book with nothing
// hidden and nothing still held.
func (f *fragFixture) settle(t *testing.T, r repairs.Row, keepOwn, tag string, tl *actionMatrixTally) {
	t.Helper()
	applied := applicableRowsWith(f.plan(t, "op-settle0"), r.BookIDs)
	f.applyAllOver(t, r, "op-settle")
	if still := heldOver(f.plan(t, "op-settle2"), r.BookIDs); len(still) > 0 {
		tl.stillHeld++
		tl.note("STILL HELD %s: %s", tag, still[0].SkipReason)
	}
	if n := f.multiHolders(t, r); n > 1 {
		tl.splits++
		tl.note("SPLIT %s: %d live books hold the work", tag, n)
	}
	if h := f.hiddenFiles(t, r, keepOwn, applied...); len(h) > 0 {
		tl.hidden++
		tl.note("HIDDEN %s: %v", tag, h)
	}
}

// runActionMatrix cuts r's apply at every step-th event of shape sh, runs
// scenario sc, and carries out every action each held row offers, each on a
// fresh replay of the same cut: every one must clear the hold with one live
// book holding the work and no planned file hidden on a retired book.
func runActionMatrix(t *testing.T, sh [2]string, sc heldScenario, step int) *actionMatrixTally {
	tl := &actionMatrixTally{}
	for at := 1; ; at += step {
		f, r, closeF := newCutFixture(t, sh[0], sh[1])
		cut, more := f.cutAt(t, r, at)
		if !more {
			closeF()
			break
		}
		if !cut || !f.cutRunRecorded(t, "op-cut") {
			closeF()
			continue
		}
		keepOwn, skip := sc.run(t, f, r, at)
		if skip {
			closeF()
			continue
		}
		tl.cases++
		held := heldOver(f.plan(t, "op-plan2"), r.BookIDs)
		if len(held) == 0 {
			// Not held: the next plan continues it (or carries it); that
			// must end clean too.
			f.settle(t, r, keepOwn, fmt.Sprintf("at %d (not held)", at), tl)
			closeF()
			continue
		}
		tl.held++
		type act struct{ row, n int }
		var acts []act
		for i, h := range held {
			as := heldActions(h.SkipReason)
			if len(as) == 0 {
				tl.noActions++
				tl.note("NO ACTION at %d row %s: %s", at, h.RowID, h.SkipReason)
			}
			for n := range as {
				acts = append(acts, act{i, n})
			}
		}
		closeF()
		for _, a := range acts {
			f, r, closeF := newCutFixture(t, sh[0], sh[1])
			f.cutAt(t, r, at)
			keepOwn, _ := sc.run(t, f, r, at)
			rows := heldOver(f.plan(t, "op-plan2"), r.BookIDs)
			require.Greater(t, len(rows), a.row, "the replay of cut %d holds the same rows", at)
			as := heldActions(rows[a.row].SkipReason)
			require.Greater(t, len(as), a.n, "the replay of cut %d offers the same actions", at)
			tl.actions++
			tag := fmt.Sprintf("at %d action %q", at, as[a.n])
			if e := f.doHeldAction(t, rows[a.row], as[a.n]); e != "" {
				tl.actErr++
				tl.note("ACTION FAILED %s: %s", tag, e)
			}
			f.settle(t, r, keepOwn, tag, tl)
			closeF()
		}
	}
	return tl
}

// TestFragmentFixer_HeldRowActionMatrix (review 9): every action a held row
// offers, in every scenario, clears its hold with no split and no hidden
// file: all four shapes at every 5th cut point (about two minutes on the
// in-memory fixtures). Under -short (CI's -race run) the prod shape at every
// 10th, unless AORG_FRAG_ACTION_MATRIX=full.
func TestFragmentFixer_HeldRowActionMatrix(t *testing.T) {
	shapes, step := cutShapes, 5
	if testing.Short() && os.Getenv("AORG_FRAG_ACTION_MATRIX") != "full" {
		shapes, step = cutShapes[:1], 10
	}
	for _, sh := range shapes {
		for _, sc := range heldScenarios() {
			t.Run(sh[0]+"/"+sh[1]+"/"+sc.name, func(t *testing.T) {
				tl := runActionMatrix(t, sh, sc, step)
				t.Logf("cases=%d held=%d actions=%d failed=%d still-held=%d splits=%d hidden=%d no-action=%d",
					tl.cases, tl.held, tl.actions, tl.actErr, tl.stillHeld, tl.splits, tl.hidden, tl.noActions)
				for _, n := range tl.notes {
					t.Log(n)
				}
				require.Zero(t, tl.bad(), "every offered action clears the hold, with one live book and nothing hidden")
				require.NotZero(t, tl.cases, "the scenario applies at some cut point")
			})
		}
	}
}

// TestFragmentFixer_DeletedSurvivorIsNotAMerge (review 9 B1): a survivor
// deleted outright, in a version group whose live primary is a run member,
// is not "merged into" that primary. The held row says it was deleted and
// offers only the restore; merging the rest into the group's primary by
// hand (the round-9 offer) leaves the run held, because the survivor's
// planned files would be hidden; restoring it lets the run finish.
func TestFragmentFixer_DeletedSurvivorIsNotAMerge(t *testing.T) {
	// deleted cuts the run at at and deletes its survivor outright; nil when
	// the run was not cut there.
	deleted := func(at int) (f *fragFixture, r repairs.Row, survivor string, held []repairs.Row, closeF func(), more bool) {
		f, r, closeF = newCutFixture(t, "all", cutVGHandOffToSurvivor)
		cut, more := f.cutAt(t, r, at)
		if !more || !cut || !f.cutRunRecorded(t, "op-cut") {
			closeF()
			return nil, r, "", nil, nil, more
		}
		survivor = r.Proposed["survivor"]
		yes, now := true, time.Now().UTC()
		_, err := f.s.ModifyBook(survivor, func(b *database.Book) error {
			b.MarkedForDeletion, b.MarkedForDeletionAt = &yes, &now
			return nil
		})
		require.NoError(t, err)
		return f, r, survivor, heldOver(f.plan(t, "op-plan2"), r.BookIDs), closeF, true
	}
	tested, attacked := 0, 0
	for at := 1; ; at += 5 {
		f, r, survivor, held, closeF, more := deleted(at)
		if !more {
			break
		}
		if f == nil {
			continue
		}
		require.Len(t, held, 1, "at %d", at)
		require.Contains(t, held[0].SkipReason, "survivor "+survivor+" was deleted", "at %d", at)
		require.Equal(t, []string{actRestore(survivor)}, heldActions(held[0].SkipReason), "at %d", at)

		// The round-9 attack: merge the rest into the version group's live
		// primary by hand, as the round-9 row offered. With planned files
		// still on the deleted survivor the run must stay held.
		sb, err := f.s.GetBookByID(survivor)
		require.NoError(t, err)
		if g := dcStr(sb.VersionGroupID); g != "" {
			members, err := f.s.GetBooksByVersionGroup(g)
			require.NoError(t, err)
			for _, m := range members {
				if m.ID == survivor || m.IsSoftDeleted() || m.IsPrimaryVersion == nil || !*m.IsPrimaryVersion {
					continue
				}
				f.finishInto(t, r.BookIDs, m.ID)
				if len(f.hiddenFiles(t, r, "")) > 0 {
					attacked++
					require.NotEmpty(t, heldOver(f.plan(t, "op-plan3"), r.BookIDs),
						"at %d: the run is not done while planned files sit on the deleted survivor", at)
				}
				break
			}
		}
		closeF()

		// The offered action, on a fresh replay of the same cut.
		f, r, survivor, held, closeF, _ = deleted(at)
		require.NotNil(t, f)
		tl := &actionMatrixTally{}
		require.Empty(t, f.doHeldAction(t, held[0], actRestore(survivor)))
		f.settle(t, r, "", fmt.Sprintf("at %d", at), tl)
		require.Zero(t, tl.bad(), "at %d: %v", at, tl.notes)
		tested++
		closeF()
	}
	require.NotZero(t, tested)
	require.NotZero(t, attacked, "the attack hid files at some cut point, so the hold was tested against it")
}

// TestFragmentFixer_DedupLoserSurvivorIsCarried (owner decision 2026-10-04):
// a dedup merge retires the survivor S into X; finishing the run into X
// moves the files the run put on S onto X too (the carry row), journaled and
// undoable; S keeps only its own planned file. All four shapes, every 5th
// cut point: no hidden file, no split. The first carry of each shape is
// also reverted: its files go back onto S and the carry row is offered
// again.
func TestFragmentFixer_DedupLoserSurvivorIsCarried(t *testing.T) {
	dedup := heldScenarios()[1]
	for _, sh := range cutShapes {
		t.Run(sh[0]+"/"+sh[1], func(t *testing.T) {
			carried, reverted, cases := 0, false, 0
			for at := 1; ; at += 5 {
				f, r, closeF := newCutFixture(t, sh[0], sh[1])
				cut, more := f.cutAt(t, r, at)
				if !more {
					closeF()
					break
				}
				if !cut || !f.cutRunRecorded(t, "op-cut") {
					closeF()
					continue
				}
				s, skip := dedup.run(t, f, r, at)
				if skip {
					closeF()
					continue
				}
				cases++
				tl := &actionMatrixTally{}
				if held := heldOver(f.plan(t, "op-plan2"), r.BookIDs); len(held) > 0 {
					as := heldActions(held[0].SkipReason)
					require.Len(t, as, 1, "at %d: %s", at, held[0].SkipReason)
					require.Regexp(t, actReFinish, as[0], "at %d", at)
					require.Empty(t, f.doHeldAction(t, held[0], as[0]))
				}
				res := f.plan(t, "op-plan3")
				var carry *repairs.Row
				for i := range res.Rows {
					if res.Rows[i].Class == fragClassCarry && slices.Contains(res.Rows[i].BookIDs, s) {
						carry = &res.Rows[i]
					}
				}
				if carry != nil {
					require.True(t, carry.Applicable(), "at %d: %s", at, carry.SkipReason)
					out := f.apply(t, "op-plan3", "op-carry", []string{carry.RowID}, nil)
					require.Equal(t, 1, out.Applied, "at %d: %v", at, out.ByOutcome)
					carried++
					if !reverted {
						reverted = true
						require.Empty(t, f.hiddenFiles(t, r, s), "at %d: the carry left nothing on S", at)
						_, err := audiobooks.NewRevertService(f.s).RevertOperation("op-carry")
						require.NoError(t, err, "at %d: the carry row's moves revert", at)
						require.NotEmpty(t, f.hiddenFiles(t, r, s), "at %d: the revert put the files back on S", at)
						again := f.plan(t, "op-plan4")
						found := false
						for _, row := range again.Rows {
							found = found || (row.Class == fragClassCarry && row.RowID == carry.RowID && row.Applicable())
						}
						require.True(t, found, "at %d: the carry row is offered again after its revert", at)
					}
				}
				f.settle(t, r, s, fmt.Sprintf("at %d", at), tl)
				require.Zero(t, tl.bad(), "at %d: %v", at, tl.notes)
				closeF()
			}
			t.Logf("cases=%d carried=%d", cases, carried)
			require.NotZero(t, cases)
		})
	}
}

// TestFragmentFixer_PrunedRunContinuesAtEveryCut (review 9 follow-up): a
// run cut at any point whose rows the prune then removed (only its plan
// record is kept) still continues, its own hand-off's flag changes
// explained by the replay from the planned flags, and finishes as one book.
func TestFragmentFixer_PrunedRunContinuesAtEveryCut(t *testing.T) {
	step := 5
	if testing.Short() {
		step = 10
	}
	for _, sh := range cutShapes {
		t.Run(sh[0]+"/"+sh[1], func(t *testing.T) {
			cases := 0
			for at := 1; ; at += step {
				f, r, closeF := newCutFixture(t, sh[0], sh[1])
				cut, more := f.cutAt(t, r, at)
				if !more {
					closeF()
					break
				}
				if !cut || !f.cutRunRecorded(t, "op-cut") {
					closeF()
					continue
				}
				pruneAll(t, f)
				cases++
				for _, h := range heldOver(f.plan(t, "op-plan2"), r.BookIDs) {
					require.NotContains(t, h.SkipReason, "this row's apply did not change it",
						"at %d: the run's own flag changes are explained after the prune", at)
					require.NotContains(t, h.SkipReason, "revert apply operation", "at %d: a revert that is refused is not offered", at)
				}
				tl := &actionMatrixTally{}
				f.settle(t, r, "", fmt.Sprintf("at %d", at), tl)
				require.Zero(t, tl.bad(), "at %d: %v", at, tl.notes)
				closeF()
			}
			require.NotZero(t, cases)
		})
	}
}

// TestFragmentFixer_RecordsOfOneFolderOfferOneActionSet (review 9 follow-up):
// two plan records of one row id in one folder, one whose survivor another
// fixer retired into X and one on another live survivor, give both held rows
// the same actions, and carrying them out clears both as one live book.
func TestFragmentFixer_RecordsOfOneFolderOfferOneActionSet(t *testing.T) {
	for _, bLater := range []bool{true, false} {
		t.Run(fmt.Sprintf("second-record-later=%v", bLater), func(t *testing.T) {
			setup := func() (*fragFixture, repairs.Row, string) {
				f := newFragFixture(t)
				planned, survivor, others, otherRows := f.looseCut(t)
				w := f.fragWriter(t, "op-cut")
				f.journalPlanRecord(t, w, planned)
				f.moveAndRetire(t, w, fragFixerID, survivor, others[:1], otherRows[:1], 0)
				x := f.newLiveBook(t, "x")
				f.otherFixerRetiresSurvivor(t, survivor, x)
				at := time.Now().UTC().Add(time.Minute)
				if !bLater {
					at = time.Now().UTC().Add(-time.Hour)
				}
				f.forgeRecord(t, planned, "op-cut2", others[1], at)
				return f, planned, x
			}
			f, planned, x := setup()
			held := heldOver(f.plan(t, "op-plan2"), planned.BookIDs)
			require.Len(t, held, 2)
			require.Equal(t, heldActions(held[0].SkipReason), heldActions(held[1].SkipReason), "one action set for the folder")
			require.Equal(t, []string{actFinish(x)}, heldActions(held[0].SkipReason), "only X can finish both runs")
			require.Empty(t, applicableRowsWith(f.plan(t, "op-plan3"), planned.BookIDs), "neither run is continued around its own survivor")
			tl := &actionMatrixTally{}
			require.Empty(t, f.doHeldAction(t, held[0], actFinish(x)))
			f.settle(t, planned, "", "finish into X", tl)
			require.Zero(t, tl.bad(), "%v", tl.notes)
		})
	}
}
