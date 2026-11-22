// file: core/kernel/taskconfig.go
// version: 1.1.0
// guid: 5b0c9d52-6f3a-4a8e-9c47-2e1d8b7a4f60
// last-edited: 2026-11-19

package kernel

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"maps"
	"reflect"
	"slices"
	"strings"
	"time"

	"github.com/falkcorp/mediacore/config"
	"github.com/falkcorp/mediacore/internal/bootseam"
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
//
// Over a boot the store is the booted, layered one (cfgBoot's) and the
// section handles are the ones P0 registered, so every adapter below
// reads the environment, file, boot-record and stored values through the same
// Handle.Get; shared then marks a store the config boot owns, which close
// leaves alone.
type coreConfig struct {
	store  *config.Store
	shared bool
	tasks  *config.Handle[tasksSettings]
	// http and shutdown are core.http and core.shutdown, which start step 7
	// reads (the listener, the perimeter and the stop budget).
	http     *config.Handle[httpSettings]
	shutdown *config.Handle[shutdownSettings]
	// leaves is every key of the core sections (and, with the config boot on,
	// of the enabled modules' sections) by dotted path, for the
	// schedule-key declaration check (ValidateSchedules' declared function).
	leaves map[string]cfgLeaf
}

// cfgLeaf is one key of a core or module section: whether its type is
// tasks.ScheduleConfig and, when it is, how to read its current value, and
// the owner its namespace names ("core", or the module ID of modules.<id>).
type cfgLeaf struct {
	schedule bool
	owner    string
	read     func() tasks.ScheduleConfig
}

// coreConfig is the core config, built on the first call and kept: the layered
// store the config boot built, else (Describe) the defaults-only one. Boot
// calls it at P7, which is single-threaded, from the two places
// that read it: taskProblems when a definition exists (the schedule keys it
// declares) and constructTasks when the start rule held (the executor's and
// the scheduler's settings). A boot that registers no kind and finds no run
// never calls it, so ABO's boot skips the build entirely; closeCore closes
// the store only when it was built.
func (k *Kernel) coreConfig() (*coreConfig, error) {
	if k.coreCfg == nil {
		var c *coreConfig
		var err error
		if cb := k.cfgBoot; cb != nil {
			// The core sections' handles and the enabled modules' declared
			// sections (P3's live handles), so a module kind's schedule key
			// is declared by the section its module registered: a module's
			// settings arrive with Manifest.Settings (F3-07 b).
			c, err = coreConfigOver(cb.store, slices.Concat(cb.handles, slices.Collect(maps.Values(k.settingHandles))))
			if c != nil {
				c.shared = true
			}
		} else {
			c, err = newCoreConfig(k.cfg.AppID, k.cfg.CoreDefaults, k.sectionCache, k.configStoreSeen)
		}
		if err != nil {
			return nil, err
		}
		k.coreCfg = c
	}
	return k.coreCfg, nil
}

// newCoreConfig builds the defaults-only store over coreSections(). cache, when
// non-nil, is where it gets its sections built (config.Options.SectionCache);
// seen is the boot's bootseam.Seams.ConfigStore (newConfigStore).
func newCoreConfig(appID string, d CoreDefaults, cache *config.SectionCache, seen func(any)) (*coreConfig, error) {
	st, err := newConfigStore(seen, config.Options{
		AppID:        appID,
		Environ:      []string{}, // no environment layer: os.Environ is not read
		Logger:       slog.New(slog.NewTextHandler(io.Discard, nil)),
		SectionCache: cache,
	})
	if err != nil {
		return nil, fmt.Errorf("kernel: core config: %w", err)
	}
	handles, err := config.RegisterSections(st.Registry(), coreSectionsWith(d)...)
	if err != nil {
		return nil, fmt.Errorf("kernel: core config: register the core sections: %w", err)
	}
	c, err := coreConfigOver(st, handles)
	if err != nil {
		return nil, errors.Join(err, st.Close(context.Background()))
	}
	return c, nil
}

