// file: core/kernel/taskconfig.go
// version: 1.0.1
// guid: 5b0c9d52-6f3a-4a8e-9c47-2e1d8b7a4f60
// last-edited: 2026-11-12

package kernel

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"reflect"
	"strings"
	"time"

	"github.com/falkcorp/mediacore/config"
	"github.com/falkcorp/mediacore/internal/executor"
	"github.com/falkcorp/mediacore/internal/scheduler"
	"github.com/falkcorp/mediacore/tasks"
)

// coreConfig is the config store the F2 foundation reads: core's section list
// (coreSections, F2-09) registered over a store with no file, no environment
// and no stored override, so every key holds its declared default. The
// executor, the scheduler and the task routes read their settings from it
// (core.tasks.*, the schedule keys). It is the F2 form of the boot's config:
// bootConfig (F3-07) replaces it with the layered store, and the adapters
// below do not change when it does, because they read through Handle.Get and
// Lookup, which see a later layer's values.
//
// It performs no I/O: it is built at P7 by Kernel.coreConfig, and only when
// the tasks wiring reads it (a registered definition, or the start rule
// holding), also for Describe, and closed by closeCore.
type coreConfig struct {
	store *config.Store
	tasks *config.Handle[tasksSettings]
	// leaves is every key of the core sections by dotted path, for the
	// schedule-key declaration check (ValidateSchedules' declared function).
	leaves map[string]cfgLeaf
}

// cfgLeaf is one key of a core section: whether its type is
// tasks.ScheduleConfig and, when it is, how to read its current value.
type cfgLeaf struct {
	schedule bool
	read     func() tasks.ScheduleConfig
}

// coreConfig is the defaults-only core config, built on the first call and
// kept. Boot calls it at P7, which is single-threaded, from the two places
// that read it: taskProblems when a definition exists (the schedule keys it
// declares) and constructTasks when the start rule held (the executor's and
// the scheduler's settings). A boot that registers no kind and finds no run
// never calls it, so ABO's boot skips the build entirely; closeCore closes
// the store only when it was built.
func (k *Kernel) coreConfig() (*coreConfig, error) {
	if k.coreCfg == nil {
		c, err := newCoreConfig(k.cfg.AppID)
		if err != nil {
			return nil, err
		}
		k.coreCfg = c
	}
	return k.coreCfg, nil
}

