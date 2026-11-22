// file: core/kernel/startup_table.go
// version: 1.0.0
// guid: 8e4b2a96-3d17-4c05-b6f1-0a9d7c35e284
// last-edited: 2026-11-22

package kernel

import (
	"errors"
	"fmt"
	"log/slog"

	"github.com/falkcorp/mediacore/health"
)

// startupTable is the kernel's startup table (05-health §5H.3.2): the core rows
// of health.CoreStartupSteps() and every enabled module's rows from
// Registrar.StartupSteps, in one *health.Startup. The Startup is kept in the
// startup field, not behind a helper's return value, because
// archtest.StartupSteps only recognises a Run call whose receiver is a local or
// a struct field declared as *health.Startup (seams.d/F8-03.md, follow-up 3):
// the F8 flip's boot code runs each step as t.startup.Run(ctx, "<literal>", fn),
// and the guard then checks every literal against this table.
//
// Dark until the F8 flip: nothing in boot builds one. NewStartup with a registry
// makes the registry report core.kernel:starting until Complete, so building
// the table is itself a readiness change and belongs to the flip, not to a
// slice that must not change what a running binary does.
type startupTable struct {
	startup *health.Startup
}

// newStartupTable builds the table over reg (nil keeps it without readiness
// effects, as health.NewStartup does) from CoreStartupSteps() and the rows each
// enabled entry recorded. A disabled module's rows are not in the table: a
// module that is off runs none of its steps, and a row nothing runs would fail
// the guard's "every row is run" half. Every module's problem is reported
// together and names the module: a row whose name core or another module
// already holds, or one Startup refuses. Startup.Add is all or none, so a
// refused module leaves the table as the modules before it made it.
func newStartupTable(reg *health.Registry, log *slog.Logger, entries []*entry) (*startupTable, error) {
	st, err := health.NewStartup(reg, log, health.CoreStartupSteps()...)
	if err != nil {
		return nil, fmt.Errorf("kernel: startup table: core rows: %w", err)
	}
	var errs []error
	for _, e := range entries {
		if !e.enabled || len(e.rec.StartupSteps) == 0 {
			continue
		}
		if err := st.Add(e.rec.StartupSteps...); err != nil {
			errs = append(errs, fmt.Errorf("kernel: module %s: startup steps: %w", e.man.ID, err))
		}
	}
	if err := errors.Join(errs...); err != nil {
		return nil, err
	}
	return &startupTable{startup: st}, nil
}
