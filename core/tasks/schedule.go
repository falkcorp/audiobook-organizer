// file: core/tasks/schedule.go
// version: 1.1.0
// guid: 231e5481-a9cd-411a-9926-86fc95b5dc49
// last-edited: 2026-11-11

package tasks

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/falkcorp/mediacore/config"
)

// Schedule is a definition's one timer declaration (5T.3.8). Build it with
// ScheduleFromConfig (every kind but the outbox relay) or Every (the relay
// only); a malformed declaration is recorded on the Schedule and reported as
// a constructor problem of the definition that carries it. A Schedule is a
// value: each builder method returns a changed copy and leaves its receiver
// as it was, so one base schedule can seed several definitions.
type Schedule struct {
	key      string        // ScheduleFromConfig
	every    time.Duration // Every
	onStart  bool
	when     func() bool
	params   json.RawMessage
	live     bool
	preview  bool
	missed   MissedPolicy
	problems []string
}

// ScheduleFromConfig is the standard schedule (SIMP-6). Its timing is a config
// value of type ScheduleConfig at key, which the kind's owner declares in its
// own config schema with a default; tasks re-reads it every poll.
// The one key rule (R2-7): key has the form
//
//	"<owner>.<subsystem>.schedules.<name>"
//
// where <owner> is "core" for a core kind and "modules.<id>" for a kind of
// module <id> (the registering owner only), and every segment matches the
// config grammar [a-z][a-z0-9_]{0,47} (no hyphens). The key must be declared
// by that owner with type ScheduleConfig, and no other kind may use it.
// Anything else fails registration. Examples: "core.ai.schedules.batch_poll",
// "core.acquire.schedules.rss_sync", "core.updater.schedules.check",
// "modules.books.scheduler.schedules.library_scan_full".
//
// This constructor checks the key's FORM only and records a malformed key as
// a problem; the owner, the declared type and uniqueness are the kernel's
// checks at registration.
func ScheduleFromConfig(key string) *Schedule {
	s := &Schedule{key: key}
	if err := checkScheduleKey(key); err != nil {
		s.problems = append(s.problems, err.Error())
	}
	return s
}

// checkScheduleKey checks the R2-7 key form.
func checkScheduleKey(key string) error {
	segs := strings.Split(key, ".")
	for i, seg := range segs {
		if !configSegment(seg) {
			return fmt.Errorf("schedule key %q: segment %d (%q) does not match [a-z][a-z0-9_]{0,47}", key, i+1, seg)
		}
	}
	var rest []string
	switch {
	case len(segs) == 4 && segs[0] == "core":
		rest = segs[1:]
	case len(segs) == 5 && segs[0] == "modules":
		rest = segs[2:]
	default:
		return fmt.Errorf("schedule key %q: want core.<subsystem>.schedules.<name> or modules.<id>.<subsystem>.schedules.<name>", key)
	}
	if rest[1] != "schedules" || rest[0] == "schedules" {
		return fmt.Errorf("schedule key %q: want core.<subsystem>.schedules.<name> or modules.<id>.<subsystem>.schedules.<name>", key)
	}
	return nil
}

// configSegment reports whether seg matches the config path grammar
// [a-z][a-z0-9_]{0,47}.
func configSegment(seg string) bool {
	if seg == "" || len(seg) > 48 || seg[0] < 'a' || seg[0] > 'z' {
		return false
	}
	for i := 1; i < len(seg); i++ {
		c := seg[i]
		if (c < 'a' || c > 'z') && (c < '0' || c > '9') && c != '_' {
			return false
		}
	}
	return true
}

// ScheduleConfig is the value type of every schedule key. Exactly one of
// Every, DailyAt, Cron and Window is set when Enabled is true.
type ScheduleConfig struct {
	Enabled bool            `json:"enabled"`
	Every   config.Duration `json:"every,omitempty"`    // >= core.tasks.schedule_poll; a string such as "24h" in defaults and files (05-config)
	DailyAt string          `json:"daily_at,omitempty"` // "HH:MM" in core.tasks.timezone
	Cron    string          `json:"cron,omitempty"`     // 5-field, in core.tasks.timezone
	Window  string          `json:"window,omitempty"`   // member of a maintenance window
	Order   int             `json:"order,omitempty"`    // position inside Window
}

// scheduleConfigJSON is ScheduleConfig without its methods, so the two
// methods below encode the plain struct without recursing.
type scheduleConfigJSON ScheduleConfig

// MarshalJSON encodes c as its plain JSON object. ScheduleConfig encodes
// itself so that config treats a schedule key as ONE key whose value is the
// whole object (05-config §1.6: "an ordinary live object key"); config walks
// into a struct that does not, which would split each schedule key into six
// keys and leave the key ScheduleFromConfig names undeclared. Method added by
// F2-04.
func (c ScheduleConfig) MarshalJSON() ([]byte, error) {
	return json.Marshal(scheduleConfigJSON(c))
}

