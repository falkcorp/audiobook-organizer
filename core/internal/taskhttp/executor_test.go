// file: core/internal/taskhttp/executor_test.go
// version: 1.0.1
// guid: 0308922c-6326-415d-9f39-f9e663e1f391
// last-edited: 2026-11-11

package taskhttp

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/cockroachdb/pebble/v2/vfs"

	"github.com/falkcorp/mediacore/auth"
	"github.com/falkcorp/mediacore/internal/executor"
	"github.com/falkcorp/mediacore/internal/taskstore"
	"github.com/falkcorp/mediacore/rbac"
	"github.com/falkcorp/mediacore/storage"
	"github.com/falkcorp/mediacore/tasks"
)

// These cells run the routes over the REAL executor (the fake client proves
// the HTTP mapping; this proves the two halves meet): the store is opened
// with storage.Open/Prepare/Migrate over MemFS, never coretest.NewStore (core/tasks
// is in the kernel's import graph).

const execStorePath = "/batcave/tasks.pebble"

func bootStores(t *testing.T) (tasks.Stores, storage.Keyspace) {
	t.Helper()
	ctx := context.Background()
	o, err := storage.Open(ctx, storage.OpenOptions{
		Path:            execStorePath,
		Owners:          []storage.OwnerSpec{{Owner: taskstore.Owner, Spec: taskstore.StorageSpec(), Enabled: true}},
		Build:           "taskhttp+fixture",
		FreeSpaceMargin: 1,
		FS:              storage.FSOf(vfs.NewMem()),
	})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	db, err := o.Prepare(ctx)
	if err == nil {
		err = db.Migrate(ctx)
	}
	if err != nil {
		_ = o.Close()
		t.Fatalf("boot: %v", err)
	}
	t.Cleanup(func() {
		if err := db.Close(); err != nil {
			t.Errorf("close: %v", err)
		}
	})
	ks, ok := db.Keyspace(taskstore.Owner)
	if !ok {
		t.Fatalf("owner %s has no keyspace", taskstore.Owner)
	}
	return tasks.NativeStores(ks), ks
}

// nightlyDef declares core.window.nightly as scheduler.WindowDefinition does:
// started with core:tasks.manage, run at system priority.
func nightlyDef() tasks.Definition {
	return tasks.Task(kindNightly, tasks.TaskSpec[struct{}, struct{}]{
		Title: "Nightly Batcave window", Effects: tasks.ReadOnly(tasks.Res("core", "window")),
		Perms: tasks.Permissions{Start: rbac.Need(scopeManage)}, Priority: tasks.PrioritySystem, Resume: tasks.ResumeContinue, Run: func(*tasks.RC, struct{}) (struct{}, error) { return struct{}{}, nil }})
}

// realRig is a Server over a real executor.
func realRig(t *testing.T) *rig {
	t.Helper()
	return realRigWith(t, append(defs(), nightlyDef()))
}

