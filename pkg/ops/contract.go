// file: pkg/ops/contract.go
// version: 1.0.0
// guid: 687d6219-d1d8-4682-a7e2-7070d16d48be
// last-edited: 2026-10-13

package ops

import "time"

// CancelContract replaces v2's Cancellable bool, which nothing verified.
type CancelContract struct {
	kind   cancelKind
	reason string
	budget time.Duration
}

type cancelKind int

const (
	cancelItems cancelKind = iota + 1
	cancelCooperative
	cancelNever
)

// AtItemBoundaries is the Batch and Fixer default: the runner checks between
// items and the Writer checks per write.
func AtItemBoundaries() CancelContract { return CancelContract{kind: cancelItems} }

// Cooperative is the Task default: rc.Context() is honored and the Task
// returns within grace after a cancel.
func Cooperative(grace time.Duration) CancelContract {
	return CancelContract{kind: cancelCooperative, budget: grace}
}

// Uninterruptible is for genuinely atomic work (one compaction call). The
// budget is the watchdog budget too; listed at WARN on every boot.
func Uninterruptible(reason string, budget time.Duration) CancelContract {
	return CancelContract{kind: cancelNever, reason: reason, budget: budget}
}

// IsUninterruptible reports an Uninterruptible contract.
func (c CancelContract) IsUninterruptible() bool { return c.kind == cancelNever }

// Budget is the grace (Cooperative) or watchdog budget (Uninterruptible).
func (c CancelContract) Budget() time.Duration { return c.budget }

// Reason is an Uninterruptible contract's stated reason.
func (c CancelContract) Reason() string { return c.reason }

// ResumePolicy says what happens to an interrupted run. Every policy keeps
// the op id.
type ResumePolicy int

const (
	// ResumeContinue: Batch and Fixer apply re-lease the chunks the ledger
	// does not record as complete; Tasks resume with rc.Restore. The Batch
	// default for Snapshot and AppendOnly sources.
	ResumeContinue ResumePolicy = iota + 1
	// ResumeFromZero: start over under the same op id; needs idempotent work.
	// The Batch default for an Unordered source.
	ResumeFromZero
	// ResumeDrop: the interrupted run ends as dropped. The Task default.
	ResumeDrop
	// ResumeAsk: wait in awaiting_decision for a person. The Fixer default.
	ResumeAsk
)

// String names the policy.
func (p ResumePolicy) String() string {
	switch p {
	case ResumeContinue:
		return "continue"
	case ResumeFromZero:
		return "from_zero"
	case ResumeDrop:
		return "drop"
	case ResumeAsk:
		return "ask"
	default:
		return "default"
	}
}
