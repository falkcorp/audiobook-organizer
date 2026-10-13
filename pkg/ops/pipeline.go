// file: pkg/ops/pipeline.go
// version: 1.0.0
// guid: e46aa4c4-ffd1-4655-92af-579ca577163d
// last-edited: 2026-10-13

package ops

import (
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
)

// PipelineSpec runs child definitions as a DAG. Effects are the union of the
// children's; any Effects set here are replaced.
type PipelineSpec[P any] struct {
	Common
	Stages []Stage[P]
}

// Stage is one child of a Pipeline.
type Stage[P any] struct {
	Name string
	// Def is the child definition to run. It must be in the catalog itself.
	Def Definition
	// After lists stage names that must SUCCEED first. Empty = starts at once.
	After []string
	// Params builds the child's params from the pipeline params and the
	// results of the stages it waits on. Nil sends the pipeline's params.
	Params func(p P, results Results) (any, error)
	// OnFail: StopPipeline (default) or ContinueOthers.
	OnFail StageFailure
}

// StageFailure is what a failed stage does to the rest of the pipeline.
type StageFailure int

const (
	// StopPipeline starts no further stage and fails the pipeline. The default.
	StopPipeline StageFailure = iota
	// ContinueOthers skips only the stages that wait on the failed one.
	ContinueOthers
)

// Results holds the finished stages' results by stage name.
type Results map[string]json.RawMessage

// Decode decodes a stage's result into into.
func (r Results) Decode(stage string, into any) error {
	raw, ok := r[stage]
	if !ok {
		return fmt.Errorf("ops: no result for stage %q", stage)
	}
	return json.Unmarshal(raw, into)
}

// PipelineResult is a Pipeline's result: each stage's outcome.
type PipelineResult struct {
	Stages map[string]StageOutcome `json:"stages"`
}

// StageOutcome is one stage's end.
type StageOutcome struct {
	RunID  string          `json:"run_id,omitempty"`
	State  string          `json:"state"` // the child's status, or "skipped"
	Result json.RawMessage `json:"result,omitempty"`
	Err    string          `json:"error,omitempty"`
}

// stateSucceeded is the v2 status the executor reports for a successful
// child; "succeeded" is the v3 spelling of the same end.
func stageSucceeded(state string) bool { return state == "completed" || state == "succeeded" }

// Pipeline builds a Pipeline definition.
func Pipeline[P any](id string, s PipelineSpec[P]) Definition {
	c := s.Common
	c.Effects = Effects{}
	for i, st := range s.Stages {
		if st.Def == nil {
			continue
		}
		ce := st.Def.spec().common.Effects
		if i == 0 {
			c.Effects = ce.clone()
			continue
		}
		c.Effects = c.Effects.union(ce)
	}
	b := newSpec(id, KindPipeline, c)
	if len(s.Stages) == 0 {
		b.problem("Pipeline has no stages")
	}
	for _, msg := range pipelineProblems(s.Stages) {
		b.problem(msg)
	}
	for _, st := range s.Stages {
		b.stages = append(b.stages, CompiledStage{
			Name: st.Name, Def: st.Def, After: slices.Clone(st.After), OnFail: st.OnFail,
			Params: stageParams(id, st.Params),
		})
	}
	stages := b.stages
	b.task = func(rc *RC, params json.RawMessage) (json.RawMessage, error) {
		return runPipeline(rc, stages, params)
	}
	return b
}

func stageParams[P any](id string, fn func(P, Results) (any, error)) func(json.RawMessage, Results) (json.RawMessage, error) {
	return func(params json.RawMessage, res Results) (json.RawMessage, error) {
		if fn == nil {
			return params, nil
		}
		p, err := decodeParams[P](id, params)
		if err != nil {
			return nil, err
		}
		v, err := fn(p, res)
		if err != nil {
			return nil, err
		}
		return json.Marshal(v)
	}
}

