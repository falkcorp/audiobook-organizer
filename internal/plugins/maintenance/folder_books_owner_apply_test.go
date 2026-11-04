// file: internal/plugins/maintenance/folder_books_owner_apply_test.go
// version: 1.3.0
// guid: 7c41e9b8-2d05-4f36-a8c1-5e3b9d0f6a24
// last-edited: 2026-11-04

package maintenance

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/audiobooks"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/repairs"
	"github.com/falkcorp/audiobook-organizer/internal/undo"
)

const (
	fbOwnerUser  = "01JBRUCEWAYNE0000000000000"
	fbOwnerEmail = "bruce.wayne@example.invalid"
)

// ownerShelf describes the folder-book seedOwnerRow builds.
type ownerShelf struct {
	// base is where the author folder sits under the fixture root: fbITunes
	// for the mixed-shelf driving shape, "" for a library-only row.
	base string
	// folder and title name the folder-book's folder and its book. Both
	// default to the author "Alfred Pennyworth", an author folder.
	folder, title string
	// libWorks are one-file works filed in the library folder
	// (root/<folder>/<work>/01.mp3), also held by the folder-book.
	libWorks []string
}

// seedOwnerRow builds a folder-book "Alfred Pennyworth" over three proper
// books (Year One, The Long Halloween, Hush: three files each), two works no
// proper book holds (Dark Victory, Knightfall: two files each) under
// s.base, plus the library-folder works of s.libWorks. It returns the
// folder-book's id.
func (f *fragFixture) seedOwnerRow(t *testing.T, s ownerShelf) string {
	t.Helper()
	if s.folder == "" {
		s.folder = "Alfred Pennyworth"
	}
	if s.title == "" {
		s.title = s.folder
	}
	var all []string
	for _, w := range []struct {
		title  string
		n      int
		proper bool
	}{{"Year One", 3, true}, {"The Long Halloween", 3, true}, {"Hush", 3, true}, {"Dark Victory", 2, false},
		{"Knightfall", 2, false}} {
		var paths []string
		for i := 1; i <= w.n; i++ {
			paths = append(paths, f.file(t, filepath.Join(s.base, s.folder, w.title, fmt.Sprintf("0%d.mp3", i)), 10))
		}
		all = append(all, paths...)
		if w.proper {
			f.fbBook(t, strings.ReplaceAll(strings.ToLower(w.title), " ", "-"), w.title,
				f.path(filepath.Join(s.base, s.folder, w.title)), 3600, paths...)
		}
	}
	author, err := f.s.CreateAuthor("Alfred Pennyworth")
	require.NoError(t, err)
	id := f.fbBook(t, "fb", s.title, f.path(filepath.Join(s.base, s.folder)), 1200, all...)
	_, err = f.s.ModifyBook(id, func(b *database.Book) error {
		if s.folder == "Alfred Pennyworth" {
			b.AuthorID = &author.ID
		}
		return nil
	})
	require.NoError(t, err)
	for i, w := range s.libWorks {
		p := f.file(t, filepath.Join("Alfred Pennyworth", w, "01.mp3"), 10)
		f.row(t, fmt.Sprintf("fb-lib-%d", i), id, p, "", int64(300+i), 3600, 20+i)
	}
	return id
}

// fbOwnerApply runs the owner's click on rowIDs the way the Repairs handler
// does: a one-shot grant in the process's store, and an apply op marked
// OwnerApply that was enqueued under the owner's Cloudflare Access sign-in.
func (f *fragFixture) fbOwnerApply(t *testing.T, planOpID, opID string, rowIDs []string) *repairs.ApplyResult {
	t.Helper()
	f.p.ownerEmail = func() string { return fbOwnerEmail }
	tok, err := repairs.DefaultOwnerGrants.Issue(repairs.OwnerGrant{UserID: fbOwnerUser, AuthMethod: repairs.OwnerAuthMethod,
		AccessEmail: fbOwnerEmail, FixerID: fbFixerID, PlanOpID: planOpID, RowIDs: rowIDs})
	require.NoError(t, err)
	user := fbOwnerUser
	f.ops.mu.Lock()
	f.ops.rows[opID] = &database.OperationV2Row{ID: opID, ActorUserID: &user, ActorAuthMethod: repairs.OwnerAuthMethod, OwnerApply: true}
	f.ops.mu.Unlock()
	no := false
	params, err := json.Marshal(repairs.ApplyParams{FixerID: fbFixerID, PlanOpID: planOpID, OwnerApplyRowIDs: rowIDs,
		OwnerGrant: tok, DryRun: &no})
	require.NoError(t, err)
	rep := &repairsOpReporter{id: opID}
	require.NoError(t, f.p.runRepairsApply(context.Background(), params, rep))
	res, ok := rep.result.(*repairs.ApplyResult)
	require.True(t, ok)
	return res
}

