// file: core/internal/executor/log_redaction_test.go
// version: 0.1.0
// guid: 8539fd40-9c26-4f85-b34d-8060e1349227
// last-edited: 2026-11-22

package executor

import (
	"context"
	"testing"

	"github.com/falkcorp/mediacore/tasks"
)

// TestClientLog_RedactsForTheCaller (05-tasks 5T.3.10, #4814 r1 m1): Client.Log itself
// applies RedactRunView's rule to each line, so a caller of the client, not only
// GET /runs/{id}/log, cannot read a line that names a user without core:users.view.
// A call with no resolved identity is in-process and sees the stored line, as mayView
// treats it (the nil-checker difference from tasks.RedactLogAppended); so does an
// executor built without a Principal.
func TestClientLog_RedactsForTheCaller(t *testing.T) {
	const secret = "robin was seen at the cave"
	body := func(rc *tasks.RC, _ noParams) (string, error) {
		rc.Progress().Note(secret)
		return "logged", nil
	}
	for _, c := range []struct {
		name        string
		ctx         context.Context
		noPrincipal bool
		want        string
	}{
		{name: "viewer without users.view", ctx: asUser("barbara", scopeTasksView), want: ""},
		{name: "viewer with users.view", ctx: asUser("lucius", scopeTasksView, "core:users.view"), want: secret},
		{name: "no identity in the context", ctx: context.Background(), want: secret},
		{name: "executor without a Principal", ctx: asUser("barbara", scopeTasksView), noPrincipal: true, want: secret},
	} {
		t.Run(c.name, func(t *testing.T) {
			edit := withPrincipal
			if c.noPrincipal {
				edit = nil
			}
			h := newHarness(t, []tasks.Definition{taskDef("gotham.signal", body, readOnly("gotham.signal"))}, edit)
			id := h.start("gotham.signal", tasks.StartRequest{})
			h.waitState(id, tasks.StateSucceeded)
			var seen []tasks.LogLine
			for l, err := range h.x.Log(c.ctx, id, tasks.PageQuery{}) {
				if err != nil {
					t.Fatal(err)
				}
				if l.Message == secret || l.Message == "" {
					seen = append(seen, l)
				}
			}
			if len(seen) == 0 {
				t.Fatal("the run's note is not in its log")
			}
			for _, l := range seen {
				if l.Message != c.want {
					t.Errorf("line %q, want %q", l.Message, c.want)
				}
				if l.Level == "" || l.At.IsZero() {
					t.Errorf("line %+v lost its level or time, which survive redaction", l)
				}
			}
		})
	}
}
