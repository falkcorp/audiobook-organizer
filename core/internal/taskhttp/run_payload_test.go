// file: core/internal/taskhttp/run_payload_test.go
// version: 0.1.0
// guid: 91c4e6a8-2b57-4d03-b6f1-3a8e0d7c5429
// last-edited: 2026-11-20

package taskhttp

import (
	"encoding/json"
	"reflect"
	"testing"
	"time"

	"github.com/falkcorp/mediacore/rbac"
	"github.com/falkcorp/mediacore/tasks"
)

// TestGetRun_SameJSONAsTheTopicPayload: for one RunView and one caller, the
// JSON GET /runs/{id} serves is the JSON core.task.run-changed.v1 delivers to
// that subscriber after the topic's own Redact, key for key, so the SPA reads a
// run one way over REST and over the stream. The view sets every field (the
// guard below fails when a new one is not), so whatever field the redaction
// rule covers, now or later, is compared on both paths. The one key that
// differs is blocked_by: GET /runs carries what Client.Get/Runs decided for the
// caller (they know the blocker's kind), and the stream, which cannot ask, never
// carries it, so the comparison drops it from REST after checking it is there.
func TestGetRun_SameJSONAsTheTopicPayload(t *testing.T) {
	for _, c := range []struct {
		who string
		as  []rbac.Permission
	}{{"without core:users.view", []rbac.Permission{scopeView}}, {"with core:users.view", []rbac.Permission{scopeView, scopeUsers}}} {
		t.Run(c.who, func(t *testing.T) {
			g := newRig(t)
			v := view("run-1", kindSweep, tasks.StateFailed, "alfred")
			v.Done, v.Total, v.Reason, v.Held = 2, 5, "fixed_token", true
			v.Parent, v.PlanRef, v.Actor.System = "run-0", "run-plan", "core.scheduler"
			v.Started, v.Finished = v.Created.Add(time.Minute), v.Created.Add(time.Hour)
			v.Deferred, v.BlockedBy, v.DeferredSince = "writeset", "run-blocker", v.Created.Add(30*time.Second)
			if z := zeroFields(reflect.ValueOf(tasks.NewRunPayload(v)), ""); len(z) != 0 {
				t.Fatalf("fixture: the run leaves %v zero, so the comparison would not cover them", z)
			}
			g.client.add(v)
			p := session("alfred", c.as...)

			rec := g.do(req{method: "GET", path: "/runs/run-1", as: p})
			if rec.Code != 200 {
				t.Fatalf("= %d\n%s", rec.Code, rec.Body.String())
			}
			var rest map[string]any
			decode(t, rec, &rest)

			payload, err := json.Marshal(tasks.NewRunPayload(v))
			if err != nil {
				t.Fatal(err)
			}
			var stream map[string]any
			if err := json.Unmarshal(tasks.RunChangedTopicSpec().Redact(p.Perms, payload), &stream); err != nil {
				t.Fatal(err)
			}
			if rest["blocked_by"] != "run-blocker" {
				t.Errorf("REST blocked_by = %v, want what the client set", rest["blocked_by"])
			}
			delete(rest, "blocked_by")
			if _, kept := stream["blocked_by"]; kept {
				t.Errorf("the stream carries blocked_by %v", stream["blocked_by"])
			}
			if !reflect.DeepEqual(rest, stream) {
				t.Errorf("REST and stream differ:\nREST   %v\nstream %v", rest, stream)
			}
			if _, kept := stream["current"]; kept != (len(c.as) == 2) {
				t.Errorf("current present = %v on the stream", kept)
			}
		})
	}
}

// zeroFields lists the fields of v (a struct) that hold their zero value,
// descending into nested structs other than time.Time, by dotted name.
func zeroFields(v reflect.Value, prefix string) []string {
	var out []string
	for i := range v.NumField() {
		f, name := v.Field(i), prefix+v.Type().Field(i).Name
		if f.Kind() == reflect.Struct && f.Type() != reflect.TypeFor[time.Time]() {
			out = append(out, zeroFields(f, name+".")...)
			continue
		}
		if f.IsZero() {
			out = append(out, name)
		}
	}
	return out
}
