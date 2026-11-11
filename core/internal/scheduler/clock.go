// file: core/internal/scheduler/clock.go
// version: 1.0.1
// guid: a3d82d6e-1ead-4517-9f64-a8ba9338e744
// last-edited: 2026-11-11

package scheduler

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"time"

	"github.com/falkcorp/mediacore/internal/schedspec"
	"github.com/falkcorp/mediacore/tasks"
)

// The durable clock (05-tasks 5T.3.8). One tasks.Clock per scheduled kind,
// read and written only through tasks.ClockStore. Its fields mean:
//
//	LastDue       the last due check: the poll that found the kind due (or
//	              seeded it), stamped with that poll's time, whether or not
//	              the When gate let it enqueue (HEAD's interval clock,
//	              interval_clock.go: ticker semantics, so a gated kind is
//	              re-checked one period later, not on every poll)
//	LastFire      the due instant of the last fire the When gate let through
//	LastRun       when the scheduler last started a run of the kind, or found
//	              one active and folded into it
//	Missed        the due instants never fired for, summed over the clock's life
//	GateClosedAt  set while the key's enabled flag is false (the close edge);
//	              due instants that pass while it is set are not missed
//	ScheduleHash  the effective timing the clock was last written under;
//	              empty on every clock the cutover imports
//
// The next due instant is always derived from LastDue and the current
// ScheduleConfig, so an imported clock (empty hash) continues where HEAD left
// it and a changed config re-times the next fire without re-seeding (a
// changed Every keeps LastDue, as does a switch between timings: HEAD's
// clocks kept their history across that switch, daily_at.go "PERSISTENCE").

// maxCount caps the instants counted for one fire (HEAD's countDue cap).
const maxCount = 1000

// timing is one effective schedule: when it comes due next. schedspec.Cron
// (Cron and DailyAt) and interval implement it.
type timing interface {
	// Next is the first due instant strictly after `after`.
	Next(after time.Time, loc *time.Location) (time.Time, bool)
}

// interval is ScheduleConfig.Every, and the relay's fixed tasks.Every.
type interval time.Duration

// Next is after plus the interval.
func (d interval) Next(after time.Time, _ *time.Location) (time.Time, bool) {
	return after.Add(time.Duration(d)), true
}

// effective is one kind's schedule as the current config states it.
type effective struct {
	enabled bool
	window  string // a window member: the window runs it, its own clock is not polled
	t       timing // nil when disabled or a window member
	hash    string
}

// errConfig wraps a schedule value the scheduler cannot run: it counts as a
// schedule error and the poll skips the kind.
var errConfig = errors.New("scheduler: invalid schedule config")

// effectiveOf checks an enabled c (tasks.ScheduleConfig.Validate, the rules
// config already enforced at load, re-checked because the scheduler must
// never run a value it cannot parse; and Every >= core.tasks.schedule_poll,
// the one rule that reads another key) and returns its timing. A disabled
// value is effective{} unchecked: "off" needs no timing.
func effectiveOf(c tasks.ScheduleConfig, poll time.Duration, loc *time.Location) (effective, error) {
	if !c.Enabled {
		return effective{}, nil
	}
	if err := c.Validate(); err != nil {
		return effective{}, fmt.Errorf("%w: %v", errConfig, err)
	}
	zone := loc.String()
	switch {
	case c.Every != 0:
		d := c.Every.Std()
		if d < poll {
			return effective{}, fmt.Errorf("%w: every %s is below core.tasks.schedule_poll %s", errConfig, d, poll)
		}
		return effective{enabled: true, t: interval(d), hash: hashOf("every", d.String())}, nil
	case c.DailyAt != "":
		d, err := schedspec.ParseDailyAt(c.DailyAt)
		if err != nil {
			return effective{}, fmt.Errorf("%w: %v", errConfig, err)
		}
		return effective{enabled: true, t: d, hash: hashOf("daily_at", c.DailyAt, zone)}, nil
	case c.Cron != "":
		cr, err := schedspec.ParseCron(c.Cron)
		if err != nil {
			return effective{}, fmt.Errorf("%w: %v", errConfig, err)
		}
		return effective{enabled: true, t: cr, hash: hashOf("cron", c.Cron, zone)}, nil
	default:
		return effective{enabled: true, window: c.Window, hash: hashOf("window", c.Window)}, nil
	}
}

// hashOf is a ScheduleHash: a short digest of the timing's class and value.
func hashOf(parts ...string) string {
	h := sha256.New()
	for _, p := range parts {
		h.Write([]byte(p))
		h.Write([]byte{0})
	}
	return hex.EncodeToString(h.Sum(nil)[:8])
}

