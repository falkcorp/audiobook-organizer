// file: core/tasks/parallelism.go
// version: 1.0.1
// guid: d0dae22a-d03d-45de-8385-0c4532e7fd77
// last-edited: 2026-11-10

package tasks

import (
	"encoding/json"
	"errors"
	"fmt"
	"runtime"
)

// Parallelism is a declared worker count with its reason. Its zero value is
// INVALID: Validate returns ErrNoParallelism, Batch/Fixer registration fails,
// and RunItems/RunSeq/ForEach return before touching an item.
type Parallelism struct {
	kind   parKind // 0 = unset
	n      int
	reason string
	field  string // FromParam
	max    int
}

type parKind uint8

const (
	parUnset parKind = iota // the zero value: invalid (A1)
	parCPU
	parFixed
	parSequential
	parParam
)

// CPU is runtime.NumCPU() workers sharing the executor CPU budget: inside a
// run every CPU() worker takes one core.tasks.cpu_slots slot per item.
func CPU() Parallelism { return Parallelism{kind: parCPU} }

// Fixed is n workers, clamped to [1, MaxFixed]; reason (what bounds n: a
// provider's rate budget, disk-bound I/O, a connection limit) must be
// non-empty. Fixed workers take no CPU slot.
func Fixed(n int, reason string) Parallelism {
	return Parallelism{kind: parFixed, n: min(max(n, 1), MaxFixed), reason: reason}
}

// Sequential is one worker; reason must be non-empty and is shown in the
// definition list.
func Sequential(reason string) Parallelism {
	return Parallelism{kind: parSequential, n: 1, reason: reason}
}

// FromParam reads the worker count from the run params' field, clamped to
// [1, min(max, MaxFixed)]; def is used when the field is absent. Batch and
// Fixer only: a Task's RunItems has no params to read and runs def workers.
func FromParam(field string, def, max int) Parallelism {
	return Parallelism{kind: parParam, field: field, n: def, max: max}
}

// MaxFixed caps Fixed and FromParam (v3 MaxNetworkWorkers, the v3 ops package's batch.go:132-134).
const MaxFixed = 64

// ErrNoParallelism is the single sentinel for an undeclared parallelism (03 R7's name).
var ErrNoParallelism = errors.New("tasks: parallelism not declared (use CPU, Fixed or Sequential)")

// ErrNoRunContext: ForEach was asked for a Watermark without an RC-derived ctx.
var ErrNoRunContext = errors.New("tasks: watermark needs a run context")

// Validate returns nil or the bare ErrNoParallelism: for the zero value, for
// Fixed or Sequential with an empty reason, and for FromParam without a field
// or with max < 1.
func (p Parallelism) Validate() error {
	switch p.kind {
	case parCPU:
		return nil
	case parFixed, parSequential:
		if p.reason == "" {
			return ErrNoParallelism
		}
		return nil
	case parParam:
		if p.field == "" || p.max < 1 {
			return ErrNoParallelism
		}
		return nil
	default:
		return ErrNoParallelism
	}
}

// String renders "cpu", "fixed(4): <reason>", "sequential: <reason>",
// "param(<field>, def <d>, max <m>)", or "unset" for the zero value.
func (p Parallelism) String() string {
	switch p.kind {
	case parCPU:
		return "cpu"
	case parFixed:
		return fmt.Sprintf("fixed(%d): %s", p.n, p.reason)
	case parSequential:
		return "sequential: " + p.reason
	case parParam:
		return fmt.Sprintf("param(%s, def %d, max %d)", p.field, p.n, p.max)
	default:
		return "unset"
	}
}

// Workers is the declared worker count: NumCPU for CPU, n for Fixed, 1 for
// Sequential, FromParam's default clamped to its bounds, and 0 for an invalid
// value (the loops refuse it before reading this). The operator dial
// core.tasks.kinds[<kind>].parallelism_cap lowers it at run time (executor).
func (p Parallelism) Workers() int {
	if p.Validate() != nil {
		return 0
	}
	switch p.kind {
	case parCPU:
		return max(runtime.NumCPU(), 1)
	case parParam:
		return p.clamp(p.n)
	default:
		return p.n
	}
}

// WorkersFor is Workers for one run: FromParam reads its field from params
// (a JSON object; a missing or non-integer field uses the default); every
// other kind ignores params. Name coined by F2-01 for the Batch/Fixer runner.
func (p Parallelism) WorkersFor(params json.RawMessage) int {
	if p.kind != parParam || p.Validate() != nil {
		return p.Workers()
	}
	n := p.n
	var m map[string]json.RawMessage
	if json.Unmarshal(params, &m) == nil {
		var v int
		if raw, ok := m[p.field]; ok && json.Unmarshal(raw, &v) == nil {
			n = v
		}
	}
	return p.clamp(n)
}

// UsesCPUBudget reports a CPU() parallelism: only its workers take a
// core.tasks.cpu_slots slot per item. Name coined by F2-01.
func (p Parallelism) UsesCPUBudget() bool { return p.kind == parCPU }

func (p Parallelism) clamp(n int) int {
	return min(max(n, 1), min(max(p.max, 1), MaxFixed))
}
