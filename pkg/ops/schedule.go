// file: pkg/ops/schedule.go
// version: 1.0.0
// guid: 569e684d-fb56-46ee-bd07-ce7a4ac835bb
// last-edited: 2026-10-13

package ops

import (
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// Schedule is the single timer declaration of a def. 05-PR8 evaluates it;
// this PR validates it at construction so a bad schedule fails
// ValidateCatalog instead of never firing.
type Schedule struct {
	kind    scheduleKind
	every   time.Duration
	at      string
	expr    string
	order   int
	onStart bool
	when    func() bool
	params  json.RawMessage
	live    bool
	err     error
}

type scheduleKind int

const (
	schedEvery scheduleKind = iota + 1
	schedDaily
	schedCron
	schedWindow
)

// Every fires on a durable interval clock (v2 interval_clock.go).
func Every(d time.Duration) *Schedule {
	s := &Schedule{kind: schedEvery, every: d}
	if d <= 0 {
		s.err = fmt.Errorf("Every(%s): interval must be positive", d)
	}
	return s
}

// DailyAt fires once a day at hh:mm server-local time (v2 daily_at.go).
func DailyAt(hhmm string) *Schedule {
	s := &Schedule{kind: schedDaily, at: hhmm}
	if _, err := time.Parse("15:04", hhmm); err != nil || len(hhmm) != 5 {
		s.err = fmt.Errorf("DailyAt(%q): want HH:MM", hhmm)
	}
	return s
}

// Cron fires on a five-field cron expression, validated here.
func Cron(expr string) *Schedule {
	s := &Schedule{kind: schedCron, expr: expr}
	s.err = validateCron(expr)
	return s
}

// InMaintenanceWindow makes the def a member of the maintenance window
// pipeline, run in ascending order.
func InMaintenanceWindow(order int) *Schedule {
	s := &Schedule{kind: schedWindow, order: order}
	if order < 0 {
		s.err = fmt.Errorf("InMaintenanceWindow(%d): order must not be negative", order)
	}
	return s
}

// OnStart also fires once after the server starts.
func (s *Schedule) OnStart() *Schedule { s.onStart = true; return s }

// When gates every fire on enabled, re-read each tick.
func (s *Schedule) When(enabled func() bool) *Schedule { s.when = enabled; return s }

// Params sets the params of scheduled runs. A scheduled run is Preview unless
// Live is also called.
func (s *Schedule) Params(p any) *Schedule {
	raw, err := json.Marshal(p)
	if err != nil {
		s.err = errors.Join(s.err, fmt.Errorf("Params: %w", err))
		return s
	}
	s.params = raw
	return s
}

// Live makes scheduled runs Live. Scheduled writers must say so explicitly
// (D8; enforced by 05-PR8).
func (s *Schedule) Live() *Schedule { s.live = true; return s }

// Err is the validation error, or nil.
func (s *Schedule) Err() error { return s.err }

// IsLive reports whether scheduled runs are Live.
func (s *Schedule) IsLive() bool { return s.live }

// RunParams is the params of scheduled runs, or nil.
func (s *Schedule) RunParams() json.RawMessage { return s.params }

// Enabled reports the When gate (true without one).
func (s *Schedule) Enabled() bool { return s.when == nil || s.when() }

// String describes the schedule for the def list.
func (s *Schedule) String() string {
	var b string
	switch s.kind {
	case schedEvery:
		b = "every " + s.every.String()
	case schedDaily:
		b = "daily at " + s.at
	case schedCron:
		b = "cron " + s.expr
	case schedWindow:
		b = "maintenance window #" + strconv.Itoa(s.order)
	}
	if s.onStart {
		b += ", on start"
	}
	if s.live {
		b += ", live"
	}
	return b
}

// cronBounds are the inclusive ranges of the five cron fields.
var cronBounds = [5][2]int{{0, 59}, {0, 23}, {1, 31}, {1, 12}, {0, 7}}

// validateCron checks a five-field expression: each field is "*", or a comma
// list of n, a-b, with an optional /step. Names (JAN, MON) and macros
// (@daily) are not accepted.
func validateCron(expr string) error {
	fields := strings.Fields(expr)
	if len(fields) != 5 {
		return fmt.Errorf("cron %q: want 5 fields, got %d", expr, len(fields))
	}
	for i, f := range fields {
		for _, part := range strings.Split(f, ",") {
			if err := validateCronPart(part, cronBounds[i]); err != nil {
				return fmt.Errorf("cron %q field %d: %w", expr, i+1, err)
			}
		}
	}
	return nil
}

func validateCronPart(part string, bounds [2]int) error {
	rng, step, hasStep := strings.Cut(part, "/")
	if hasStep {
		n, err := strconv.Atoi(step)
		if err != nil || n <= 0 {
			return fmt.Errorf("bad step %q", step)
		}
	}
	if rng == "*" {
		return nil
	}
	lo, hi, isRange := strings.Cut(rng, "-")
	nums := []string{lo}
	if isRange {
		nums = append(nums, hi)
	}
	vals := make([]int, 0, 2)
	for _, s := range nums {
		n, err := strconv.Atoi(s)
		if err != nil || n < bounds[0] || n > bounds[1] {
			return fmt.Errorf("value %q out of range %d-%d", s, bounds[0], bounds[1])
		}
		vals = append(vals, n)
	}
	if isRange && vals[0] > vals[1] {
		return fmt.Errorf("range %q is backwards", rng)
	}
	return nil
}
