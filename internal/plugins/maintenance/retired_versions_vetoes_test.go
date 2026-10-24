// file: internal/plugins/maintenance/retired_versions_vetoes_test.go
// version: 1.0.0
// guid: 91e6c0d4-3b2a-4f75-8d18-6a4f2c97e0b3
// last-edited: 2026-10-23

package maintenance

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/decisions"
	"github.com/falkcorp/audiobook-organizer/internal/repairs"
)

// retireInto writes the retired book_versions row a retire of shell onto
// holder leaves (the index idx:bv:from:<shell> the resolver follows). It moves
// no book_file row: the resolver reads only the version.
func (rf *retiredFixture) retireInto(shell, holder string) {
	rf.t.Helper()
	_, err := rf.st.CreateBookVersion(&database.BookVersion{
		BookID: rf.ids[holder], Status: database.BookVersionStatusRetired, Format: "mp3", Source: "retired_book",
		RetiredFromBookID: rf.ids[shell], RetireOpID: "op-retire", RetiredAt: retiredPtr(rf.now),
	})
	require.NoError(rf.t, err)
}

// purge deletes a fixture book's row, as the purge that follows a retire does.
func (rf *retiredFixture) purge(name string) {
	rf.t.Helper()
	require.NoError(rf.t, rf.st.DeleteBook(rf.ids[name]))
}

// trash moves a live fixture book into the trash.
func (rf *retiredFixture) trash(name string) {
	rf.t.Helper()
	b, err := rf.st.GetBookByID(rf.ids[name])
	require.NoError(rf.t, err)
	b.MarkedForDeletion = retiredPtr(true)
	b.MarkedForDeletionAt = retiredPtr(rf.now.Add(-30 * 24 * time.Hour))
	_, err = rf.st.UpdateBook(b.ID, b)
	require.NoError(rf.t, err)
}

// rejectShell records the owner's reject of the retired-versions proposal for
// a fixture shell.
func (rf *retiredFixture) rejectShell(name string) {
	rf.t.Helper()
	kv, ok := database.AsCapability[decisions.KV](rf.st)
	require.True(rf.t, ok)
	_, err := decisions.New(kv).Record(decisions.Input{
		OpKind: decisions.OpKindRetiredVersions, EntityType: retiredEntityBook, EntityIDs: []string{rf.ids[name]},
		Decision: decisions.DecisionReject, Source: decisions.SourceHuman, ActorUserID: "owner-user",
		ReasonCode: decisions.ReasonOther,
	})
	require.NoError(rf.t, err)
}

func (rf *retiredFixture) vetoReader() OwnerVetoReader {
	rf.t.Helper()
	r := NewOwnerVetoReader(rf.st)
	require.NotNil(rf.t, r)
	return r
}

func pairVetoed(t *testing.T, r OwnerVetoReader, a, b, fam string) bool {
	t.Helper()
	got, err := r.PairVetoed(retiredEntityBook, a, b, fam)
	require.NoError(t, err)
	return got
}

