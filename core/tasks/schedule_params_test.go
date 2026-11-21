// file: core/tasks/schedule_params_test.go
// version: 0.1.0
// guid: 8d1f4c62-3b7e-4a95-b0d8-5e2c9a17f364
// last-edited: 2026-11-21

package tasks_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/falkcorp/mediacore/tasks"
)

// TestSchedule_ParamsFunc (F5-14): ParamsFunc builds the params at each
// ResolveParams call, where Params encodes them once; RunParams stays the
// static copy, nil under ParamsFunc.
func TestSchedule_ParamsFunc(t *testing.T) {
	limit := 3
	base := tasks.ScheduleFromConfig("modules.gotham.cave.schedules.lookup")
	s := base.ParamsFunc(func() any { return map[string]any{"limit": limit} })
	if !s.HasParamsFunc() || base.HasParamsFunc() {
		t.Fatal("HasParamsFunc: the copy must report it and the receiver must not")
	}
	if s.RunParams() != nil {
		t.Fatalf("RunParams = %s under ParamsFunc, want nil", s.RunParams())
	}
	for _, want := range []string{`{"limit":3}`, `{"limit":11}`} {
		got, err := s.ResolveParams()
		if err != nil || string(got) != want {
			t.Fatalf("ResolveParams = %s, %v; want %s", got, err, want)
		}
		limit = 11
	}
	static := base.Params(map[string]int{"limit": 5})
	if got, err := static.ResolveParams(); err != nil || string(got) != `{"limit":5}` {
		t.Fatalf("static ResolveParams = %s, %v", got, err)
	}
	if got, err := base.ResolveParams(); err != nil || got != nil {
		t.Fatalf("no params: ResolveParams = %s, %v; want nil", got, err)
	}
	bad := base.ParamsFunc(func() any { return func() {} })
	if got, err := bad.ResolveParams(); !errors.Is(err, tasks.ErrScheduleParams) || got != nil {
		t.Fatalf("an unencodable value: ResolveParams = %s, %v; want ErrScheduleParams", got, err)
	}
}

// TestSchedule_ParamsFuncProblems: a nil func, and ParamsFunc beside Params,
// are constructor problems of the definition that carries the schedule.
func TestSchedule_ParamsFuncProblems(t *testing.T) {
	task := func(s *tasks.Schedule) string {
		c := validCommon()
		c.Schedule = s
		return problemText(tasks.Task("gotham.cases.review", tasks.TaskSpec[struct{}, struct{}]{
			Common: c, Run: func(*tasks.RC, struct{}) (struct{}, error) { return struct{}{}, nil },
		}))
	}
	base := tasks.ScheduleFromConfig("modules.gotham.cave.schedules.lookup")
	fn := func() any { return struct{}{} }
	if p := task(base.ParamsFunc(nil)); !strings.Contains(p, "ParamsFunc: nil func") {
		t.Fatalf("ParamsFunc(nil) accepted: %q", p)
	}
	if p := task(base.Params(struct{}{}).ParamsFunc(fn)); !strings.Contains(p, "both Params and ParamsFunc") {
		t.Fatalf("Params beside ParamsFunc accepted: %q", p)
	}
	if p := task(base.ParamsFunc(fn)); p != "" {
		t.Fatalf("ParamsFunc alone: %q", p)
	}
}
