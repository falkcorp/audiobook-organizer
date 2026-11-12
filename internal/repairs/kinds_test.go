// file: internal/repairs/kinds_test.go
// version: 1.0.0
// guid: 7d2e9a40-5c1b-4f86-b3a7-0e4d8c6a1f52
// last-edited: 2026-11-12

package repairs

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/falkcorp/mediacore/entity"
	"github.com/falkcorp/mediacore/rbac"
	"github.com/falkcorp/mediacore/tasks"
	"github.com/falkcorp/mediacore/tasks/taskstest"

	"github.com/falkcorp/audiobook-organizer/internal/auth"
	database "github.com/falkcorp/audiobook-organizer/internal/books/store"
	"github.com/falkcorp/audiobook-organizer/internal/operations/opswriter"
	"github.com/falkcorp/audiobook-organizer/internal/operations/registry"
)

// The real executor lives in core/internal/executor, which the root module may
// not import, so these tests drive the definitions with a small driver that
// does what the executor's fixer runner does around the callbacks (fixerApply:
// Load, Evaluate, fingerprint compare, Apply, MarkRowApplied, the marks that
// skip an applied row, ExclusiveRefs from Row.Subjects). The driver is the
// executor's code reduced to what these callbacks can observe.

// ---- fakes ----

// fakePlanRows is the kernel's plan row reader over rows a test planned.
type fakePlanRows struct {
	mu      sync.Mutex
	rows    map[string]tasks.Row
	bytes   map[string]json.RawMessage // overrides rows: HEAD's stored bytes
	readErr error
}

func (p *fakePlanRows) PlanOf(context.Context, tasks.RunID) (string, error) { return "plan-1", nil }

func (p *fakePlanRows) PlannedRow(_ context.Context, _ tasks.RunID, id string) (json.RawMessage, bool, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.readErr != nil {
		return nil, false, p.readErr
	}
	if b, ok := p.bytes[id]; ok {
		return b, true, nil
	}
	r, ok := p.rows[id]
	return r.State, ok, nil
}

// fakeTaskWriter is the executor's tasks.Writer: it records what is written,
// refuses in the way a fenced run does, and records nothing in Preview.
type fakeTaskWriter struct {
	mu       sync.Mutex
	preview  bool
	fenced   error
	writes   []tasks.Write
	marked   map[string]bool
	markList []string
}

func (w *fakeTaskWriter) Do(ctx context.Context, wr tasks.Write, fn func(context.Context) ([]tasks.Change, error)) error {
	w.mu.Lock()
	w.writes = append(w.writes, wr)
	fenced, preview := w.fenced, w.preview
	w.mu.Unlock()
	if fenced != nil {
		return fenced
	}
	if preview {
		return tasks.ErrPreview
	}
	_, err := fn(ctx)
	return err
}

func (w *fakeTaskWriter) MarkRowApplied(_ context.Context, id string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.marked == nil {
		w.marked = map[string]bool{}
	}
	w.marked[id] = true
	w.markList = append(w.markList, id)
}

func (w *fakeTaskWriter) RowApplied(_ context.Context, id string) (bool, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.marked[id], nil
}

// scopes resolves the two atoms the tests use.
func testScopes(atom auth.Permission) ([]rbac.Permission, bool) {
	switch atom {
	case auth.PermLibraryEditMetadata:
		return []rbac.Permission{"books:edit"}, true
	case auth.PermUsersManage:
		return []rbac.Permission{"core:users.manage", "core:roles.view"}, true
	}
	return nil, false
}

type kitStoresKey struct{}

// kit is one fixer's definition callbacks over a test store.
type kit struct {
	t    *testing.T
	s    *memStore
	k    *repairKind
	rows *fakePlanRows
	w    *fakeTaskWriter
	// bound counts BindStores calls; the stores are the string "shell".
	bound atomic.Int64
}

func newKit(t *testing.T, s *memStore, f Fixer) *kit {
	t.Helper()
	kt := &kit{t: t, s: s, rows: &fakePlanRows{rows: map[string]tasks.Row{}, bytes: map[string]json.RawMessage{}}, w: &fakeTaskWriter{}}
	kt.k = newKind(f, KindDeps{
		Guard:  s,
		Series: seriesNamer,
		NewWriter: func(_ context.Context, _ tasks.RunID, f Fixer) (*Writer, error) {
			return NewWriter(s, s, f.ID(), "bulk_update", "rp-"), nil
		},
		PlanRows: kt.rows,
		BindStores: func(ctx context.Context) context.Context {
			kt.bound.Add(1)
			return WithStores(ctx, "shell")
		},
		Scopes:      testScopes,
		Concurrency: 4,
	})
	return kt
}

func (kt *kit) kind() tasks.Kind { return tasks.Kind(KindPrefix + kt.k.f.ID()) }

// plan runs the definition's plan: Candidates, then Evaluate over every
// candidate on a pool, as the executor does. Rows come back sorted by id and
// are stored for the apply's reads.
func (kt *kit) plan(ctx context.Context) []tasks.Row {
	kt.t.Helper()
	rc := taskstest.NewFakeRun(ctx, kt.kind()).RC()
	src, err := kt.k.candidates(rc, nil)
	require.NoError(kt.t, err)
	total, known := src.Total.Value()
	require.True(kt.t, known)
	require.EqualValues(kt.t, len(src.Items), total)
	var (
		mu  sync.Mutex
		out []tasks.Row
	)
	_, err = tasks.RunItems(rc, tasks.Fixed(4, "test"), src.Items, func(ic *tasks.ItemRC, c candidate) error {
		r, err := kt.k.evaluate(ic, nil, c)
		if err != nil {
			return err
		}
		mu.Lock()
		out = append(out, r)
		mu.Unlock()
		return nil
	})
	require.NoError(kt.t, err)
	slices.SortFunc(out, func(a, b tasks.Row) int { return strings.Compare(a.ID, b.ID) })
	kt.rows.mu.Lock()
	for _, r := range out {
		kt.rows.rows[r.ID] = r
	}
	kt.rows.mu.Unlock()
	return out
}

// applyResult is what one apply run did.
type applyResult struct {
	Applied []string
	Skipped map[string]string // row -> reason the executor skipped it
	Failed  map[string]error
}

