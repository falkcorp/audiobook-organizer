// file: internal/taskkit/schedule.go
// version: 1.0.0
// guid: 4c1f8b7e-3a52-4d96-8e0b-6f2d9a7c1e43
// last-edited: 2026-11-28

// Package taskkit holds the one helper every books task constructor shares for
// declaring a schedule: the schedule and the SystemStart reason it needs are
// set in the same statement, so a kind can not carry one without the other.
package taskkit

import "github.com/falkcorp/mediacore/tasks"

// ScheduledStartReason is the reviewed reason (tasks.Permissions.SystemStart) of
// every scheduled books kind: the scheduler's tick starts the kind as the books
// system actor, which cannot hold a Start that asks for core:system.manage or
// another AuthRequired scope (the settings.manage expansion does), and the
// schedule's own authority (05-tasks 5T.3.8) is what runs it. A person's or an
// API key's start of the same kind is judged on Start alone.
const ScheduledStartReason = "started by the scheduler's tick as the books system actor, which cannot hold a Start that asks for an AuthRequired scope (core:system.manage in the settings.manage expansion); a person's or an API key's start of the kind keeps Start"

// SetSchedule applies a module's schedule hook to c for kind and, when the hook
// declares a schedule, sets Perms.SystemStart in the same statement. A nil hook
// or a hook that returns nil (the kind is not scheduled) changes nothing, so an
// unscheduled kind never carries the flag.
func SetSchedule(c *tasks.Common, kind tasks.Kind, sched func(tasks.Kind) *tasks.Schedule) {
	if sched == nil {
		return
	}
	s := sched(kind)
	if s == nil {
		return
	}
	c.Schedule = s
	c.Perms.SystemStart = ScheduledStartReason
}

// Scheduled returns p with Perms.SystemStart set to ScheduledStartReason when
// the kind is scheduled (sched is non-nil), for the constructors that receive
// the schedule itself instead of the hook. An unscheduled kind (nil) is
// returned unchanged.
func Scheduled(p tasks.Permissions, sched *tasks.Schedule) tasks.Permissions {
	if sched != nil {
		p.SystemStart = ScheduledStartReason
	}
	return p
}
