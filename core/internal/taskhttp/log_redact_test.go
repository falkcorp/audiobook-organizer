// file: core/internal/taskhttp/log_redact_test.go
// version: 0.1.0
// guid: 0e5b7a93-6d42-4c1f-9a38-b7c2d4e61f85
// last-edited: 2026-11-20

package taskhttp

import (
	"testing"

	"github.com/falkcorp/mediacore/tasks"
)

// TestGetLog_RedactsLikeTheStream: GET /runs/{id}/log passes each line through
// tasks.RedactLogAppended, the Redact of core.task.log-appended.v1, so a caller
// without core:users.view gets no Message and no Fields values while a holder
// of it gets the line whole.
func TestGetLog_RedactsLikeTheStream(t *testing.T) {
	for _, c := range []struct {
		who  string
		as   string
		full bool
	}{{"without core:users.view", "plain", false}, {"with core:users.view", "named", true}} {
		t.Run(c.who, func(t *testing.T) {
			g := newRig(t)
			g.client.add(view("run-1", kindSweep, tasks.StateRunning, "bruce"))
			g.client.logLines = []tasks.LogLine{{Level: "warn", Message: "Selina Kyle signed in", Fields: map[string]string{"user": "selina"}}}
			as := session("alfred", scopeView)
			if c.full {
				as = session("alfred", scopeView, scopeUsers)
			}
			rec := g.do(req{method: "GET", path: "/runs/run-1/log", as: as})
			if rec.Code != 200 {
				t.Fatalf("= %d\n%s", rec.Code, rec.Body.String())
			}
			var body struct{ Lines []tasks.LogAppended }
			decode(t, rec, &body)
			if len(body.Lines) != 1 {
				t.Fatalf("lines = %+v", body.Lines)
			}
			l := body.Lines[0]
			if l.RunID != "run-1" {
				t.Errorf("line names run %q, want the path's run-1 (redacted or not)", l.RunID)
			}
			if c.full && (l.Message != "Selina Kyle signed in" || l.Fields["user"] != "selina") {
				t.Errorf("a holder of core:users.view saw %+v", l)
			}
			if !c.full && (l.Message != "" || l.Fields["user"] != "" || l.Level != "warn") {
				t.Errorf("a caller without core:users.view saw %+v", l)
			}
			if _, ok := l.Fields["user"]; !ok {
				t.Errorf("the Fields key was dropped by redaction: %+v", l)
			}
		})
	}
}
