// file: core/kernel/fakemigration_hook.go
// version: 1.0.0
// guid: 3082bc6e-96eb-4f32-a0c1-5312f06af38c
// last-edited: 2026-11-09

//go:build unitlifecycle

package kernel

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"time"
)

// fakeMigrationEnv names the duration of the fake migration, a Go duration
// such as "12m". Unset or empty means no fake steps.
const fakeMigrationEnv = "MEDIACORE_FAKE_MIGRATION"

// fakeMigrationStep is how long one fake step sleeps: one minute, the length
// of a real migration step that makes progress. This file's tests run the
// steps on a synctest clock, so they take no real time.
const fakeMigrationStep = time.Minute

// fakeMigrationSteps is the test-only hook behind the unitlifecycle build tag
// (03 §3.5.1 item 4; 09 §9.2.1's unit-lifecycle job). It returns one boot step
// per fakeMigrationStep of the duration in MEDIACORE_FAKE_MIGRATION (rounded
// up), so a 12 minute migration is twelve steps.
//
// Each step calls extend with its own name immediately before it sleeps, the
// per-step rule of 05-health §5H.3.4: a step that is making progress asks the
// service manager for more start time as it begins, and there is no ticker or
// timer goroutine, so a step that hung would still be killed when its own
// budget ran out. The job builds this file into its binary and no release
// build carries it.
//
// A value that is not a positive duration returns one step that fails the
// boot with that reason: a typo in the job's environment must not turn the
// 12 minute cell into a boot with no migration, which would pass for the
// wrong reason.
func fakeMigrationSteps() []bootStep {
	raw := os.Getenv(fakeMigrationEnv)
	if raw == "" {
		return nil
	}
	total, err := time.ParseDuration(raw)
	if err == nil && total <= 0 {
		err = fmt.Errorf("%s is not a positive duration", raw)
	}
	if err != nil {
		bad := fmt.Errorf("kernel: %s=%q: %w", fakeMigrationEnv, raw, err)
		return []bootStep{{name: "fake migration", run: func(context.Context, func(string), *slog.Logger) error { return bad }}}
	}
	n := int((total + fakeMigrationStep - 1) / fakeMigrationStep)
	steps := make([]bootStep, 0, n)
	for i := 1; i <= n; i++ {
		name := fmt.Sprintf("fake migration step %d of %d", i, n)
		steps = append(steps, bootStep{name: name, run: func(ctx context.Context, extend func(string), l *slog.Logger) error {
			extend(name)
			l.Info("kernel: fake migration step", "step", name)
			t := time.NewTimer(fakeMigrationStep)
			defer t.Stop()
			select {
			case <-t.C:
				return nil
			case <-ctx.Done():
				return ctx.Err()
			}
		}})
	}
	return steps
}