// apply is the executor's fixerApply over the named rows.
func (kt *kit) apply(ctx context.Context, ids []string) applyResult {
	kt.t.Helper()
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	rc := taskstest.NewFakeRun(ctx, kt.kind()).RC()
	res := applyResult{Skipped: map[string]string{}, Failed: map[string]error{}}
	var mu sync.Mutex
	opts := tasks.ItemsOptions[string]{
		OnError: tasks.Collect,
		ExclusiveRefs: func(id string) []entity.Ref {
			kt.rows.mu.Lock()
			defer kt.rows.mu.Unlock()
			return kt.rows.rows[id].Subjects
		},
	}
	_, _ = tasks.RunItems(rc, tasks.Fixed(4, "test"), ids, func(ic *tasks.ItemRC, id string) error {
		// A row an earlier run of this plan marked is not applied again.
		if ok, _ := kt.w.RowApplied(ctx, id); ok {
			mu.Lock()
			res.Skipped[id] = "marked"
			mu.Unlock()
			return nil
		}
		kt.rows.mu.Lock()
		planned := kt.rows.rows[id]
		kt.rows.mu.Unlock()
		c, ok, err := kt.k.load(rc, nil, id)
		if err != nil {
			mu.Lock()
			res.Failed[id] = err
			mu.Unlock()
			return err
		}
		if !ok {
			mu.Lock()
			res.Skipped[id] = "gone"
			mu.Unlock()
			return nil
		}
		row, err := kt.k.evaluate(ic, nil, c)
		if err != nil {
			mu.Lock()
			res.Failed[id] = err
			mu.Unlock()
			return err
		}
		if row.Fingerprint() != planned.Fingerprint() {
			mu.Lock()
			res.Skipped[id] = "changed_since_plan"
			mu.Unlock()
			return nil
		}
		if err := kt.k.apply(ic, kt.w, nil, row); err != nil && !errors.Is(err, tasks.ErrPreview) {
			mu.Lock()
			res.Failed[id] = err
			mu.Unlock()
			return err
		}
		kt.w.MarkRowApplied(ic.Context(), id)
		mu.Lock()
		res.Applied = append(res.Applied, id)
		mu.Unlock()
		return nil
	}, opts)
	slices.Sort(res.Applied)
	require.NoError(kt.t, kt.k.afterApply(rc, res.Applied))
	return res
}

// ---- plan ----

func TestRepairsKinds_PlanRowsMatchRunPlan(t *testing.T) {
	s := newMemStore()
	seed(s)
	f := &trimFixer{s: s}
	head := planFor(t, s, f)

	kt := newKit(t, s, f)
	native := kt.plan(context.Background())

	require.Len(t, native, len(head.Rows))
	for i, h := range head.Rows {
		n := native[i]
		require.Equal(t, h.RowID, n.ID, "same rows in the same order")
		require.Equal(t, h.Skipped, n.Hold, "a row HEAD's plan skips is held, not dropped: %s", h.RowID)
		stored, err := json.Marshal(h)
		require.NoError(t, err)
		require.JSONEq(t, string(stored), string(n.State), "the stored State is HEAD's stored row: %s", h.RowID)
		require.Equal(t, h.Fingerprint, n.Inputs[inputFingerprint])
		require.Equal(t, h.Class, n.Class)
		require.Equal(t, h.Title, n.Title)
		require.Len(t, n.Subjects, len(h.BookIDs))
	}
	// The guards held what they hold at HEAD.
	byID := map[string]tasks.Row{}
	for _, n := range native {
		byID[n.ID] = n
	}
	require.Empty(t, byID["b1"].Hold)
	require.Equal(t, SkipITunes, byID["b4"].Hold)
	require.Equal(t, SkipOwnerManual, byID["b5"].Hold)
	require.Equal(t, SkipOwnerManual, byID["b6"].Hold)
	require.Equal(t, tasks.RiskLow, byID["b1"].Risk)
}

func TestRepairsKinds_PlanRefusesWithoutGuardOrOnBadRows(t *testing.T) {
	s := newMemStore()
	seed(s)
	kt := newKit(t, s, &trimFixer{s: s})
	rc := taskstest.NewFakeRun(context.Background(), kt.kind()).RC()

	noGuard := newKind(&trimFixer{s: s}, KindDeps{})
	_, err := noGuard.candidates(rc, nil)
	require.ErrorContains(t, err, "no guard reader", "fail closed: a row the guard cannot see is not applicable")

	dup := newKit(t, s, dupRowFixer{&trimFixer{s: s}})
	_, err = dup.k.candidates(rc, nil)
	require.ErrorContains(t, err, "empty or duplicate row id")

	empty := newKit(t, s, emptyPlanFixer{&trimFixer{s: s}})
	src, err := empty.k.candidates(rc, nil)
	require.NoError(t, err)
	require.Empty(t, src.Items)
}

type dupRowFixer struct{ *trimFixer }

func (dupRowFixer) ID() string { return "dup-rows" }
func (dupRowFixer) Plan(context.Context, json.RawMessage, registry.Reporter) ([]Row, error) {
	return []Row{{RowID: "x"}, {RowID: "x"}}, nil
}

type emptyPlanFixer struct{ *trimFixer }

func (emptyPlanFixer) ID() string { return "empty-plan" }
func (emptyPlanFixer) Plan(context.Context, json.RawMessage, registry.Reporter) ([]Row, error) {
	return nil, nil
}

func TestPlanRowOf_Mapping(t *testing.T) {
	r := Row{RowID: "r1", BookIDs: []string{"b2", "b1"}, Title: "Year One", Fingerprint: "fp1", Risk: RiskReview,
		Skipped: "held", RetryLater: true, OwnerApplicable: true, Class: "moved",
		OwnerITunesDatabaseOnly: []string{"b2", "b1"}, Evidence: []string{"e"}}
	got, err := PlanRowOf(r)
	require.NoError(t, err)
	require.Equal(t, "r1", got.ID)
	require.Equal(t, []string{"books:books:b2", "books:books:b1"}, subjectStrings(got), "every book of the row is a subject, in the row's order")
	require.Equal(t, tasks.RiskMedium, got.Risk, "review is medium")
	require.Equal(t, "held", got.Hold)
	require.True(t, got.Retry)
	require.True(t, got.OwnerOnly)
	require.Equal(t, "b1,b2", got.Inputs[inputOwnerDBOnly], "the owner's iTunes-database books are fingerprinted, sorted")
	require.Equal(t, "fp1", got.Inputs[inputFingerprint])
	// A row with no books still encodes book_ids as an array.
	got, err = PlanRowOf(Row{RowID: "r2"})
	require.NoError(t, err)
	require.Contains(t, string(got.State), `"book_ids":[]`)
	// Unknown risk words grade medium, never low.
	require.Equal(t, tasks.RiskMedium, taskRisk("whatever"))
	require.Equal(t, tasks.RiskLow, taskRisk(RiskLow))
}

func subjectStrings(r tasks.Row) []string {
	out := make([]string, len(r.Subjects))
	for i, s := range r.Subjects {
		out[i] = s.Module + ":" + s.Kind + ":" + s.ID
	}
	return out
}

