// file: core/internal/scheduler/clock.go
// version: 1.0.0
// guid: a3d82d6e-1ead-4517-9f64-a8ba9338e744
// last-edited: 2026-11-11

package scheduler

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"time"

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

// timing is one effective schedule: when it comes due next.
type timing interface {
	// next is the first due instant strictly after `after`.
	next(after time.Time, loc *time.Location) (time.Time, bool)
}

// interval is ScheduleConfig.Every, and the relay's fixed tasks.Every.
type interval time.Duration

func (d interval) next(after time.Time, _ *time.Location) (time.Time, bool) {
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

// effectiveOf checks c against 5T.3.8's value rules (exactly one of Every,
// DailyAt, Cron and Window when enabled; Every >= core.tasks.schedule_poll)
// and returns its timing. A disabled value is not checked: "off" needs no
// timing.
func effectiveOf(c tasks.ScheduleConfig, poll time.Duration, loc *time.Location) (effective, error) {
	if !c.Enabled {
		return effective{}, nil
	}
	set := 0
	for _, on := range []bool{c.Every != 0, c.DailyAt != "", c.Cron != "", c.Window != ""} {
		if on {
			set++
		}
	}
	if set != 1 {
		return effective{}, fmt.Errorf("%w: exactly one of every, daily_at, cron and window must be set when enabled (got %d)", errConfig, set)
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
		d, err := parseDailyAt(c.DailyAt)
		if err != nil {
			return effective{}, fmt.Errorf("%w: %v", errConfig, err)
		}
		return effective{enabled: true, t: d, hash: hashOf("daily_at", c.DailyAt, zone)}, nil
	case c.Cron != "":
		s, err := parseCron(c.Cron)
		if err != nil {
			return effective{}, fmt.Errorf("%w: %v", errConfig, err)
		}
		return effective{enabled: true, t: s, hash: hashOf("cron", c.Cron, zone)}, nil
	default:
		if tasks.WindowKind(c.Window) == "" {
			return effective{}, fmt.Errorf("%w: window %q is not a config segment", errConfig, c.Window)
		}
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
	now    time.Time
	poll   time.Duration // core.tasks.schedule_poll: the freshness bound of SkipMissed
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
	missed int         // instants this check counts as missed
}

// decide is one due check of a kind with an interval, DailyAt, Cron or fixed
// timing, from the clock the poll read (had false: no clock).
//
//   - First sight seeds LastDue = now and does not fire (no boot stampede).
//   - Disabled: the first poll that sees it stamps GateClosedAt; nothing fires.
//   - Not yet due: nothing is written (the re-open edge excepted, which clears
//     GateClosedAt).
//   - Due: LastDue = now, always. Coalesce fires once for any number of due
//     instants since LastDue and counts the others as missed; SkipMissed
//     fires only when the latest instant is no older than one poll (the
//     process was up for it) and otherwise counts them all and waits for the
//     next. On the re-open edge nothing counts as missed: the instants passed
//     while the key was disabled.
func decide(prev tasks.Clock, had bool, in pollInput) decision {
	now := in.now
	if !had {
		seed := tasks.Clock{LastDue: now, ScheduleHash: in.eff.hash}
		if !in.eff.enabled {
			seed.GateClosedAt = now
		}
		return decision{write: true, next: seed}
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
	first, ok := in.eff.t.next(prev.LastDue, in.loc)
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
	d := decision{write: true, next: prev}
	d.next.LastDue = now
	d.next.GateClosedAt = time.Time{}
	d.next.ScheduleHash = in.eff.hash
	switch in.missed {
	case tasks.SkipMissed:
		d.fire = now.Sub(latest) <= in.poll
		d.dueAt = latest
		d.missed = n
		if d.fire {
			d.missed = n - 1
		}
	default: // Coalesce
		d.fire = true
		d.dueAt = first
		d.missed = n - 1
	}
	if reopen {
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
		nt, more := t.next(latest, loc)
		if !more || nt.After(now) {
			break
		}
		n, latest = n+1, nt
	}
	return n, latest
}