func fbOutcome(res *repairs.ApplyResult, rowID string) repairs.RowResult {
	for _, r := range res.Rows {
		if r.RowID == rowID {
			return r
		}
	}
	return repairs.RowResult{}
}

func (f *fragFixture) liveAtDir(t *testing.T, rel string) *database.Book {
	t.Helper()
	return f.livePathTitled(t, f.path(rel))
}

// The driving shape: an author folder on the iTunes shelf with two works
// filed in the library folder that no book holds. Owner decision 2026-11-03:
// the row is listed (skipped_mixed_shelf) and offered to the owner alone.
func TestFolderBooksOwner_MixedShelfRowIsOfferedToTheOwner(t *testing.T) {
	f := newGlobalRootFragFixture(t)
	fb := f.seedOwnerRow(t, ownerShelf{base: fbITunes, libWorks: []string{"Arkham Asylum", "The Killing Joke"}})
	res := f.fbPlan(t, fbFixerID, "op-plan")
	require.Len(t, res.Rows, 1)
	row := res.Rows[0]
	require.Equal(t, fbTierMixedShelf, row.Class, "the class stays mixed-shelf")
	require.Equal(t, fbSkipMixedShelf, row.Skipped, "a plain apply reports what it always did: %s", row.SkipReason)
	require.True(t, row.OwnerApplicable, "%s", row.SkipReason)
	require.Equal(t, 1, res.OwnerApplicable)
	require.Zero(t, res.Applicable)
	require.Empty(t, row.Flags)
	require.Equal(t, "4", row.Proposed["new_books"])
	require.Equal(t, row.BookIDs, row.OwnerWrites)
	require.Contains(t, row.OwnerWrites, fb)
	require.NotEmpty(t, row.Holds)
	require.Equal(t, fbSkipMixedShelf, row.Holds[0].Kind, "the first hold is the row's Skipped")
	for _, h := range row.Holds {
		require.True(t, h.OwnerOK, "an owner row lists only holds the owner may resolve: %+v", h)
	}
	for _, want := range []string{"Arkham Asylum", "The Killing Joke", "Dark Victory", "Knightfall", "organized", "no file moves",
		"reverts from the operation"} {
		require.Contains(t, row.OwnerApplyReason, want)
	}
	plan := row.Detail.(*fbPlan)
	states := map[string]string{}
	for _, g := range plan.Groups {
		states[g.Title] = g.State
	}
	require.Equal(t, map[string]string{"Dark Victory": "imported", "Knightfall": "imported",
		"Arkham Asylum": "organized", "The Killing Joke": "organized"}, states,
		"library books are organized (owner decision 2026-11-03); iTunes books keep the folder-book's state")
}

// A held-back row is never written by a plain apply, bulk selection included.
func TestFolderBooksOwner_PlainApplyNeverWritesAnOwnerRow(t *testing.T) {
	f := newGlobalRootFragFixture(t)
	f.seedOwnerRow(t, ownerShelf{base: fbITunes, libWorks: []string{"Arkham Asylum"}})
	row := f.fbSingleRow(t, "op-plan")
	require.True(t, row.OwnerApplicable)
	rowsBefore := fbRowCount(t, f.s)
	out := f.fbApply(t, "op-plan", "op-apply", []string{row.RowID})
	require.Zero(t, out.Applied)
	require.Equal(t, repairs.OutcomeNotApplicable, fbOutcome(out, row.RowID).Outcome)
	require.True(t, f.live(t, "fb"))
	require.Equal(t, rowsBefore, fbRowCount(t, f.s))
}

