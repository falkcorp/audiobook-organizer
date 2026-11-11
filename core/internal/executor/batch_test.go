// file: core/internal/executor/batch_test.go
// version: 1.0.0
// guid: ff587c0b-7c81-4a29-8286-03cb21859652
// last-edited: 2026-11-10

package executor

import (
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"sync"
	"testing"

	"github.com/falkcorp/mediacore/tasks"
)

// archiveRun is one Batch run over ten case files; Item records what it ran.
type archiveRun struct {
	mu     sync.Mutex
	ran    []string
	block  string        // the item that blocks until the run ends
	fail   string        // the item that fails
	reach  chan struct{} // closed when the blocking item starts
	finish chan tasks.Summary
}

func (r *archiveRun) got() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := slices.Clone(r.ran)
	slices.Sort(out)
	return out
}

var caseFiles = func() []string {
	out := make([]string, 10)
	for i := range out {
		out[i] = fmt.Sprintf("file-%02d", i)
	}
	return out
}()

func archiveDef(r *archiveRun, edit func(*tasks.BatchSpec[noParams, string, tasks.Summary])) tasks.Definition {
	s := tasks.BatchSpec[noParams, string, tasks.Summary]{
		Common: common("gotham.archive"),
		Source: func(*tasks.RC, noParams) (tasks.Source[string], error) {
			return tasks.Source[string]{Items: caseFiles, Key: func(s string) string { return s }}, nil
		},
		Item: func(ic *tasks.ItemRC, _ noParams, f string) error {
			if f == r.block {
				close(r.reach)
				<-ic.Context().Done()
				return ic.Context().Err()
			}
			r.mu.Lock()
			r.ran = append(r.ran, f)
			r.mu.Unlock()
			if f == r.fail {
				return errors.New("the file is water damaged")
			}
			return nil
		},
		Finish: func(_ *tasks.RC, _ noParams, s tasks.Summary) (tasks.Summary, error) {
			if r.finish != nil {
				r.finish <- s
			}
			return s, nil
		},
		Parallelism: tasks.Sequential("one file at a time"),
		ChunkSize:   3,
	}
	if edit != nil {
		edit(&s)
	}
	return tasks.Batch(tasks.Kind("gotham.archive"), s)
}

// TestBatch_ChunkLedgerResume (5T.3.1, 5T.3.6; added for the chunk ledger):
// a resumed Batch skips the chunks its ledger records complete, redoes the
// incomplete one, and its summary counts both attempts; an item failure
// under BatchContinue is counted and the run still succeeds.
func TestBatch_ChunkLedgerResume(t *testing.T) {
	first := &archiveRun{block: "file-07", reach: make(chan struct{})}
	h := newHarness(t, []tasks.Definition{archiveDef(first, nil)}, nil)
	id := h.start("gotham.archive", tasks.StartRequest{})
	<-first.reach
	if got := first.got(); !slices.Equal(got, caseFiles[:7]) {
		t.Fatalf("first attempt ran %v, want files 00..06", got)
	}
	second := &archiveRun{fail: "file-09", finish: make(chan tasks.Summary, 1)}
	next := h.crashBoot([]tasks.Definition{archiveDef(second, nil)}, nil)
	r := next.waitState(id, tasks.StateSucceeded, tasks.StateFailed)
	if r.State != tasks.StateSucceeded {
		t.Fatalf("resumed batch: %s (%q)", r.State, r.Error)
	}
	if got := second.got(); !slices.Equal(got, caseFiles[6:]) {
		t.Fatalf("resumed attempt ran %v, want files 06..09 (chunks 0 and 1 were complete)", got)
	}
	s := <-second.finish
	if s.Done != 9 || s.Failed != 1 || s.ChunksDone != 4 {
		t.Fatalf("summary %+v, want 9 done, 1 failed, 4 chunks", s)
	}
	var stored tasks.Summary
	if err := json.Unmarshal(r.Result, &stored); err != nil || stored.Failed != 1 {
		t.Fatalf("stored result %s (%v)", r.Result, err)
	}
}

// TestBatch_StopPolicyAndPartitions (5T.3.1): BatchStop fails the run at
// the first failed item without dispatching the rest; PartitionBy runs the
// items of one key in order.
func TestBatch_StopPolicyAndPartitions(t *testing.T) {
	t.Run("BatchStop", func(t *testing.T) {
		r := &archiveRun{fail: "file-01"}
		h := newHarness(t, []tasks.Definition{archiveDef(r, func(s *tasks.BatchSpec[noParams, string, tasks.Summary]) {
			s.OnItemError = tasks.BatchStop
		})}, nil)
		rec := h.waitState(h.start("gotham.archive", tasks.StartRequest{}), tasks.StateFailed, tasks.StateSucceeded)
		if rec.State != tasks.StateFailed {
			t.Fatalf("BatchStop run: %s, want failed", rec.State)
		}
		if got := r.got(); !slices.Equal(got, caseFiles[:2]) {
			t.Fatalf("ran %v, want only files 00 and 01", got)
		}
	})
	t.Run("PartitionBy", func(t *testing.T) {
		r := &archiveRun{}
		h := newHarness(t, []tasks.Definition{archiveDef(r, func(s *tasks.BatchSpec[noParams, string, tasks.Summary]) {
			s.Parallelism = tasks.Fixed(4, "four clerks")
			s.ChunkSize = 10
			s.PartitionBy = func(f string) string {
				if (f[len(f)-1]-'0')%2 == 0 {
					return "even"
				}
				return "odd"
			}
		})}, nil)
		h.waitState(h.start("gotham.archive", tasks.StartRequest{}), tasks.StateSucceeded)
		r.mu.Lock()
		ran := slices.Clone(r.ran)
		r.mu.Unlock()
		var even, odd []string
		for _, f := range ran {
			if (f[len(f)-1]-'0')%2 == 0 {
				even = append(even, f)
			} else {
				odd = append(odd, f)
			}
		}
		if !slices.IsSorted(even) || !slices.IsSorted(odd) || len(even)+len(odd) != 10 {
			t.Fatalf("partitions ran out of order: even %v odd %v", even, odd)
		}
	})
}
