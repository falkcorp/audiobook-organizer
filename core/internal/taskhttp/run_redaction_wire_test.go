// file: core/internal/taskhttp/run_redaction_wire_test.go
// version: 0.1.0
// guid: 2f8a6c1d-9b34-4e75-a0d8-5c3e7b12f946
// last-edited: 2026-11-22

package taskhttp

import (
	"encoding/json"
	"reflect"
	"slices"
	"testing"
	"time"

	"github.com/falkcorp/mediacore/rbac"
	"github.com/falkcorp/mediacore/tasks"
)

// wireRunKeys are the top-level JSON keys of a run on the wire, spelled out as
// literals: the SPA's coreEvents client reads exactly these (web/src/services/
// coreEvents/types.ts), so a rename of a Go tag must fail here and not only in
// a client that nothing type-checks against the server. blocked_by is the one
// key the stream never carries (the stream cannot ask for the blocker's kind).
var wireRunKeys = []string{
	"actor", "created", "deferred", "deferred_since", "done", "finished", "held", "id", "kind", "mode",
	"parent", "plan_ref", "reason", "started", "state", "total", "trigger",
}

// wireLogKeys are the keys of one log line on the wire.
var wireLogKeys = []string{"at", "fields", "level", "message", "run_id"}

func keysOf(m map[string]any) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	slices.Sort(out)
	return out
}

// TestRunRedaction_SameOnStreamAndREST: for each caller, the run GET /runs/{id}
// serves and the run core.task.run-changed.v1 delivers carry the same literal
// wire keys, the same values, and the same blanking: current is present only
// for a holder of core:users.view, on both paths. The keys are pinned as
// literals (wireRunKeys), not read back from the struct, so a renamed or
// dropped tag fails here. The log line gets the same treatment against
// core.task.log-appended.v1.
func TestRunRedaction_SameOnStreamAndREST(t *testing.T) {
	for _, c := range []struct {
		who  string
		as   []rbac.Permission
		full bool
	}{
		{"without core:users.view", []rbac.Permission{scopeView}, false},
		{"with core:users.view", []rbac.Permission{scopeView, scopeUsers}, true},
	} {
		t.Run(c.who, func(t *testing.T) {
			g := newRig(t)
			v := view("run-1", kindSweep, tasks.StateFailed, "alfred")
			v.Done, v.Total, v.Reason, v.Held = 2, 5, "fixed_token", true
			v.Parent, v.PlanRef, v.Actor.System = "run-0", "run-plan", "core.scheduler"
			v.Started, v.Finished = v.Created.Add(time.Minute), v.Created.Add(time.Hour)
			v.Deferred, v.DeferredSince = "writeset", v.Created.Add(30*time.Second)
			v.Current = "Selina Kyle's catalog"
			g.client.add(v)
			g.client.logLines = []tasks.LogLine{{Level: "warn", Message: "Selina Kyle signed in", Fields: map[string]string{"user": "selina"}}}
			p := session("alfred", c.as...)

			// The run.
			rec := g.do(req{method: "GET", path: "/runs/run-1", as: p})
			if rec.Code != 200 {
				t.Fatalf("GET /runs/run-1 = %d\n%s", rec.Code, rec.Body.String())
			}
			var rest map[string]any
			decode(t, rec, &rest)
			raw, err := json.Marshal(tasks.NewRunPayload(v))
			if err != nil {
				t.Fatal(err)
			}
			var stream map[string]any
			if err := json.Unmarshal(tasks.RunChangedTopicSpec().Redact(p.Perms, raw), &stream); err != nil {
				t.Fatal(err)
			}
			delete(rest, "blocked_by") // REST only: see wireRunKeys
			want := slices.Clone(wireRunKeys)
			if c.full {
				want = append(want, "current", "message")
				slices.Sort(want)
			}
			for name, got := range map[string]map[string]any{"REST": rest, "stream": stream} {
				if k := keysOf(got); !slices.Equal(k, want) {
					t.Errorf("%s run keys = %v, want %v", name, k, want)
				}
			}
			if !reflect.DeepEqual(rest, stream) {
				t.Errorf("REST and stream differ:\nREST   %v\nstream %v", rest, stream)
			}

			// The log line.
			rec = g.do(req{method: "GET", path: "/runs/run-1/log", as: p})
			if rec.Code != 200 {
				t.Fatalf("GET /runs/run-1/log = %d\n%s", rec.Code, rec.Body.String())
			}
			var body struct{ Lines []map[string]any }
			decode(t, rec, &body)
			if len(body.Lines) != 1 {
				t.Fatalf("log lines = %v", body.Lines)
			}
			lraw, err := json.Marshal(tasks.NewLogAppended("run-1", g.client.logLines[0]))
			if err != nil {
				t.Fatal(err)
			}
			var lstream map[string]any
			if err := json.Unmarshal(tasks.LogAppendedTopicSpec().Redact(p.Perms, lraw), &lstream); err != nil {
				t.Fatal(err)
			}
			for name, got := range map[string]map[string]any{"REST": body.Lines[0], "stream": lstream} {
				if k := keysOf(got); !slices.Equal(k, wireLogKeys) {
					t.Errorf("%s log keys = %v, want %v", name, k, wireLogKeys)
				}
			}
			if !reflect.DeepEqual(body.Lines[0], lstream) {
				t.Errorf("REST and stream log lines differ:\nREST   %v\nstream %v", body.Lines[0], lstream)
			}
			if got := lstream["message"]; (got != "") != c.full {
				t.Errorf("stream message = %v, full = %v", got, c.full)
			}
		})
	}
}