// The fixer's own gate: Apply refuses a held-back row when ctx carries no
// marker for it, however the caller got there, and writes it with one.
func TestFolderBooksOwner_ApplyFailsClosedWithoutTheMarker(t *testing.T) {
	f := newGlobalRootFragFixture(t)
	f.seedOwnerRow(t, ownerShelf{base: fbITunes, libWorks: []string{"Arkham Asylum"}})
	row := f.fbSingleRow(t, "op-plan")
	require.True(t, row.OwnerApplicable)
	fx := newFolderBooksFixer(f.p)
	store := f.p.deps.OpsStore()
	w := repairs.NewWriter(store, f.p.deps.VersionPrimaryStore(), fbFixerID, "bulk_update", "repairs-").
		WithJournal(store, store, "op-direct")
	for name, ctx := range map[string]context.Context{
		"no marker":        context.Background(),
		"another row":      repairs.WithOwnerApply(context.Background(), "folder:other"),
		"empty row marker": repairs.WithOwnerApply(context.Background(), ""),
	} {
		err := fx.Apply(ctx, w, row)
		require.ErrorIs(t, err, repairs.ErrChangedSincePlan, name)
		require.True(t, f.live(t, "fb"), name)
	}
	// A plain row whose re-plan is not owner-applicable is refused even with
	// the marker.
	_, err := f.s.ModifyBook(f.ids["fb"], func(b *database.Book) error {
		pid := "00000000000000AB"
		b.ITunesPersistentID = &pid
		return nil
	})
	require.NoError(t, err)
	require.ErrorIs(t, fx.Apply(repairs.WithOwnerApply(context.Background(), row.RowID), w, row), repairs.ErrChangedSincePlan)
	require.True(t, f.live(t, "fb"))
}

// The owner's click applies the row: library works become organized books at
// their own folders, the iTunes orphans keep the folder-book's state, nothing
// moves on disk, the audit note is journaled, and the op reverts.
func TestFolderBooksOwner_OwnerApplyCreatesTheBooksAndReverts(t *testing.T) {
	f := newGlobalRootFragFixture(t)
	f.seedOwnerRow(t, ownerShelf{base: fbITunes, libWorks: []string{"Arkham Asylum", "The Killing Joke"}})
	row := f.fbSingleRow(t, "op-plan")
	require.True(t, row.OwnerApplicable)
	rowsBefore := fbRowCount(t, f.s)

	out := f.fbOwnerApply(t, "op-plan", "op-owner", []string{row.RowID})
	require.Equal(t, 1, out.Applied, "%+v", out.Rows)
	require.Equal(t, fbOwnerUser, fbOutcome(out, row.RowID).OwnerUserID)
	require.False(t, f.live(t, "fb"), "the folder-book is retired")
	require.Equal(t, rowsBefore+6, fbRowCount(t, f.s), "two library files and four iTunes files got new rows")

	wantState := map[string]string{"Arkham Asylum": "organized", "The Killing Joke": "organized",
		"Dark Victory": "imported", "Knightfall": "imported"}
	paths := map[string]string{
		"Arkham Asylum":    filepath.Join("Alfred Pennyworth", "Arkham Asylum"),
		"The Killing Joke": filepath.Join("Alfred Pennyworth", "The Killing Joke"),
		"Dark Victory":     filepath.Join(fbITunes, "Alfred Pennyworth", "Dark Victory"),
		"Knightfall":       filepath.Join(fbITunes, "Alfred Pennyworth", "Knightfall"),
	}
	created := map[string]string{}
	for title, rel := range paths {
		b := f.liveAtDir(t, rel)
		require.NotNil(t, b, title)
		require.Equal(t, title, b.Title)
		require.NotNil(t, b.LibraryState)
		require.Equal(t, wantState[title], *b.LibraryState, title)
		require.NotNil(t, b.AuthorID, title)
		created[title] = b.ID
		rows, err := f.s.GetBookFiles(b.ID)
		require.NoError(t, err)
		for _, r := range rows {
			_, err := os.Stat(r.FilePath)
			require.NoError(t, err, "nothing moved on disk")
		}
	}

	changes, err := f.s.GetOperationChanges("op-owner")
	require.NoError(t, err)
	notes := 0
	for _, c := range changes {
		if c.ChangeType == undo.ChangeTypeRepairOwnerApply {
			notes++
		}
	}
	require.Equal(t, len(row.OwnerWrites), notes, "one audit note per book the owner apply writes")

	// A re-plan finds nothing left to flag, and the grant is single-use.
	require.Empty(t, f.fbPlan(t, fbFixerID, "op-plan-2").Rows)

	_, err = audiobooks.NewRevertService(f.s).RevertOperation("op-owner")
	require.NoError(t, err)
	require.True(t, f.live(t, "fb"), "the revert brings the folder-book back")
	for title, id := range created {
		b, err := f.s.GetBookByID(id)
		require.NoError(t, err)
		require.True(t, b.IsSoftDeleted(), "%s is hidden by the revert", title)
	}
	require.Equal(t, rowsBefore+6, fbRowCount(t, f.s), "the revert deletes no row")
}

