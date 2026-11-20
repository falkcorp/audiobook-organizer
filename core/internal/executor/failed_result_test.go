// file: core/internal/executor/failed_result_test.go
// version: 0.1.0
// guid: 8cfdec92-35d0-4ce9-987f-0c05fae9dc58
// last-edited: 2026-11-20

package executor

import (
	"encoding/json"
	"errors"
	"testing"

	"github.com/falkcorp/mediacore/tasks"
)

// ledgerTally is the result the fixture kinds return beside their error: what
// the run did before it failed.
type ledgerTally struct {
	Filed int `json:"filed"`
}

// A run that ends in an error but returns a non-zero result keeps that result
// on its failed record (F5-18d, O5): HEAD stored a run's payload on its row
// before returning the run's error, so a failed trash-cleanup or credits
// backfill still showed what it had done. A run that returns the zero result
// with its error stores no result, as before.
func TestFailedRun_KeepsANonZeroResult(t *testing.T) {
	boom := errors.New("the Batcomputer lost the case files")
	partial := tasks.Task(tasks.Kind("gotham.ledger"), tasks.TaskSpec[noParams, ledgerTally]{
		Common: common("gotham.ledger"),
		Run:    func(*tasks.RC, noParams) (ledgerTally, error) { return ledgerTally{Filed: 7}, boom },
	})
	empty := tasks.Task(tasks.Kind("gotham.empty"), tasks.TaskSpec[noParams, ledgerTally]{
		Common: common("gotham.empty"),
		Run:    func(*tasks.RC, noParams) (ledgerTally, error) { return ledgerTally{}, boom },
	})
	batch := tasks.Batch(tasks.Kind("gotham.sweep"), tasks.BatchSpec[noParams, string, ledgerTally]{
		Common: common("gotham.sweep"),
		Source: func(*tasks.RC, noParams) (tasks.Source[string], error) {
			return tasks.Source[string]{Items: []string{"joker", "riddler"}}, nil
		},
		Item:        func(*tasks.ItemRC, noParams, string) error { return nil },
		Parallelism: tasks.Sequential("one case file at a time"),
		Finish: func(_ *tasks.RC, _ noParams, s tasks.Summary) (ledgerTally, error) {
			return ledgerTally{Filed: int(s.Done)}, boom
		},
	})
	h := newHarness(t, []tasks.Definition{partial, empty, batch}, nil)

	for _, c := range []struct {
		kind string
		want *ledgerTally
	}{
		{"gotham.ledger", &ledgerTally{Filed: 7}},
		{"gotham.empty", nil},
		{"gotham.sweep", &ledgerTally{Filed: 2}},
	} {
		id := h.start(c.kind, tasks.StartRequest{})
		r := h.waitState(id, tasks.StateFailed, tasks.StateSucceeded)
		if r.State != tasks.StateFailed {
			t.Fatalf("%s: %s, want failed", c.kind, r.State)
		}
		if r.Error == "" {
			t.Fatalf("%s: a failed run lost its error text", c.kind)
		}
		if c.want == nil {
			// The native store reads a record with no result back as JSON null.
			if len(r.Result) != 0 && string(r.Result) != "null" {
				t.Fatalf("%s: a zero result was stored on the failed run: %s", c.kind, r.Result)
			}
			continue
		}
		var got ledgerTally
		if err := json.Unmarshal(r.Result, &got); err != nil {
			t.Fatalf("%s: the failed run's result: %v (%s)", c.kind, err, r.Result)
		}
		if got != *c.want {
			t.Fatalf("%s: result %+v, want %+v", c.kind, got, *c.want)
		}
	}
}
