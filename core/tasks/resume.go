// file: core/tasks/resume.go
// version: 1.0.0
// guid: 5e169cfc-8547-4db3-a3af-c8f48bc13dd8
// last-edited: 2026-11-10

package tasks

import (
	"fmt"
	"time"
)

// ResumePolicy decides what an interrupted run becomes at the next boot.
type ResumePolicy uint8 // zero = unset, refused at registration (A2)

const (
	ResumeContinue ResumePolicy = iota + 1 // Task: Restore checkpoint; Batch/Fixer: chunk ledger. v2 Restart, v3 Continue
	ResumeFromZero                         // same RunID, from zero; idempotent work. v2 Requeue
	ResumeDrop                             // interrupted -> dropped. v2 Drop
	ResumeAsk                              // interrupted -> awaiting_decision. v2 Ask
)

// String names the policy ("continue", "from_zero", "drop", "ask"), or "unset".
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
		return "unset"
	}
}

func (p ResumePolicy) valid() bool { return p >= ResumeContinue && p <= ResumeAsk }

// CancelContract is how a run answers a cancel. The zero value means the
// shape default: AtItemBoundaries for Batch/Fixer, Cooperative(30s) for Task.
type CancelContract struct {
	kind   cancelKind
	grace  time.Duration
	reason string
	budget time.Duration
}

type cancelKind uint8

const (
	cancelDefault cancelKind = iota
	cancelItems
	cancelCooperative
	cancelNever
)

// DefaultCancelGrace is a Task's default Cooperative grace. Name coined by F2-01.
const DefaultCancelGrace = 30 * time.Second

// AtItemBoundaries stops at the next item boundary: the Batch/Fixer default.
func AtItemBoundaries() CancelContract { return CancelContract{kind: cancelItems} }

// Cooperative cancels the run context and waits grace for the run to return
// before revoking its fence: the Task default is Cooperative(30*time.Second).
func Cooperative(grace time.Duration) CancelContract {
	return CancelContract{kind: cancelCooperative, grace: grace}
}

// Uninterruptible declares a run that cannot stop early, with why and the
// longest it may run past a cancel; listed at WARN on boot.
func Uninterruptible(reason string, budget time.Duration) CancelContract {
	return CancelContract{kind: cancelNever, reason: reason, budget: budget}
}

// IsZero reports the zero value (shape default). Accessor coined by F2-01.
func (c CancelContract) IsZero() bool { return c.kind == cancelDefault }

// IsUninterruptible reports Uninterruptible. Accessor coined by F2-01.
func (c CancelContract) IsUninterruptible() bool { return c.kind == cancelNever }

// Grace is Cooperative's grace. Accessor coined by F2-01.
func (c CancelContract) Grace() time.Duration { return c.grace }

// Budget is Uninterruptible's budget. Accessor coined by F2-01.
func (c CancelContract) Budget() time.Duration { return c.budget }

// Reason is Uninterruptible's reason. Accessor coined by F2-01.
func (c CancelContract) Reason() string { return c.reason }

func (c CancelContract) problems() []string {
	switch c.kind {
	case cancelCooperative:
		if c.grace <= 0 {
			return []string{fmt.Sprintf("Cancel: Cooperative(%s) needs a positive grace", c.grace)}
		}
	case cancelNever:
		var out []string
		if c.reason == "" {
			out = append(out, "Cancel: Uninterruptible needs a reason")
		}
		if c.budget <= 0 {
			out = append(out, fmt.Sprintf("Cancel: Uninterruptible budget %s must be positive", c.budget))
		}
		return out
	}
	return nil
}