// A different fingerprint, owner set, hold or retry flag each move the
// executor's fingerprint of the row; the same inputs do not.
func TestPlanRowOf_FingerprintTracksWhatApplyRechecks(t *testing.T) {
	base := Row{RowID: "r", BookIDs: []string{"b"}, Fingerprint: "fp"}
	fp := func(r Row) string {
		row, err := PlanRowOf(r)
		require.NoError(t, err)
		return row.Fingerprint()
	}
	same := base
	same.Title, same.Reason = "other words", "other reason"
	require.Equal(t, fp(base), fp(same), "display text is not an input")
	moved := base
	moved.Fingerprint = "fp2"
	require.NotEqual(t, fp(base), fp(moved))
	owner := base
	owner.OwnerITunesDatabaseOnly = []string{"b"}
	require.NotEqual(t, fp(base), fp(owner))
	held := base
	held.Skipped = "held"
	require.NotEqual(t, fp(base), fp(held))
	transient := held
	transient.RetryLater = true
	require.Equal(t, fp(base), fp(transient), "a transient hold is not part of the fingerprint")
}

// ---- apply ----

func TestRepairsKinds_ApplyWritesRowsAndMarksThem(t *testing.T) {
	s := newMemStore()
	seed(s)
	f := &trimFixer{s: s}
	kt := newKit(t, s, f)
	kt.plan(context.Background())

	res := kt.apply(context.Background(), []string{"b1", "b2"})
	require.Equal(t, []string{"b1", "b2"}, res.Applied)
	require.Empty(t, res.Failed)
	require.Equal(t, "One", s.title("b1"))
	require.Equal(t, "Two", s.title("b2"))
	require.Equal(t, " iTunes Copy ", s.title("b4"), "an unselected row is untouched")
	require.Len(t, kt.w.writes, 2, "one gated write per applied row")
	for _, w := range kt.w.writes {
		require.Equal(t, tasks.Res("books", "books"), w.Resource, "the write is inside the definition's Effects")
		require.Equal(t, "books.repairs.trim-titles", w.Name)
	}
	require.Equal(t, []string{"b1", "b2"}, sortedMarks(kt.w))
}

func sortedMarks(w *fakeTaskWriter) []string {
	w.mu.Lock()
	defer w.mu.Unlock()
	out := slices.Clone(w.markList)
	slices.Sort(out)
	return out
}

func TestRepairsKinds_ChangedSincePlanIsNotApplied(t *testing.T) {
	s := newMemStore()
	seed(s)
	f := &trimFixer{s: s}
	kt := newKit(t, s, f)
	kt.plan(context.Background())

	// Another writer edits b1 after the plan was made.
	_, err := s.ModifyBook("b1", func(b *database.Book) error { b.Title = " One, Revised "; return nil })
	require.NoError(t, err)

	res := kt.apply(context.Background(), []string{"b1", "b2"})
	require.Equal(t, []string{"b2"}, res.Applied)
	require.Equal(t, "changed_since_plan", res.Skipped["b1"])
	require.Equal(t, " One, Revised ", s.title("b1"), "a stale plan row is never applied")
	require.Equal(t, []string{"b2"}, sortedMarks(kt.w), "a refused row is not marked")
}

// Two apply runs of one plan never double-apply a row: a second run's
// re-evaluation finds the row already fixed, whose fingerprint is not the
// planned one. Marks are per run, so the second run has none: this is the
// only guard across runs, and it is HEAD's.
func TestRepairsKinds_SecondApplyOfSamePlanDoesNotDoubleApply(t *testing.T) {
	s := newMemStore()
	seed(s)
	f := &trimFixer{s: s}
	kt := newKit(t, s, f)
	kt.plan(context.Background())

	first := kt.apply(context.Background(), []string{"b1", "b2"})
	require.Equal(t, []string{"b1", "b2"}, first.Applied)
	history := len(s.history)

	// A second run: fresh marks (marks are per run), the same plan.
	kt.w = &fakeTaskWriter{}
	second := kt.apply(context.Background(), []string{"b1", "b2"})
	require.Empty(t, second.Applied)
	require.Equal(t, "changed_since_plan", second.Skipped["b1"])
	require.Equal(t, "changed_since_plan", second.Skipped["b2"])
	require.Empty(t, kt.w.writes, "nothing is written")
	require.Len(t, s.history, history, "no history row is added")
}

// Within one run an applied row carries its mark, so it is not applied again
// even when its row is named twice.
func TestRepairsKinds_ApplyRowTakenOnce(t *testing.T) {
	s := newMemStore()
	seed(s)
	kt := newKit(t, s, &trimFixer{s: s})
	kt.plan(context.Background())
	rc := taskstest.NewFakeRun(context.Background(), kt.kind()).RC()

	var applied, secondErr error
	_, err := tasks.RunItems(rc, tasks.Fixed(1, "test"), []string{"b1"}, func(ic *tasks.ItemRC, id string) error {
		c, ok, err := kt.k.load(rc, nil, id)
		require.NoError(t, err)
		require.True(t, ok)
		row, err := kt.k.evaluate(ic, nil, c)
		require.NoError(t, err)
		applied = kt.k.apply(ic, kt.w, nil, row)
		secondErr = kt.k.apply(ic, kt.w, nil, row)
		return nil
	})
	require.NoError(t, err)
	require.NoError(t, applied)
	require.ErrorContains(t, secondErr, "no fresh evaluation", "one evaluation backs one apply")
	require.Len(t, kt.w.writes, 1)
}

func TestRepairsKinds_ApplyWithoutEvaluateWritesNothing(t *testing.T) {
	s := newMemStore()
	seed(s)
	kt := newKit(t, s, &trimFixer{s: s})
	rows := kt.plan(context.Background())
	rc := taskstest.NewFakeRun(context.Background(), kt.kind()).RC()
	_, err := tasks.RunItems(rc, tasks.Fixed(1, "test"), []string{"b1"}, func(ic *tasks.ItemRC, _ string) error {
		var planned tasks.Row
		for _, r := range rows {
			if r.ID == "b1" {
				planned = r
			}
		}
		// The planned row itself, as a caller that skipped Evaluate would have it.
		err := kt.k.apply(ic, kt.w, nil, planned)
		require.ErrorContains(t, err, "was not evaluated by this run")
		return nil
	})
	require.NoError(t, err)
	require.Empty(t, kt.w.writes)
	require.Equal(t, " One ", s.title("b1"))
}

func TestRepairsKinds_LoadNeedsPlanRows(t *testing.T) {
	s := newMemStore()
	seed(s)
	kt := newKit(t, s, &trimFixer{s: s})
	rc := taskstest.NewFakeRun(context.Background(), kt.kind()).RC()

	noRows := newKind(&trimFixer{s: s}, KindDeps{Guard: s})
	_, _, err := noRows.load(rc, nil, "b1")
	require.ErrorContains(t, err, "no plan row reader")

	kt.rows.readErr = errors.New("plan store down")
	_, _, err = kt.k.load(rc, nil, "b1")
	require.ErrorContains(t, err, "plan store down")
	kt.rows.readErr = nil

	_, ok, err := kt.k.load(rc, nil, "no-such-row")
	require.NoError(t, err)
	require.False(t, ok, "a row the plan does not hold is gone")

	kt.rows.bytes["bad"] = json.RawMessage(`{not json`)
	_, _, err = kt.k.load(rc, nil, "bad")
	require.ErrorIs(t, err, ErrPlanRowsUnreadable, "an undecodable stored row is refused, never guessed")
}