// newCoreConfig builds the defaults-only store over coreSections().
func newCoreConfig(appID string) (*coreConfig, error) {
	st, err := config.NewStore(config.Options{
		AppID:   appID,
		Environ: []string{}, // no environment layer: os.Environ is not read
		Logger:  slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	if err != nil {
		return nil, fmt.Errorf("kernel: core config: %w", err)
	}
	handles, err := config.RegisterSections(st.Registry(), coreSections()...)
	if err != nil {
		return nil, fmt.Errorf("kernel: core config: register the core sections: %w", err)
	}
	c := &coreConfig{store: st, leaves: map[string]cfgLeaf{}}
	for _, h := range handles {
		if th, ok := h.(*config.Handle[tasksSettings]); ok {
			c.tasks = th
		}
		cfgWalkLeaves(c.leaves, h)
	}
	if c.tasks == nil {
		return nil, fmt.Errorf("kernel: core config: coreSections lacks core.tasks")
	}
	return c, nil
}

// close releases the store.
func (c *coreConfig) close(ctx context.Context) error {
	if c == nil {
		return nil
	}
	return c.store.Close(ctx)
}

// cfgWalkLeaves records every key under a section handle. A struct field is a
// group unless its type encodes itself (tasks.ScheduleConfig, config.Duration),
// which is one key; a map or slice is one key too.
func cfgWalkLeaves(out map[string]cfgLeaf, handle any) {
	p, ok := handle.(interface{ Path() config.Path })
	if !ok {
		return
	}
	get := reflect.ValueOf(handle).MethodByName("Get")
	if !get.IsValid() {
		return
	}
	root := func() reflect.Value { return get.Call(nil)[0].Elem() }
	cfgWalkType(out, string(p.Path()), root, nil, reflect.TypeOf(root().Interface()))
}

// cfgWalkType recurses over t (a section's value type) and records its keys.
func cfgWalkType(out map[string]cfgLeaf, base string, root func() reflect.Value, idx []int, t reflect.Type) {
	if t.Kind() != reflect.Struct || t == reflect.TypeFor[tasks.ScheduleConfig]() || selfEncoding(t) {
		leaf := cfgLeaf{}
		if t == reflect.TypeFor[tasks.ScheduleConfig]() {
			leaf.schedule = true
			leaf.read = func() tasks.ScheduleConfig {
				c, _ := root().FieldByIndex(idx).Interface().(tasks.ScheduleConfig)
				return c
			}
		}
		out[base] = leaf
		return
	}
	for i := range t.NumField() {
		f := t.Field(i)
		if !f.IsExported() {
			continue
		}
		name, _, _ := strings.Cut(f.Tag.Get("json"), ",")
		if name == "-" {
			continue
		}
		if name == "" {
			name = f.Name
		}
		cfgWalkType(out, base+"."+name, root, append(append([]int(nil), idx...), i), f.Type)
	}
}

// selfEncoding reports a struct type that writes its own JSON (a key, not a
// group): it implements json.Marshaler.
func selfEncoding(t reflect.Type) bool {
	return t.Implements(reflect.TypeFor[json.Marshaler]()) || reflect.PointerTo(t).Implements(reflect.TypeFor[json.Marshaler]())
}

// declared is scheduler.ValidateSchedules' declaration oracle: whether key is a
// declared core key, whether its type is tasks.ScheduleConfig, and its owner.
// Only core's keys are declared at F2: a module's settings arrive with
// Manifest.Settings (F3), so a modules.<id>... schedule key is not declared
// and ValidateSchedules refuses it.
func (c *coreConfig) declared(key string) (isSchedule bool, owner string, ok bool) {
	l, ok := c.leaves[key]
	if !ok {
		return false, "", false
	}
	return l.schedule, "core", true
}

// schedule is scheduler.Config.Schedule: the current value of a schedule key.
func (c *coreConfig) schedule(key string) (tasks.ScheduleConfig, error) {
	l, ok := c.leaves[key]
	if !ok || !l.schedule {
		return tasks.ScheduleConfig{}, fmt.Errorf("kernel: %s is not a declared schedule key", key)
	}
	return l.read(), nil
}

// window is scheduler.Config.Window: core.tasks.windows.<name>.
func (c *coreConfig) window(name string) (scheduler.WindowConfig, bool) {
	w, ok := c.tasks.Get().Windows[name]
	return scheduler.WindowConfig{Start: w.Start, End: w.End, Enabled: w.Enabled}, ok
}

// schedulerConfig adapts the core config to scheduler.Config.
type schedulerConfig struct{ c *coreConfig }

func (s schedulerConfig) Schedule(key string) (tasks.ScheduleConfig, error) { return s.c.schedule(key) }

func (s schedulerConfig) Window(name string) (scheduler.WindowConfig, bool) { return s.c.window(name) }

// executorConfig is the executor's Config from core.tasks.*. The per-module
// ceiling (modules.<id>.tasks.max_concurrent) arrives with module settings
// (F3), so it is unset here.
func (c *coreConfig) executorConfig() executor.Config {
	s := c.tasks.Get()
	kinds := make(map[tasks.Kind]tasks.KindDial, len(s.Kinds))
	for k, d := range s.Kinds {
		kinds[tasks.Kind(k)] = tasks.KindDial{ParallelismCap: d.ParallelismCap}
	}
	return executor.Config{
		Workers:               s.Workers,
		SystemWorkers:         s.SystemWorkers,
		CPUSlots:              s.CPUSlots,
		Kinds:                 kinds,
		ShutdownDrain:         time.Duration(s.ShutdownDrain),
		ShutdownExtendMax:     time.Duration(s.ShutdownExtendMax),
		MinCheckpointInterval: time.Duration(s.MinCheckpointInterval),
		MaxYields:             s.MaxYields,
		ZombieCap:             s.ZombieCap,
		StandDownLease:        time.Duration(s.StanddownLease),
		IdempotencyTTL:        time.Duration(s.IdempotencyTTL),
	}
}

// location is core.tasks.timezone: the IANA zone the scheduler's windows and
// cron schedules are read in; empty is the server's local zone.
func (c *coreConfig) location() (*time.Location, error) {
	tz := c.tasks.Get().Timezone
	if tz == "" {
		return time.Local, nil
	}
	loc, err := time.LoadLocation(tz)
	if err != nil {
		return nil, fmt.Errorf("kernel: core.tasks.timezone %q: %w", tz, err)
	}
	return loc, nil
}

// schedulePoll is core.tasks.schedule_poll.
func (c *coreConfig) schedulePoll() time.Duration {
	return time.Duration(c.tasks.Get().SchedulePoll)
}
