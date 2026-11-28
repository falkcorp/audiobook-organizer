// file: core/internal/taskhttp/reach_test.go
// version: 1.0.0
// guid: 5c1d7a93-e2b4-4f68-a0d9-3b8e6f2c1a47
// last-edited: 2026-11-28

package taskhttp

import (
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/falkcorp/mediacore/internal/executor"
	"github.com/falkcorp/mediacore/internal/taskstore"
	"github.com/falkcorp/mediacore/rbac"
	"github.com/falkcorp/mediacore/storage"
	"github.com/falkcorp/mediacore/tasks"
)

// F5-18 reach R2 and R3 over the HTTP surface and a real executor: runs a
// caller may not view are missing from the census, and the runs of a kind
// this build no longer registers follow the View rule their snapshot
// recorded, on every read route.

const kindRetired = tasks.Kind("batcave.retired") // stored runs, no live definition

func importedRun(id string, kind tasks.Kind) tasks.RunRecord {
	fin := time.Date(2026, 11, 20, 3, 0, 0, 0, time.UTC)
	r := tasks.RunRecord{Priority: tasks.PriorityNormal}
	r.ID, r.Kind, r.State, r.Mode = tasks.RunID(id), kind, tasks.StateSucceeded, tasks.ModeLive
	r.Created, r.Started, r.Finished = fin.Add(-time.Hour), fin.Add(-time.Hour), fin
	r.Actor = tasks.Actor{UserID: "bruce.wayne", AuthMethod: "session"}
	return r
}

// seedRetired imports one run of each kind in the table and, when snap is
// non-nil, the snapshot of the retired kind.
func seedRetired(snap tasks.Definition, kinds ...tasks.Kind) func(*testing.T, storage.Keyspace) {
	return func(t *testing.T, ks storage.Keyspace) {
		t.Helper()
		imp := tasks.NativeImporter(ks)
		b := ks.NewIndexedBatch()
		for _, k := range kinds {
			if err := imp.Run(t.Context(), b, importedRun("run-"+string(k), k)); err != nil {
				t.Fatal(err)
			}
		}
		if err := b.Commit(t.Context(), storage.Sync); err != nil {
			t.Fatal(err)
		}
		if snap == nil {
			return
		}
		raw, err := executor.EncodeDefSnapshot(snap)
		if err != nil {
			t.Fatal(err)
		}
		st := taskstore.New(ks)
		sb := st.NewBatch()
		if err := st.Set(sb, taskstore.DefKey(string(kindRetired)), raw); err != nil {
			t.Fatal(err)
		}
		if err := sb.Commit(t.Context(), storage.Sync); err != nil {
			t.Fatal(err)
		}
	}
}

func retiredDef(view rbac.Requirement) tasks.Definition {
	return tasks.Task(kindRetired, tasks.TaskSpec[struct{}, struct{}]{
		Title: "Retired gadget", Effects: tasks.ReadOnly(tasks.Res("batcave", "gadget")),
		Perms:  tasks.Permissions{Start: rbac.Need(scopeRun), View: view},
		Resume: tasks.ResumeContinue, Run: func(*tasks.RC, struct{}) (struct{}, error) { return struct{}{}, nil }})
}

// TestRetiredKind_FollowsItsSnapshotOnEveryReadRoute (R3): the run of a kind
// with no live definition is 404 for a viewer on every read route when its
// snapshot recorded a View, 200 when the snapshot recorded none, and 404 for
// everyone when there is no snapshot. Killed by removing the snapshot
// consult and by treating a missing snapshot as open.
func TestRetiredKind_FollowsItsSnapshotOnEveryReadRoute(t *testing.T) {
	routes := []string{"", "/log", "/items", "/rows"}
	cells := []struct {
		name         string
		snap         tasks.Definition
		viewer, full int // status of GET /runs/{id} for a viewer and for an admin
	}{
		{"snapshot records a View", retiredDef(rbac.Need(scopeSee)), http.StatusNotFound, http.StatusOK},
		{"snapshot records no View", retiredDef(rbac.Requirement{}), http.StatusOK, http.StatusOK},
		{"no snapshot", nil, http.StatusNotFound, http.StatusNotFound},
	}
	for _, c := range cells {
		t.Run(c.name, func(t *testing.T) {
			g := realRigSeeded(t, seedRetired(c.snap, kindRetired))
			viewer := session("robin", scopeView)
			admin := session("alfred", scopeView, scopeSee, scopeManage)
			for _, r := range routes {
				path := "/runs/run-" + string(kindRetired) + r
				if got := g.do(req{method: "GET", path: path, as: viewer}).Code; (got == http.StatusOK) != (c.viewer == http.StatusOK) {
					t.Errorf("viewer GET %s = %d, want %d", path, got, c.viewer)
				}
				if got := g.do(req{method: "GET", path: path, as: admin}).Code; (got == http.StatusOK) != (c.full == http.StatusOK) {
					t.Errorf("admin GET %s = %d, want %d", path, got, c.full)
				}
			}
			want := int64(0)
			if c.viewer == http.StatusOK {
				want = 1
			}
			for _, path := range []string{"/runs", "/timeline"} {
				rec := g.do(req{method: "GET", path: path, as: viewer})
				if rec.Code != http.StatusOK {
					t.Fatalf("GET %s = %d", path, rec.Code)
				}
				if has := strings.Contains(rec.Body.String(), "run-"+string(kindRetired)); has != (want == 1) {
					t.Errorf("viewer GET %s lists the retired run = %v, want %v", path, has, want == 1)
				}
			}
		})
	}
}
