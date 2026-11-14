// file: core/kernel/health_loops_test.go
// version: 1.0.1
// guid: 4d740c96-10cf-4011-bacb-8508f19cebed
// last-edited: 2026-11-13

package kernel

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/falkcorp/mediacore/health"
)

func up(context.Context) health.Result { return health.Result{Status: health.StatusUp} }

func ids(m map[health.ComponentID]health.Reporter) []string {
	var out []string
	for id := range m {
		out = append(out, string(id))
	}
	slices.Sort(out)
	return out
}

func TestKernel_RegistersTheRemainingCoreComponents(t *testing.T) {
	reg := health.NewRegistry()
	all := coreComponentSources{Config: up, Tasks: up, Scheduler: up, Outbox: up, Events: up, Metrics: up, Updater: up, Backup: up}
	reps, err := registerCoreComponents(reg, all)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"core.backup", "core.config", "core.events", "core.metrics", "core.outbox", "core.scheduler", "core.tasks", "core.updater"}
	if got := ids(reps); !slices.Equal(got, want) {
		t.Errorf("registered %v, want %v", got, want)
	}
	if got := len(reg.Snapshot().Components); got != len(want) {
		t.Errorf("registry holds %d components, want %d", got, len(want))
	}
	if _, err := registerCoreComponents(reg, all); err == nil {
		t.Error("registering the same components twice succeeded")
	}
}

func TestKernel_NilSourcesRegisterOnlyThePushComponents(t *testing.T) {
	reg := health.NewRegistry()
	reps, err := registerCoreComponents(reg, coreComponentSources{})
	if err != nil {
		t.Fatal(err)
	}
	// core.config and core.backup are push-only: other services push their tokens.
	// core.updater is registered only when the updater is enabled.
	if got, want := ids(reps), []string{"core.backup", "core.config"}; !slices.Equal(got, want) {
		t.Errorf("registered %v, want %v", got, want)
	}
	reps["core.config"].Report(health.Result{Status: health.StatusDegraded, Reason: "config_retired_name_ignored"})
	if got := states(reg)["core.config"].Result.Reason; got != "config_retired_name_ignored" {
		t.Errorf("a pushed reason = %q", got)
	}
	if _, err := registerCoreComponents(nil, coreComponentSources{}); err == nil {
		t.Error("a nil registry was accepted")
	}
}

func TestKernel_OutboxDegradedWhileDeadLettersExist(t *testing.T) {
	n, err := 0, error(nil)
	probe := outboxProbe(func(context.Context) (int, error) { return n, err })
	ctx := context.Background()
	if r := probe(ctx); r.Status != health.StatusUp {
		t.Errorf("no dead letters = %+v", r)
	}
	n = 3
	if r := probe(ctx); r.Status != health.StatusDegraded || r.Reason != "dead_letters" {
		t.Errorf("3 dead letters = %+v", r)
	}
	n, err = 0, errors.New("outbox keyspace unreadable")
	if r := probe(ctx); r.Status != health.StatusDown || r.Reason != "outbox_unreadable" {
		t.Errorf("an unreadable outbox = %+v", r)
	}
}

func TestKernel_TasksProbe(t *testing.T) {
	st := tasksState{Running: true}
	probe := tasksProbe(func() tasksState { return st })
	ctx := context.Background()
	for name, tc := range map[string]struct {
		state  tasksState
		status health.Status
		reason string
	}{
		"running":  {tasksState{Running: true}, health.StatusUp, ""},
		"stopped":  {tasksState{}, health.StatusDown, "executor_stopped"},
		"paused":   {tasksState{Running: true, Paused: true}, health.StatusDegraded, "paused"},
		"zombies":  {tasksState{Running: true, ZombieCapHit: true}, health.StatusDegraded, "zombie_cap"},
		"stopped+": {tasksState{Paused: true}, health.StatusDown, "executor_stopped"},
	} {
		st = tc.state
		if r := probe(ctx); r.Status != tc.status || r.Reason != tc.reason {
			t.Errorf("%s = %+v, want %v %q", name, r, tc.status, tc.reason)
		}
	}
}

func TestKernel_EventsProbeDegradedOnABurstOnly(t *testing.T) {
	var dropped uint64
	probe := eventsProbe(func() uint64 { return dropped }, 5)
	ctx := context.Background()
	dropped = 1000 // the baseline reading is never a burst
	if r := probe(ctx); r.Status != health.StatusUp {
		t.Errorf("first reading = %+v", r)
	}
	dropped += 5
	if r := probe(ctx); r.Status != health.StatusUp {
		t.Errorf("a drop at the limit = %+v", r)
	}
	dropped += 6
	if r := probe(ctx); r.Status != health.StatusDegraded || r.Reason != "events_dropped" {
		t.Errorf("a burst = %+v", r)
	}
	if r := probe(ctx); r.Status != health.StatusUp {
		t.Errorf("after the burst, with no new drops = %+v", r)
	}
	dropped = 2 // the counter was reset (a restart of the bus)
	if r := probe(ctx); r.Status != health.StatusUp {
		t.Errorf("a counter reset = %+v", r)
	}
}

// supNotifier records the supervisor signals the kernel loops send.
type supNotifier struct {
	mu        sync.Mutex
	watchdogs int
	stopping  int
}

