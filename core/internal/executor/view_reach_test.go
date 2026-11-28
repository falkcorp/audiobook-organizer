// file: core/internal/executor/view_reach_test.go
// version: 1.0.0
// guid: 2e8b6f14-93a7-4d5c-8c01-7f4a1d9e6b82
// last-edited: 2026-11-28

package executor

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/falkcorp/mediacore/coretest/logcapture"
	"github.com/falkcorp/mediacore/internal/taskstore"
	"github.com/falkcorp/mediacore/rbac"
	"github.com/falkcorp/mediacore/storage"
	"github.com/falkcorp/mediacore/tasks"
)

// F5-18 reach R2 and R3: which runs a caller may view, over a real executor
// and a real store, for kinds that have a live definition and kinds that have
// only a stored snapshot.

const scopeSettings = rbac.Permission("gotham:settings")

func withView(r rbac.Requirement) func(*tasks.Common) {
	return func(c *tasks.Common) { c.Perms.View = r }
}

// importRuns stages recs as an M4 import does (tasks.NativeImporter.Run), in
// bounded batches.
func importRuns(t testing.TB, h *harness, recs []tasks.RunRecord) {
	t.Helper()
	imp := tasks.NativeImporter(h.ks)
	for chunk := range slices.Chunk(recs, 250) {
		b := h.ks.NewIndexedBatch()
		for _, r := range chunk {
			if err := imp.Run(t.Context(), b, r); err != nil {
				_ = b.Close()
				t.Fatalf("import %s: %v", r.ID, err)
			}
		}
		if err := b.Commit(t.Context(), storage.Sync); err != nil {
			t.Fatalf("commit import: %v", err)
		}
	}
}

// putSnapshot writes def/<kind> as the kernel's upsert does.
func putSnapshot(t testing.TB, h *harness, kind tasks.Kind, raw []byte) {
	t.Helper()
	st := taskstore.New(h.ks)
	b := st.NewBatch()
	if err := st.Set(b, taskstore.DefKey(string(kind)), raw); err != nil {
		t.Fatal(err)
	}
	if err := b.Commit(t.Context(), storage.Sync); err != nil {
		t.Fatal(err)
	}
}