// pollInput is what one due check decides from, besides the stored clock.
type pollInput struct {
	now time.Time
	// since is when this process last polled, or booted when it has not
	// polled yet: a due instant at or after it came due while the process was
	// up. Zero when Poll runs on a scheduler that never booted; SkipMissed then
	// falls back to one poll of freshness.
	since  time.Time
	poll   time.Duration // core.tasks.schedule_poll
	loc    *time.Location
	eff    effective
	missed tasks.MissedPolicy
}

// decision is one due check's outcome.
type decision struct {
	write  bool        // stage Advance(prev -> next)
	next   tasks.Clock //
	fire   bool        // a due instant fires (subject to the When gate)
	dueAt  time.Time   // the instant that fires: lag is measured from it
	at     time.Time   // the poll that fires: LastRun when a run starts or folds
	missed int         // instants this check counts as missed
}

// decide is one due check of a kind with an interval, DailyAt, Cron or fixed
// timing, from the clock the poll read (had false: no clock).
//
//   - First sight of an enabled kind seeds LastDue = now and does not fire (no
//     boot stampede). First sight of a disabled kind writes nothing, so the
//     kind is seeded by the first poll that finds it enabled and runs one
//     period after that, as HEAD does (v3_schedule.go pollV3Entry returns
//     before the claim while the gate is closed; interval_clock.go
//     claimIntervalRun seeds on the first observation and declines).
//   - Disabled with a clock: the first poll that sees it stamps GateClosedAt;
//     nothing fires.
//   - Not yet due: nothing is written (the re-open edge excepted, which clears
//     GateClosedAt).
//   - Due: LastDue = now, always. Coalesce fires once for any number of due
//     instants since LastDue and counts the others as missed. SkipMissed
//     fires only when the latest instant came due while this process was up
//     (at or after its previous poll, or its boot), and otherwise counts them
//     all and waits for the next: a slow or delayed poll is not downtime.
//   - On the re-open edge the kind's Missed policy applies as for any other
//     gap: Coalesce fires once, due at the edge itself (the poll that found it
//     enabled, so its lag is measured from there and not from instants that
//     passed while the key was off), and SkipMissed fires only for an instant
//     that is fresh. Nothing counts as missed: those instants passed while the
//     key was disabled. Nothing counts as missed either when
//     the schedule changed since the clock was written (a non-empty hash that
//     differs): the instants were the old schedule's.
func decide(prev tasks.Clock, had bool, in pollInput) decision {
	now := in.now
	if !had {
		if !in.eff.enabled {
			return decision{}
		}
		return decision{write: true, next: tasks.Clock{LastDue: now, ScheduleHash: in.eff.hash}}
	}
	if !in.eff.enabled {
		if !prev.GateClosedAt.IsZero() {
			return decision{}
		}
		next := prev
		next.GateClosedAt = now
		return decision{write: true, next: next}
	}
	reopen := !prev.GateClosedAt.IsZero()
	first, ok := in.eff.t.Next(prev.LastDue, in.loc)
	if !ok || now.Before(first) {
		if !reopen {
			return decision{}
		}
		next := prev
		next.GateClosedAt = time.Time{}
		next.ScheduleHash = in.eff.hash
		return decision{write: true, next: next}
	}
	n, latest := countDue(in.eff.t, first, now, in.loc)
	d := decision{write: true, next: prev, at: now}
	d.next.LastDue = now
	d.next.GateClosedAt = time.Time{}
	d.next.ScheduleHash = in.eff.hash
	switch {
	case in.missed == tasks.SkipMissed:
		if in.since.IsZero() {
			d.fire = now.Sub(latest) <= in.poll
		} else {
			d.fire = !latest.Before(in.since)
		}
		d.dueAt = latest
		d.missed = n
		if d.fire {
			d.missed = n - 1
		}
	default: // Coalesce
		d.fire = true
		d.dueAt = first
		if reopen {
			d.dueAt = now // lag is measured from the open edge, not from instants that passed while off
		}
		d.missed = n - 1
	}
	if reopen || (prev.ScheduleHash != "" && prev.ScheduleHash != in.eff.hash) {
		d.missed = 0
	}
	d.next.Missed += d.missed
	return d
}

// countDue counts the due instants in [first, now] (first is due) and
// returns the latest, stepping from one instant to the next (capped at
// maxCount, HEAD's countDue). An interval is counted arithmetically.
func countDue(t timing, first, now time.Time, loc *time.Location) (n int, latest time.Time) {
	if d, ok := t.(interval); ok && d > 0 {
		k := int64(now.Sub(first) / time.Duration(d))
		k = min(k, maxCount-1)
		return int(k) + 1, first.Add(time.Duration(k) * time.Duration(d))
	}
	n, latest = 1, first
	for n < maxCount {
		nt, more := t.Next(latest, loc)
		if !more || nt.After(now) {
			break
		}
		n, latest = n+1, nt
	}
	return n, latest
}