// Plan and Replan key a minor-shelf group the same way: a cut-off owner apply
// leaves hidden created books that Replan must recognise as this row's own, or
// the row would re-plan as "previously resolved" and its fingerprint change.
func TestFolderBooksOwner_ReplanAgreesWithPlanAfterACutOffCreate(t *testing.T) {
	f := newGlobalRootFragFixture(t)
	f.seedOwnerRow(t, ownerShelf{base: fbITunes, libWorks: []string{"Arkham Asylum", "The Killing Joke"}})
	row := f.fbSingleRow(t, "op-plan")
	require.True(t, row.OwnerApplicable)
	books := 0
	fbCrashHook = func(stage string, _ int) error {
		if stage == "book" {
			books++
			if books == 4 {
				return fmt.Errorf("injected stop after the third created book")
			}
		}
		return nil
	}
	t.Cleanup(func() { fbCrashHook = nil })
	out := f.fbOwnerApply(t, "op-plan", "op-owner", []string{row.RowID})
	fbCrashHook = nil
	require.Zero(t, out.Applied, "%+v", out.Rows)
	require.True(t, f.live(t, "fb"))

	fx := newFolderBooksFixer(f.p)
	fresh, err := fx.Replan(context.Background(), nil, row, nil)
	require.NoError(t, err)
	require.Equal(t, row.Fingerprint, fresh.Fingerprint, "skip %s: %s", fresh.Skipped, fresh.SkipReason)
	require.True(t, fresh.OwnerApplicable, "%s", fresh.SkipReason)
	require.Equal(t, row.OwnerApplyReason, fresh.OwnerApplyReason)
}

