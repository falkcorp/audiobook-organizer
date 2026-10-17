// file: pkg/ops/schedule.go
// version: 1.2.0
// guid: 569e684d-fb56-46ee-bd07-ce7a4ac835bb
// last-edited: 2026-10-17

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
	preview bool
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

// Live makes scheduled runs Live. A scheduled writer must say Live or Preview
// explicitly (D8; enforced by opscatalog.ValidateCatalog). Declaring both is
// an error.
func (s *Schedule) Live() *Schedule {
	s.live = true
	s.checkMode()
	return s
}

// Preview says a scheduled writer is deliberately a Preview run: the nightly
// job reports what it would change and writes nothing. Exactly one of Live and
// Preview satisfies D8 for a scheduled writer.
func (s *Schedule) Preview() *Schedule {
	s.preview = true
	s.checkMode()
	return s
}

func (s *Schedule) checkMode() {
	if s.live && s.preview && s.err == nil {
		s.err = errors.New("schedule declares both Live() and Preview(); say exactly one")
	}
}

// Err is the validation error, or nil.
func (s *Schedule) Err() error { return s.err }

// IsLive reports whether scheduled runs are Live.
func (s *Schedule) IsLive() bool { return s.live }

// IsPreview reports whether scheduled runs are declared Preview.
func (s *Schedule) IsPreview() bool { return s.preview }

// The accessors below are how the TaskScheduler reads a declared timer; the
// value is built by exactly one of Every, DailyAt, Cron and InMaintenanceWindow.

// TimerKind names the timer: "every", "daily_at", "cron" or "window".
func (s *Schedule) TimerKind() string {
	switch s.kind {
	case schedEvery:
		return "every"
	case schedDaily:
		return "daily_at"
	case schedCron:
		return "cron"
	case schedWindow:
		return "window"
	}
	return ""
}

// Interval is the Every period, or 0.
func (s *Schedule) Interval() time.Duration { return s.every }

// At is the DailyAt "HH:MM", or "".
func (s *Schedule) At() string { return s.at }

// Expr is the Cron expression, or "".
func (s *Schedule) Expr() string { return s.expr }

// Order is the InMaintenanceWindow position.
func (s *Schedule) Order() int { return s.order }

// RunsOnStart reports whether OnStart was called.
func (s *Schedule) RunsOnStart() bool { return s.onStart }

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
	if s.preview {
		b += ", preview"
	}
	return b
}

// cronBounds are the inclusive ranges of the five cron fields. Day-of-week
// accepts 7 as Sunday, as standard cron does; it is folded onto 0 when parsed.
var cronBounds = [5][2]int{{0, 59}, {0, 23}, {1, 31}, {1, 12}, {0, 7}}

var cronFieldNames = [5]string{"minute", "hour", "day-of-month", "month", "day-of-week"}

// CronFields is a parsed five-field cron expression: one bitset per field
// (bit v set when value v matches), plus whether day-of-month and day-of-week
// started with "*" (standard cron ORs the two only when neither does). It is
// the one cron grammar in the module; the scheduler evaluates it.
type CronFields struct {
	Minute, Hour, DayOfMonth, Month, DayOfWeek uint64
	DayOfMonthStar, DayOfWeekStar              bool
}

// ParseCronFields parses "m h dom mon dow". Each field is "*", or a comma list
// of n, a-b, with an optional /step ("*/n", "a-b/n"; "a/n" runs from a to the
// end of the range). Names (JAN, MON), "?", "L", "W", "#", signs ("+5"),
// seconds, years and macros such as @daily are errors that name the field.
func ParseCronFields(expr string) (CronFields, error) {
	fields := strings.Fields(expr)
	if len(fields) != 5 {
		return CronFields{}, fmt.Errorf("cron %q: want 5 fields, got %d", expr, len(fields))
	}
	var sets [5]uint64
	for i, f := range fields {
		set, err := parseCronField(f, cronBounds[i])
		if err != nil {
			return CronFields{}, fmt.Errorf("cron %q: %s field %q: %w", expr, cronFieldNames[i], f, err)
		}
		sets[i] = set
	}
	if sets[4]&(1<<7) != 0 { // 7 is Sunday: fold it onto 0
		sets[4] = sets[4]&^(1<<7) | 1
	}
	return CronFields{
		Minute: sets[0], Hour: sets[1], DayOfMonth: sets[2], Month: sets[3], DayOfWeek: sets[4],
		DayOfMonthStar: strings.HasPrefix(fields[2], "*"), DayOfWeekStar: strings.HasPrefix(fields[4], "*"),
	}, nil
}

func validateCron(expr string) error {
	_, err := ParseCronFields(expr)
	return err
}

func parseCronField(f string, bounds [2]int) (uint64, error) {
	var set uint64
	for _, part := range strings.Split(f, ",") {
		rng, stepStr, hasStep := strings.Cut(part, "/")
		step := 1
		if hasStep {
			n, err := cronNum(stepStr)
			if err != nil || n <= 0 {
				return 0, fmt.Errorf("bad step %q", stepStr)
			}
			step = n
		}
		lo, hi := bounds[0], bounds[1]
		switch {
		case rng == "*":
		case strings.Contains(rng, "-"):
			a, b, _ := strings.Cut(rng, "-")
			var err error
			if lo, err = cronValue(a, bounds); err != nil {
				return 0, err
			}
			if hi, err = cronValue(b, bounds); err != nil {
				return 0, err
			}
			if lo > hi {
				return 0, fmt.Errorf("range %q is backwards", rng)
			}
		default:
			n, err := cronValue(rng, bounds)
			if err != nil {
				return 0, err
			}
			lo, hi = n, n
			if hasStep {
				hi = bounds[1] // "a/n" runs from a to the end, as in Vixie cron
			}
		}
		for v := lo; v <= hi; v += step {
			set |= 1 << uint(v)
		}
	}
	return set, nil
}

// cronNum parses an unsigned decimal. strconv.Atoi would also take "+5".
func cronNum(s string) (int, error) {
	if s == "" {
		return 0, errors.New("empty")
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return 0, fmt.Errorf("%q is not a number", s)
		}
	}
	return strconv.Atoi(s)
}

func cronValue(s string, bounds [2]int) (int, error) {
	n, err := cronNum(s)
	if err != nil {
		return 0, fmt.Errorf("%q is not a number (names, signs, ?, L, W and # are not supported)", s)
	}
	if n < bounds[0] || n > bounds[1] {
		return 0, fmt.Errorf("value %d out of range %d-%d", n, bounds[0], bounds[1])
	}
	return n, nil
}
