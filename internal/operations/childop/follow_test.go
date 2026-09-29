// file: internal/operations/childop/follow_test.go
// version: 1.0.0
// guid: 2b8d4e17-6a93-4c5f-b1e2-9f7a3c0d5e64
// last-edited: 2026-09-28

package childop

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// scriptReader returns one scripted row per read and repeats the last one.
// A nil entry is a read that finds no row; errAt marks reads that fail.
type scriptReader struct {
	mu    sync.Mutex
	rows  []*database.OperationV2Row
	errAt map[int]bool
	reads int
}

func (r *scriptReader) GetOperationV2(string) (*database.OperationV2Row, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	i := min(r.reads, len(r.rows)-1)
	r.reads++
	if r.errAt[i] {
		return nil, errors.New("store unavailable")
	}
	return r.rows[i], nil
}

func row(status string, cur, total int, msg string) *database.OperationV2Row {
	return &database.OperationV2Row{Status: status, ProgressCurrent: cur, ProgressTotal: total, ProgressMessage: msg}
}

func follow(t *testing.T, r Reader, paused func() bool) (*database.OperationV2Row, []Observation, error) {
	t.Helper()
	var obs []Observation
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	final, err := Follow(ctx, r, "child", Options{
		Interval:  time.Millisecond,
		Paused:    paused,
		OnObserve: func(o Observation) { obs = append(obs, o) },
	})
	return final, obs, err
}

func TestFollow_ReturnsTerminalRow(t *testing.T) {
	for _, status := range []string{"completed", "failed", "canceled", "interrupted_dropped", "interrupted_quiesced"} {
		r := &scriptReader{rows: []*database.OperationV2Row{row("running", 1, 2, "a"), row(status, 2, 2, "")}}
		final, _, err := follow(t, r, nil)
		if err != nil || final == nil || final.Status != status {
			t.Fatalf("%s: got row %+v err %v", status, final, err)
		}
	}
}

// A running child is relayed only when its row changes, so a wedged child
// makes its parent go quiet too and the parent's watchdog can see it.
func TestFollow_RunningReportsOnlyChanges(t *testing.T) {
	a := row("running", 1, 10, "one")
	r := &scriptReader{rows: []*database.OperationV2Row{
		a, a, a, // unchanged: one report
		row("running", 2, 10, "one"), row("running", 2, 10, "one"), // counts moved: one report
		row("running", 2, 10, "two"), // message moved: one report
		row("completed", 10, 10, ""),
	}}
	_, obs, err := follow(t, r, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(obs) != 3 {
		t.Fatalf("want 3 progress reports, got %d: %+v", len(obs), obs)
	}
	for _, o := range obs {
		if o.Event != EventProgress {
			t.Fatalf("running child reported as %v", o.Event)
		}
	}
}

// last_progress_at moving counts as a change even when counts and message do not.
func TestFollow_LastProgressAtIsAChange(t *testing.T) {
	t1, t2 := time.Unix(100, 0), time.Unix(200, 0)
	a, b := row("running", 1, 10, "x"), row("running", 1, 10, "x")
	a.LastProgressAt, b.LastProgressAt = &t1, &t2
	r := &scriptReader{rows: []*database.OperationV2Row{a, b, row("completed", 1, 1, "")}}
	_, obs, _ := follow(t, r, nil)
	if len(obs) != 2 {
		t.Fatalf("want 2 reports, got %d", len(obs))
	}
}

// Queued and paused rows do not change, and must be relayed on every poll.
func TestFollow_QueuedAndPausedReportEveryPoll(t *testing.T) {
	q := row("queued", 0, 0, "")
	r := &scriptReader{rows: []*database.OperationV2Row{q, q, q, row("completed", 1, 1, "")}}
	_, obs, _ := follow(t, r, nil)
	if len(obs) != 3 || obs[0].Event != EventQueued {
		t.Fatalf("queued: want 3 EventQueued, got %+v", obs)
	}

	run := row("running", 3, 10, "parked")
	r = &scriptReader{rows: []*database.OperationV2Row{run, run, run, run, row("completed", 1, 1, "")}}
	_, obs, _ = follow(t, r, func() bool { return true })
	if len(obs) != 4 || obs[0].Event != EventPaused {
		t.Fatalf("paused: want 4 EventPaused, got %+v", obs)
	}
}

// A read error or a not-yet-visible row is neither progress nor the end.
func TestFollow_ReadErrorsAreSilent(t *testing.T) {
	r := &scriptReader{
		rows:  []*database.OperationV2Row{nil, row("running", 0, 1, ""), row("running", 0, 1, ""), row("completed", 1, 1, "")},
		errAt: map[int]bool{1: true},
	}
	final, obs, err := follow(t, r, nil)
	if err != nil || final.Status != "completed" {
		t.Fatalf("got %+v %v", final, err)
	}
	if len(obs) != 1 {
		t.Fatalf("only the one readable running row is progress, got %d", len(obs))
	}
}

func TestFollow_ContextEnds(t *testing.T) {
	r := &scriptReader{rows: []*database.OperationV2Row{row("running", 0, 1, "")}}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := Follow(ctx, r, "child", Options{Interval: time.Millisecond}); !errors.Is(err, context.Canceled) {
		t.Fatalf("want context.Canceled, got %v", err)
	}
	if _, err := Follow(context.Background(), nil, "child", Options{}); err == nil {
		t.Fatal("a nil reader must be an error, not a hang")
	}
}

func TestPercent(t *testing.T) {
	cases := []struct {
		cur, total, want int
	}{{0, 0, 0}, {5, 10, 50}, {12, 10, 100}, {-1, 10, 0}}
	for _, c := range cases {
		if got := Percent(row("running", c.cur, c.total, "")); got != c.want {
			t.Errorf("Percent(%d/%d)=%d want %d", c.cur, c.total, got, c.want)
		}
	}
	if Percent(nil) != 0 {
		t.Error("Percent(nil) must be 0")
	}
}