// The bytes HEAD stored for a plan row are what a native apply reads: the same
// JSON as State, so a plan made before the cutover applies after it.
func TestRepairsKinds_LoadsHEADRowBytes(t *testing.T) {
	s := newMemStore()
	seed(s)
	f := &trimFixer{s: s}
	head := planFor(t, s, f)
	kt := newKit(t, s, f)
	for _, h := range head.Rows {
		b, err := json.Marshal(h) // what HEAD's plan store keeps under "opv2:row:"
		require.NoError(t, err)
		kt.rows.bytes[h.RowID] = b
		mapped, err := PlanRowOf(h)
		require.NoError(t, err)
		kt.rows.rows[h.RowID] = mapped
	}
	res := kt.apply(context.Background(), []string{"b1", "b3"})
	require.Equal(t, []string{"b1"}, res.Applied, "b3 is already clean and was planned held: its stored fingerprint shows it")
	require.Equal(t, "One", s.title("b1"))
}

func TestRepairsKinds_GuardsHoldARowAtApply(t *testing.T) {
	s := newMemStore()
	seed(s)
	f := &trimFixer{s: s}
	kt := newKit(t, s, f)
	kt.plan(context.Background())

	// After the plan, b1 gains a file under books/itunes, and b2 becomes Doctor Who.
	s.mu.Lock()
	s.files["b1"] = []database.BookFile{{ID: "f1", BookID: "b1", FilePath: "/mnt/books/itunes/Music/one.m4b"}}
	s.books["b2"].FilePath = "/lib/Doctor Who/Two/two.m4b"
	s.mu.Unlock()

	res := kt.apply(context.Background(), []string{"b1", "b2"})
	require.Empty(t, res.Applied)
	require.Equal(t, "changed_since_plan", res.Skipped["b1"])
	require.Equal(t, "changed_since_plan", res.Skipped["b2"])
	require.Equal(t, " One ", s.title("b1"))
	require.Equal(t, " Two ", s.title("b2"))
	require.Empty(t, kt.w.writes)
}

func TestRepairsKinds_GuardUnreadableFailsClosed(t *testing.T) {
	s := newMemStore()
	seed(s)
	kt := newKit(t, s, &trimFixer{s: s})
	kt.plan(context.Background())
	s.authorErr = errors.New("author store down")
	s.mu.Lock()
	s.bookAuthors = map[string][]database.BookAuthor{"b1": {{BookID: "b1", AuthorID: 1}}}
	s.mu.Unlock()
	res := kt.apply(context.Background(), []string{"b1"})
	require.Empty(t, res.Applied, "a row the guard cannot read is not applied")
	require.Equal(t, " One ", s.title("b1"))
}

// A guard-held row is held at plan time with the guard's own reason.
func TestRepairsKinds_PlanGuardUnreadableHoldsRow(t *testing.T) {
	s := newMemStore()
	seed(s)
	s.authorErr = errors.New("author store down")
	s.bookAuthors = map[string][]database.BookAuthor{"b1": {{BookID: "b1", AuthorID: 1}}}
	kt := newKit(t, s, &trimFixer{s: s})
	rows := kt.plan(context.Background())
	for _, r := range rows {
		if r.ID == "b1" {
			require.Equal(t, SkipGuardUnreadable, r.Hold)
		}
	}
}

// A plan-only fixer applies nothing, dry or live.
type planOnlyFixer struct{ *trimFixer }

func (planOnlyFixer) ID() string     { return "plan-only" }
func (planOnlyFixer) PlanOnly() bool { return true }

func TestRepairsKinds_PlanOnlyFixerNeverWrites(t *testing.T) {
	s := newMemStore()
	seed(s)
	f := planOnlyFixer{&trimFixer{s: s}}
	kt := newKit(t, s, f)
	kt.plan(context.Background())
	res := kt.apply(context.Background(), []string{"b1"})
	require.Empty(t, res.Applied)
	require.Empty(t, kt.w.writes)
	require.Equal(t, " One ", s.title("b1"))
}

// A fixer whose Replan answers "retry later" writes nothing and is not marked;
// the same plan applies once the condition clears.
type retryFixer struct {
	*trimFixer
	retry atomic.Bool
}

func (f *retryFixer) ID() string { return "retry-later" }
func (f *retryFixer) Replan(ctx context.Context, p json.RawMessage, planned Row, rep registry.Reporter) (Row, error) {
	fresh, err := f.trimFixer.Replan(ctx, p, planned, rep)
	if err != nil || !f.retry.Load() {
		return fresh, err
	}
	fresh.Skipped, fresh.SkipReason, fresh.RetryLater = "index_warming", "the index is warming up", true
	fresh.RetryFingerprint = planned.Fingerprint
	fresh.Fingerprint = "held:" + planned.Fingerprint
	return fresh, nil
}

func TestRepairsKinds_RetryLaterWritesNothingAndIsNotMarked(t *testing.T) {
	s := newMemStore()
	seed(s)
	f := &retryFixer{trimFixer: &trimFixer{s: s}}
	kt := newKit(t, s, f)
	kt.plan(context.Background())

	f.retry.Store(true)
	res := kt.apply(context.Background(), []string{"b1"})
	require.Empty(t, res.Applied)
	require.Error(t, res.Failed["b1"])
	require.ErrorIs(t, res.Failed["b1"], ErrRetryLater)
	require.Empty(t, kt.w.writes)
	require.Empty(t, kt.w.markList, "retry_later is not settled")

	f.retry.Store(false)
	res = kt.apply(context.Background(), []string{"b1"})
	require.Equal(t, []string{"b1"}, res.Applied)
	require.Equal(t, "One", s.title("b1"))
}

// A retry-later hold whose row also changed is a change, not a retry.
func TestRepairsKinds_RetryLaterWithOtherChangeIsChanged(t *testing.T) {
	s := newMemStore()
	seed(s)
	f := &retryFixer{trimFixer: &trimFixer{s: s}}
	kt := newKit(t, s, f)
	kt.plan(context.Background())
	f.retry.Store(true)
	_, err := s.ModifyBook("b1", func(b *database.Book) error { b.Title = " One, Revised "; return nil })
	require.NoError(t, err)
	// Replan's RetryFingerprint is the planned one, so make it differ as a real
	// change would: the fixer computes it from the changed inputs.
	f2 := &retryChangedFixer{retryFixer: f}
	kt.k.f = f2
	res := kt.apply(context.Background(), []string{"b1"})
	require.Empty(t, res.Applied)
	require.Equal(t, "changed_since_plan", res.Skipped["b1"])
	require.Empty(t, kt.w.writes)
}