func snapshotOfDef(t testing.TB, d tasks.Definition) []byte {
	t.Helper()
	raw, err := EncodeDefSnapshot(d)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

var (
	reachFinished = time.Date(2026, 11, 20, 3, 0, 0, 0, time.UTC)
	// robin held a start scope when HEAD recorded his run; the imported actor
	// is his user id and method.
	robinSession = tasks.Actor{UserID: "robin", AuthMethod: "session"}
)

func doneRun(id string, kind tasks.Kind, state tasks.RunState) tasks.RunRecord {
	r := tasks.RunRecord{Priority: tasks.PriorityNormal}
	r.ID, r.Kind, r.State, r.Mode = tasks.RunID(id), kind, state, tasks.ModeLive
	r.Created, r.Started, r.Finished, r.Actor = reachFinished.Add(-time.Hour), reachFinished.Add(-time.Hour), reachFinished, robinSession
	return r
}

// reachHarness registers gotham.patrol (View omitted: core:tasks.view) and
// gotham.cowl (View gotham:settings); gotham.retired and gotham.unrestricted are
// NOT registered.
func reachHarness(t testing.TB, edit func(*Options)) *harness {
	t.Helper()
	return newHarness(t, []tasks.Definition{
		taskDef("gotham.patrol", okBody),
		taskDef("gotham.cowl", okBody, withView(rbac.Need(scopeSettings))),
	}, func(o *Options) {
		withPrincipal(o)
		o.Catalog = catalogWith(t, "gotham:run", scopeSettings)
		if edit != nil {
			edit(o)
		}
	})
}

func viewerCtx() context.Context { return asUser("robin", scopeTasksView) }
func adminCtx() context.Context {
	return asUser("alfred", scopeTasksView, scopeTasksManage, scopeSettings)
}

// countingChecker counts every permission question put to it.
type countingChecker struct {
	rbac.Checker // the inner checker: Concrete and Confined pass through
	mu           sync.Mutex
	calls        int
}

func (c *countingChecker) bump() { c.mu.Lock(); c.calls++; c.mu.Unlock() }
func (c *countingChecker) Has(p rbac.Permission) bool {
	c.bump()
	return c.Checker.Has(p)
}
func (c *countingChecker) Satisfies(r rbac.Requirement) bool {
	c.bump()
	return c.Checker.Satisfies(r)
}
func (c *countingChecker) n() int { c.mu.Lock(); defer c.mu.Unlock(); return c.calls }

// bigTable is 3,000 terminal runs over ten kinds: eight registered (four
// restricted to gotham:settings), one unregistered with a snapshot that
// restricts it, one unregistered with an unrestricted snapshot. It returns the
// runs a viewer (core:tasks.view only) may see, by state.
func bigTable(t testing.TB) (*harness, *countingChecker, map[tasks.RunState]int64, int) {
	t.Helper()
	var defs []tasks.Definition
	visibleKinds := map[tasks.Kind]bool{}
	for i := range 8 {
		kind := fmt.Sprintf("gotham.ward%d", i)
		if i%2 == 0 {
			defs = append(defs, taskDef(kind, okBody))
			visibleKinds[tasks.Kind(kind)] = true
		} else {
			defs = append(defs, taskDef(kind, okBody, withView(rbac.Need(scopeSettings))))
		}
	}
	cc := &countingChecker{Checker: grants{scopeTasksView: true}}
	h := newHarness(t, defs, func(o *Options) {
		o.Catalog = catalogWith(t, "gotham:run", scopeSettings)
		o.Principal = func(ctx context.Context) (tasks.Actor, rbac.Checker, bool) {
			if ctx.Value(callerKey{}) != nil {
				return robinSession, cc, true
			}
			return tasks.Actor{}, nil, false
		}
	})
	retired := taskDef("gotham.retiredward", okBody, withView(rbac.Need(scopeSettings)))
	open := taskDef("gotham.openward", okBody)
	putSnapshot(t, h, "gotham.retiredward", snapshotOfDef(t, retired))
	putSnapshot(t, h, "gotham.openward", snapshotOfDef(t, open))
	visibleKinds["gotham.openward"] = true
	kinds := []tasks.Kind{"gotham.retiredward", "gotham.openward"}
	for i := range 8 {
		kinds = append(kinds, tasks.Kind(fmt.Sprintf("gotham.ward%d", i)))
	}
	states := []tasks.RunState{tasks.StateSucceeded, tasks.StateFailed, tasks.StateCanceled}
	want := map[tasks.RunState]int64{}
	var recs []tasks.RunRecord
	for i := range 3000 {
		kind, state := kinds[i%len(kinds)], states[i%len(states)]
		recs = append(recs, doneRun(fmt.Sprintf("run-%05d", i), kind, state))
		if visibleKinds[kind] {
			want[state]++
		}
	}
	importRuns(t, h, recs)
	return h, cc, want, len(kinds)
}

// TestRuns_DecidesOncePerKind (R3, the same cost rule on the list path): the
// iteration over 3,000 runs asks once per kind and yields exactly the viewable
// runs. Killed by dropping viewer's memo.
func TestRuns_DecidesOncePerKind(t *testing.T) {
	h, cc, want, kinds := bigTable(t)
	got := map[tasks.RunState]int64{}
	var after tasks.RunID
	for { // Runs pages by cursor; the page limit counts the runs the caller may view
		n := 0
		for v, err := range h.x.Runs(viewerCtx(), tasks.RunQuery{RunFilter: tasks.RunFilter{After: after, Limit: 1000}}) {
			if err != nil {
				t.Fatal(err)
			}
			got[v.State]++
			after = v.ID
			n++
		}
		if n == 0 {
			break
		}
	}
	if !equalCounts(got, want) {
		t.Fatalf("listed runs by state = %v, want %v", got, want)
	}
	// One Runs call is one rule per kind however many runs it reads: a page of
	// 1,000 runs of ten kinds asks at most ten times.
	cc.mu.Lock()
	cc.calls = 0
	cc.mu.Unlock()
	seen := 0
	for _, err := range h.x.Runs(viewerCtx(), tasks.RunQuery{RunFilter: tasks.RunFilter{Limit: 1000}}) {
		if err != nil {
			t.Fatal(err)
		}
		seen++
	}
	if n := cc.n(); seen < 400 || n == 0 || n > kinds {
		t.Fatalf("one page of 1000 runs: %d yielded, checker asked %d times for %d kinds", seen, n, kinds)
	}
}

func equalCounts(a, b map[tasks.RunState]int64) bool {
	for s, n := range a {
		if b[s] != n {
			return false
		}
	}
	for s, n := range b {
		if a[s] != n {
			return false
		}
	}
	return true
}

// TestMayView_KindWithoutALiveDefinition (R3). Runs of a kind this build
// registers nothing for take the View rule the kind's snapshot recorded; a
// snapshot that says nothing usable hides the run from every signed-in caller.
func TestMayView_KindWithoutALiveDefinition(t *testing.T) {
	restricted := snapshotOfDef(t, taskDef("gotham.x", okBody, withView(rbac.Need(scopeSettings))))
	unrestricted := snapshotOfDef(t, taskDef("gotham.x", okBody))
	anyOf := snapshotOfDef(t, taskDef("gotham.x", okBody, withView(rbac.AnyOf(scopeSettings, "gotham:other"))))
	var v1 struct {
		V     int    `json:"v"`
		Shape string `json:"shape"`
		Title string `json:"title"`
	}
	v1.V, v1.Shape, v1.Title = 1, "task", "Batcave gotham.x"
	v1Raw, _ := json.Marshal(v1)
	cells := []struct {
		name     string
		snapshot []byte // nil: none stored
		viewer   bool
		admin    bool
	}{
		{"retired Repairs-shaped kind, View recorded", restricted, false, true},
		{"View recorded as any-of", anyOf, false, true},
		{"unknown non-Repairs kind, recorded unrestricted", unrestricted, true, true},
		{"no snapshot at all", nil, false, false},
		{"snapshot that does not decode", []byte(`{"v":2,"view":`), false, false},
		{"version-1 snapshot (predates View)", v1Raw, false, false},
		{"version-2 snapshot with no View field", []byte(`{"v":2,"shape":"task","title":"x"}`), false, false},
		{"View kind this build does not know", []byte(`{"v":2,"shape":"task","title":"x","view":{"kind":"quantum"}}`), false, false},
		{"all-of with no scopes", []byte(`{"v":2,"shape":"task","title":"x","view":{"kind":"all_of"}}`), false, false},
		{"requirement the snapshot cannot carry", []byte(`{"v":2,"shape":"task","title":"x","view":{"kind":"unsatisfiable"}}`), false, false},
	}
	for _, c := range cells {
		t.Run(c.name, func(t *testing.T) {
			h := reachHarness(t, nil)
			const kind = tasks.Kind("gotham.retired")
			if c.snapshot != nil {
				putSnapshot(t, h, kind, c.snapshot)
			}
			importRuns(t, h, []tasks.RunRecord{doneRun("retired-1", kind, tasks.StateSucceeded)})
			for _, who := range []struct {
				name string
				ctx  context.Context
				want bool
			}{{"viewer", viewerCtx(), c.viewer}, {"admin", adminCtx(), c.admin}, {"kernel-internal", context.Background(), true}} {
				_, err := h.x.Get(who.ctx, "retired-1")
				if (err == nil) != who.want {
					t.Errorf("%s Get = %v, want visible=%v", who.name, err, who.want)
				}
				if err != nil && !isErr(err, tasks.ErrRunNotFound) {
					t.Errorf("%s Get refused with %v, want ErrRunNotFound (a hidden run reads as a missing one)", who.name, err)
				}
				listed := 0
				for _, err := range h.x.Runs(who.ctx, tasks.RunQuery{}) {
					if err != nil {
						t.Fatal(err)
					}
					listed++
				}
				if (listed == 1) != who.want {
					t.Errorf("%s Runs listed %d, want visible=%v", who.name, listed, who.want)
				}
				tl, err := h.x.Timeline(who.ctx, tasks.TimelineQuery{})
				if err != nil || (len(tl) == 1) != who.want {
					t.Errorf("%s Timeline = %d runs, %v; want visible=%v", who.name, len(tl), err, who.want)
				}
				logged := 0
				var logErr error
				for _, err := range h.x.Log(who.ctx, "retired-1", tasks.PageQuery{}) {
					if err != nil {
						logErr = err
						break
					}
					logged++
				}
				if (logErr == nil) != who.want {
					t.Errorf("%s Log = %v, want visible=%v", who.name, logErr, who.want)
				}
			}
		})
	}
}

// TestMayView_NoKeyspaceIsClosed (R3): an executor built with no Keyspace has
// no snapshot to read, so a kind it has no definition for is hidden from
// every signed-in caller.
func TestMayView_NoKeyspaceIsClosed(t *testing.T) {
	h := reachHarness(t, func(o *Options) { o.Keyspace = nil })
	snap := snapshotOfDef(t, taskDef("gotham.x", okBody))
	putSnapshot(t, h, "gotham.retired", snap) // present on disk, unreachable to the executor
	importRuns(t, h, []tasks.RunRecord{doneRun("retired-1", "gotham.retired", tasks.StateSucceeded)})
	if _, err := h.x.Get(adminCtx(), "retired-1"); !isErr(err, tasks.ErrRunNotFound) {
		t.Fatalf("Get with no Keyspace = %v, want ErrRunNotFound", err)
	}
}

// TestMayView_RegisteredKindIgnoresItsSnapshot (R3): a live definition wins
// over a stored snapshot of the same kind, whatever it says.
func TestMayView_RegisteredKindIgnoresItsSnapshot(t *testing.T) {
	h := reachHarness(t, nil)
	putSnapshot(t, h, "gotham.patrol", snapshotOfDef(t, taskDef("gotham.x", okBody, withView(rbac.Need(scopeSettings)))))
	importRuns(t, h, []tasks.RunRecord{doneRun("patrol-1", "gotham.patrol", tasks.StateSucceeded)})
	if _, err := h.x.Get(viewerCtx(), "patrol-1"); err != nil {
		t.Fatalf("a registered kind's run is hidden by a stale snapshot: %v", err)
	}
}

// TestDefSnapshot_RoundTripsEveryView: what EncodeDefSnapshot records,
// taskstore.DecodeView reads back, for each requirement a definition can carry.
func TestDefSnapshot_RoundTripsEveryView(t *testing.T) {
	cells := []struct {
		name  string
		view  rbac.Requirement
		state taskstore.ViewState
		req   string
	}{
		{"omitted", rbac.Requirement{}, taskstore.ViewDefault, ""},
		{"need", rbac.Need(scopeSettings), taskstore.ViewRequires, "gotham:settings"},
		{"all of two", rbac.AllOf(scopeSettings, "gotham:run"), taskstore.ViewRequires, "all(gotham:settings,gotham:run)"},
		{"any of two", rbac.AnyOf(scopeSettings, "gotham:run"), taskstore.ViewRequires, "any(gotham:settings,gotham:run)"},
		{"authenticated", rbac.AnyAuthenticated, taskstore.ViewRequires, "authenticated"},
		{"public", rbac.Public, taskstore.ViewRequires, "public"},
		{"legacy atom (no checker satisfies it)", rbac.LegacyAtom("library.view"), taskstore.ViewRequires, "invalid"},
	}
	for _, c := range cells {
		t.Run(c.name, func(t *testing.T) {
			raw := snapshotOfDef(t, taskDef("gotham.x", okBody, withView(c.view)))
			req, st := taskstore.DecodeView(raw)
			if st != c.state || (st == taskstore.ViewRequires && req.String() != c.req) {
				t.Fatalf("DecodeView(%s) = %s, %v; want %v, %s", raw, req, st, c.state, c.req)
			}
		})
	}
}

// TestMayView_NamesTheKindItHides (R3, operator-facing): when a read leaves
// out the runs of a kind because it has no usable View snapshot, the log
// says so once per kind per request, with the kind and the reason and nothing
// of a run. An admin listing the runs of a kind with a version-1 snapshot gets
// the line and no run data. Killed by dropping the Info call.
func TestMayView_NamesTheKindItHides(t *testing.T) {
	lc := logcapture.New(t)
	h := reachHarness(t, func(o *Options) { o.Logger = lc.Logger() })
	putSnapshot(t, h, "gotham.stale", []byte(`{"v":1,"shape":"task","title":"Stale"}`))
	var recs []tasks.RunRecord
	for i := range 5 {
		recs = append(recs, doneRun(fmt.Sprintf("stale-%d", i), "gotham.stale", tasks.StateSucceeded))
		recs = append(recs, doneRun(fmt.Sprintf("nosnap-%d", i), "gotham.nosnap", tasks.StateSucceeded))
	}
	importRuns(t, h, recs)
	hidden := func() (lines []string) {
		for _, r := range lc.Records() {
			if r.Message != "task runs of a kind are hidden from every caller" {
				continue
			}
			var kind, reason string
			r.Attrs(func(a slog.Attr) bool {
				switch a.Key {
				case "kind":
					kind = a.Value.String()
				case "reason":
					reason = a.Value.String()
				default:
					t.Errorf("hidden-kind line carries %q, only kind and reason are allowed", a.Key)
				}
				return true
			})
			lines = append(lines, kind+"|"+reason)
		}
		slices.Sort(lines)
		return lines
	}
	n := 0
	for range h.x.Runs(adminCtx(), tasks.RunQuery{}) {
		n++
	}
	if n != 0 {
		t.Fatalf("admin listed %d runs of kinds with no usable snapshot, want none", n)
	}
	want := []string{"gotham.nosnap|no view snapshot", "gotham.stale|no view snapshot"}
	if got := hidden(); !slices.Equal(got, want) {
		t.Fatalf("one list of 10 runs of two hidden kinds logged %q, want exactly %q (once per kind)", got, want)
	}
	// A second request is a second read: its own lines.
	_, _ = h.x.Get(adminCtx(), "stale-1")
	if got := hidden(); len(got) != 3 {
		t.Fatalf("after a Get of a hidden run the log has %d lines, want 3", len(got))
	}
	// A registered kind, and a kernel-internal read, log nothing.
	before := len(hidden())
	importRuns(t, h, []tasks.RunRecord{doneRun("patrol-1", "gotham.patrol", tasks.StateSucceeded)})
	_, _ = h.x.Get(viewerCtx(), "patrol-1")
	_, _ = h.x.Get(context.Background(), "stale-1")
	if got := len(hidden()); got != before {
		t.Fatalf("a registered kind or an internal read logged %d new lines", got-before)
	}
}
