// file: core/health/reporter.go
// version: 1.0.1
// guid: 20a5ee3e-4349-4ef1-a6de-3b6eeacc4b92
// last-edited: 2026-11-13

package health

import "errors"

// Reporter is the push half of a component (05-health §5H.3.1): the handle a
// Register call returns, through which breakers, the outbox, the watchdog and
// warm-ups say what state their component is in.
//
// Report replaces the component's current Result. It is goroutine-safe and
// never blocks on a reader. A Result whose Reason is outside the grammar
// [a-z0-9_]{1,40} is stored with the fixed Reason InvalidReason instead, and
// a zero At is stamped with the registry's clock (see Registry.Report).
type Reporter interface {
	Report(res Result)
}

// Reporters is the module handle, module.Deps.Health (06 §6.4.4, COR-21): it
// hands out the push Reporter of a component the module itself declared with
// Registrar.Health. The kernel builds one per enabled module. Only the
// interface exists at F2; the implementation and the Registrar/Deps members
// land at F5.
type Reporters interface {
	// Reporter returns ErrNotOwned for an ID this module did not register,
	// so a module can never report another component's status.
	Reporter(id ComponentID) (Reporter, error)
}

// ErrNotOwned is what Reporters.Reporter returns for a component the module
// did not register.
var ErrNotOwned = errors.New("health: component not registered by this module")

// ErrComponentExists is Register's answer for an ID already registered; the
// startup table's degraded component, one per token, tests for it.
var ErrComponentExists = errors.New("health: component is already registered")

// componentReporter is the Reporter of one registered component.
type componentReporter struct {
	reg *Registry
	id  ComponentID
}

// Report implements Reporter.
func (c componentReporter) Report(res Result) { c.reg.report(c.id, res) }