type retryChangedFixer struct{ *retryFixer }

func (f *retryChangedFixer) Replan(ctx context.Context, p json.RawMessage, planned Row, rep registry.Reporter) (Row, error) {
	fresh, err := f.retryFixer.Replan(ctx, p, planned, rep)
	fresh.RetryFingerprint = "other-input:" + fresh.RetryFingerprint
	return fresh, err
}

// An empty RetryFingerprint never matches, even a planned row with an empty
// fingerprint: fail closed.
func TestRepairsKinds_RetryLaterWithoutFingerprintIsChanged(t *testing.T) {
	s := newMemStore()
	s.add("b1", " One ", "/lib/A/One/one.m4b", nil)
	f := &emptyRetryFixer{trimFixer: &trimFixer{s: s}}
	kt := newKit(t, s, f)
	kt.plan(context.Background())
	res := kt.apply(context.Background(), []string{"b1"})
	require.Empty(t, res.Applied)
	require.Equal(t, "changed_since_plan", res.Skipped["b1"])
	require.Empty(t, kt.w.writes)
}

type emptyRetryFixer struct{ *trimFixer }

func (f *emptyRetryFixer) ID() string { return "retry-empty" }
func (f *emptyRetryFixer) Plan(ctx context.Context, p json.RawMessage, rep registry.Reporter) ([]Row, error) {
	rows, err := f.trimFixer.Plan(ctx, p, rep)
	for i := range rows {
		rows[i].Fingerprint = ""
	}
	return rows, err
}
func (f *emptyRetryFixer) Replan(ctx context.Context, p json.RawMessage, planned Row, rep registry.Reporter) (Row, error) {
	fresh, err := f.trimFixer.Replan(ctx, p, planned, rep)
	fresh.Fingerprint = ""
	fresh.Skipped, fresh.RetryLater, fresh.RetryFingerprint = "warming", true, ""
	return fresh, err
}

func TestRepairsKinds_ReplanErrorFailsTheRowAndWrongIDIsRefused(t *testing.T) {
	s := newMemStore()
	seed(s)
	f := &replanBrokenFixer{trimFixer: &trimFixer{s: s}}
	kt := newKit(t, s, f)
	kt.plan(context.Background())

	f.mode.Store(1)
	res := kt.apply(context.Background(), []string{"b1"})
	require.ErrorContains(t, res.Failed["b1"], "replan: boom")
	f.mode.Store(2)
	res = kt.apply(context.Background(), []string{"b1"})
	require.ErrorContains(t, res.Failed["b1"], `replan returned row "other" for "b1"`)
	require.Empty(t, kt.w.writes)
	require.Equal(t, " One ", s.title("b1"))
}

type replanBrokenFixer struct {
	*trimFixer
	mode atomic.Int32
}

func (f *replanBrokenFixer) ID() string { return "replan-broken" }
func (f *replanBrokenFixer) Replan(ctx context.Context, p json.RawMessage, planned Row, rep registry.Reporter) (Row, error) {
	switch f.mode.Load() {
	case 1:
		return Row{}, errors.New("boom")
	case 2:
		r, err := f.trimFixer.Replan(ctx, p, planned, rep)
		r.RowID = "other"
		return r, err
	}
	return f.trimFixer.Replan(ctx, p, planned, rep)
}

// A re-plan that adds a book to the row puts that book through the guard.
type growingFixer struct{ *trimFixer }

func (f *growingFixer) ID() string { return "growing" }
func (f *growingFixer) Replan(ctx context.Context, p json.RawMessage, planned Row, rep registry.Reporter) (Row, error) {
	r, err := f.trimFixer.Replan(ctx, p, planned, rep)
	r.BookIDs = append(r.BookIDs, "b5") // a Big Finish book, owner-manual
	return r, err
}

func TestRepairsKinds_ReplanAddedBookIsGuarded(t *testing.T) {
	s := newMemStore()
	seed(s)
	f := &growingFixer{&trimFixer{s: s}}
	kt := newKit(t, s, f)
	kt.plan(context.Background())
	res := kt.apply(context.Background(), []string{"b1"})
	require.Empty(t, res.Applied)
	require.Equal(t, " One ", s.title("b1"))
	require.Empty(t, kt.w.writes)
}

// A re-plan that lists other owner iTunes books than the plan did is a change.
func TestRepairsKinds_OwnerITunesBooksDifferFromPlan(t *testing.T) {
	s := newMemStore()
	seed(s)
	f := &ownerSetFixer{trimFixer: &trimFixer{s: s}}
	kt := newKit(t, s, f)
	kt.plan(context.Background())
	f.extra.Store(true)
	res := kt.apply(context.Background(), []string{"b1"})
	require.Empty(t, res.Applied)
	require.Empty(t, kt.w.writes)
}

type ownerSetFixer struct {
	*trimFixer
	extra atomic.Bool
}

func (f *ownerSetFixer) ID() string { return "owner-set" }
func (f *ownerSetFixer) Replan(ctx context.Context, p json.RawMessage, planned Row, rep registry.Reporter) (Row, error) {
	r, err := f.trimFixer.Replan(ctx, p, planned, rep)
	if f.extra.Load() {
		r.OwnerITunesDatabaseOnly = []string{"b1"}
	}
	return r, err
}

// A row Apply finds changed under its own lock is ErrChangedSincePlan on the
// executor's side too, and a fenced write is tasks.ErrFenced.
func TestRepairsKinds_ApplyErrorsAreMapped(t *testing.T) {
	require.NoError(t, mapApplyError(nil))
	changed := mapApplyError(fmt.Errorf("row: %w", ErrChangedSincePlan))
	require.ErrorIs(t, changed, tasks.ErrChangedSincePlan)
	require.ErrorIs(t, changed, ErrChangedSincePlan, "the fixer's own sentinel stays in the chain")
	fenced := mapApplyError(fmt.Errorf("step 2 of 3 written: %w", opswriter.ErrFenced))
	require.ErrorIs(t, fenced, tasks.ErrFenced)
	require.ErrorIs(t, fenced, opswriter.ErrFenced)
	require.ErrorIs(t, mapApplyError(tasks.ErrPreview), tasks.ErrPreview)
	other := errors.New("disk on fire")
	require.Equal(t, other, mapApplyError(other))
	require.False(t, errors.Is(mapApplyError(other), tasks.ErrFenced))
}

func TestRepairsKinds_PreviewWritesNothing(t *testing.T) {
	s := newMemStore()
	seed(s)
	f := &trimFixer{s: s}
	kt := newKit(t, s, f)
	kt.plan(context.Background())
	kt.w.preview = true
	res := kt.apply(context.Background(), []string{"b1"})
	require.Equal(t, []string{"b1"}, res.Applied, "the executor counts a previewed row, as HEAD's would_apply")
	require.Equal(t, " One ", s.title("b1"), "the fixer's Apply is not called in Preview")
	require.Len(t, kt.w.writes, 1, "the intent is recorded")
}

