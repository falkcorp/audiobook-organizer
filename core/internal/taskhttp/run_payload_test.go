// file: core/internal/taskhttp/run_payload_test.go
// version: 0.1.0
// guid: 91c4e6a8-2b57-4d03-b6f1-3a8e0d7c5429
// last-edited: 2026-11-20

package taskhttp

import (
	"encoding/json"
	"reflect"
	"testing"

	"github.com/falkcorp/mediacore/rbac"
	"github.com/falkcorp/mediacore/tasks"
)

// TestGetRun_SameJSONAsTheTopicPayload: for one RunView and one caller, the
// JSON GET /runs/{id} serves is the JSON core.task.run-changed.v1 delivers to
// that subscriber after the topic's own Redact, key for key, so the SPA reads a
// run one way over REST and over the stream.
func TestGetRun_SameJSONAsTheTopicPayload(t *testing.T) {
	for _, c := range []struct {
		who string
		as  []rbac.Permission
	}{{"without core:users.view", []rbac.Permission{scopeView}}, {"with core:users.view", []rbac.Permission{scopeView, scopeUsers}}} {
		t.Run(c.who, func(t *testing.T) {
			g := newRig(t)
			v := view("run-1", kindSweep, tasks.StateRunning, "alfred")
			v.Done, v.Total, v.Reason = 2, 5, "fixed_token"
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
			if !reflect.DeepEqual(rest, stream) {
				t.Errorf("REST and stream differ:\nREST   %v\nstream %v", rest, stream)
			}
			if _, kept := stream["current"]; kept != (len(c.as) == 2) {
				t.Errorf("current present = %v on the stream", kept)
			}
		})
	}
}
