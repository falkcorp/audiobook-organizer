// file: core/kernel/standdown_lease_default_test.go
// version: 0.1.0
// guid: 3d7b9f20-5c81-4e6a-b4d2-97a0c1e85f36
// last-edited: 2026-11-22

package kernel

import (
	"context"
	"testing"

	"github.com/falkcorp/mediacore/internal/executor"
)

// TestStandDownLease_ProductionDefaultIsTheExecutorsDefault: the executor takes its
// lease from core.tasks.standdown_lease, and executor.New keeps any non-zero
// Config.StandDownLease, so the executor's own 5m default only applies to a bare
// Config (tests). The lease production runs on is the section's default; before it
// named executor.DefaultStandDownLease the section said 2m while the spec, rc.go and
// the bookfile batch budget all said 5m. The test reads the value through the same
// path the kernel boots on (the core config's executorConfig), not the section's
// struct, so it also fails if executorConfig stops passing the key through.
func TestStandDownLease_ProductionDefaultIsTheExecutorsDefault(t *testing.T) {
	c, err := newCoreConfig("batcave", CoreDefaults{}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.close(context.Background()) })
	if got := c.executorConfig().StandDownLease; got != executor.DefaultStandDownLease {
		t.Errorf("the booted executor's stand-down lease = %v, want executor.DefaultStandDownLease (%v)", got, executor.DefaultStandDownLease)
	}
}