func TestRepairsKinds_FencedRunWritesNothingMore(t *testing.T) {
	s := newMemStore()
	seed(s)
	f := &trimFixer{s: s}
	kt := newKit(t, s, f)
	kt.plan(context.Background())
	kt.w.fenced = fmt.Errorf("%w: run stopped", tasks.ErrFenced)
	res := kt.apply(context.Background(), []string{"b1", "b2"})
	require.Empty(t, res.Applied)
	require.Len(t, res.Failed, 2)
	require.ErrorIs(t, res.Failed["b1"], tasks.ErrFenced)
	require.Equal(t, " One ", s.title("b1"))
}

// The inner writer's own fence refuses once the run's context is done, and the
// refusal is both a tasks.ErrFenced and an opswriter.ErrFenced.
func TestRepairsKinds_WriterFenceFollowsTheRunContext(t *testing.T) {
	s := newMemStore()
	seed(s)
	f := &trimFixer{s: s}
	kt := newKit(t, s, f)
	kt.plan(context.Background())

	ctx, cancel := context.WithCancel(context.Background())
	rc := taskstest.NewFakeRun(ctx, kt.kind()).RC()
	rs, err := kt.k.runState(rc)
	require.NoError(t, err)
	_, err = rs.writer.Modify("b1", func(b *database.Book) error { b.Title = "Edited"; return nil })
	require.NoError(t, err, "a live run writes")
	cancel()
	_, err = rs.writer.Modify("b2", func(b *database.Book) error { b.Title = "Edited"; return nil })
	require.Error(t, err)
	require.ErrorIs(t, err, tasks.ErrFenced)
	require.ErrorIs(t, err, opswriter.ErrFenced)
	require.Equal(t, " Two ", s.title("b2"))
	require.Eventually(t, func() bool { _, ok := kt.k.runs.Load(rc.RunID()); return !ok }, 2*time.Second, 5*time.Millisecond,
		"the run's state is released when its context ends")
}

// ---- stores, scope, follow-up ----

// probeFixer records what its context carried and asks for ApplyScoped and
// AfterApply.
type probeFixer struct {
	*trimFixer
	mu        sync.Mutex
	storesAt  map[string]bool
	scopedAt  map[string]bool
	begun     atomic.Int64
	ended     atomic.Int64
	dryBegin  atomic.Bool
	afterIDs  []string
	afterErr  error
	afterSeen bool
}

type probeScopeKey struct{}

func (f *probeFixer) ID() string { return "probe" }

func (f *probeFixer) note(where string, ctx context.Context) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.storesAt == nil {
		f.storesAt, f.scopedAt = map[string]bool{}, map[string]bool{}
	}
	s, ok := StoresFrom[string](ctx)
	f.storesAt[where] = ok && s == "shell"
	f.scopedAt[where] = ctx.Value(probeScopeKey{}) == "scope"
}

func (f *probeFixer) Plan(ctx context.Context, p json.RawMessage, rep registry.Reporter) ([]Row, error) {
	f.note("plan", ctx)
	return f.trimFixer.Plan(ctx, p, rep)
}

func (f *probeFixer) Replan(ctx context.Context, p json.RawMessage, planned Row, rep registry.Reporter) (Row, error) {
	f.note("replan", ctx)
	return f.trimFixer.Replan(ctx, p, planned, rep)
}

func (f *probeFixer) Apply(ctx context.Context, w *Writer, fresh Row) error {
	f.note("apply", ctx)
	return f.trimFixer.Apply(ctx, w, fresh)
}

func (f *probeFixer) BeginApply(ctx context.Context, dry bool) (context.Context, func()) {
	f.begun.Add(1)
	f.dryBegin.Store(dry)
	f.note("begin", ctx)
	return context.WithValue(ctx, probeScopeKey{}, "scope"), func() { f.ended.Add(1) }
}

func (f *probeFixer) AfterApply(ctx context.Context, ids []string) (string, error) {
	f.note("after", ctx)
	f.mu.Lock()
	defer f.mu.Unlock()
	f.afterIDs, f.afterSeen = ids, true
	return "op-follow", f.afterErr
}

func TestRepairsKinds_StoresScopeAndFollowUp(t *testing.T) {
	s := newMemStore()
	seed(s)
	f := &probeFixer{trimFixer: &trimFixer{s: s}}
	kt := newKit(t, s, f)
	kt.plan(context.Background())
	res := kt.apply(context.Background(), []string{"b2", "b1"})
	require.Equal(t, []string{"b1", "b2"}, res.Applied)

	for _, where := range []string{"plan", "replan", "apply", "begin", "after"} {
		require.True(t, f.storesAt[where], "the shell's stores are on the %s context", where)
	}
	for _, where := range []string{"replan", "apply", "after"} {
		require.True(t, f.scopedAt[where], "the ApplyScoped scope reaches %s", where)
	}
	require.False(t, f.scopedAt["plan"])
	require.EqualValues(t, 1, f.begun.Load(), "BeginApply runs once per run, not per row")
	require.False(t, f.dryBegin.Load())
	require.EqualValues(t, 1, f.ended.Load(), "the scope ends with the run")
	require.Equal(t, []string{"b1", "b2"}, f.afterIDs, "the follow-up gets the books of the applied rows, sorted")
}

func TestRepairsKinds_FollowUpErrorFailsNothingAndPreviewHasNone(t *testing.T) {
	s := newMemStore()
	seed(s)
	f := &probeFixer{trimFixer: &trimFixer{s: s}, afterErr: errors.New("queue full")}
	kt := newKit(t, s, f)
	kt.plan(context.Background())
	res := kt.apply(context.Background(), []string{"b1"})
	require.Equal(t, []string{"b1"}, res.Applied, "a failed follow-up fails no row")
	require.True(t, f.afterSeen)

	// Preview: no write run, so no follow-up and a dry BeginApply.
	f2 := &probeFixer{trimFixer: &trimFixer{s: s}}
	kt2 := newKit(t, s, f2)
	kt2.w.preview = true
	kt2.plan(context.Background())
	// The FakeRun is Live; drive a Preview run through a host override.
	pv := &modeRun{FakeRun: taskstest.NewFakeRun(context.Background(), kt2.kind()), mode: tasks.ModePreview}
	rc := tasks.NewRC(pv)
	_, err := kt2.k.runState(rc)
	require.NoError(t, err)
	require.True(t, f2.dryBegin.Load(), "a Preview run's scope is a dry one")
	require.NoError(t, kt2.k.afterApply(rc, []string{"b1"}))
	require.False(t, f2.afterSeen, "a Preview run starts no follow-up")
}