// UnmarshalJSON decodes the plain JSON object (see MarshalJSON). Fields it
// does not know are ignored, as encoding/json does; config refuses them first,
// through the key's schema. Method added by F2-04.
func (c *ScheduleConfig) UnmarshalJSON(b []byte) error {
	var v scheduleConfigJSON
	if err := json.Unmarshal(b, &v); err != nil {
		return err
	}
	*c = ScheduleConfig(v)
	return nil
}

// Every is a fixed, non-configurable schedule reserved for the outbox relay's
// ensure-running tick (core.outbox.relay, 05-events 5E.5.3). The kernel
// refuses it on every other kind, core or module.
func Every(d time.Duration) *Schedule {
	s := &Schedule{every: d}
	if d <= 0 {
		s.problems = append(s.problems, fmt.Sprintf("Every(%s): the interval must be positive", d))
	}
	return s
}

// clone copies s (nil stays nil); builders change the copy only.
func (s *Schedule) clone() *Schedule {
	if s == nil {
		return nil
	}
	c := *s
	c.params = slices.Clone(s.params)
	c.problems = slices.Clone(s.problems)
	return &c
}

// OnStart returns a copy that also fires once when the scheduler starts.
func (s *Schedule) OnStart() *Schedule { c := s.clone(); c.onStart = true; return c }

// When returns a copy with a non-timing gate re-read each poll (e.g. updater
// mode).
func (s *Schedule) When(gate func() bool) *Schedule { c := s.clone(); c.when = gate; return c }

// Params returns a copy whose scheduled runs get params p, JSON-encoded now;
// an encoding error is recorded as a problem.
func (s *Schedule) Params(p any) *Schedule {
	c := s.clone()
	raw, err := json.Marshal(p)
	if err != nil {
		c.problems = append(c.problems, fmt.Sprintf("Schedule.Params: %v", err))
		return c
	}
	c.params = raw
	return c
}

// Live returns a copy that runs scheduled runs in ModeLive. A scheduled
// writer must state Live or Preview (v3 rule D8); stating both is a problem.
func (s *Schedule) Live() *Schedule { c := s.clone(); c.live = true; return c }

// Preview returns a copy that runs scheduled runs in ModePreview.
func (s *Schedule) Preview() *Schedule { c := s.clone(); c.preview = true; return c }

// Missed returns a copy with the policy for a fire missed while the process
// was down.
func (s *Schedule) Missed(p MissedPolicy) *Schedule { c := s.clone(); c.missed = p; return c }

// ConfigKey is ScheduleFromConfig's key ("" for Every). Accessor coined by F2-01.
func (s *Schedule) ConfigKey() string { return s.key }

// Interval is Every's interval (0 for ScheduleFromConfig). Accessor coined by F2-01.
func (s *Schedule) Interval() time.Duration { return s.every }

// RunsOnStart reports OnStart. Accessor coined by F2-01.
func (s *Schedule) RunsOnStart() bool { return s.onStart }

// Open reports the When gate (true without one). Accessor coined by F2-01.
func (s *Schedule) Open() bool { return s.when == nil || s.when() }

// RunParams is a copy of the encoded Params (nil when unset). Accessor coined
// by F2-01.
func (s *Schedule) RunParams() json.RawMessage { return slices.Clone(s.params) }

// IsLive reports Live. Accessor coined by F2-01.
func (s *Schedule) IsLive() bool { return s.live }

// IsPreview reports Preview. Accessor coined by F2-01.
func (s *Schedule) IsPreview() bool { return s.preview }

// OnMissed is the Missed policy (Coalesce by default). Accessor coined by F2-01.
func (s *Schedule) OnMissed() MissedPolicy { return s.missed }

// problemsFor reports the schedule's problems for a definition: its own, a
// mode stated twice, and a writer that states no mode.
func (s *Schedule) problemsFor(writer bool) []string {
	out := append([]string(nil), s.problems...)
	if s.live && s.preview {
		out = append(out, "Schedule: both Live() and Preview()")
	}
	if writer && !s.live && !s.preview {
		out = append(out, "Schedule: a scheduled writer must state Live() or Preview() (D8)")
	}
	if s.missed > SkipMissed {
		out = append(out, fmt.Sprintf("Schedule: unknown MissedPolicy %d", s.missed))
	}
	return out
}

// MissedPolicy is what fires missed while the process was down do.
type MissedPolicy uint8

const (
	Coalesce   MissedPolicy = iota // fire once for any number of missed instants; count them (current countDue, v3_schedule.go:501)
	SkipMissed                     // do not fire for instants missed while down; wait for the next
)

// Window is declared by core config (core.tasks.windows.<name>); kinds join it
// with ScheduleConfig.Window. The window runs as core.window.<name>: members in
// Order, once per window opening.
type Window struct {
	Name       string
	Start, End int // hour 0-23, wraps midnight
}
