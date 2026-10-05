// file: internal/plugins/maintenance/tag_franchise_fixer_test.go
// version: 1.1.0
// guid: 9f3a6c28-1e74-4b5d-8c02-7a6e4d9b1f35
// last-edited: 2026-10-05

package maintenance

import (
	"context"
	"encoding/json"
	"sort"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/audiobooks"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/repairs"
	"github.com/falkcorp/audiobook-organizer/internal/undo"
)

const tfTestOpID = "op-tag-franchise-apply"

type tfLib struct {
	t     *testing.T
	store *database.PebbleStore
	p     *Plugin
	fixer *tagFranchiseFixer
	ids   map[string]string
}

func newTFLib(t *testing.T) *tfLib {
	t.Helper()
	st, err := database.NewPebbleStore(t.TempDir())
	require.NoError(t, err)
	t.Cleanup(func() { _ = st.Close() })
	p := &Plugin{deps: fakeDeps{store: st}, standDownWait: noWait}
	return &tfLib{t: t, store: st, p: p, fixer: newTagFranchiseFixer(p), ids: map[string]string{}}
}

type tfBook struct {
	title, path, narrator, author string
	size                          int64
}

func (l *tfLib) book(key string, b tfBook) string {
	l.t.Helper()
	nb := &database.Book{Title: b.title, Format: "m4b", FilePath: b.path}
	if b.narrator != "" {
		nb.Narrator = &b.narrator
	}
	if b.author != "" {
		a, err := l.store.CreateAuthor(b.author)
		require.NoError(l.t, err)
		nb.AuthorID = &a.ID
	}
	created, err := l.store.CreateBook(nb)
	require.NoError(l.t, err)
	require.NoError(l.t, l.store.CreateBookFile(&database.BookFile{BookID: created.ID, FilePath: b.path + "/01.m4b", Format: "m4b", FileSize: b.size}))
	l.ids[key] = created.ID
	return created.ID
}

func (l *tfLib) plan() (*repairs.PlanResult, map[string]repairs.Row) {
	l.t.Helper()
	series, err := l.store.GetAllSeries()
	require.NoError(l.t, err)
	res, err := repairs.RunPlan(context.Background(), l.fixer, nil, l.p.repairsPlanDeps(l.store, series), &fakeReporter{})
	require.NoError(l.t, err)
	raw, err := json.Marshal(res)
	require.NoError(l.t, err)
	var stored repairs.PlanResult
	require.NoError(l.t, json.Unmarshal(raw, &stored))
	byKey := map[string]repairs.Row{}
	for key, id := range l.ids {
		for _, r := range stored.Rows {
			if r.RowID == id {
				byKey[key] = r
			}
		}
	}
	return &stored, byKey
}

func (l *tfLib) apply(plan *repairs.PlanResult, rowIDs []string) *repairs.ApplyResult {
	l.t.Helper()
	series, err := l.store.GetAllSeries()
	require.NoError(l.t, err)
	w := repairs.NewWriter(l.store, l.store, l.fixer.ID(), "bulk_update", "repairs-").
		WithJournal(l.store, l.store, tfTestOpID).WithTags(l.p.deps.BookTagWriter())
	res, err := repairs.RunApply(context.Background(), l.fixer, plan, "op-tag-franchise-plan", rowIDs, false,
		repairs.ApplyDeps{Guard: l.store, Tags: l.p.repairsGuardTags(), Series: repairs.SeriesNamesFrom(series), Writer: w, OpID: tfTestOpID}, &fakeReporter{})
	require.NoError(l.t, err)
	return res
}

func (l *tfLib) tags(key string) map[string]string {
	l.t.Helper()
	ts, err := l.store.GetBookTagsDetailed(l.ids[key])
	require.NoError(l.t, err)
	out := map[string]string{}
	for _, t := range ts {
		out[t.Tag] = t.Source
	}
	return out
}