// Every hold that is not the owner's to resolve withdraws the owner click and
// leaves the row listed exactly as a plain apply reports it.
func TestFolderBooksOwner_AHardHoldWithdrawsTheOwnerRow(t *testing.T) {
	cases := []struct {
		name  string
		shelf ownerShelf
		prep  func(t *testing.T, f *fragFixture)
		// hard is the hold the owner may not resolve, which row.Holds must
		// name even when it is not the row's first (Skipped) hold.
		hard string
	}{
		{name: "iTunes id on the folder-book", shelf: ownerShelf{base: fbITunes, libWorks: []string{"Arkham Asylum"}},
			prep: func(t *testing.T, f *fragFixture) {
				_, err := f.s.ModifyBook(f.ids["fb"], func(b *database.Book) error {
					pid := "00000000000000AB"
					b.ITunesPersistentID = &pid
					return nil
				})
				require.NoError(t, err)
			}, hard: fbSkipITunesID},
		{name: "primary with no heir", shelf: ownerShelf{base: fbITunes, libWorks: []string{"Arkham Asylum"}},
			prep: func(t *testing.T, f *fragFixture) {
				f.setVG(t, f.ids["fb"], "vg-pennyworth", true)
				f.setVG(t, f.ids["hush"], "vg-pennyworth", false) // never organized: cannot be crowned
			}, hard: fbSkipNoHeir},
		{name: "second shelf over a fifth of the files",
			shelf: ownerShelf{base: fbITunes, libWorks: []string{"Arkham Asylum", "The Killing Joke", "Gotham Central", "Hush Returns"}}, hard: fbSkipMixedShelf},
		{name: "deep base tier", shelf: ownerShelf{base: fbITunes, folder: "Gotham Collection", title: "Gotham Collection Box",
			libWorks: []string{"Arkham Asylum"}}, hard: fbSkipDeep},
		{name: "library work of one file under an import path", shelf: ownerShelf{base: fbITunes},
			prep: func(t *testing.T, f *fragFixture) {
				ip := f.path("imports")
				_, err := f.s.CreateImportPath(ip, "Imports")
				require.NoError(t, err)
				p := f.file(t, filepath.Join("imports", "Alfred Pennyworth", "Arkham Asylum", "01.mp3"), 10)
				f.row(t, "fb-import", f.ids["fb"], p, "", 400, 3600, 30)
			}, hard: fbSkipNeedsNewBook},
		{name: "a library work that repeats another group's title", shelf: ownerShelf{base: fbITunes, libWorks: []string{"Knightfall"}}, hard: fbSkipUnclearWork},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newGlobalRootFragFixture(t)
			f.seedOwnerRow(t, tc.shelf)
			if tc.prep != nil {
				tc.prep(t, f)
			}
			res := f.fbPlan(t, fbFixerID, "op-plan")
			require.Len(t, res.Rows, 1)
			row := res.Rows[0]
			require.Equal(t, fbTierMixedShelf, row.Class, "%s", row.SkipReason)
			require.NotEmpty(t, row.Skipped)
			require.False(t, row.OwnerApplicable, "owner-applicable despite a hard hold; reason %q", row.OwnerApplyReason)
			require.NotEmpty(t, row.Holds)
			require.Equal(t, row.Skipped, row.Holds[0].Kind, "the first hold is the row's Skipped")
			require.NotEmpty(t, fbHardHoldWhy(row.Holds, tc.hard), "the hard hold %s is listed and marked hard: %+v", tc.hard, row.Holds)
			require.Zero(t, res.OwnerApplicable)
						out := f.fbOwnerApply(t, "op-plan", "op-owner", []string{row.RowID})
			require.Zero(t, out.Applied)
			require.Equal(t, repairs.OutcomeOwnerRefused, fbOutcome(out, row.RowID).Outcome)
			require.True(t, f.live(t, "fb"))
		})
	}
}

// fbHardHoldWhy returns the reason of the first hold of kind the owner may
// not resolve, "" when there is none.
func fbHardHoldWhy(holds []repairs.RowHold, kind string) string {
	for _, h := range holds {
		if h.Kind == kind && !h.OwnerOK {
			return h.Why
		}
	}
	return ""
}

// Rows are classified by the base tier even when mixed-shelf overwrites Tier.
func TestFolderBooksOwner_EvaluateKeepsTheBaseTier(t *testing.T) {
	dir := "/media/iTunes Media/Audiobooks/Dark Knight Returns"
	var major []string
	for _, w := range []string{"Year One", "The Long Halloween", "Hush", "The Killing Joke", "Dark Victory",
		"Knightfall", "No Mans Land", "Batman RIP", "Court of Owls"} {
		major = append(major, dir+"/"+w+".m4b")
	}
	minor := []string{"/lib/Bruce Wayne/Arkham Asylum/Arkham Asylum.m4a"}

	// Held minority: single verdict, base tier equals tier, share counted.
	ev := fbMixedLib(major, minor, map[string]bool{minor[0]: true}).evaluate("b")
	require.Equal(t, ev.Tier, ev.BaseTier)
	require.InDelta(t, 0.9, ev.MajorShare, 1e-9)
	require.Zero(t, ev.MinorUnheld)

	// Unheld minority: mixed-shelf over a deep base (nine 11h files, no author folder).
	ev = fbMixedLib(major, minor, nil).evaluate("b")
	require.Equal(t, fbTierMixedShelf, ev.Tier)
	require.Equal(t, fbTierDeep, ev.BaseTier, "%v", ev.Evidence)
	require.False(t, fbApplicableTier(ev.BaseTier))
	require.Equal(t, 1, ev.MinorUnheld)
	require.InDelta(t, 0.9, ev.MajorShare, 1e-9)

	// One shelf only: share is 1.
	ev = fbMixedLib(major, nil, nil).evaluate("b")
	require.InDelta(t, 1.0, ev.MajorShare, 1e-9)
	require.Equal(t, ev.Tier, ev.BaseTier)
}

