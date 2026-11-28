// file: core/kernel/system_start.go
// version: 1.0.0
// guid: 5a9d2e71-6c43-4b18-8f0a-3d7e1c9b4a65
// last-edited: 2026-11-28

package kernel

import (
	"fmt"

	"github.com/falkcorp/mediacore/internal/executor"
	"github.com/falkcorp/mediacore/module"
	"github.com/falkcorp/mediacore/outbox"
	"github.com/falkcorp/mediacore/tasks"
)

// P7's check of Permissions.SystemStart (05-tasks 5T.3.8; the executor's
// admitsSystemStart is the rule). A kind carries the flag exactly when the
// executor itself starts it on the system's behalf, and the executor's REAL
// start rule is evaluated for every such start, so the boot can not drift from
// what StartIn does at run time:
//
//   - declarations (systemStartDeclProblems, pure, so Describe shows them): a
//     flagged kind is declared by a Schedule of its own or by a ConsumeIntoTask
//     consumer of its own module; a flag with neither is an unused grant and is
//     refused, with the outbox wired or not. A consumer's target of another
//     module is refused.
//   - the rule (systemStartRuleProblems, with the built executor): every
//     scheduled kind is started under CheckSchedule, every consumer's target
//     under CheckOutbox, and each of them under CheckEnqueue, by
//     executor.CheckSystemStart, the function permitStart answers. A scheduled
//     kind whose Start its module's system actor cannot hold and that has no
//     flag is refused here, with the refusal StartIn would give, so a schedule
//     that could never fire fails the boot instead of every tick.
//
// An rc.Enqueue by a running system-started run has no declaration of its own
// (the SDK has no field that names what a run enqueues): a kind needs the
// flag for it only as a scheduled kind or a consumer's target as well.

// outboxTaskTargets is the ConsumeIntoTask consumers declared at boot. The
// kernel's outbox is not built yet: nothing in boot.go builds a kernelOutbox
// until F6-03 wires Manifest.OutboxConsumers and Registrar.OutboxHandler
// (outbox.go), so there is none to list and no kind may carry SystemStart for a
// consumer; the owner sets the flag only when it declares the consumer (books:
// KindDeps.RelocationConsumer). F6-03 returns ko.ob.TaskConsumers() here.
func (k *Kernel) outboxTaskTargets() []outbox.TaskConsumer { return nil }

// systemStartDeclProblems is the declaration half of the check, over defs and
// the declared consumer targets.
func systemStartDeclProblems(defs []tasks.Definition, targets []outbox.TaskConsumer) []module.Problem {
	var probs []module.Problem
	target := make(map[tasks.Kind]bool, len(targets))
	for _, c := range targets {
		if c.Owner != c.Kind.Module() {
			probs = append(probs, module.NewProblem(module.ID(c.Owner), module.ProblemInvalidRegistration,
				fmt.Errorf("outbox consumer %s starts %s, a kind of module %s: a consumer starts its own module's kinds only", c.Name, c.Kind, c.Kind.Module())))
			continue
		}
		target[c.Kind] = true
	}
	for _, d := range defs {
		if d == nil {
			continue
		}
		c := tasks.CommonOf(d)
		if c.Perms.SystemStart == "" || c.Schedule != nil || target[d.Kind()] {
			continue
		}
		probs = append(probs, module.NewProblem(module.ID(d.Kind().Module()), module.ProblemInvalidRegistration,
			fmt.Errorf("task kind %s carries Permissions.SystemStart but declares no Schedule and is no outbox consumer's target: the grant is unused", d.Kind())))
	}
	return probs
}

// systemStartRuleProblems is the rule half of the check: the executor's real
// start rule over every declared start of every kind.
func systemStartRuleProblems(x tasks.Executor, defs []tasks.Definition, targets []outbox.TaskConsumer) []module.Problem {
	var probs []module.Problem
	check := func(owner string, kind tasks.Kind, from executor.CheckOrigin, what string) {
		if err := executor.CheckSystemStart(x, owner, kind, from); err != nil {
			probs = append(probs, module.NewProblem(module.ID(owner), module.ProblemInvalidRegistration,
				fmt.Errorf("%s %s: its owner's system actor may not start it from the %s origin (%w)", what, kind, from, err)))
		}
	}
	for _, d := range defs {
		if d == nil || tasks.CommonOf(d).Schedule == nil {
			continue
		}
		owner := d.Kind().Module()
		check(owner, d.Kind(), executor.CheckSchedule, "scheduled task kind")
		check(owner, d.Kind(), executor.CheckEnqueue, "scheduled task kind")
	}
	for _, c := range targets {
		if c.Owner != c.Kind.Module() {
			continue // refused by systemStartDeclProblems; the rule is the owner's alone
		}
		check(c.Owner, c.Kind, executor.CheckOutbox, "outbox consumer "+c.Name+" target")
		check(c.Owner, c.Kind, executor.CheckEnqueue, "outbox consumer "+c.Name+" target")
	}
	return probs
}