func TestTagFranchise_PlanApplyRevert(t *testing.T) {
	l := newTFLib(t)
	l.book("dw", tfBook{title: "Placebo Effect", path: "/lib/Doctor Who/Placebo Effect", size: 5 << 20})
	// An iTunes-imported Big Finish book: junk path and title, the album in
	// the narrator and the studio as author. Tagging is not a file write, so
	// the iTunes guard does not skip it.
	l.book("itunes", tfBook{title: "The Shell Game", path: "/mnt/bigdata/books/itunes/iTunes Media/Audiobooks/Big Finish/Gift",
		narrator: "Stargate SG-1 - Series 2", author: "Big Finish Productions"})
	// A neutral book whose only file (01.m4b, 5 MiB) is a twin of the
	// Doctor Who book's file: weak.
	l.book("twin", tfBook{title: "Unknown", path: "/lib/Unknown Author/Unknown", size: 5 << 20})
	l.book("missy", tfBook{title: "Get Ur Freak On", path: "/lib/Hip Hop/Get Ur Freak On", author: "Missy Elliott"})
	l.book("conflict", tfBook{title: "End Game", path: "/lib/Doctor Who/End Game"})
	require.NoError(t, l.store.AddBookTagWithSource(l.ids["conflict"], "franchise:torchwood", "user"))
	l.book("neutral", tfBook{title: "Doctor Sleep", path: "/lib/Stephen King/Doctor Sleep"})
	l.book("tchaikovsky", tfBook{title: "War Master's Gate", path: "/lib/Adrian Tchaikovsky/War Master's Gate"})

	plan, rows := l.plan()
	require.NotContains(t, rows, "neutral")
	require.NotContains(t, rows, "tchaikovsky")

	dw := rows["dw"]
	require.True(t, dw.Applicable(), dw.SkipReason)
	require.Equal(t, tfClassStrong, dw.Class)
	require.Equal(t, repairs.RiskLow, dw.Risk)
	require.Equal(t, "franchise:doctor-who", dw.Proposed["tags"])
	require.NotEmpty(t, dw.Evidence)

	it := rows["itunes"]
	require.True(t, it.Applicable(), "iTunes book skipped: %s %s", it.Skipped, it.SkipReason)
	require.Equal(t, "franchise:big-finish, range:stargate", it.Proposed["tags"])

	tw := rows["twin"]
	require.Equal(t, tfClassWeak, tw.Class, "%+v", tw.Evidence)
	require.Equal(t, repairs.RiskReview, tw.Risk)

	mi := rows["missy"]
	require.Equal(t, tfClassWeak, mi.Class)
	require.Equal(t, repairs.RiskReview, mi.Risk)

	require.Equal(t, tfSkipConflicting, rows["conflict"].Skipped)

	res := l.apply(plan, []string{dw.RowID, it.RowID, tw.RowID})
	byRow := map[string]string{}
	for _, r := range res.Rows {
		byRow[r.RowID] = r.Outcome
	}
	require.Equal(t, repairs.OutcomeApplied, byRow[dw.RowID], "%+v", res.Rows)
	require.Equal(t, repairs.OutcomeApplied, byRow[it.RowID], "%+v", res.Rows)
	require.Equal(t, repairs.OutcomeApplied, byRow[tw.RowID], "a weak row applies only when chosen by id: %+v", res.Rows)
	src := "franchise-matcher:" + tfTestOpID
	require.Equal(t, map[string]string{"franchise:doctor-who": src}, l.tags("dw"))
	require.Equal(t, map[string]string{"franchise:big-finish": src, "range:stargate": src}, l.tags("itunes"))

	// Tags do not touch files: the book and its file rows are unchanged.
	b, err := l.store.GetBookByID(l.ids["itunes"])
	require.NoError(t, err)
	require.Equal(t, "The Shell Game", b.Title)

	// The guard now holds the tagged book even after a rename to a neutral
	// title and path.
	_, err = l.store.ModifyBook(l.ids["dw"], func(b *database.Book) error {
		b.Title, b.FilePath = "Neutral", "/lib/neutral/x"
		return nil
	})
	require.NoError(t, err)
	k, _, err := repairs.GuardBooks(l.store, l.p.repairsGuardTags(), nil, repairs.NewPathResolver(), []string{l.ids["dw"]})
	require.NoError(t, err)
	require.Equal(t, repairs.SkipOwnerManual, k)

	changes, err := l.store.GetOperationChanges(tfTestOpID)
	require.NoError(t, err)
	var types []string
	for _, c := range changes {
		types = append(types, c.ChangeType+":"+c.FieldName)
	}
	sort.Strings(types)
	require.Equal(t, []string{
		undo.ChangeTypeBookTagAdd + ":franchise:big-finish",
		undo.ChangeTypeBookTagAdd + ":franchise:doctor-who",
		undo.ChangeTypeBookTagAdd + ":franchise:doctor-who",
		undo.ChangeTypeBookTagAdd + ":range:stargate",
	}, types)

	// Revert: every tag the apply added goes; the person's tag stays.
	_, err = audiobooks.NewRevertService(l.store).RevertOperation(tfTestOpID)
	require.NoError(t, err)
	require.Empty(t, l.tags("dw"))
	require.Empty(t, l.tags("itunes"))
	require.Empty(t, l.tags("twin"))
	require.Equal(t, map[string]string{"franchise:torchwood": "user"}, l.tags("conflict"))

}

// The fixer is registered in the production registry (not only in tests)
// and the plan/apply guard deps carry the tag reader.
func TestTagFranchise_ProdWiring(t *testing.T) {
	l := newTFLib(t)
	_, ok := l.p.Repairs().Get(tagFranchiseFixerID)
	require.True(t, ok, "maintenance.tag-franchise not registered")
	deps := l.p.repairsPlanDeps(l.store, nil)
	require.NotNil(t, deps.Tags, "the plan guard must read franchise tags")
	require.NotNil(t, l.p.repairsGuardTags())
	require.True(t, repairs.AllowsBookTagsOnly(l.fixer))
}

// Replan after an apply: the row is already tagged, not applicable.
func TestTagFranchise_ReplanAfterApply(t *testing.T) {
	l := newTFLib(t)
	l.book("dw", tfBook{title: "Placebo Effect", path: "/lib/Doctor Who/Placebo Effect"})
	plan, rows := l.plan()
	l.apply(plan, []string{rows["dw"].RowID})
	_, rows = l.plan()
	require.Equal(t, tfSkipTagged, rows["dw"].Skipped)
}
