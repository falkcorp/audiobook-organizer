// file: core/kernel/system_start_test.go
// version: 1.0.0
// guid: c41d7a92-3e58-4f06-b9a1-72d0e8c5f3b4
// last-edited: 2026-11-28

package kernel

import (
	"slices"
	"strings"
	"testing"

	"github.com/falkcorp/mediacore/module"
	"github.com/falkcorp/mediacore/outbox"
	"github.com/falkcorp/mediacore/rbac"
	"github.com/falkcorp/mediacore/tasks"
)

const vaultReason = "started by the scheduler's tick as gotham's system actor, which cannot hold gotham:vault (AuthRequired)"

// vaultPatrol is gotham's scheduled kind whose Start is an AuthRequired scope, so
// gotham's system actor cannot hold it (the shape of core:system.manage).
func vaultPatrol(systemStart string) tasks.Definition {
	return patrol(func(s *tasks.TaskSpec[struct{}, struct{}]) {
		s.Perms.Start = rbac.Need("gotham:vault")
		s.Perms.SystemStart = systemStart
		s.Schedule = tasks.ScheduleFromConfig("modules.gotham.tasks.schedules.patrol").Live()
	})
}

// TestSystemStart_P7RuleAtBoot: P7 runs the executor's real start rule for every
// scheduled kind. A scheduled kind whose Start its module's system actor cannot
// hold boots only with SystemStart; without it the schedule could never fire, and
// boot refuses (SystemStart necessary), and the flag on a kind a system actor
// holds changes nothing.
func TestSystemStart_P7RuleAtBoot(t *testing.T) {
	boot := func(t *testing.T, def tasks.Definition) error {
		h := newSwapHost(t)
		c := h.envConfig(nil)
		c.Modules = append(c.Modules, gotham([]tasks.Kind{patrolKind}, def).with(func(m *module.Manifest) {
			m.Settings = []module.SettingsSection{module.Settings(rooftopSection)}
			m.Permissions = []rbac.PermissionSpec{{Name: "gotham:vault", Title: "Vault", AuthRequired: true}}
		}))
		_, err := h.boot(c)
		return err
	}
	if err := boot(t, vaultPatrol(vaultReason)); err != nil {
		t.Fatalf("a scheduled kind with SystemStart whose Start the system actor cannot hold: %v, want it booted", err)
	}
	err := boot(t, vaultPatrol(""))
	if !hasKind(err, module.ProblemInvalidRegistration) || !strings.Contains(err.Error(), "scheduled task kind gotham.patrol") {
		t.Fatalf("an unflagged scheduled kind the system actor cannot hold: %v, want an invalid registration naming it", err)
	}
	holdable := patrol(func(s *tasks.TaskSpec[struct{}, struct{}]) {
		s.Perms.SystemStart = vaultReason
		s.Schedule = tasks.ScheduleFromConfig("modules.gotham.tasks.schedules.patrol").Live()
	})
	if err := boot(t, holdable); err != nil {
		t.Fatalf("a flagged scheduled kind the system actor holds: %v, want it booted", err)
	}
}

func flagged(kind tasks.Kind, reason string, scheduled bool) tasks.Definition {
	return tasks.Task(kind, tasks.TaskSpec[struct{}, struct{}]{
		Title: "Relocate the Batmobile", Effects: tasks.ReadOnly(), Resume: tasks.ResumeFromZero,
		Perms: tasks.Permissions{Start: rbac.Need("gotham:patrol"), SystemStart: reason},
		Schedule: func() *tasks.Schedule {
			if scheduled {
				return tasks.ScheduleFromConfig("modules.gotham.tasks.schedules.patrol").Live()
			}
			return nil
		}(),
		Run: func(*tasks.RC, struct{}) (struct{}, error) { return struct{}{}, nil },
	})
}

// TestSystemStart_DeclarationsP7: a flagged kind is declared by a Schedule of its
// own or by a consumer of its own module; a flag with neither is refused (an
// unused grant), with no gate on the outbox being wired, and a consumer's target
// of another module is refused.
func TestSystemStart_DeclarationsP7(t *testing.T) {
	const kind tasks.Kind = "gotham.relocate"
	consumer := func(owner string, k tasks.Kind) []outbox.TaskConsumer {
		return []outbox.TaskConsumer{{Name: "gotham.relocated", Owner: owner, Kind: k}}
	}
	cases := []struct {
		name    string
		def     tasks.Definition
		targets []outbox.TaskConsumer
		problem string // substring; "" means none
	}{
		{"flag declared by a Schedule", flagged(kind, vaultReason, true), nil, ""},
		{"flag declared by a consumer of its module", flagged(kind, vaultReason, false), consumer("gotham", kind), ""},
		{"flag with no declaration", flagged(kind, vaultReason, false), nil, "the grant is unused"},
		{"a consumer's target of another module", flagged(kind, vaultReason, false), consumer("arkham", kind), "a consumer starts its own module's kinds only"},
		{"no flag and no declaration", flagged(kind, "", false), nil, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			probs := systemStartDeclProblems([]tasks.Definition{tc.def}, tc.targets)
			switch {
			case tc.problem == "" && len(probs) != 0:
				t.Fatalf("problems = %v, want none", probs)
			case tc.problem != "" && !slices.ContainsFunc(probs, func(p module.Problem) bool { return strings.Contains(p.Error(), tc.problem) }):
				t.Fatalf("problems = %v, want one containing %q", probs, tc.problem)
			}
		})
	}
}