// modeRun is a FakeRun with another mode and an observable stand-down.
type modeRun struct {
	*taskstest.FakeRun
	mode      tasks.Mode
	standDown func(tasks.Key) (tasks.Hold, error)
}

func (r *modeRun) Mode() tasks.Mode { return r.mode }
func (r *modeRun) StandDown(k tasks.Key) (tasks.Hold, error) {
	if r.standDown == nil {
		return r.FakeRun.StandDown(k)
	}
	return r.standDown(k)
}

type recordedHold struct{ released atomic.Int64 }

func (h *recordedHold) Release() { h.released.Add(1) }

func TestRepairsKinds_ApplyHoldsTheScanStandDown(t *testing.T) {
	s := newMemStore()
	seed(s)
	f := &trimFixer{s: s}
	kt := newKit(t, s, f)
	kt.k.deps.StandDownKey = "books.scan"
	kt.plan(context.Background())

	hold := &recordedHold{}
	var key atomic.Value
	run := &modeRun{FakeRun: taskstest.NewFakeRun(context.Background(), kt.kind()), mode: tasks.ModeLive,
		standDown: func(k tasks.Key) (tasks.Hold, error) { key.Store(k); return hold, nil }}
	rc := tasks.NewRC(run)
	rs, err := kt.k.runState(rc)
	require.NoError(t, err)
	require.Equal(t, tasks.Key("books.scan"), key.Load())
	require.EqualValues(t, 0, hold.released.Load())
	rs.release()
	rs.release()
	require.EqualValues(t, 1, hold.released.Load(), "released once, with the run state")

	// A stand-down the executor refuses fails the run's first row: no write
	// goes ahead of a scan that was not quiesced.
	refusing := &modeRun{FakeRun: taskstest.NewFakeRun(context.Background(), kt.kind()), mode: tasks.ModeLive,
		standDown: func(tasks.Key) (tasks.Hold, error) { return nil, errors.New("scan would not park") }}
	_, _, err = kt.k.load(tasks.NewRC(refusing), nil, "b1")
	require.ErrorContains(t, err, "scan would not park")

	// A NoScanStandDown fixer takes none, and its writer refuses every write.
	nf := &noStandDownFixer{trimFixer: &trimFixer{s: s}}
	nk := newKit(t, s, nf)
	nk.k.deps.StandDownKey = "books.scan"
	nk.plan(context.Background())
	nrun := &modeRun{FakeRun: taskstest.NewFakeRun(context.Background(), nk.kind()), mode: tasks.ModeLive,
		standDown: func(tasks.Key) (tasks.Hold, error) { t.Fatal("a NoScanStandDown fixer holds none"); return nil, nil }}
	nrs, err := nk.k.runState(tasks.NewRC(nrun))
	require.NoError(t, err)
	_, err = nrs.writer.Modify("b1", func(b *database.Book) error { b.Title = "x"; return nil })
	require.Error(t, err, "the writer refuses every write")
}

type noStandDownFixer struct{ *trimFixer }

func (f *noStandDownFixer) ID() string            { return "no-standdown" }
func (f *noStandDownFixer) NoScanStandDown() bool { return true }

func TestRepairsKinds_RunStateErrors(t *testing.T) {
	s := newMemStore()
	seed(s)
	f := &trimFixer{s: s}
	rc := taskstest.NewFakeRun(context.Background(), "books.repairs.trim-titles").RC()

	k := newKind(f, KindDeps{Guard: s, PlanRows: &fakePlanRows{}})
	_, err := k.runState(rc)
	require.ErrorContains(t, err, "no writer")

	k = newKind(f, KindDeps{Guard: s, PlanRows: &fakePlanRows{},
		NewWriter: func(context.Context, tasks.RunID, Fixer) (*Writer, error) { return nil, errors.New("store closed") }})
	_, err = k.runState(rc)
	require.ErrorContains(t, err, "store closed")

	k = newKind(f, KindDeps{Guard: s, PlanRows: &fakePlanRows{},
		NewWriter: func(context.Context, tasks.RunID, Fixer) (*Writer, error) { return nil, nil }})
	_, err = k.runState(rc)
	require.ErrorContains(t, err, "no writer")
}

// ---- concurrency ----

// pairFixer plans rows that share books, and records how many applies touch a
// book at once.
type pairFixer struct {
	*trimFixer
	mu       sync.Mutex
	inflight map[string]int
	maxSeen  atomic.Int64
	applies  atomic.Int64
}

func (f *pairFixer) ID() string { return "pairs" }

func (f *pairFixer) Plan(context.Context, json.RawMessage, registry.Reporter) ([]Row, error) {
	var rows []Row
	for i := 0; i < 24; i++ {
		// Row i names books i and i+1: neighbours overlap, so no two rows that
		// share a book may run at once.
		a, b := fmt.Sprintf("p%02d", i), fmt.Sprintf("p%02d", i+1)
		rows = append(rows, Row{RowID: fmt.Sprintf("row%02d", i), BookIDs: []string{a, b}, Title: "Pair", Risk: RiskLow,
			Fingerprint: "fp"})
	}
	return rows, nil
}

func (f *pairFixer) Replan(_ context.Context, _ json.RawMessage, planned Row, _ registry.Reporter) (Row, error) {
	return planned, nil
}

func (f *pairFixer) Apply(_ context.Context, _ *Writer, fresh Row) error {
	f.mu.Lock()
	if f.inflight == nil {
		f.inflight = map[string]int{}
	}
	for _, b := range fresh.BookIDs {
		f.inflight[b]++
		if int64(f.inflight[b]) > f.maxSeen.Load() {
			f.maxSeen.Store(int64(f.inflight[b]))
		}
	}
	f.mu.Unlock()
	time.Sleep(2 * time.Millisecond)
	f.mu.Lock()
	for _, b := range fresh.BookIDs {
		f.inflight[b]--
	}
	f.mu.Unlock()
	f.applies.Add(1)
	return nil
}

func TestRepairsKinds_RowsSharingABookNeverApplyAtOnce(t *testing.T) {
	s := newMemStore()
	for i := 0; i <= 24; i++ {
		s.add(fmt.Sprintf("p%02d", i), "Pair", fmt.Sprintf("/lib/Bat/P%02d/p.m4b", i), nil)
	}
	f := &pairFixer{trimFixer: &trimFixer{s: s}}
	kt := newKit(t, s, f)
	rows := kt.plan(context.Background())
	require.Len(t, rows, 24)
	ids := make([]string, len(rows))
	for i, r := range rows {
		ids[i] = r.ID
		require.Len(t, r.Subjects, 2)
	}
	res := kt.apply(context.Background(), ids)
	require.Len(t, res.Applied, 24)
	require.EqualValues(t, 24, f.applies.Load())
	require.EqualValues(t, 1, f.maxSeen.Load(), "no book is written by two rows at once")
}

