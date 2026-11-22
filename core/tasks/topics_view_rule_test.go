// file: core/tasks/topics_view_rule_test.go
// version: 0.1.0
// guid: 7c3e91a4-2d58-4b06-8f1e-a4d9b60c5e27
// last-edited: 2026-11-22

package tasks_test

import (
	"context"
	"encoding/json"
	"reflect"
	"slices"
	"testing"

	"github.com/falkcorp/mediacore/events"
	"github.com/falkcorp/mediacore/rbac"
	"github.com/falkcorp/mediacore/tasks"
)

// viewer is a Checker that holds perms and satisfies exactly the requirements
// in sat.
type viewer struct {
	checker
	sat []rbac.Requirement
}

func (v viewer) Satisfies(r rbac.Requirement) bool {
	return slices.ContainsFunc(v.sat, func(s rbac.Requirement) bool { return reflect.DeepEqual(s, r) })
}

// TestViewRuleVisible: the Visible of a run topic follows the definition's View
// requirement, the default core:tasks.view for a kind with none (or none
// registered), core:admin always, and nothing for a nil checker or a payload
// that names no kind.
func TestViewRuleVisible(t *testing.T) {
	batcave := rbac.Need("batcave:view")
	view := func(k tasks.Kind) (rbac.Requirement, bool) {
		switch k {
		case "batcave.sweep":
			return batcave, true
		case "batcave.bare": // registered, View omitted
			return rbac.Requirement{}, true
		}
		return rbac.Requirement{}, false
	}
	visible := tasks.ViewRuleVisible(view)
	env := func(payload string) events.RawEnvelope { return events.RawEnvelope{Payload: json.RawMessage(payload)} }
	dick := viewer{checker{[]rbac.Permission{"batcave:view"}}, []rbac.Requirement{batcave}}
	auditor := viewer{checker: checker{[]rbac.Permission{"core:tasks.view"}}}
	bruce := viewer{checker: checker{[]rbac.Permission{rbac.CoreAdmin}}}

	for _, tc := range []struct {
		name    string
		who     rbac.Checker
		payload string
		want    bool
	}{
		{"holder of the definition's View", dick, `{"kind":"batcave.sweep"}`, true},
		{"core:tasks.view does not satisfy a definition that asks batcave:view", auditor, `{"kind":"batcave.sweep"}`, false},
		{"module scope does not satisfy the default", dick, `{"kind":"batcave.bare"}`, false},
		{"default View, holder of core:tasks.view", auditor, `{"kind":"batcave.bare"}`, true},
		{"unregistered kind takes the default", auditor, `{"kind":"batcave.gone"}`, true},
		{"unregistered kind, module scope only", dick, `{"kind":"batcave.gone"}`, false},
		{"core:admin sees every kind", bruce, `{"kind":"batcave.sweep"}`, true},
		{"nil checker", nil, `{"kind":"batcave.sweep"}`, false},
		{"no kind in the payload", dick, `{"id":"r1"}`, false},
		{"undecodable payload", dick, `not json`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := visible(context.Background(), tc.who, env(tc.payload)); got != tc.want {
				t.Errorf("visible = %v, want %v", got, tc.want)
			}
		})
	}
}

// TestWithViewRule: only the two run-carrying topics take the definition rule;
// log-appended keeps its admin-only default, any other entry is untouched, and
// the input slice is not written to.
func TestWithViewRule(t *testing.T) {
	in := []events.TopicSpec{tasks.RunChangedTopicSpec(), tasks.LogAppendedTopicSpec(), tasks.FinishedTopicSpec()}
	view := func(tasks.Kind) (rbac.Requirement, bool) { return rbac.Need("batcave:view"), true }
	dick := viewer{checker{[]rbac.Permission{"batcave:view"}}, []rbac.Requirement{rbac.Need("batcave:view")}}
	e := events.RawEnvelope{Payload: json.RawMessage(`{"kind":"batcave.sweep"}`)}

	out := tasks.WithViewRule(in, view)
	for _, s := range out {
		got := s.Visible(context.Background(), dick, e)
		if want := s.Name != tasks.LogAppendedTopicName; got != want {
			t.Errorf("%s: dick sees a batcave run = %v, want %v", s.Name, got, want)
		}
	}
	for _, s := range in {
		if s.Visible(context.Background(), dick, e) {
			t.Errorf("%s: WithViewRule wrote to its input", s.Name)
		}
	}
}