// The resolver follows idx:bv:from to the holder (also once the shell's row is
// gone) and then the merge survivor, so a veto on a retired shell resolves and
// blocks. The unresolved lookup, which is what the plan used before, does not.
func TestOwnerVetoReader_ResolverFollowsTheIndexAndComposesWithTheMergeSurvivor(t *testing.T) {
	rf := newRetiredFixture(t)
	rf.live("holder", "")
	rf.live("survivor", "")
	rf.live("other", "")
	rf.trashed("shell", "", 30)
	rf.retireInto("shell", "holder")
	rf.reject("shell", "other", decisions.FamilySameWork)

	kv, ok := database.AsCapability[decisions.KV](rf.st)
	require.True(t, ok)
	exact := decisions.New(kv)
	exactVetoed := func(a, b string) bool {
		v, err := exact.PairVetoed(retiredEntityBook, rf.ids[a], rf.ids[b], decisions.FamilySameWork)
		require.NoError(t, err)
		return v
	}
	require.True(t, exactVetoed("shell", "other"), "the reject names the shell")
	require.False(t, exactVetoed("holder", "other"), "the unresolved lookup knows nothing of the holder")

	// 1. The shell is trashed: the index leads to the holder.
	r := rf.vetoReader()
	require.True(t, pairVetoed(t, r, rf.ids["holder"], rf.ids["other"], decisions.FamilySameWork))
	require.True(t, pairVetoed(t, r, rf.ids["shell"], rf.ids["other"], decisions.FamilySameWork), "the old id still answers")
	require.False(t, pairVetoed(t, r, rf.ids["holder"], rf.ids["other"], decisions.FamilySameCopy), "family scoped")
	require.False(t, pairVetoed(t, r, rf.ids["survivor"], rf.ids["other"], decisions.FamilySameWork))

	// 2. The shell is purged: only the index is left.
	rf.purge("shell")
	require.True(t, pairVetoed(t, rf.vetoReader(), rf.ids["holder"], rf.ids["other"], decisions.FamilySameWork))

	// 3. The holder is itself trashed and merged into a survivor: the chain goes
	// on through the merge resolver.
	rf.trash("holder")
	rf.mergedInto("holder", "survivor")
	r = rf.vetoReader()
	require.True(t, pairVetoed(t, r, rf.ids["survivor"], rf.ids["other"], decisions.FamilySameWork))
	require.True(t, pairVetoed(t, r, rf.ids["holder"], rf.ids["other"], decisions.FamilySameWork))
	require.False(t, exactVetoed("survivor", "other"))
}

// A book restored from the trash is live: a stale version row naming it as a
// shell does not redirect it.
func TestOwnerVetoReader_LiveBookIsNotRedirected(t *testing.T) {
	rf := newRetiredFixture(t)
	rf.live("holder", "")
	rf.live("restored", "")
	rf.live("other", "")
	rf.retireInto("restored", "holder")
	rf.reject("restored", "other", decisions.FamilySameWork)
	r := rf.vetoReader()
	require.True(t, pairVetoed(t, r, rf.ids["restored"], rf.ids["other"], decisions.FamilySameWork))
	require.False(t, pairVetoed(t, r, rf.ids["holder"], rf.ids["other"], decisions.FamilySameWork))
}

// The plan drops a row whose shell the owner rejected, whatever its target, and
// a re-plan notices a reject made after the plan.
func TestRetiredVersions_PlanHonoursAShellReject(t *testing.T) {
	rf := newRetiredFixture(t)
	rf.live("primary", "g", rf.file("g/primary.mp3"))
	rf.trashed("rejected", "g", 30, rf.file("g/rejected.mp3"))
	rf.trashed("free", "g", 30, rf.file("g/free.mp3"))
	rf.trashed("late", "g", 30, rf.file("g/late.mp3"))
	rf.rejectShell("rejected")

	rows := rf.plan()
	r := rows[rf.ids["rejected"]]
	require.Equal(t, retCatVetoed, r.Class, r.Reason)
	require.Equal(t, retVetoShellRejected, r.Current["owner_veto_family"])
	require.False(t, r.Applicable())
	require.Contains(t, r.Reason, "rejected retiring this book")
	require.Equal(t, retCatWouldRetire, rows[rf.ids["free"]].Class)

	// A reject lands after the plan: the re-plan of that row differs and is held.
	planned := rows[rf.ids["late"]]
	require.Equal(t, retCatWouldRetire, planned.Class)
	rf.rejectShell("late")
	again, err := rf.f.Replan(context.Background(), nil, planned, &fakeReporter{})
	require.NoError(t, err)
	require.Equal(t, retCatVetoed, again.Class)
	require.NotEqual(t, planned.Fingerprint, again.Fingerprint, "changed_since_plan")
}

// A pair veto that names a book which was retired into the target and purged
// holds the row: the owner said that book and this shell are not the same, and
// that book's files are now the target's.
func TestRetiredVersions_PlanPairVetoThroughARetiredBook(t *testing.T) {
	rf := newRetiredFixture(t)
	rf.live("primary", "g", rf.file("g/primary.mp3"))
	rf.trashed("old", "", 30)
	rf.trashed("shell", "g", 30, rf.file("g/shell.mp3"))
	rf.trashed("copy-shell", "g", 30, rf.file("g/copy.mp3"))
	rf.trashed("free", "g", 30, rf.file("g/free.mp3"))
	rf.retireInto("old", "primary")
	rf.reject("old", "shell", decisions.FamilySameWork)
	rf.reject("old", "copy-shell", decisions.FamilySameCopy)
	rf.purge("old")

	rows := rf.plan()
	for name, fam := range map[string]string{"shell": decisions.FamilySameWork, "copy-shell": decisions.FamilySameCopy} {
		r := rows[rf.ids[name]]
		require.Equal(t, retCatVetoed, r.Class, "%s: %s", name, r.Reason)
		require.Equal(t, fam, r.Current["owner_veto_family"], name)
		require.False(t, r.Applicable(), name)
	}
	require.Equal(t, retCatWouldRetire, rows[rf.ids["free"]].Class)
}

