// file: core/kernel/tasks_checks.go
// version: 1.0.0
// guid: 8d3a6f17-2c94-4b5e-a1d0-7f6e9c2b5a38
// last-edited: 2026-11-11

package kernel

import (
	"fmt"
	"slices"
	"strings"

	"github.com/falkcorp/mediacore/internal/scheduler"
	"github.com/falkcorp/mediacore/module"
	"github.com/falkcorp/mediacore/rbac"
	"github.com/falkcorp/mediacore/storage"
	"github.com/falkcorp/mediacore/tasks"
)

// ledgerProblems is P1's static task check of every manifest, enabled or not
// (06 §6.5.1 P1; 05-tasks 5T.3.11): each TaskLedger entry parses as a
// tasks.Kind whose module segment is the manifest's own ID, and no kind is
// listed twice, within a manifest or across two. A disabled module's ledger
// is read while it is off (its stored runs are held, not unknown), so a
// malformed or foreign entry would let it claim another module's runs.
func ledgerProblems(entries []*entry) []module.Problem {
	var probs []module.Problem
	owner := make(map[tasks.Kind]module.ID)
	for _, e := range entries {
		id := e.man.ID
		for _, raw := range e.man.TaskLedger {
			k, err := tasks.ParseKind(string(raw))
			if err != nil {
				probs = append(probs, module.NewProblem(id, module.ProblemTaskKindNamespace, fmt.Errorf("TaskLedger entry %q: %w", raw, err)))
				continue
			}
			if k.Module() != string(id) {
				probs = append(probs, module.Problem{Module: id, Kind: module.ProblemTaskKindNamespace,
					Detail: fmt.Sprintf("TaskLedger entry %q is not in module %s's namespace (%s.<name>)", k, id, id)})
				continue
			}
			if first, dup := owner[k]; dup {
				probs = append(probs, module.Problem{Module: id, Kind: module.ProblemDuplicateTaskKind,
					Detail: fmt.Sprintf("TaskLedger entry %q is already listed by module %s", k, first)})
				continue
			}
			owner[k] = id
		}
	}
	return probs
}

// dataMigrationProblems is P1's ownership check of Manifest.DataMigrations
// (06 §6.4.2): a module migrates its own owner's rows only, so a migration
// whose Owner is not storage.Owner(<the module's ID>) is a ProblemMigrationOrder.
// Their ordering and the rest of 05-storage 5S.3.6's registration rules are
// NewDataMigrations' (it lists every problem, once, when the store opens).
func dataMigrationProblems(entries []*entry) []module.Problem {
	var probs []module.Problem
	for _, e := range entries {
		for _, m := range e.man.DataMigrations {
			if m.Owner != storage.Owner(e.man.ID) {
				probs = append(probs, module.Problem{Module: e.man.ID, Kind: module.ProblemMigrationOrder,
					Detail: fmt.Sprintf("data migration %q names owner %q; a module's data migrations are its own owner's (%q)", m.ID, m.Owner, storage.Owner(e.man.ID))})
			}
		}
	}
	return probs
}

// taskDefinitions are the definitions every enabled module registered, in
// module ID order and registration order.
func (k *Kernel) taskDefinitions() []tasks.Definition {
	var defs []tasks.Definition
	for _, e := range k.entries {
		if e.enabled {
			defs = append(defs, e.rec.Tasks...)
		}
	}
	return defs
}

