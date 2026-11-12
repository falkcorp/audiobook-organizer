// file: core/kernel/lazycoreconfig_test.go
// version: 1.0.0
// guid: 1b69ef8e-ed2c-4179-bffa-8f9bede4fe1a
// last-edited: 2026-11-12

package kernel

import (
	"context"
	"testing"

	"github.com/falkcorp/mediacore/internal/bootseam"
	"github.com/falkcorp/mediacore/module"
	"github.com/falkcorp/mediacore/tasks"
)

// TestLazyCoreConfig_BuiltOnlyWhenTasksNeedIt: the defaults-only core config
// costs about 14 ms a boot (130 to 240 ms under -race), and only the tasks
// wiring reads it, so P7 builds it when a definition is registered or the
// start rule holds, and a boot that does neither (ABO's shape) never does.
func TestLazyCoreConfig_BuiltOnlyWhenTasksNeedIt(t *testing.T) {
	disabled := func(m *module.Manifest) { m.DefaultEnabled = false }
	for _, tc := range []struct {
		name  string
		cfg   func(t *testing.T) Config
		seeds []bootseam.TaskRunSeed
		built bool
	}{
		{"zero modules, no store", func(t *testing.T) Config { return testConfig(t) }, nil, false},
		{"zero modules, an empty store", func(t *testing.T) Config { c, _ := taskConfig(t); return c }, nil, false},
		{"a module that registers no kind", func(t *testing.T) Config {
			c, _ := taskConfig(t, gotham([]tasks.Kind{patrolKind}))
			return c
		}, nil, false},
		{"a disabled module with a ledger and no run", func(t *testing.T) Config {
			c, _ := taskConfig(t, gotham([]tasks.Kind{patrolKind}).with(disabled))
			return c
		}, nil, false},
		{"a registered kind", func(t *testing.T) Config {
			c, _ := taskConfig(t, gotham([]tasks.Kind{patrolKind}, patrol()))
			return c
		}, nil, true},
		{"stored runs of a disabled module's kind and no definition", func(t *testing.T) Config {
			c, _ := taskConfig(t, gotham([]tasks.Kind{patrolKind}, patrol()).with(disabled))
			return c
		}, []bootseam.TaskRunSeed{{Kind: string(patrolKind), N: 2}}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := t.Context()
			if tc.seeds != nil {
				ctx = bootseam.With(ctx, &bootseam.Seams{TaskRuns: tc.seeds})
			}
			k, err := New(ctx, tc.cfg(t))
			if err != nil {
				t.Fatal(err)
			}
			if got := k.coreCfg != nil; got != tc.built {
				t.Errorf("core config built = %v, want %v", got, tc.built)
			}
			if err := k.Close(context.Background()); err != nil {
				t.Errorf("Close = %v (the store is closed only when it was built)", err)
			}
		})
	}
}

// TestLazyCoreConfig_StillBuildsAndKeepsOneStore: the store the tasks wiring
// builds is the one coreConfig keeps, and the build itself, which a boot with
// no kind no longer runs, still succeeds over the core sections.
func TestLazyCoreConfig_StillBuildsAndKeepsOneStore(t *testing.T) {
	c, err := newCoreConfig("batcave")
	if err != nil {
		t.Fatal(err)
	}
	if err := c.close(context.Background()); err != nil {
		t.Fatal(err)
	}
	k := &Kernel{cfg: Config{AppID: "batcave"}}
	first, err := k.coreConfig()
	if err != nil {
		t.Fatal(err)
	}
	second, err := k.coreConfig()
	if err != nil || first != second {
		t.Errorf("a second coreConfig = %p, %v; want the first store %p", second, err, first)
	}
	if err := k.coreCfg.close(context.Background()); err != nil {
		t.Fatal(err)
	}
}
