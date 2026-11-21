// file: core/tasks/schedule_gates_test.go
// version: 0.1.0
// guid: 06290529-d62e-49d1-b30c-268819666c00
// last-edited: 2026-11-21

package tasks_test

import (
	"strings"
	"testing"

	"github.com/falkcorp/mediacore/tasks"
)

// TestSchedule_TriggerGates (F5-14): OnStartWhen gives the start trigger a
// gate of its own, independent of When; WindowWhen closes window membership
// only. The table is each accessor's answer for each combination of gates.
func TestSchedule_TriggerGates(t *testing.T) {
	const key = "modules.gotham.cave.schedules.scan"
	yes := func() bool { return true }
	no := func() bool { return false }
	base := tasks.ScheduleFromConfig(key)
	for _, tc := range []struct {
		name                                    string
		s                                       *tasks.Schedule
		onStart, startGate, open, start, window bool
	}{
		{"no gates", base, false, false, true, true, true},
		{"OnStart", base.OnStart(), true, false, true, true, true},
		{"When closed", base.OnStart().When(no), true, false, false, false, false},
		{"OnStartWhen open, When closed", base.When(no).OnStartWhen(yes), true, true, false, true, false},
		{"OnStartWhen closed, When open", base.When(yes).OnStartWhen(no), true, true, true, false, true},
		{"OnStartWhen closed", base.OnStartWhen(no), true, true, true, false, true},
		{"WindowWhen closed", base.WindowWhen(no), false, false, true, true, false},
		{"WindowWhen open", base.WindowWhen(yes), false, false, true, true, true},
		{"WindowWhen open, When closed", base.WindowWhen(yes).When(no), false, false, false, false, false},
		{"WindowWhen closed, OnStartWhen open", base.WindowWhen(no).OnStartWhen(yes), true, true, true, true, false},
	} {
		s := tc.s
		got := [5]bool{s.RunsOnStart(), s.HasStartGate(), s.Open(), s.StartOpen(), s.WindowOpen()}
		want := [5]bool{tc.onStart, tc.startGate, tc.open, tc.start, tc.window}
		if got != want {
			t.Errorf("%s: RunsOnStart/HasStartGate/Open/StartOpen/WindowOpen = %v, want %v", tc.name, got, want)
		}
	}
	if base.RunsOnStart() || base.HasStartGate() || !base.WindowOpen() {
		t.Fatal("a builder changed its receiver")
	}
}

// TestSchedule_TriggerGatesReadEachCall: both gates are functions read at
// each call, not values captured when the schedule was built.
func TestSchedule_TriggerGatesReadEachCall(t *testing.T) {
	startOn, windowOn := false, false
	s := tasks.ScheduleFromConfig("modules.gotham.cave.schedules.scan").
		OnStartWhen(func() bool { return startOn }).
		WindowWhen(func() bool { return windowOn })
	if s.StartOpen() || s.WindowOpen() {
		t.Fatal("closed gates read open")
	}
	startOn, windowOn = true, true
	if !s.StartOpen() || !s.WindowOpen() {
		t.Fatal("gates were captured at build time")
	}
}

// TestSchedule_TriggerGatesNilIsProblem: a nil gate on either builder is a
// constructor problem of the definition that carries the schedule.
func TestSchedule_TriggerGatesNilIsProblem(t *testing.T) {
	task := func(s *tasks.Schedule) string {
		c := validCommon()
		c.Schedule = s
		return problemText(tasks.Task("gotham.cases.review", tasks.TaskSpec[struct{}, struct{}]{
			Common: c, Run: func(*tasks.RC, struct{}) (struct{}, error) { return struct{}{}, nil },
		}))
	}
	base := tasks.ScheduleFromConfig("modules.gotham.cave.schedules.scan")
	if p := task(base.OnStartWhen(nil)); !strings.Contains(p, "OnStartWhen: nil gate") {
		t.Fatalf("OnStartWhen(nil) accepted: %q", p)
	}
	if p := task(base.WindowWhen(nil)); !strings.Contains(p, "WindowWhen: nil gate") {
		t.Fatalf("WindowWhen(nil) accepted: %q", p)
	}
	ok := func() bool { return true }
	if p := task(base.OnStartWhen(ok).WindowWhen(ok)); p != "" {
		t.Fatalf("both gates set: %q", p)
	}
}

// TestSchedule_IntervalWhen (F5-14): IntervalWhen gates the timing trigger
// only. IntervalEnabled reads that gate alone (not When), and the start and
// window accessors ignore it.
func TestSchedule_IntervalWhen(t *testing.T) {
	on := false
	base := tasks.ScheduleFromConfig("modules.gotham.cave.schedules.scan")
	s := base.IntervalWhen(func() bool { return on })
	if !s.HasIntervalGate() || s.IntervalEnabled() || !s.Open() || !s.StartOpen() || !s.WindowOpen() {
		t.Fatal("a closed IntervalWhen leaked into another trigger, or did not close the interval")
	}
	on = true
	if !s.IntervalEnabled() {
		t.Fatal("the interval gate was captured at build time")
	}
	if !s.When(func() bool { return false }).IntervalEnabled() {
		t.Fatal("IntervalEnabled read the When gate")
	}
	if base.HasIntervalGate() || !base.IntervalEnabled() {
		t.Fatal("IntervalWhen changed its receiver, or no gate reads closed")
	}
	c := validCommon()
	c.Schedule = base.IntervalWhen(nil)
	p := problemText(tasks.Task("gotham.cases.review", tasks.TaskSpec[struct{}, struct{}]{
		Common: c, Run: func(*tasks.RC, struct{}) (struct{}, error) { return struct{}{}, nil },
	}))
	if !strings.Contains(p, "IntervalWhen: nil gate") {
		t.Fatalf("IntervalWhen(nil) accepted: %q", p)
	}
}