func (n *supNotifier) Ready() error               { return nil }
func (n *supNotifier) Status(string) error        { return nil }
func (n *supNotifier) Extend(time.Duration) error { return nil }
func (n *supNotifier) Stopping() error {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.stopping++
	return nil
}
func (n *supNotifier) Watchdog() error {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.watchdogs++
	return nil
}
func (n *supNotifier) WatchdogInterval() time.Duration { return 20 * time.Millisecond }
func (n *supNotifier) Active() bool                    { return true }

func (n *supNotifier) counts() (watchdogs, stopping int) {
	n.mu.Lock()
	defer n.mu.Unlock()
	return n.watchdogs, n.stopping
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// TestKernel_HealthLoopsBeatAndPing: the loops record a beat and send the
// watchdog ping from the heartbeat goroutine, and stop doing both when stopped.
func TestKernel_HealthLoopsBeatAndPing(t *testing.T) {
	reg := health.NewRegistry()
	n := &supNotifier{}
	sup := health.NewSupervisor(nil, n)
	var works int
	var mu sync.Mutex
	stop := startHealthLoops(context.Background(), reg, sup, func() { mu.Lock(); works++; mu.Unlock() })
	waitFor(t, "two watchdog pings", func() bool { w, _ := n.counts(); return w >= 2 })
	mu.Lock()
	if works == 0 {
		t.Error("the heartbeat never ran its work func")
	}
	mu.Unlock()
	if !reg.Live() {
		t.Error("a beating process is not live")
	}
	stop()
	time.Sleep(60 * time.Millisecond) // let a tick that was already due finish
	w1, _ := n.counts()
	time.Sleep(80 * time.Millisecond)
	if w2, _ := n.counts(); w2 != w1 {
		t.Errorf("the watchdog kept pinging after stop: %d -> %d", w1, w2)
	}
}

// TestKernel_WedgedWorkStopsThePings: a work func that never returns models a
// wedged process: no beat is recorded, so the watchdog is not pinged.
func TestKernel_WedgedWorkStopsThePings(t *testing.T) {
	reg := health.NewRegistry()
	n := &supNotifier{}
	sup := health.NewSupervisor(nil, n)
	release := make(chan struct{})
	t.Cleanup(func() { close(release) })
	stop := startHealthLoops(context.Background(), reg, sup, func() { <-release })
	t.Cleanup(stop)
	time.Sleep(150 * time.Millisecond)
	if w, _ := n.counts(); w > 1 {
		t.Errorf("%d watchdog pings from a wedged heartbeat, want at most the first", w)
	}
}

func TestKernel_BeginHealthShutdownFlipsReadinessAndSaysStopping(t *testing.T) {
	reg := health.NewRegistry()
	n := &supNotifier{}
	sup := health.NewSupervisor(nil, n)
	if ready, _ := reg.Ready(); !ready {
		t.Fatal("an empty registry is not ready")
	}
	beginHealthShutdown(reg, sup)
	beginHealthShutdown(reg, sup)
	ready, tokens := reg.Ready()
	if ready || !slices.Contains(tokens, "core.kernel:stopping") {
		t.Errorf("Ready = %v %v, want not ready with core.kernel:stopping", ready, tokens)
	}
	if _, stopping := n.counts(); stopping != 2 {
		t.Errorf("STOPPING sent %d times for 2 calls (the registry flip is idempotent; the notify is per call)", stopping)
	}
}

// TestKernel_ReadyzFlipsBeforeHTTPDrain runs the kernel's own shutdown
// preamble, beginHealthShutdown, ahead of the HTTP drain phase of a real
// StopBudget and looks from inside the drain: /readyz already answers 503 with
// core.kernel:stopping, systemd has already been told STOPPING=1, and the
// status topic carried exactly one ready:false (05-health 5H.4). A shutdown
// path that drains first, or that forgets either half of the preamble, fails.
func TestKernel_ReadyzFlipsBeforeHTTPDrain(t *testing.T) {
	reg := health.NewRegistry()
	n := &supNotifier{}
	sup := health.NewSupervisor(nil, n)
	var published []health.StatusPayload
	reg.SetStatusPublisher(func(p health.StatusPayload) { published = append(published, p) })
	ready := health.ReadyHandler(reg)
	readyz := func() *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		ready.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/readyz", nil))
		return rec
	}
	if rec := readyz(); rec.Code != http.StatusOK {
		t.Fatalf("/readyz before shutdown = %d, want 200", rec.Code)
	}

	b := health.NewStopBudget(25*time.Second, health.StopLimits{}, nil)
	beginHealthShutdown(reg, sup)
	var duringDrain *httptest.ResponseRecorder
	var stoppingDuringDrain int
	if err := b.Run(context.Background(), health.PhaseHTTPDrain, func(context.Context) error {
		duringDrain = readyz()
		_, stoppingDuringDrain = n.counts()
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if duringDrain.Code != http.StatusServiceUnavailable {
		t.Errorf("/readyz during the HTTP drain = %d, want 503", duringDrain.Code)
	}
	if body := duringDrain.Body.String(); !strings.Contains(body, "core.kernel:stopping") {
		t.Errorf("/readyz body during the drain = %s, want the stopping token", body)
	}
	if stoppingDuringDrain != 1 {
		t.Errorf("STOPPING=1 sent %d times before the drain started, want 1", stoppingDuringDrain)
	}
	if len(published) != 2 || !published[0].Ready || published[1].Ready || !slices.Equal(published[1].NotReady, []string{"core.kernel:stopping"}) {
		t.Errorf("status feed = %+v, want [ready, not-ready(stopping)]", published)
	}
}
