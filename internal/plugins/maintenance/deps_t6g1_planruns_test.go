// file: internal/plugins/maintenance/deps_t6g1_planruns_test.go
// version: 0.1.0
// guid: 3e8b1c54-9a27-4d60-b5f3-7c04e1a92d68
// last-edited: 2026-11-28

package maintenance

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"iter"
	"sort"
	"testing"
	"time"

	"github.com/falkcorp/mediacore/tasks"
	"github.com/stretchr/testify/require"
)

// The handle the books module holds is core's own-kinds view of the run store
// (module.TaskRows), which lists and has no Get. Both it and the wide store
// satisfy the lister, and the reader the builder hands the apply is an
// AuthorityRunReader made from the lister alone.
var (
	_ AuthorityRunLister = tasks.RunStore(nil)
	_ AuthorityRunLister = g1NoRuns{}
	_ AuthorityRunLister = CreditsRunStore(nil)
	_ AuthorityRunReader = NewAuthorityRunReader(g1NoRuns{})
)

// pagedRuns is a run store that lists like the real one: ascending by id, the
// query's kinds honoured, After a keyset cursor and Limit a page size. It counts
// the listings it served and keeps the queries it was asked.
type pagedRuns struct {
	runs    []tasks.RunRecord
	queries []tasks.RunQuery
	err     error
}

func (p *pagedRuns) List(_ context.Context, q tasks.RunQuery) iter.Seq2[tasks.RunRecord, error] {
	p.queries = append(p.queries, q)
	return func(yield func(tasks.RunRecord, error) bool) {
		if p.err != nil {
			yield(tasks.RunRecord{}, p.err)
			return
		}
		n := 0
		for _, r := range p.runs {
			if len(q.Kinds) > 0 && !hasKind(q.Kinds, r.Kind) {
				continue
			}
			if q.After != "" && r.ID <= q.After {
				continue
			}
			if q.Limit > 0 && n == q.Limit {
				return
			}
			n++
			if !yield(r, nil) {
				return
			}
		}
	}
}

func hasKind(kinds []tasks.Kind, k tasks.Kind) bool {
	for _, c := range kinds {
		if c == k {
			return true
		}
	}
	return false
}

func pagedAuthorityRuns(n int) *pagedRuns {
	p := &pagedRuns{}
	for i := range n {
		p.runs = append(p.runs, tasks.RunRecord{RunView: tasks.RunView{
			ID: tasks.RunID(fmt.Sprintf("01JBATCAVE%016d", i)), Kind: authorityBuildKind, Mode: tasks.ModePreview, State: tasks.StateSucceeded,
		}})
	}
	// Runs of another kind sit between them, as the books module's other kinds'
	// runs do: the reader must not be handed them.
	p.runs = append(p.runs, tasks.RunRecord{RunView: tasks.RunView{ID: "01JBATCAVE9999999999999999", Kind: t6g3CreditsBackfillKind}})
	sort.Slice(p.runs, func(i, j int) bool { return p.runs[i].ID < p.runs[j].ID })
	return p
}

// The reader finds one run by listing the authority-build kind, and pages to the
// end: the oldest runs sort first, so a reader that stopped at the first page
// would lose the plan of a run that is not among the newest.
func TestAuthorityRunReader_FindsARunByListingToTheEnd(t *testing.T) {
	runs := pagedAuthorityRuns(2*cbRunListPage + 500)
	r := NewAuthorityRunReader(runs)

	last := runs.runs[len(runs.runs)-2] // the last authority-build run (the other kind sorts after it)
	require.Equal(t, authorityBuildKind, last.Kind)
	got, err := r.Get(context.Background(), last.ID)
	require.NoError(t, err)
	require.Equal(t, last.ID, got.ID)
	require.GreaterOrEqual(t, len(runs.queries), 3, "the listing paged: the run is on the third page")
	for _, q := range runs.queries {
		require.Equal(t, []tasks.Kind{authorityBuildKind}, q.Kinds, "only authority-build runs are listed")
		require.Equal(t, cbRunListPage, q.Limit)
	}

	first, err := r.Get(context.Background(), runs.runs[0].ID)
	require.NoError(t, err)
	require.Equal(t, runs.runs[0].ID, first.ID)
}