func TestFBGroupRoot(t *testing.T) {
	lib := newFBLib()
	lib.shelves = []string{"/lib", "/imports"}
	shelf, root := "/media/iTunes Media/Audiobooks", "/media/iTunes Media/Audiobooks/Bruce Wayne"
	cases := []struct{ name, p, want string }{
		{"majority path keeps the root", shelf + "/Bruce Wayne/Year One/01.mp3", root},
		{"majority flat file keeps the root", shelf + "/Bruce Wayne/Hush.m4b", root},
		{"library work keys under its author folder", "/lib/Bruce Wayne/Arkham Asylum/01.mp3", "/lib/Bruce Wayne"},
		{"library flat file keys under its author folder", "/lib/Bruce Wayne/The Killing Joke.m4b", "/lib/Bruce Wayne"},
		{"deeper library path still keys under the author folder", "/lib/Bruce Wayne/Year Two/Part 1/01.mp3", "/lib/Bruce Wayne"},
		{"import path work", "/imports/Dick Grayson/Nightwing/01.mp3", "/imports/Dick Grayson"},
		{"file directly in a shelf has no author folder: the shelf", "/lib/Hush.m4b", "/lib"},
		{"no shelf at all keeps the root", "/elsewhere/Bruce Wayne/Hush.m4b", root},
	}
	for _, c := range cases {
		require.Equal(t, c.want, lib.groupRoot(c.p, shelf, root), c.name)
	}
	// Keys of a minor group never sit under the majority root.
	k, _, dir := fbGroupKey("/lib/Bruce Wayne/Arkham Asylum/01.mp3", lib.groupRoot("/lib/Bruce Wayne/Arkham Asylum/01.mp3", shelf, root))
	require.Equal(t, "dir:/lib/Bruce Wayne/Arkham Asylum", k)
	require.Equal(t, "/lib/Bruce Wayne/Arkham Asylum", dir)
}