func TestRepairsKinds_ManyRowsApplyConcurrentlyUnderRace(t *testing.T) {
	s := newMemStore()
	for i := 0; i < 60; i++ {
		s.add(fmt.Sprintf("c%02d", i), " Gotham ", fmt.Sprintf("/lib/Bat/C%02d/c.m4b", i), nil)
	}
	f := &trimFixer{s: s}
	kt := newKit(t, s, f)
	rows := kt.plan(context.Background())
	ids := make([]string, len(rows))
	for i, r := range rows {
		ids[i] = r.ID
	}
	res := kt.apply(context.Background(), ids)
	require.Len(t, res.Applied, 60)
	require.Empty(t, res.Failed)
	for _, id := range ids {
		require.Equal(t, "Gotham", s.title(id))
	}
	require.Len(t, sortedMarks(kt.w), 60)
}

// ---- definitions ----

func TestRepairsKinds_DefinitionShape(t *testing.T) {
	s := newMemStore()
	deps := KindDeps{Guard: s, Scopes: testScopes, Concurrency: 6}
	defs := Kinds([]Fixer{&trimFixer{s: s}, &scopedPermFixer{trimFixer: &trimFixer{s: s}}}, deps)
	require.Len(t, defs, 2)
	taskstest.Conformance(t, defs...)

	d := defs[0]
	require.Equal(t, tasks.Kind("books.repairs.trim-titles"), d.Kind())
	require.Equal(t, tasks.ShapeFixer, d.Shape())
	require.Empty(t, tasks.Problems(d))
	c := tasks.CommonOf(d)
	require.Equal(t, "Trim titles", c.Title)
	require.Equal(t, "test fixer", c.Help)
	require.Equal(t, ApplyConcurrencyKey, c.ConcurrencyKey)
	require.True(t, c.Approval.PlanRequired)
	require.Equal(t, tasks.ResumeContinue, c.Resume)
	require.Equal(t, 6*time.Hour, c.Timeout)
	require.Equal(t, []rbac.Permission{"books:edit"}, c.Perms.Start.Permissions())
	require.Equal(t, c.Perms.Start.Permissions(), c.Perms.Approve.Permissions())
	require.Equal(t, c.Perms.Start.Permissions(), c.Perms.View.Permissions(), "reading the plan rows needs what applying it does")

	// A fixer that names a stronger permission gets every scope of it, for
	// starting, approving and reading.
	scoped := tasks.CommonOf(defs[1])
	want := []rbac.Permission{"core:users.manage", "core:roles.view"}
	require.Equal(t, want, scoped.Perms.Start.Permissions())
	require.Equal(t, want, scoped.Perms.Approve.Permissions())
	require.Equal(t, want, scoped.Perms.View.Permissions())
}

type scopedPermFixer struct{ *trimFixer }

func (f *scopedPermFixer) ID() string { return "admin-only" }
func (f *scopedPermFixer) RequiredPermissions() []auth.Permission {
	return []auth.Permission{auth.PermUsersManage}
}

// A permission the atom table does not know leaves the kind unregistrable, not
// open to everyone.
func TestRepairsKinds_UnresolvedPermissionFailsClosed(t *testing.T) {
	s := newMemStore()
	f := &unknownPermFixer{&trimFixer{s: s}}
	for name, deps := range map[string]KindDeps{
		"unknown atom": {Guard: s, Scopes: testScopes},
		"no resolver":  {Guard: s},
	} {
		defs := Kinds([]Fixer{f}, deps)
		probs := tasks.Problems(defs[0])
		require.NotEmpty(t, probs, name)
		require.ErrorContains(t, errors.Join(probs...), "Perms.Start", name)
	}
}

type unknownPermFixer struct{ *trimFixer }

func (f *unknownPermFixer) ID() string { return "unknown-perm" }
func (f *unknownPermFixer) RequiredPermissions() []auth.Permission {
	return []auth.Permission{"nobody.has.this"}
}

func TestRepairsKinds_RegistryKindsFollowTheLaneOrder(t *testing.T) {
	s := newMemStore()
	r := NewRegistry()
	require.NoError(t, r.Register(&trimFixer{s: s}))
	require.NoError(t, r.Register(&scopedPermFixer{trimFixer: &trimFixer{s: s}}))
	defs := r.Kinds(KindDeps{Guard: s, Scopes: testScopes})
	listed := r.List()
	require.Len(t, defs, len(listed))
	for i, f := range listed {
		require.Equal(t, tasks.Kind(KindPrefix+f.ID()), defs[i].Kind())
	}
}

func TestRepairsKinds_StoresRoundTripByType(t *testing.T) {
	type other struct{ n int }
	ctx := WithStores(context.Background(), "shell")
	got, ok := StoresFrom[string](ctx)
	require.True(t, ok)
	require.Equal(t, "shell", got)
	_, ok = StoresFrom[other](ctx)
	require.False(t, ok, "another type's stores are absent, never converted")
	_, ok = StoresFrom[string](context.Background())
	require.False(t, ok)
	_, ok = StoresFrom[string](nil) //nolint:staticcheck // a nil context is the case under test
	require.False(t, ok)
	var none fmt.Stringer
	_, ok = StoresFrom[fmt.Stringer](WithStores(context.Background(), none))
	require.False(t, ok, "a nil interface value carries nothing")
}

func TestRcReporter_RunAndItem(t *testing.T) {
	rc := taskstest.NewFakeRun(context.Background(), "books.repairs.trim-titles").RC()
	rep := newRCReporter(rc)
	require.NoError(t, rep.UpdateProgress(3, 10, "three of ten"))
	require.NoError(t, rep.UpdateProgress(4, 0, ""))
	require.NoError(t, rep.Log(0, "hello"))
	require.NotNil(t, rep.Logger())
	require.False(t, rep.IsCanceled())
	rep.SetCurrentItem("x")
	rep.TouchLiveness()
	require.ErrorIs(t, rep.Trigger(context.Background(), "x", nil), ErrNoRunEvents)
	require.NoError(t, rep.RunPhase(context.Background(), "phase", func(_ context.Context, r registry.Reporter) error {
		require.Equal(t, rep, r)
		return nil
	}))
	require.NoError(t, rep.Checkpoint(map[string]int{"n": 1}))

	ctx, cancel := context.WithCancel(context.Background())
	crc := taskstest.NewFakeRun(ctx, "books.repairs.trim-titles").RC()
	_, err := tasks.RunItems(crc, tasks.Fixed(1, "test"), []int{1}, func(ic *tasks.ItemRC, _ int) error {
		item := newItemReporter(ic)
		require.NoError(t, item.UpdateProgress(1, 1, "ignored"))
		require.NoError(t, item.Log(0, "row log"))
		require.Error(t, item.Checkpoint("state"), "an item has no checkpoint of its own")
		require.False(t, item.IsCanceled())
		cancel()
		require.True(t, item.IsCanceled())
		return nil
	})
	require.NoError(t, err)
}