// taskProblems is P7's check of every enabled module's Registrar.Tasks and
// Registrar.Resources records (06 §6.5.1 P7; 05-tasks 5T.3.1, 5T.3.8). It is
// pure, so Describe runs it too. Each registered definition must:
//
//   - carry no constructor problem (tasks.Problems);
//   - name a kind in its registering module's namespace, and in the module's
//     TaskLedger (ProblemTaskKindNamespace, ProblemTaskLedger), once;
//   - not claim tasks.PrioritySystem, which only core's own kinds may;
//   - start under a permission of the module's own namespace or core's
//     (ProblemPermissionNamespace; the 05-rbac R3 rule);
//   - name, in its Effects, only resource kinds its module declared through
//     Registrar.Resources, and write only its own module's resources.
//
// scheduler.ValidateSchedules then judges every schedule over the declared
// keys. Every problem is reported, none stops the others.
//
// What 06 §6.4.2 leaves to the kernel and F2 cannot fully check: that a
// view-class scope is not what Perms.Start asks for holds for core's scopes
// only (a module's PermissionSpec.Class arrives with Manifest.Permissions,
// F4), and a module kind's schedule config key is never declared (modules'
// settings arrive with F3, so every such key is refused today).
func (k *Kernel) taskProblems() []module.Problem {
	var probs []module.Problem
	for _, e := range k.entries {
		if !e.enabled {
			continue
		}
		probs = append(probs, k.moduleTaskProblems(e)...)
	}
	if k.coreCfg != nil {
		if err := scheduler.ValidateSchedules(k.taskDefinitions(), k.coreCfg.declared); err != nil {
			probs = append(probs, module.NewProblem("", module.ProblemInvalidRegistration, err))
		}
	}
	return probs
}

// moduleTaskProblems is taskProblems for one module.
func (k *Kernel) moduleTaskProblems(e *entry) []module.Problem {
	id := e.man.ID
	var probs []module.Problem
	bad := func(kind module.ProblemKind, format string, args ...any) {
		probs = append(probs, module.Problem{Module: id, Kind: kind, Detail: fmt.Sprintf(format, args...)})
	}
	resources := e.rec.Resources
	seen := make(map[tasks.Kind]bool)
	for _, d := range e.rec.Tasks {
		if d == nil {
			bad(module.ProblemInvalidRegistration, "Registrar.Tasks was given a nil definition")
			continue
		}
		kind := d.Kind()
		for _, err := range tasks.Problems(d) {
			probs = append(probs, module.NewProblem(id, module.ProblemInvalidRegistration, fmt.Errorf("task %s: %w", kind, err)))
		}
		switch {
		case kind.Module() != string(id):
			bad(module.ProblemTaskKindNamespace, "task kind %q is not in module %s's namespace (%s.<name>)", kind, id, id)
		case !slices.Contains(e.man.TaskLedger, kind):
			bad(module.ProblemTaskLedger, "task kind %q is registered but not listed in Manifest.TaskLedger", kind)
		}
		if seen[kind] {
			bad(module.ProblemDuplicateTaskKind, "task kind %q is registered twice", kind)
		}
		seen[kind] = true
		c := tasks.CommonOf(d)
		if c.Priority == tasks.PrioritySystem {
			bad(module.ProblemInvalidRegistration, "task kind %q asks for PrioritySystem, which only core's own kinds may", kind)
		}
		for _, p := range c.Perms.Start.Permissions() {
			if ns, _, _ := strings.Cut(string(p), ":"); ns != string(id) && ns != "core" {
				bad(module.ProblemPermissionNamespace, "task kind %q starts under %q, outside module %s's namespace and core's", kind, p, id)
			}
			// SEC-9: a view-class scope starts nothing unless the definition
			// gives a reviewed reason. Only core's scopes are in the catalog at
			// F2; a module's PermissionSpec.Class arrives with its manifest's
			// permissions (F4).
			if spec, ok := k.rbacCat.Lookup(p); ok && spec.Class == rbac.ClassView && c.Perms.StartableByViewers == "" {
				bad(module.ProblemInvalidRegistration, "task kind %q starts under the view-class scope %q (Permissions.StartableByViewers is empty, SEC-9)", kind, p)
			}
		}
		for _, r := range c.Effects.ReadSet() {
			if r.Module == string(id) && !slices.Contains(resources, r.Kind) {
				bad(module.ProblemInvalidRegistration, "task kind %q reads resource kind %q, which module %s did not declare with Registrar.Resources", kind, r.Kind, id)
			}
		}
		for _, r := range c.Effects.WriteSet() {
			switch {
			case r.Module != string(id):
				bad(module.ProblemInvalidRegistration, "task kind %q writes %s/%s, another module's resource", kind, r.Module, r.Kind)
			case !slices.Contains(resources, r.Kind):
				bad(module.ProblemInvalidRegistration, "task kind %q writes resource kind %q, which module %s did not declare with Registrar.Resources", kind, r.Kind, id)
			}
		}
	}
	return probs
}
