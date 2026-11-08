// file: internal/serviceregistry/graph.go
// version: 1.1.0
// last-edited: 2026-11-08

package serviceregistry

import (
	"fmt"
	"sort"
)

// Resolve computes the transitive closure of c.include, validates the
// dep graph, and produces a topologically sorted build order. The ready
// queue is sorted lexically before processing for deterministic order
// (stable startup logs, reproducible tests).
//
// Returns ErrUnknownService if a dep references a name with no def in the container.
// Returns ErrCycle if the dep graph contains a cycle.
// Idempotent after first successful call.
func (c *Container) Resolve() error {
	if c.phase >= phaseResolved {
		return nil
	}

	// 1. Transitive closure: walk include set following Needs.
	//    Overrides short-circuit — they are leaves in the graph.
	wanted := map[string]bool{}
	var walk func(name string) error
	walk = func(name string) error {
		if wanted[name] {
			return nil
		}
		if _, isOverride := c.overrides[name]; isOverride {
			wanted[name] = true
			return nil
		}
		def, ok := c.defs[name]
		if !ok {
			return fmt.Errorf("%w: %q (declared as a dep but not in the container's defs)", ErrUnknownService, name)
		}
		wanted[name] = true
		for _, dep := range def.Needs {
			if err := walk(dep); err != nil {
				return err
			}
		}
		return nil
	}
	for name := range c.include {
		if err := walk(name); err != nil {
			return err
		}
	}

	// 2. Build dep graph over wanted set. Overrides are treated as leaves.
	incoming := map[string]int{}
	outgoing := map[string][]string{}
	for name := range wanted {
		if _, isOverride := c.overrides[name]; isOverride {
			incoming[name] = 0
			continue
		}
		def := c.defs[name]
		incoming[name] = len(def.Needs)
		for _, dep := range def.Needs {
			outgoing[dep] = append(outgoing[dep], name)
		}
	}

	// 3. Kahn's algorithm with lex-stable ready queue.
	ready := []string{}
	for name, n := range incoming {
		if n == 0 {
			ready = append(ready, name)
		}
	}
	sort.Strings(ready)
	order := make([]string, 0, len(wanted))
	for len(ready) > 0 {
		name := ready[0]
		ready = ready[1:]
		order = append(order, name)

		// Sort dependents lexically before appending so the queue stays stable.
		dependents := append([]string{}, outgoing[name]...)
		sort.Strings(dependents)
		for _, dep := range dependents {
			incoming[dep]--
			if incoming[dep] == 0 {
				ready = append(ready, dep)
			}
		}
		sort.Strings(ready)
	}

	// 4. Cycle check.
	if len(order) != len(wanted) {
		cycle := []string{}
		for name := range wanted {
			if incoming[name] > 0 {
				cycle = append(cycle, name)
			}
		}
		sort.Strings(cycle)
		return fmt.Errorf("%w among: %v", ErrCycle, cycle)
	}

	c.order = order
	c.phase = phaseResolved
	return nil
}