// A shell that was merged into its target resolves to it, so the snapshot sees
// one book, not a pair. A veto on that very pair must still hold the row (the
// exact lookup stands beside the resolved one).
func TestRetiredVersions_PlanDirectPairVetoSurvivesAMergedIntoTarget(t *testing.T) {
	rf := newRetiredFixture(t)
	rf.live("primary", "g", rf.file("g/primary.mp3"))
	rf.trashed("merged", "g", 30, rf.file("g/merged.mp3"))
	rf.mergedInto("merged", "primary")
	rf.reject("primary", "merged", decisions.FamilySameWork)

	r := rf.plan()[rf.ids["merged"]]
	require.Equal(t, retMethodMergedInto, r.Current["target_method"], "the row's target is its merged-into book")
	require.Equal(t, retCatVetoed, r.Class, r.Reason)
	require.Equal(t, decisions.FamilySameWork, r.Current["owner_veto_family"])
}

// loadErrVetoes is a reader whose snapshot cannot be loaded.
type loadErrVetoes struct{ errVetoes }

func (loadErrVetoes) Load(context.Context) error { return errors.New("decisions store unreadable") }

// A run that cannot load the rejects is refused whole, never a plan of error
// rows (and never a plan with no vetoes).
func TestRetiredVersions_UnloadableRejectsRefuseThePlan(t *testing.T) {
	rf := newRetiredFixture(t)
	rf.live("primary", "g", rf.file("g/primary.mp3"))
	rf.trashed("shell", "g", 30, rf.file("g/shell.mp3"))
	rf.withDeps(fakeDeps{store: rf.st, vetoes: loadErrVetoes{}})
	_, err := rf.f.Plan(context.Background(), nil, &fakeReporter{})
	require.ErrorContains(t, err, "decisions store unreadable")
	_, err = rf.f.Replan(context.Background(), nil, repairs.Row{RowID: rf.ids["shell"]}, &fakeReporter{})
	require.ErrorContains(t, err, "decisions store unreadable")
}

// The applied and undone records the Apply and the revert write.
func TestRecordRetiredVersionDecisions(t *testing.T) {
	rf := newRetiredFixture(t)
	rf.live("primary", "g", rf.file("g/primary.mp3"))
	rf.trashed("shell", "g", 30, rf.file("g/shell.mp3"))
	ev := decisions.RetiredVersionsEvent{
		ShellID: rf.ids["shell"], TargetID: rf.ids["primary"], VersionID: "ver-1", OpID: "op-apply",
		PlanOpID: "plan-op", PlanRowID: rf.ids["shell"],
	}
	applied, err := recordRetiredVersionApplied(rf.st, ev)
	require.NoError(t, err)
	require.Equal(t, decisions.DecisionApplied, applied.Decision)
	ev.OpID = "op-undo"
	undone, err := recordRetiredVersionUndone(rf.st, ev)
	require.NoError(t, err)
	require.Equal(t, decisions.DecisionUndone, undone.Decision)
	require.Equal(t, applied.SubjectKey, undone.SubjectKey)

	// Neither is a veto: the shell is planned like any other afterwards.
	require.Equal(t, retCatWouldRetire, rf.plan()[rf.ids["shell"]].Class)

	kv, ok := database.AsCapability[decisions.KV](rf.st)
	require.True(t, ok)
	hist, err := decisions.New(kv).History(decisions.OpKindRetiredVersions, applied.SubjectKey)
	require.NoError(t, err)
	require.Len(t, hist, 2)

	_, err = recordRetiredVersionApplied(struct{}{}, ev)
	require.ErrorContains(t, err, "cannot serve the decisions log")
}