// Owner decision 2026-11-04: a row held back only by holds the owner could
// resolve is his whether its files sit on one shelf or two. Here every file is
// on the library shelf, so the row is a plain "shelf" row, offered to the owner
// (no flag, no measurement), and the owner's click writes it.
func TestFolderBooksOwner_SingleShelfRowIsOfferedToTheOwner(t *testing.T) {
	f := newGlobalRootFragFixture(t)
	fb := f.seedOwnerRow(t, ownerShelf{}) // every file in the library folder
	rep := &repairsOpReporter{id: "op-plan"}
	params, err := json.Marshal(repairs.PlanParams{FixerID: fbFixerID})
	require.NoError(t, err)
	require.NoError(t, f.p.runRepairsPlan(context.Background(), params, rep))
	res := rep.result.(*repairs.PlanResult)
	require.Len(t, res.Rows, 1)
	row := res.Rows[0]
	require.Equal(t, fbTierShelf, row.Class)
	require.Equal(t, fbSkipNeedsNewBook, row.Skipped, "a plain apply reports what it always did: %s", row.SkipReason)
	require.True(t, row.OwnerApplicable, "%s", row.SkipReason)
	require.Equal(t, 1, res.OwnerApplicable)
	require.Zero(t, res.Applicable)
	require.Empty(t, row.Flags)
	require.Empty(t, res.ByFlag)
	require.Equal(t, row.BookIDs, row.OwnerWrites)
	require.Contains(t, row.OwnerWrites, fb)
	require.NotEmpty(t, row.Holds)
	for _, h := range row.Holds {
		require.True(t, h.OwnerOK, "an owner row lists only holds the owner may resolve: %+v", h)
	}
	require.Contains(t, row.OwnerApplyReason, "Owner apply of a shelf folder-book row", "the preview names the row's class")
	require.NotContains(t, row.OwnerApplyReason, "mixed-shelf")
	for _, want := range []string{"Dark Victory", "Knightfall", "organized", "no file moves", "reverts from the operation"} {
		require.Contains(t, row.OwnerApplyReason, want)
	}
	rep.mu.Lock()
	logs := strings.Join(rep.logs, "\n")
	rep.mu.Unlock()
	require.Contains(t, logs, "1 owner-applicable")

	// A plain apply still never writes it.
	data, err := json.Marshal(res)
	require.NoError(t, err)
	s := string(data)
	f.ops.mu.Lock()
	f.ops.rows["op-plan"] = &database.OperationV2Row{ID: "op-plan", DefID: repairs.PlanOpID, Status: "completed", ResultData: &s}
	f.ops.mu.Unlock()
	plain := f.fbApply(t, "op-plan", "op-plain", []string{row.RowID})
	require.Zero(t, plain.Applied)
	require.True(t, f.live(t, "fb"))
	rowsBefore := fbRowCount(t, f.s)

	out := f.fbOwnerApply(t, "op-plan", "op-owner", []string{row.RowID})
	require.Equal(t, 1, out.Applied, "%+v", out.Rows)
	require.Equal(t, fbOwnerUser, fbOutcome(out, row.RowID).OwnerUserID)
	require.False(t, f.live(t, "fb"), "the folder-book is retired")
	require.Equal(t, rowsBefore+4, fbRowCount(t, f.s), "the four orphan files got new rows")
	created := map[string]string{}
	for _, title := range []string{"Dark Victory", "Knightfall"} {
		b := f.liveAtDir(t, filepath.Join("Alfred Pennyworth", title))
		require.NotNil(t, b, title)
		require.Equal(t, title, b.Title)
		require.NotNil(t, b.LibraryState)
		require.Equal(t, "organized", *b.LibraryState, "library books are organized")
		require.NotNil(t, b.AuthorID, title)
		created[title] = b.ID
		rows, err := f.s.GetBookFiles(b.ID)
		require.NoError(t, err)
		for _, r := range rows {
			_, err := os.Stat(r.FilePath)
			require.NoError(t, err, "nothing moved on disk")
		}
	}
	changes, err := f.s.GetOperationChanges("op-owner")
	require.NoError(t, err)
	notes := 0
	for _, c := range changes {
		if c.ChangeType == undo.ChangeTypeRepairOwnerApply {
			notes++
		}
	}
	require.Equal(t, len(row.OwnerWrites), notes, "one audit note per book the owner apply writes")

	_, err = audiobooks.NewRevertService(f.s).RevertOperation("op-owner")
	require.NoError(t, err)
	require.True(t, f.live(t, "fb"), "the revert brings the folder-book back")
	for title, id := range created {
		b, err := f.s.GetBookByID(id)
		require.NoError(t, err)
		require.True(t, b.IsSoftDeleted(), "%s is hidden by the revert", title)
	}
	require.Equal(t, rowsBefore+4, fbRowCount(t, f.s), "the revert deletes no row")
}

// A single-shelf row with one hold the owner may not resolve is not offered,
// and the owner's click on it is refused.
func TestFolderBooksOwner_SingleShelfRowWithAHardHoldIsNotOffered(t *testing.T) {
	f := newGlobalRootFragFixture(t)
	f.seedOwnerRow(t, ownerShelf{})
	_, err := f.s.ModifyBook(f.ids["fb"], func(b *database.Book) error {
		pid := "00000000000000AB"
		b.ITunesPersistentID = &pid
		return nil
	})
	require.NoError(t, err)
	res := f.fbPlan(t, fbFixerID, "op-plan")
	require.Len(t, res.Rows, 1)
	row := res.Rows[0]
	require.Equal(t, fbTierShelf, row.Class)
	require.NotEmpty(t, row.Skipped)
	require.False(t, row.OwnerApplicable, "owner-applicable despite a hard hold; reason %q", row.OwnerApplyReason)
	require.Empty(t, row.OwnerApplyReason)
	require.NotEmpty(t, fbHardHoldWhy(row.Holds, fbSkipITunesID), "the hard hold is listed and marked hard: %+v", row.Holds)
	require.Zero(t, res.OwnerApplicable)
	out := f.fbOwnerApply(t, "op-plan", "op-owner", []string{row.RowID})
	require.Zero(t, out.Applied)
	require.Equal(t, repairs.OutcomeOwnerRefused, fbOutcome(out, row.RowID).Outcome)
	require.True(t, f.live(t, "fb"))
}