// pipelineProblems checks names, After references and cycles.
func pipelineProblems[P any](stages []Stage[P]) []string {
	var out []string
	seen := map[string]bool{}
	for _, st := range stages {
		switch {
		case st.Name == "":
			out = append(out, "a stage has no name")
		case seen[st.Name]:
			out = append(out, "duplicate stage "+st.Name)
		}
		seen[st.Name] = true
		if st.Def == nil {
			out = append(out, "stage "+st.Name+" has no Def")
		}
	}
	after := map[string][]string{}
	for _, st := range stages {
		for _, a := range st.After {
			if !seen[a] {
				out = append(out, fmt.Sprintf("stage %s waits on unknown stage %s", st.Name, a))
			}
		}
		after[st.Name] = st.After
	}
	// A DAG check: repeatedly remove stages whose prerequisites are gone.
	done := map[string]bool{}
	for progress := true; progress; {
		progress = false
		for name, deps := range after {
			if done[name] {
				continue
			}
			ready := true
			for _, d := range deps {
				if !done[d] && seen[d] {
					ready = false
				}
			}
			if ready {
				done[name], progress = true, true
			}
		}
	}
	var cyc []string
	for name := range after {
		if !done[name] {
			cyc = append(cyc, name)
		}
	}
	if len(cyc) > 0 {
		slices.Sort(cyc)
		out = append(out, "stages form a cycle: "+strings.Join(cyc, ", "))
	}
	return out
}

// stageEnd is one finished stage, sent back to runPipeline.
type stageEnd struct {
	idx int
	out StageOutcome
	err error
}

// runPipeline starts every stage whose prerequisites succeeded, as a child
// run, and waits for them. One goroutine per started stage (a pipeline's
// stages are a small fixed set), each blocked in rc.Await.
func runPipeline(rc *RC, stages []CompiledStage, params json.RawMessage) (json.RawMessage, error) {
	res := Results{}
	outcomes := map[string]StageOutcome{}
	state := make([]int, len(stages)) // 0 pending, 1 running, 2 done
	failed := map[string]bool{}
	stopped := false
	ends := make(chan stageEnd, len(stages))
	running := 0
	var errs []error

	start := func(i int) {
		st := stages[i]
		raw, err := st.Params(params, res)
		if err != nil {
			ends <- stageEnd{idx: i, out: StageOutcome{State: "failed", Err: err.Error()}, err: err}
			return
		}
		ref, err := rc.Enqueue(st.Def, raw, AsStage(st.Name))
		if err != nil {
			ends <- stageEnd{idx: i, out: StageOutcome{State: "failed", Err: err.Error()}, err: err}
			return
		}
		go func() {
			o, err := rc.Await(ref)
			ends <- stageEnd{idx: i, out: StageOutcome{RunID: ref.ID, State: o.State, Result: o.Result, Err: o.Err}, err: err}
		}()
	}

	for {
		if !stopped && rc.Context().Err() == nil {
			for i, st := range stages {
				if state[i] != 0 || !depsSettled(st, stages, state) {
					continue
				}
				if blockedBy(st, failed) {
					state[i] = 2
					outcomes[st.Name] = StageOutcome{State: "skipped"}
					failed[st.Name] = true
					continue
				}
				state[i] = 1
				running++
				start(i)
			}
		}
		if running == 0 {
			break
		}
		e := <-ends
		running--
		st := stages[e.idx]
		state[e.idx] = 2
		outcomes[st.Name] = e.out
		if e.err == nil && stageSucceeded(e.out.State) {
			res[st.Name] = e.out.Result
			continue
		}
		failed[st.Name] = true
		errs = append(errs, fmt.Errorf("stage %s: %s %s", st.Name, e.out.State, errText(e)))
		if st.OnFail == StopPipeline {
			stopped = true
		}
	}
	for i, st := range stages {
		if state[i] == 0 {
			outcomes[st.Name] = StageOutcome{State: "skipped"}
		}
	}
	raw, err := json.Marshal(PipelineResult{Stages: outcomes})
	if err != nil {
		return nil, err
	}
	if ctxErr := rc.Context().Err(); ctxErr != nil {
		errs = append(errs, ctxErr)
	}
	return raw, errors.Join(errs...)
}

func errText(e stageEnd) string {
	if e.err != nil {
		return e.err.Error()
	}
	return e.out.Err
}

// depsSettled: every stage st waits on has ended.
func depsSettled(st CompiledStage, stages []CompiledStage, state []int) bool {
	for _, a := range st.After {
		for j, o := range stages {
			if o.Name == a && state[j] != 2 {
				return false
			}
		}
	}
	return true
}

func blockedBy(st CompiledStage, failed map[string]bool) bool {
	for _, a := range st.After {
		if failed[a] {
			return true
		}
	}
	return false
}