// realRigWith is realRig over the given definitions, with extra scopes the
// fixtures declare in the catalog.
func realRigWith(t *testing.T, all []tasks.Definition, extra ...rbac.Permission) *rig {
	t.Helper()
	stores, ks := bootStores(t)
	specs := rbac.CoreScopes()
	for _, p := range append([]rbac.Permission{scopeRun, scopeSee, scopeApprove}, extra...) {
		specs = append(specs, rbac.PermissionSpec{Name: p, Title: string(p), Description: "Fixture scope " + string(p)})
	}
	cat, err := rbac.NewCatalog(specs...)
	if err != nil {
		t.Fatal(err)
	}
	x, err := executor.New(executor.Options{
		Stores: stores, Keyspace: &ks, Definitions: all, Catalog: cat, InstrumentsOff: true,
		Principal:     Principal,
		WindowMembers: func(w string) []tasks.Kind { return map[string][]tasks.Kind{"nightly": {kindSweep}}[w] },
		Config:        executor.Config{DispatchTick: 20 * time.Millisecond, WatchdogInterval: time.Hour},
		Logger:        slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	if err != nil {
		t.Fatalf("executor.New: %v", err)
	}
	t.Cleanup(func() { _ = x.Shutdown(context.Background()) })
	if err := x.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}
	g := newRig(t, func(d *Deps) { d.Client, d.Approver, d.Runs, d.Definitions = x, x, stores.Runs, all })
	return g
}

// TestExecutor_WindowStartThroughHTTP: a caller with core:tasks.manage alone
// cannot start the window (it would run its members as their own modules'
// system actors); one that also holds every member's start scope can.
func TestExecutor_WindowStartThroughHTTP(t *testing.T) {
	g := realRig(t)
	start := func(as ...rbac.Permission) *httptest.ResponseRecorder {
		return g.do(req{method: "POST", path: "/definitions/" + string(kindNightly) + "/runs", as: session("lucius", as...)})
	}
	// The window guard is the executor's: it answers the route's 403 for a
	// caller the route's own pre-check let through.
	wantProblem(t, start(scopeManage), 403, "tasks.actor_not_permitted")
	// Short of manage the route's own pre-check refuses first.
	wantProblem(t, start(scopeRun), 403, "permission_denied")
	if rec := start(scopeManage, scopeRun); rec.Code != http.StatusAccepted {
		t.Fatalf("manage and the member's scope = %d, want 202\n%s", rec.Code, rec.Body.String())
	}
}

// TestExecutor_IdempotencyThroughHTTP: a repeated key replays the one run, and
// concurrent starts with one key insert one.
func TestExecutor_IdempotencyThroughHTTP(t *testing.T) {
	g := realRig(t)
	do := func(key string) (int, string, string) {
		rec := g.do(startReq(`{"mode": "live"}`, key))
		var v runDTO
		decodeAsync(t, rec, &v)
		return rec.Code, v.ID, rec.Header().Get("Idempotent-Replayed")
	}
	c1, id1, h1 := do("scarecrow-1939")
	c2, id2, h2 := do("scarecrow-1939")
	if c1 != http.StatusAccepted || h1 != "" || c2 != http.StatusOK || h2 != "true" || id1 == "" || id1 != id2 {
		t.Fatalf("first %d %q %q, replay %d %q %q", c1, id1, h1, c2, id2, h2)
	}
	// A second executor: a start also folds into any live run of the same kind
	// (the dedupe), so the concurrent cell begins with none.
	g = realRig(t)
	const n = 16
	var wg sync.WaitGroup
	codes, ids := make([]int, n), make([]string, n)
	for i := range n {
		wg.Go(func() {
			codes[i], ids[i], _ = do("penguin-umbrella-7")
		})
	}
	wg.Wait()
	fresh := 0
	for i := range n {
		if codes[i] == http.StatusAccepted {
			fresh++
		} else if codes[i] != http.StatusOK {
			t.Fatalf("request %d = %d", i, codes[i])
		}
		if ids[i] != ids[0] {
			t.Fatalf("request %d named %s, request 0 named %s", i, ids[i], ids[0])
		}
	}
	if fresh != 1 {
		t.Fatalf("fresh starts = %d, want exactly 1", fresh)
	}
}

// TestExecutor_ViewGateThroughHTTP: the executor's per-definition View rule
// reaches the route: a caller who may not view the run's definition gets 404
// for the run, never 403, and a viewer sees it with Current redacted.
func TestExecutor_ViewGateThroughHTTP(t *testing.T) {
	g := realRig(t)
	rec := g.do(req{method: "POST", path: "/definitions/" + string(kindScan) + "/runs", as: session("lucius", scopeRun, scopeSee)})
	if rec.Code != http.StatusAccepted {
		t.Fatalf("start = %d\n%s", rec.Code, rec.Body.String())
	}
	var v runDTO
	decode(t, rec, &v)
	get := func(as ...rbac.Permission) int {
		return g.do(req{method: "GET", path: "/runs/" + v.ID, as: session("robin", as...)}).Code
	}
	if got := get(scopeRun); got != http.StatusNotFound {
		t.Fatalf("a caller who may not view = %d, want 404", got)
	}
	if got := get(scopeSee); got != http.StatusOK {
		t.Fatalf("a viewer = %d, want 200", got)
	}
}

// TestExecutor_CancelSkipsAreAuditedThroughHTTP (F5-18 C6, the cancel authority
// ruling): lucius may cancel the patrol and the yard it enqueued but not the
// vault (its Perms.Cancel is batcave:vault and lucius did not start it). The
// route answers 200, the vault keeps running, and the core.task.cancel record
// names the skipped run. A holder of the vault's scope skips nothing, and its
// record has no skipped list.
func TestExecutor_CancelSkipsAreAuditedThroughHTTP(t *testing.T) {
	const (
		scopeVault   = rbac.Permission("batcave:vault")
		scopeViewAll = rbac.Permission("core:tasks.view")
	)
	started := make(chan tasks.RunID, 4)
	leaf := func(kind tasks.Kind, edit func(*tasks.Permissions)) tasks.Definition {
		perms := tasks.Permissions{Start: rbac.Need(scopeRun)}
		if edit != nil {
			edit(&perms)
		}
		return tasks.Task(kind, tasks.TaskSpec[struct{}, struct{}]{
			Title: "Hold the " + string(kind), Effects: tasks.Writes(tasks.Res("batcave", string(kind))),
			Perms: perms, Resume: tasks.ResumeContinue, Cancel: tasks.Cooperative(time.Second),
			Run: func(rc *tasks.RC, _ struct{}) (struct{}, error) {
				started <- rc.RunID()
				<-rc.Context().Done()
				return struct{}{}, context.Cause(rc.Context())
			}})
	}
	patrol := tasks.Task("batcave.patrol", tasks.TaskSpec[struct{}, struct{}]{
		Title: "Patrol the Batcave", Effects: tasks.Writes(tasks.Res("batcave", "patrol")),
		Perms: tasks.Permissions{Start: rbac.Need(scopeRun)}, Resume: tasks.ResumeContinue, Cancel: tasks.Cooperative(time.Second),
		Run: func(rc *tasks.RC, _ struct{}) (struct{}, error) {
			for _, k := range []tasks.Kind{"batcave.yard", "batcave.vault"} {
				if _, err := rc.Enqueue(k, struct{}{}); err != nil {
					return struct{}{}, err
				}
			}
			started <- rc.RunID()
			<-rc.Context().Done()
			return struct{}{}, context.Cause(rc.Context())
		}})
	all := []tasks.Definition{patrol, leaf("batcave.yard", nil),
		leaf("batcave.vault", func(p *tasks.Permissions) { p.Cancel = rbac.Need(scopeVault) })}
	run := func(t *testing.T, by *auth.Principal) (g *rig, patrolID, vaultID string, rec *httptest.ResponseRecorder) {
		g = realRigWith(t, all, scopeVault)
		rs := g.do(req{method: "POST", path: "/definitions/batcave.patrol/runs", body: `{"mode": "live"}`, as: session("bruce", scopeRun, scopeSee)})
		var pv runDTO
		decodeAsync(t, rs, &pv)
		for range 3 {
			<-started
		}
		var kids struct {
			Runs []runDTO `json:"runs"`
		}
		lr := g.do(req{method: "GET", path: "/runs?parent=" + pv.ID, as: session("bruce", scopeRun, scopeSee, scopeViewAll)})
		decode(t, lr, &kids)
		for _, k := range kids.Runs {
			if k.Kind == "batcave.vault" {
				vaultID = k.ID
			}
		}
		if vaultID == "" {
			t.Fatalf("no vault among the patrol's children: %+v", kids.Runs)
		}
		return g, pv.ID, vaultID, g.do(req{method: "POST", path: "/runs/" + pv.ID + "/cancel", as: by})
	}
	t.Run("a caller who may not cancel the vault", func(t *testing.T) {
		g, patrolID, vaultID, rec := run(t, session("lucius", scopeRun, scopeSee, scopeViewAll))
		if rec.Code != http.StatusOK {
			t.Fatalf("cancel = %d\n%s", rec.Code, rec.Body.String())
		}
		if len(g.aud.recorded) != 1 {
			t.Fatalf("recorded = %+v", g.aud.recorded)
		}
		e := g.aud.recorded[0]
		skipped, _ := e.Details["skipped"].([]string)
		if e.Action != "core.task.cancel" || string(e.Target.ID) != patrolID || len(skipped) != 1 || skipped[0] != vaultID {
			t.Fatalf("record = %+v, want core.task.cancel of %s with skipped [%s]", e, patrolID, vaultID)
		}
		var v runDTO
		decode(t, g.do(req{method: "GET", path: "/runs/" + vaultID, as: session("lucius", scopeRun, scopeSee, scopeViewAll)}), &v)
		if v.State != string(tasks.StateRunning) {
			t.Fatalf("the vault is %s, want running", v.State)
		}
	})
	t.Run("a caller who may", func(t *testing.T) {
		g, _, _, rec := run(t, session("lucius", scopeRun, scopeSee, scopeVault, scopeViewAll))
		if rec.Code != http.StatusOK {
			t.Fatalf("cancel = %d\n%s", rec.Code, rec.Body.String())
		}
		if len(g.aud.recorded) != 1 {
			t.Fatalf("recorded = %+v", g.aud.recorded)
		}
		if _, has := g.aud.recorded[0].Details["skipped"]; has {
			t.Fatalf("a cancel that skipped nothing lists skipped runs: %+v", g.aud.recorded[0].Details)
		}
	})
}
