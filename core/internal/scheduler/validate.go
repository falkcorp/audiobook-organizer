// file: core/internal/scheduler/validate.go
// version: 1.0.0
// guid: f13c956a-820f-41e7-b966-672cd969eb3a
// last-edited: 2026-11-11

package scheduler

import (
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/falkcorp/mediacore/tasks"
)

// relayKind is the one kind that may carry tasks.Every (05-events 5E.5.3:
// the outbox relay's ensure-running tick).
const relayKind tasks.Kind = "core.outbox.relay"

// ValidateSchedules is 5T.3.1's registration check of every definition's
// Schedule, as a pure function the kernel calls at P6/P7 (05-tasks 5T.4 step
// 1); the kernel fails boot on its error (startup class Fatal). declared
// answers for a config key whether it is declared, whether its type is
// tasks.ScheduleConfig, and its owner: "core" for a key in a core section,
// the module ID for a key in modules.<id>. Name coined by F2-04.
//
// It refuses (the key rule R2-7):
//   - a ScheduleFromConfig key not of the form <owner>.<subsystem>.schedules.<name>
//     with every segment [a-z][a-z0-9_]{0,47};
//   - a key outside the registering owner's namespace: a core kind's key must
//     be core.…, a module kind's modules.<its module>.…;
//   - a key that is not declared, or not declared with type
//     tasks.ScheduleConfig (a duration or a cron string), or declared by
//     another owner;
//   - one key used by two kinds;
//   - tasks.Every on any kind but core.outbox.relay;
//   - a schedule with no timing, and one stating both Live and Preview;
//   - a scheduled writer with neither Live nor Preview (v3 rule D8).
//
// Every refusal is reported, sorted by kind, joined into one error.
func ValidateSchedules(defs []tasks.Definition, declared func(key string) (isScheduleConfig bool, owner string, ok bool)) error {
	var problems []string
	users := map[string][]tasks.Kind{}
	for _, d := range defs {
		if d == nil {
			problems = append(problems, "a nil definition")
			continue
		}
		c := tasks.CommonOf(d)
		s := c.Schedule
		if s == nil {
			continue
		}
		k := d.Kind()
		refuse := func(format string, args ...any) {
			problems = append(problems, fmt.Sprintf("%s: %s", k, fmt.Sprintf(format, args...)))
		}
		writer := c.Effects.Declared() && !c.Effects.IsReadOnly()
		switch {
		case s.IsLive() && s.IsPreview():
			refuse("the schedule states both Live() and Preview()")
		case writer && !s.IsLive() && !s.IsPreview():
			refuse("a scheduled writer must state Live() or Preview() (D8)")
		}
		key := s.ConfigKey()
		if s.Interval() > 0 {
			if k != relayKind {
				refuse("tasks.Every is reserved for %s; use tasks.ScheduleFromConfig with a key of type tasks.ScheduleConfig", relayKind)
			}
			continue
		}
		if key == "" {
			refuse("the schedule has no timing (build it with tasks.ScheduleFromConfig)")
			continue
		}
		users[key] = append(users[key], k)
		owner, err := keyOwner(key)
		if err != nil {
			refuse("%v", err)
			continue
		}
		want := k.Module()
		if want != "core" && owner == "core" {
			refuse("schedule key %q is in core's namespace; module %s's keys are modules.%s.<subsystem>.schedules.<name>", key, want, want)
			continue
		}
		if owner != want {
			refuse("schedule key %q belongs to %s, not to the registering owner %s", key, ownerName(owner), ownerName(want))
			continue
		}
		isSched, declOwner, ok := declared(key)
		switch {
		case !ok:
			refuse("schedule key %q is not declared (the owner declares it in its config section with type tasks.ScheduleConfig and a default)", key)
		case !isSched:
			refuse("schedule key %q is not of type tasks.ScheduleConfig (a duration or cron string is not a schedule key)", key)
		case declOwner != want:
			refuse("schedule key %q is declared by %s, not by the registering owner %s", key, ownerName(declOwner), ownerName(want))
		}
	}
	for key, ks := range users {
		if len(ks) > 1 {
			slices.Sort(ks)
			problems = append(problems, fmt.Sprintf("%s: schedule key %q is used by %d kinds (%s); one key, one kind", ks[0], key, len(ks), joinKinds(ks)))
		}
	}
	if len(problems) == 0 {
		return nil
	}
	slices.Sort(problems)
	errs := make([]error, len(problems))
	for i, p := range problems {
		errs[i] = errors.New("tasks: " + p)
	}
	return errors.Join(errs...)
}

// keyOwner checks key's form (R2-7) and returns the owner its namespace
// names: "core", or the module ID of modules.<id>.
func keyOwner(key string) (string, error) {
	segs := strings.Split(key, ".")
	for i, seg := range segs {
		if !configSegment(seg) {
			return "", fmt.Errorf("schedule key %q: segment %d (%q) does not match [a-z][a-z0-9_]{0,47}", key, i+1, seg)
		}
	}
	var owner string
	var rest []string
	switch {
	case len(segs) == 4 && segs[0] == "core":
		owner, rest = "core", segs[1:]
	case len(segs) == 5 && segs[0] == "modules":
		owner, rest = segs[1], segs[2:]
	}
	if rest == nil || rest[1] != "schedules" || rest[0] == "schedules" {
		return "", fmt.Errorf("schedule key %q: want core.<subsystem>.schedules.<name> or modules.<id>.<subsystem>.schedules.<name>", key)
	}
	return owner, nil
}

// configSegment reports whether seg matches [a-z][a-z0-9_]{0,47}.
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

func ownerName(owner string) string {
	if owner == "core" {
		return "core"
	}
	return "module " + owner
}

func joinKinds(ks []tasks.Kind) string {
	ss := make([]string, len(ks))
	for i, k := range ks {
		ss[i] = string(k)
	}
	return strings.Join(ss, ", ")
}
