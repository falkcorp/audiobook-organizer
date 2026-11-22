// file: core/realtime/hub_taskview_test.go
// version: 0.1.0
// guid: 5b0d7e32-8c41-4f6a-9d17-3e2a6c8b4f01
// last-edited: 2026-11-22

package realtime_test

import (
	"slices"
	"testing"
	"time"

	"github.com/falkcorp/mediacore/events"
	"github.com/falkcorp/mediacore/rbac"
	"github.com/falkcorp/mediacore/tasks"
)

// taskViews is the definitions' View requirements of the cell below: a Batcave
// kind that asks batcave:view, an Arkham kind that asks arkham:view; any other
// kind is not registered and takes core:tasks.view.
func taskViews(kind tasks.Kind) (rbac.Requirement, bool) {
	switch kind {
	case "batcave.sweep":
		return rbac.Need(scopeView), true
	case "arkham.audit":
		return rbac.Need(scopeArk), true
	}
	return rbac.Requirement{}, false
}

// TestHub_TaskTopicVisibleWithoutTasksView (05-realtime §11, COR-20): over the
// real core.task.run-changed.v1 spec with the definition rule installed
// (tasks.WithViewRule, as the kernel declares it), a user holding only a
// module's scope, no core:tasks.view, receives the runs of the kinds whose View
// they hold and none of the others: not an Arkham kind, not a kind nobody
// registered. The admin sees all three. Every frame still goes through the
// topic's Redact: Current survives only for the holder of core:users.view.
func TestHub_TaskTopicVisibleWithoutTasksView(t *testing.T) {
	c := newCast(t, hubOptions{specs: func(core, _, _ *[]events.TopicSpec) {
		*core = slices.DeleteFunc(*core, func(s events.TopicSpec) bool { return s.Name == tasks.RunChangedTopicName })
		*core = append(*core, tasks.WithViewRule([]events.TopicSpec{tasks.RunChangedTopicSpec()}, taskViews)...)
	}})
	run := func(id, kind string) {
		v := runView("Selina Kyle's cell")
		v.ID, v.Kind = tasks.RunID(id), tasks.Kind(kind)
		c.runPayload(v)
	}
	for _, tc := range []struct {
		who     string
		want    []string // run ids received, in order
		current bool     // holds core:users.view, so Current survives Redact
	}{
		{"dick", []string{"r-sweep"}, false},
		{"barbara", []string{"r-sweep", "r-audit"}, false},
		{"bruce", []string{"r-sweep", "r-audit", "r-orphan"}, true},
	} {
		t.Run(tc.who, func(t *testing.T) {
			rd := c.open(t, tc.who, "/events?topic="+tasks.RunChangedTopicName)
			rd.expectOpened()
			run("r-sweep", "batcave.sweep")
			run("r-audit", "arkham.audit")
			run("r-orphan", "batcave.orphan")
			var got []string
			for range tc.want {
				p := streamRun(t, rd)
				got = append(got, p.ID)
				if (p.Current != "") != tc.current {
					t.Errorf("%s saw Current %q, want kept = %v", tc.who, p.Current, tc.current)
				}
			}
			if !slices.Equal(got, tc.want) {
				t.Errorf("%s received runs %v, want %v", tc.who, got, tc.want)
			}
			rd.quiet(100 * time.Millisecond)
		})
	}
}