func TestAuthorityRunReader_AnAbsentRunIsNotFoundAndAListingErrorIsNotHidden(t *testing.T) {
	runs := pagedAuthorityRuns(10)
	r := NewAuthorityRunReader(runs)

	_, err := r.Get(context.Background(), "01JBATCAVEMISSING00000000A")
	require.ErrorIs(t, err, tasks.ErrRunNotFound)
	// A run of another kind is a run the plan lookup cannot be handed.
	_, err = r.Get(context.Background(), "01JBATCAVE9999999999999999")
	require.ErrorIs(t, err, tasks.ErrRunNotFound)
	_, err = r.Get(context.Background(), "")
	require.ErrorIs(t, err, tasks.ErrRunNotFound)

	boom := errors.New("the run store is down")
	runs.err = boom
	_, err = r.Get(context.Background(), runs.runs[0].ID)
	require.ErrorIs(t, err, boom)
	require.NotErrorIs(t, err, tasks.ErrRunNotFound, "a failed listing is not 'no such run'")
}

// The builder wires the reader from the run store it is handed, and an apply
// over a list-only handle finds the plan its Preview stored: the same lookup the
// wide store served in TestT6G1Kinds_AuthorityBuildAppliesANativePreviewsOwnPlan,
// through a handle that has no Get.
func TestBooksT6G1Deps_AuthorityApplyFindsItsPlanThroughAListOnlyRunStore(t *testing.T) {
	ps := authorityTestStore(t)
	probe := &t6g1LiveProbe{}
	deps := probe.deps(ps)
	runs := newNativeRuns(t)

	built, err := newT6G1Deps(ps, listOnlyRuns{runs}, g1TestInputs(ps, nil), g1TestKey)
	require.NoError(t, err)
	require.NotNil(t, built.PlanRuns)
	deps.PlanRuns = built.PlanRuns
	def := t6g1ByKind(t, (&Plugin{}).t6g1Kinds(deps, nil), authorityBuildKind)
	gate := &t6g1GateWriter{name: "authority"}

	res, _, err := runT6G1Full(t, def, tasks.ModePreview, gate, fmt.Sprintf(`{"library_export_path":%q}`, writeExport(t, false)), nil)
	require.NoError(t, err)
	var plan authorityPlanResult
	require.NoError(t, json.Unmarshal(res, &plan))

	created := time.Date(2026, time.March, 14, 21, 0, 0, 0, time.UTC)
	preview := tasks.RunID("01JBATCAVEPREVIEW00000000B")
	putNativeRun(t, runs, nativeRunAt(preview, authorityBuildKind, tasks.ModePreview, tasks.StateRunning, created))
	finishNativeRun(t, runs, preview, tasks.StateSucceeded, res)

	res, _, err = runT6G1Full(t, def, tasks.ModeLive, gate, fmt.Sprintf(`{"plan_op_id":%q}`, preview), nil)
	require.NoError(t, err)
	var applied authorityPlanResult
	require.NoError(t, json.Unmarshal(res, &applied))
	require.NotNil(t, applied.Apply)
	require.Equal(t, plan.Writes, applied.Apply.Written)

	// A plan that is not a succeeded Preview of this kind is still refused, and
	// an unknown id is named not found.
	live := tasks.RunID("01JBATCAVELIVE0000000000BB")
	putNativeRun(t, runs, nativeRunAt(live, authorityBuildKind, tasks.ModeLive, tasks.StateRunning, created))
	finishNativeRun(t, runs, live, tasks.StateSucceeded, res)
	_, _, err = runT6G1Full(t, def, tasks.ModeLive, gate, fmt.Sprintf(`{"plan_op_id":%q}`, live), nil)
	require.ErrorContains(t, err, "not a Preview")
	_, _, err = runT6G1Full(t, def, tasks.ModeLive, gate, `{"plan_op_id":"01JBATCAVEMISSING00000000B"}`, nil)
	require.ErrorContains(t, err, "not found")
}

// Without a run store the builder leaves the plan lookup unset, so an apply
// refuses by name (as HEAD's did with an operation store that cannot read one).
func TestBooksT6G1Deps_NoRunStoreLeavesThePlanLookupUnset(t *testing.T) {
	ps := authorityTestStore(t)
	for name, runs := range map[string]AuthorityRunLister{
		"nil":       nil,
		"typed nil": (*pagedRuns)(nil),
	} {
		built, err := newT6G1Deps(ps, runs, g1TestInputs(ps, nil), g1TestKey)
		require.NoError(t, err, name)
		require.Nil(t, built.PlanRuns, name)
	}
}

// listOnlyRuns hides everything of a run store but its List, as the module's own
// handle does.
type listOnlyRuns struct{ r tasks.RunStore }

func (l listOnlyRuns) List(ctx context.Context, q tasks.RunQuery) iter.Seq2[tasks.RunRecord, error] {
	return l.r.List(ctx, q)
}