// A duplicate title that resolves to exactly one live book is owner-resolvable
// (by a version link, owner rows only, Q2), but the apply cannot write that
// link yet, so the row is not offered; plain rows keep the skip.
func TestFolderBooksOwner_DuplicateTitleIsCarriedAndLinkedByTheOwner(t *testing.T) {
	seedTwin := func(t *testing.T, f *fragFixture, n int) {
		author, err := f.s.GetAuthorByName("Alfred Pennyworth")
		require.NoError(t, err)
		for i := 0; i < n; i++ {
			_, err := f.s.CreateBook(&database.Book{Title: "Arkham Asylum", AuthorID: &author.ID,
				FilePath: f.path(fmt.Sprintf("elsewhere/Arkham Asylum %d.m4b", i))})
			require.NoError(t, err)
		}
	}
	for _, tc := range []struct {
		name       string
		twins      int
		wantSingle bool
	}{{"one live twin", 1, true}, {"two live twins", 2, false}} {
		t.Run(tc.name, func(t *testing.T) {
			f := newGlobalRootFragFixture(t)
			f.seedOwnerRow(t, ownerShelf{base: fbITunes, libWorks: []string{"Arkham Asylum"}})
			seedTwin(t, f, tc.twins)
			row := f.fbSingleRow(t, "op-plan")
			require.Equal(t, fbSkipMixedShelf, row.Skipped)
			// One twin is the owner's to link to; two are a hard hold.
			require.Equal(t, tc.wantSingle, row.OwnerApplicable, "%s", row.SkipReason)
			var st fbState
			require.NoError(t, json.Unmarshal(row.State, &st))
			require.Len(t, st.DupTitles, 1)
			require.Equal(t, tc.wantSingle, st.DupSingle)
			require.Equal(t, tc.wantSingle, len(st.Links) == 1)
			// Replan carries the verdict, so Plan and Replan agree.
			fresh, err := newFolderBooksFixer(f.p).Replan(context.Background(), nil, row, nil)
			require.NoError(t, err)
			require.Equal(t, row.Fingerprint, fresh.Fingerprint)
			require.Equal(t, tc.wantSingle, fresh.OwnerApplicable)
		})
	}
	t.Run("plain row keeps skipped_duplicate_title", func(t *testing.T) {
		f := newGlobalRootFragFixture(t)
		f.seedOwnerRow(t, ownerShelf{base: fbITunes})
		author, err := f.s.GetAuthorByName("Alfred Pennyworth")
		require.NoError(t, err)
		_, err = f.s.CreateBook(&database.Book{Title: "Dark Victory", AuthorID: &author.ID, FilePath: f.path("elsewhere/Dark Victory.m4b")})
		require.NoError(t, err)
		row := f.fbSingleRow(t, "op-plan")
		require.Equal(t, fbSkipDupTitle, row.Skipped, "%s", row.SkipReason)
		// Single shelf and the only hold is one the owner may resolve (one live
		// twin to link to): offered to the owner (owner decision 2026-11-04).
		require.True(t, row.OwnerApplicable, "%s", row.SkipReason)
		require.Empty(t, row.Flags)
	})
	t.Run("plain row with two live twins is a hard hold", func(t *testing.T) {
		f := newGlobalRootFragFixture(t)
		f.seedOwnerRow(t, ownerShelf{base: fbITunes})
		author, err := f.s.GetAuthorByName("Alfred Pennyworth")
		require.NoError(t, err)
		for i := 0; i < 2; i++ {
			_, err = f.s.CreateBook(&database.Book{Title: "Dark Victory", AuthorID: &author.ID,
				FilePath: f.path(fmt.Sprintf("elsewhere/Dark Victory %d.m4b", i))})
			require.NoError(t, err)
		}
		row := f.fbSingleRow(t, "op-plan")
		require.Equal(t, fbSkipDupTitle, row.Skipped, "%s", row.SkipReason)
		require.Empty(t, row.Flags)
	})
}