// coreConfigOver is the core config over st and the handles of the sections
// registered on it (core's, and with the config boot on the enabled modules'):
// the tasks section and every key of every section.
func coreConfigOver(st *config.Store, handles []any) (*coreConfig, error) {
	c := &coreConfig{store: st, leaves: map[string]cfgLeaf{}}
	for _, h := range handles {
		switch th := h.(type) {
		case *config.Handle[tasksSettings]:
			c.tasks = th
		case *config.Handle[httpSettings]:
			c.http = th
		case *config.Handle[shutdownSettings]:
			c.shutdown = th
		}
		cfgWalkLeaves(c.leaves, h)
	}
	if c.tasks == nil || c.http == nil || c.shutdown == nil {
		return nil, fmt.Errorf("kernel: core config: coreSections lacks core.tasks, core.http or core.shutdown")
	}
	return c, nil
}

// close releases the store.
func (c *coreConfig) close(ctx context.Context) error {
	if c == nil || c.shared {
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
	owner := leafOwner(string(p.Path()))
	get := reflect.ValueOf(handle).MethodByName("Get")
	if !get.IsValid() {
		return
	}
	root := func() reflect.Value { return get.Call(nil)[0].Elem() }
	cfgWalkType(out, owner, string(p.Path()), root, nil, reflect.TypeOf(root().Interface()))
}

// cfgWalkType recurses over t (a section's value type) and records its keys.
func cfgWalkType(out map[string]cfgLeaf, owner, base string, root func() reflect.Value, idx []int, t reflect.Type) {
	if t.Kind() != reflect.Struct || t == reflect.TypeFor[tasks.ScheduleConfig]() || selfEncoding(t) {
		leaf := cfgLeaf{owner: owner}
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
		cfgWalkType(out, owner, base+"."+name, root, append(append([]int(nil), idx...), i), f.Type)
	}
}

// selfEncoding reports a struct type that writes its own JSON (a key, not a
// group): it implements json.Marshaler.
func selfEncoding(t reflect.Type) bool {
	return t.Implements(reflect.TypeFor[json.Marshaler]()) || reflect.PointerTo(t).Implements(reflect.TypeFor[json.Marshaler]())
}

// leafOwner is the owner a section path names: the module ID of
// modules.<id>[.…], else "core". It is schedspec.KeyOwner's reading of a key's
// namespace, applied to the section the key lives in.
func leafOwner(path string) string {
	if rest, ok := strings.CutPrefix(path, "modules."); ok {
		id, _, _ := strings.Cut(rest, ".")
		return id
	}
	return "core"
}

// declared is scheduler.ValidateSchedules' declaration oracle: whether key is a
// declared key, whether its type is tasks.ScheduleConfig, and its owner. With
// the config boot off only core's keys are declared (no module section is
// registered, so a modules.<id>... schedule key is refused); with it on the
// enabled modules' Manifest.Settings sections are declared too, each owned by
// its module ID.
func (c *coreConfig) declared(key string) (isSchedule bool, owner string, ok bool) {
	l, ok := c.leaves[key]
	if !ok {
		return false, "", false
	}
	return l.schedule, l.owner, true
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

// bootConfigStoreSeen is the boot's bootseam.Seams.ConfigStore, the observer
// newConfigStore tells about each store; nil in every production boot.
func bootConfigStoreSeen(ctx context.Context) func(any) {
	if s := bootseam.From(ctx); s != nil {
		return s.ConfigStore
	}
	return nil
}

// bootSectionCache is the section cache a test handed the boot
// (bootseam.SectionCache: Seams.SectionCache, else bootseam.WithSectionCache),
// nil when it handed none (every production boot).
func bootSectionCache(ctx context.Context) (*config.SectionCache, error) {
	sc := bootseam.SectionCache(ctx)
	if sc == nil {
		return nil, nil
	}
	c, ok := sc.(*config.SectionCache)
	if !ok {
		return nil, fmt.Errorf("kernel: the boot's section cache is a %T, want *config.SectionCache", sc)
	}
	return c, nil
}
