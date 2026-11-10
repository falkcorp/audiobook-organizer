// file: core/config/store_test.go
// version: 1.0.3
// guid: 922df5f6-7010-42b3-883c-cc23b3885d55
// last-edited: 2026-11-10

package config_test

import (
	"context"
	"encoding/json"
	"errors"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/falkcorp/mediacore/config"
	"github.com/falkcorp/mediacore/config/configtest"
	"github.com/falkcorp/mediacore/secrets"
)

// noStorage asserts the override store and the committer were never touched.
func noStorage(t *testing.T, r *rig, when string) {
	t.Helper()
	if r.mem.Loads() != 0 || r.mem.Stages() != 0 || r.cm.Calls() != 0 {
		t.Errorf("%s: loads %d, stages %d, commits %d; want no storage access", when, r.mem.Loads(), r.mem.Stages(), r.cm.Calls())
	}
}

// TestBoot_ResolvesPathsWithoutStorage (05-config §1.11): Boot resolves
// core.paths.state_dir from flag > env > default, hands exactly that
// directory to BootRecordRoot, and exposes the boot keys, all before any
// storage exists.
func TestBoot_ResolvesPathsWithoutStorage(t *testing.T) {
	ctx := context.Background()
	rec := `{"format":"mediacore-boot/1","gen":1,"values":{"core.batcave.mode":"day"}}`
	cases := []struct {
		name  string
		ro    rigOpts
		want  string
		src   config.Source
		lockd string
	}{
		{"default", rigOpts{}, "/var/lib/gotham", config.SourceDefault, ""},
		{"env", rigOpts{env: map[config.Path]string{stateDir: `"/srv/gotham"`}}, "/srv/gotham", config.SourceEnv, "env GOTHAM_CORE_PATHS_STATE_DIR"},
		{"flag beats env", rigOpts{env: map[config.Path]string{stateDir: `"/srv/gotham"`}, o: config.Options{Flags: map[config.Path]string{stateDir: "/opt/gotham"}}}, "/opt/gotham", config.SourceFlag, "flag core.paths.state_dir"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			ro := c.ro
			ro.stateDir, ro.bootRecord = "/var/lib/gotham", rec
			r := newRig(t, ro)
			bv, err := r.st.Boot(ctx)
			if err != nil {
				t.Fatal(err)
			}
			if !slices.Equal(r.roots, []string{c.want}) {
				t.Errorf("BootRecordRoot called with %q, want [%s]", r.roots, c.want)
			}
			raw, src, ok := bv.Raw(stateDir)
			if !ok || string(raw) != mustJSON(t, c.want) || src != c.src {
				t.Errorf("BootView %s = %s %v %v, want %q from %v", stateDir, raw, src, ok, c.want, c.src)
			}
			if raw, src, ok := bv.Raw(kMode); !ok || string(raw) != `"day"` || src != config.SourceBootRecord {
				t.Errorf("BootView %s = %s %v %v, want \"day\" from the boot record", kMode, raw, src, ok)
			}
			if raw, src, ok := bv.Raw(kVault); !ok || string(raw) != `"wayne-manor"` || src != config.SourceDefault {
				t.Errorf("BootView %s = %s %v %v, want the operator-only default", kVault, raw, src, ok)
			}
			if _, _, ok := bv.Raw(kLevel); ok {
				t.Errorf("BootView exposes the live key %s", kLevel)
			}
			noStorage(t, r, "after Boot")
			if err := r.st.Load(ctx); err != nil {
				t.Fatal(err)
			}
			noStorage(t, r, "after Load")
			if len(r.roots) != 1 {
				t.Errorf("Load reopened the boot record's directory: %q", r.roots)
			}
			if ks, _ := r.st.Lookup(stateDir); ks.LockedBy != c.lockd {
				t.Errorf("LockedBy = %q, want %q", ks.LockedBy, c.lockd)
			}
			if _, err := r.st.Boot(ctx); err == nil {
				t.Error("a second Boot succeeded")
			}
		})
	}
	// BootRecordRoot failing fails boot.
	st, err := config.NewStore(config.Options{AppID: "gotham", BootRecordRoot: func(string) (*os.Root, error) { return nil, os.ErrPermission }})
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := st.Close(ctx); err != nil {
			t.Error(err)
		}
	}()
	if _, err := st.Boot(ctx); !errors.Is(err, os.ErrPermission) {
		t.Errorf("Boot with a failing root: %v", err)
	}
}

// TestPreBootKeyInConfigFileRefused (06 §6.10): core.paths.state_dir in the
// config file fails P0 before any directory is opened; the same value by env
// boots.
func TestPreBootKeyInConfigFileRefused(t *testing.T) {
	ctx := context.Background()
	r := newRig(t, rigOpts{stateDir: "/var/lib/gotham", file: map[config.Path]string{stateDir: `"/srv/gotham"`}})
	_, err := r.st.Boot(ctx)
	if err == nil || !strings.Contains(err.Error(), string(stateDir)) || !strings.Contains(err.Error(), "pre-boot") {
		t.Fatalf("Boot = %v, want a refusal naming %s as pre-boot", err, stateDir)
	}
	if len(r.roots) != 0 {
		t.Errorf("BootRecordRoot was called with %q before the refusal", r.roots)
	}
	if err := r.st.Load(ctx); err == nil {
		t.Error("Load after the refused Boot succeeded")
	}
	r = newRig(t, rigOpts{stateDir: "/var/lib/gotham", env: map[config.Path]string{stateDir: `"/srv/gotham"`}})
	r.start()
	if got := r.roots; !slices.Equal(got, []string{"/srv/gotham"}) {
		t.Errorf("by env: BootRecordRoot called with %q", got)
	}
}

// TestOverride_IgnoredWhenOperatorLocked (05-config §1.3.5): a row of a key
// an operator layer supplies is kept, listed in AttachReport.Ignored, logged
// once, and the operator value stays effective.
func TestOverride_IgnoredWhenOperatorLocked(t *testing.T) {
	r := newRig(t, rigOpts{
		env:  map[config.Path]string{kLevel: `"warn"`},
		o:    config.Options{Flags: map[config.Path]string{kMax: "20"}},
		rows: map[config.Path]string{kLevel: `"debug"`, kMax: `30`, kCount: `7`},
	})
	rep := r.start()
	want := map[config.Path]string{kLevel: "env GOTHAM_CORE_BATCAVE_LEVEL", kMax: "flag modules.gotham.watch.max"}
	if !maps.Equal(rep.Ignored, want) {
		t.Errorf("Ignored = %v, want %v", rep.Ignored, want)
	}
	if rep.Applied != 1 || len(rep.Quarantined) != 0 {
		t.Errorf("Applied %d, Quarantined %v; want only gadget.count applied", rep.Applied, rep.Quarantined)
	}
	if g := r.cave.Get(); g.Level != "warn" || g.Gadget.Count != 7 {
		t.Errorf("Get = level %s count %d, want warn (env) and 7 (row)", g.Level, g.Gadget.Count)
	}
	if r.watch.Get().Max != 20 {
		t.Errorf("max = %d, want the flag's 20", r.watch.Get().Max)
	}
	if rows := r.mem.Rows(); string(rows[kLevel].Value) != `"debug"` || string(rows[kMax].Value) != `30` {
		t.Errorf("an ignored row was not kept: %v", rows)
	}
	if n := r.logs.count("ignored because an operator layer"); n != 2 {
		t.Errorf("logged %d times, want once per ignored row (2)", n)
	}
	ks, _ := r.st.Lookup(kLevel)
	if ks.Source != config.SourceEnv || ks.LockedBy != "env GOTHAM_CORE_BATCAVE_LEVEL" || string(ks.Effective) != `"warn"` {
		t.Errorf("KeyState = %v %q %s", ks.Source, ks.LockedBy, ks.Effective)
	}
	noWrite(t, r)
}

// noWrite asserts Attach and everything before it wrote nothing.
func noWrite(t *testing.T, r *rig) {
	t.Helper()
	if r.mem.Stages() != 0 || r.cm.Calls() != 0 {
		t.Errorf("stages %d, commits %d; want no write", r.mem.Stages(), r.cm.Calls())
	}
}

// TestBoot_ModuleEnablementFromBootRecord (05-config §1.11): a UI-written
// disable is honoured by Boot at the next start; operator env beats it.
func TestBoot_ModuleEnablementFromBootRecord(t *testing.T) {
	ctx := context.Background()
	mods := map[string]configtest.Module{"batman": {Enabled: true}, "robin": {Enabled: true}}
	r1 := started(t, rigOpts{modules: mods})
	res, err := r1.apply(admin(), map[config.Path]string{kBatman: `false`})
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(res.RestartPending, []config.Path{kBatman}) || !(*r1.mods.Get())["batman"].Enabled || (*r1.mods.Pending())["batman"].Enabled {
		t.Fatalf("RestartPending %v, Get %v, Pending %v", res.RestartPending, r1.mods.Get(), r1.mods.Pending())
	}
	if c := r1.cm.Commits(); len(c) != 1 || len(c[0].Set) != 0 || !slices.Equal(c[0].Boot, []config.Path{kBatman}) {
		t.Errorf("commit = %+v, want no row and the boot key named", c)
	}

	r2 := newRig(t, rigOpts{dir: r1.dir, modules: mods})
	bv, err := r2.st.Boot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if v, src, ok := bv.Bool(kBatman); !ok || v || src != config.SourceBootRecord {
		t.Errorf("next start: %s = %v %v %v, want false from the boot record", kBatman, v, src, ok)
	}
	if _, _, ok := bv.Bool(kRobin); ok {
		t.Errorf("%s listed with no layer supplying it (its default is the manifest's)", kRobin)
	}
	r2.start2(ctx)
	if (*r2.mods.Get())["batman"].Enabled {
		t.Error("the stored disable is not effective after the restart")
	}

	r3 := newRig(t, rigOpts{dir: r1.dir, modules: mods, env: map[config.Path]string{kBatman: `true`}})
	bv, err = r3.st.Boot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if v, src, ok := bv.Bool(kBatman); !ok || !v || src != config.SourceEnv {
		t.Errorf("env beats the record: %s = %v %v %v", kBatman, v, src, ok)
	}
	r3.start2(ctx)
	if _, err := r3.apply(admin(), map[config.Path]string{kBatman: `false`}); !errors.Is(err, config.ErrLocked) {
		t.Errorf("Apply of an env-locked boot key: %v, want ErrLocked", err)
	}
}

// start2 finishes a start after the test called Boot itself.
func (r *rig) start2(ctx context.Context) {
	r.t.Helper()
	if err := r.st.Load(ctx); err != nil {
		r.t.Fatalf("Load: %v", err)
	}
	if _, err := r.st.Attach(ctx, r.mem, r.cm); err != nil {
		r.t.Fatalf("Attach: %v", err)
	}
}

// TestBootRecord_AtomicAndQuarantine (05-config §1.5): a crash between the
// temp file and the rename keeps the previous record; an unparsable record
// is renamed .bad and raises health; an unreadable one fails boot.
func TestBootRecord_AtomicAndQuarantine(t *testing.T) {
	ctx := context.Background()
	prior := `{"format":"mediacore-boot/1","gen":1,"values":{"core.batcave.mode":"day","core.gotham_unknown.flag":true}}`

	t.Run("crash before rename", func(t *testing.T) {
		r := started(t, rigOpts{bootRecord: prior})
		before := r.state()
		config.SetBootRecordRenameHook(r.st, func() error { return errors.New("power cut") })
		if _, err := r.apply(admin(), map[config.Path]string{kMode: `"night"`}); err == nil || !strings.Contains(err.Error(), "power cut") {
			t.Fatalf("Apply = %v, want the write failure", err)
		}
		refused(t, r, before, 0)
		if got := string(r.record()); got != prior {
			t.Errorf("record = %s, want the previous bytes", got)
		}
		next := newRig(t, rigOpts{dir: r.dir})
		next.start()
		if next.cave.Get().Mode != "day" {
			t.Errorf("after the crash the next start reads mode %s, want day", next.cave.Get().Mode)
		}
		config.SetBootRecordRenameHook(r.st, nil)
		if _, err := r.apply(admin(), map[config.Path]string{kMode: `"night"`}); err != nil {
			t.Fatal(err)
		}
		if _, err := os.Stat(filepath.Join(r.dir, "config.boot.json.tmp")); !os.IsNotExist(err) {
			t.Errorf("the temp file survived a successful write: %v", err)
		}
		fi, err := os.Stat(filepath.Join(r.dir, "config.boot.json"))
		if err != nil || fi.Mode().Perm() != 0o600 {
			t.Errorf("record mode = %v (%v), want 0600", fi.Mode(), err)
		}
		var rec struct {
			Format string                     `json:"format"`
			Gen    uint64                     `json:"gen"`
			Values map[string]json.RawMessage `json:"values"`
		}
		if err := json.Unmarshal(r.record(), &rec); err != nil {
			t.Fatal(err)
		}
		if rec.Format != "mediacore-boot/1" || rec.Gen != 1 || string(rec.Values["core.gotham_unknown.flag"]) != "true" {
			t.Errorf("record = %+v, want the unknown entry retained", rec)
		}
		if _, ok := rec.Values[string(kMode)]; ok {
			t.Error("a boot key set back to its default kept its record entry")
		}
	})

	for name, garbage := range map[string]string{"not JSON": "{not json", "wrong format": `{"format":"other/9","gen":1,"values":{}}`, "unknown field": `{"format":"mediacore-boot/1","values":{},"extra":1}`} {
		t.Run(name, func(t *testing.T) {
			r := started(t, rigOpts{bootRecord: garbage})
			if r.record() != nil {
				t.Error("the unparsable record was not moved aside")
			}
			if b, err := os.ReadFile(filepath.Join(r.dir, "config.boot.json.bad")); err != nil || string(b) != garbage {
				t.Errorf(".bad = %q (%v), want the original bytes", b, err)
			}
			if !slices.Contains(r.st.HealthTokens(), "config_boot_record_quarantined") {
				t.Errorf("health tokens %v", r.st.HealthTokens())
			}
			if r.cave.Get().Mode != "night" {
				t.Errorf("mode = %s, want the default", r.cave.Get().Mode)
			}
		})
	}

	t.Run("one value does not decode", func(t *testing.T) {
		r := started(t, rigOpts{bootRecord: `{"format":"mediacore-boot/1","gen":1,"values":{"core.batcave.mode":"dusk","core.modules.batman.enabled":false}}`, modules: map[string]configtest.Module{"batman": {Enabled: true}}})
		if r.cave.Get().Mode != "night" || (*r.mods.Get())["batman"].Enabled {
			t.Errorf("mode %s, batman %v: want the bad value ignored and the good one kept", r.cave.Get().Mode, (*r.mods.Get())["batman"].Enabled)
		}
		if ks, _ := r.st.Lookup(kMode); ks.Quarantined == nil {
			t.Error("KeyState.Quarantined is nil for the bad boot value")
		}
		if !slices.Contains(r.st.HealthTokens(), "config_boot_record_quarantined") {
			t.Errorf("health tokens %v: a quarantined boot value must degrade health", r.st.HealthTokens())
		}
		if !strings.Contains(string(r.record()), `"dusk"`) {
			t.Error("the bad value was dropped from the record")
		}
	})

	t.Run("unknown key", func(t *testing.T) {
		r := started(t, rigOpts{bootRecord: prior})
		if !slices.Contains(r.st.HealthTokens(), "config_boot_record_unknown_keys") {
			t.Errorf("health tokens %v", r.st.HealthTokens())
		}
	})

	t.Run("unreadable", func(t *testing.T) {
		r := newRig(t, rigOpts{})
		if err := os.Mkdir(filepath.Join(r.dir, "config.boot.json"), 0o700); err != nil {
			t.Fatal(err)
		}
		if _, err := r.st.Boot(ctx); err == nil || !strings.Contains(err.Error(), "boot record") {
			t.Fatalf("Boot = %v, want a fail-closed boot-record error", err)
		}
	})
}

// TestAttach_QuarantineInvalidSection (05-config §1.4 step 6): a section that
// fails with its rows and passes without them has every row quarantined, kept
// and reported; boot continues; a row that does not decode never reaches the
// build as a zero value.
func TestAttach_QuarantineInvalidSection(t *testing.T) {
	t.Run("validator", func(t *testing.T) {
		r := newRig(t, rigOpts{
			o:    config.Options{Flags: map[config.Path]string{kMax: "5"}},
			rows: map[config.Path]string{kMin: `8`, kVillains: `["bane"]`, kLevel: `"warn"`},
		})
		rep := r.start()
		if len(rep.Quarantined) != 2 || rep.Quarantined[kMin] == nil || rep.Quarantined[kVillains] == nil || rep.Applied != 1 {
			t.Fatalf("report = %+v, want min and villains quarantined, level applied", rep)
		}
		if w := r.watch.Get(); w.Min != 1 || w.Max != 5 || !slices.Equal(w.Villains, []string{"joker"}) {
			t.Errorf("watch = %+v, want defaults with the flag's max", w)
		}
		if r.cave.Get().Level != "warn" {
			t.Error("another section's row was not applied")
		}
		if ks, _ := r.st.Lookup(kMin); ks.Quarantined == nil || ks.Source != config.SourceDefault {
			t.Errorf("KeyState = %+v", ks)
		}
		if len(r.mem.Rows()) != 3 {
			t.Errorf("rows %v: a quarantined row must be kept", r.mem.Rows())
		}
		if !slices.Contains(r.st.HealthTokens(), "config_overrides_quarantined") {
			t.Errorf("health tokens %v", r.st.HealthTokens())
		}
		noWrite(t, r)
	})
	t.Run("undecodable row", func(t *testing.T) {
		r := newRig(t, rigOpts{rows: map[config.Path]string{kCount: `"three"`, kLevel: `"warn"`, kMax: `20`}})
		rep := r.start()
		if len(rep.Quarantined) != 2 || rep.Quarantined[kCount] == nil || rep.Quarantined[kLevel] == nil || rep.Applied != 1 {
			t.Fatalf("report = %+v, want the batcave rows quarantined and max applied", rep)
		}
		if g := r.cave.Get(); g.Gadget.Count != 3 || g.Level != "info" || g.Gadget.Name != "batarang" {
			t.Errorf("batcave = %+v, want defaults, never zero values", g)
		}
		if r.watch.Get().Max != 20 {
			t.Error("the healthy section's row was not applied")
		}
		noWrite(t, r)
	})
	t.Run("wrong-layer rows", func(t *testing.T) {
		r := newRig(t, rigOpts{
			o:    config.Options{Passthrough: func(p config.Path) bool { return strings.HasPrefix(string(p), "integrations.") }},
			rows: map[config.Path]string{kAPIKey: `"wayne-tech"`, kVault: `"arkham"`, kMode: `"day"`, ptBaseURL: `"x"`, "core.batcave.utility_belt": `1`},
		})
		rep := r.start()
		for _, p := range []config.Path{kAPIKey, kVault, kMode} {
			if rep.Quarantined[p] == nil {
				t.Errorf("%s: not quarantined", p)
			}
		}
		if !slices.Equal(rep.Unknown, []config.Path{"core.batcave.utility_belt", ptBaseURL}) {
			t.Errorf("Unknown = %v", rep.Unknown)
		}
		if g := r.cave.Get(); g.Vault != "wayne-manor" || g.Mode != "night" {
			t.Errorf("batcave = %+v: a wrong-layer row took effect", g)
		}
		noWrite(t, r)
	})
	t.Run("load error", func(t *testing.T) {
		r := newRig(t, rigOpts{})
		boom := errors.New("the batcomputer is down")
		r.mem.FailLoad(boom)
		ctx := context.Background()
		if err := r.st.Load(ctx); err != nil {
			t.Fatal(err)
		}
		if _, err := r.st.Attach(ctx, r.mem, r.cm); !errors.Is(err, boom) {
			t.Fatalf("Attach = %v, want the load error", err)
		}
	})
}

// TestLoad_OperatorValueFailsBoot: a section whose operator values fail
// decode or Validate fails Load, naming the layer; an unknown flag fails Load.
func TestLoad_OperatorValueFailsBoot(t *testing.T) {
	ctx := context.Background()
	cases := []struct {
		ro   rigOpts
		want error
		name string
	}{
		{rigOpts{o: config.Options{Flags: map[config.Path]string{kWorkers: "0"}}}, config.ErrInvalid, "flag core.batcave.workers"},
		{rigOpts{env: map[config.Path]string{kMin: `50`}}, config.ErrInvalid, "env GOTHAM_MODULES_GOTHAM_WATCH_MIN"},
		{rigOpts{o: config.Options{Flags: map[config.Path]string{"core.batcave.utility_belt": "x"}}}, config.ErrUnknownKey, "core.batcave.utility_belt"},
		{rigOpts{env: map[config.Path]string{kMax: `-1`}}, config.ErrInvalid, "GOTHAM_MODULES_GOTHAM_WATCH_MAX"},
	}
	for _, c := range cases {
		r := newRig(t, c.ro)
		err := r.st.Load(ctx)
		if !errors.Is(err, c.want) || !strings.Contains(err.Error(), c.name) {
			t.Errorf("Load = %v, want %v naming %s", err, c.want, c.name)
		}
	}
}

// TestRegistry_SealedAfterLoad: a module section registers after Boot;
// nothing registers after Load; a path registers once.
func TestRegistry_SealedAfterLoad(t *testing.T) {
	ctx := context.Background()
	type late struct {
		On bool `json:"on"`
	}
	r := newRig(t, rigOpts{})
	if _, err := r.st.Boot(ctx); err != nil {
		t.Fatal(err)
	}
	h, err := config.Register(r.st.Registry(), config.Define[late]("modules.gotham.late", late{On: true}))
	if err != nil {
		t.Fatalf("register after Boot: %v", err)
	}
	if _, err := config.Register(r.st.Registry(), configtest.WatchSection()); !errors.Is(err, config.ErrDuplicate) {
		t.Errorf("a second registration of %s: %v, want ErrDuplicate", configtest.WatchPath, err)
	}
	if err := r.st.Load(ctx); err != nil {
		t.Fatal(err)
	}
	if !h.Get().On {
		t.Error("the late section was not built")
	}
	if _, err := config.Register(r.st.Registry(), config.Define[late]("modules.gotham.later", late{})); !errors.Is(err, config.ErrSealed) {
		t.Errorf("register after Load: %v, want ErrSealed", err)
	}
	if _, ok := r.st.Lookup("modules.gotham.later.on"); ok {
		t.Error("a refused section's key is indexed")
	}
}

// TestSectionPath_NestedNoOverlap (05-config §1.11): core.log,
// core.log.journal and core.log.schedules register side by side; a parent
// field named journal collides with the child section, in either order.
func TestSectionPath_NestedNoOverlap(t *testing.T) {
	ctx := context.Background()
	type logFix struct {
		Level string `json:"level"`
	}
	type journalFix struct {
		Dir string `json:"dir"`
	}
	type schedFix struct {
		Cron string `json:"cron"`
	}
	type logScalarJournal struct {
		Level   string `json:"level"`
		Journal string `json:"journal"`
	}
	type logNestedJournal struct {
		Level   string     `json:"level"`
		Journal journalFix `json:"journal"`
	}
	newStore := func(t *testing.T) *config.Store {
		st, err := config.NewStore(config.Options{AppID: "gotham"})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			if err := st.Close(ctx); err != nil {
				t.Error(err)
			}
		})
		return st
	}
	st := newStore(t)
	for _, err := range []error{
		register(st, config.Define[logFix]("core.log", logFix{Level: "info"})),
		register(st, config.Define[journalFix]("core.log.journal", journalFix{})),
		register(st, config.Define[schedFix]("core.log.schedules", schedFix{})),
	} {
		if err != nil {
			t.Fatal(err)
		}
	}
	if err := st.Load(ctx); err != nil {
		t.Fatal(err)
	}
	var got []config.Path
	for p := range st.Keys() {
		got = append(got, p)
	}
	if want := []config.Path{"core.log.journal.dir", "core.log.level", "core.log.schedules.cron"}; !slices.Equal(got, want) {
		t.Errorf("keys = %v, want %v", got, want)
	}
	for name, parent := range map[string]any{"scalar": config.Define[logScalarJournal]("core.log", logScalarJournal{}), "struct": config.Define[logNestedJournal]("core.log", logNestedJournal{})} {
		st := newStore(t)
		if err := registerAny(st, parent); err != nil {
			t.Fatal(err)
		}
		if err := register(st, config.Define[journalFix]("core.log.journal", journalFix{})); !errors.Is(err, config.ErrDuplicate) {
			t.Errorf("%s parent first: %v, want ErrDuplicate", name, err)
		}
		st = newStore(t)
		if err := register(st, config.Define[journalFix]("core.log.journal", journalFix{})); err != nil {
			t.Fatal(err)
		}
		if err := registerAny(st, parent); !errors.Is(err, config.ErrDuplicate) {
			t.Errorf("%s child first: %v, want ErrDuplicate", name, err)
		}
	}
}

func register[T any](st *config.Store, s config.Section[T]) error {
	_, err := config.Register(st.Registry(), s)
	return err
}

func registerAny(st *config.Store, s any) error {
	_, err := config.RegisterSections(st.Registry(), s)
	return err
}

// TestConfigFileHasSecrets_Redacted (DATA2-17): a file holding a secret key
// or a passthrough value reports it, and the redacted rendering drops both.
func TestConfigFileHasSecrets_Redacted(t *testing.T) {
	pt := config.Options{Passthrough: func(p config.Path) bool { return strings.HasPrefix(string(p), "integrations.") }}
	none := started(t, rigOpts{o: pt})
	if none.st.ConfigFileHasSecrets() {
		t.Error("no file: holds secrets")
	}
	if b, err := none.st.RedactedConfigFile(); b != nil || err != nil {
		t.Errorf("no file: redacted = %s, %v", b, err)
	}
	plain := started(t, rigOpts{o: pt, file: map[config.Path]string{kLevel: `"warn"`}})
	if plain.st.ConfigFileHasSecrets() {
		t.Error("a file of plain keys holds secrets")
	}
	for name, file := range map[string]map[config.Path]string{
		"secret key":  {kLevel: `"warn"`, kAPIKey: `"wayne-tech"`},
		"passthrough": {kLevel: `"warn"`, ptAPIKey: `"sk-gotham"`, ptBaseURL: `"https://ai.example.com"`},
	} {
		r := started(t, rigOpts{o: pt, file: file})
		if !r.st.ConfigFileHasSecrets() {
			t.Errorf("%s: ConfigFileHasSecrets false", name)
		}
		b, err := r.st.RedactedConfigFile()
		if err != nil {
			t.Fatal(err)
		}
		for _, leak := range []string{"wayne-tech", "sk-gotham", "ai.example.com", "api_key", "integrations."} {
			if strings.Contains(string(b), leak) {
				t.Errorf("%s: redacted file holds %q: %s", name, leak, b)
			}
		}
		if !strings.Contains(string(b), `"warn"`) {
			t.Errorf("%s: redacted file lost the plain key: %s", name, b)
		}
		for _, s := range []string{mustJSON(t, r.cave.Get()), keysJSON(t, r.st)} {
			if strings.Contains(s, "wayne-tech") {
				t.Errorf("%s: a file secret leaked into a snapshot or KeyState", name)
			}
		}
	}
}

func keysJSON(t *testing.T, st *config.Store) string {
	t.Helper()
	all := maps.Collect(st.Keys())
	return mustJSON(t, all)
}

// TestPassthrough_NotOwnedNotWritable (C11): passthrough paths are read raw
// from the operator layers and handed out, never owned, indexed, applied,
// imported or read from the DB layer.
func TestPassthrough_NotOwnedNotWritable(t *testing.T) {
	r := started(t, rigOpts{
		o: config.Options{
			Passthrough: func(p config.Path) bool { return strings.HasPrefix(string(p), "integrations.") },
			Flags:       map[config.Path]string{"integrations.ai.wayne.model": "batcomputer"},
		},
		env:  map[config.Path]string{ptAPIKey: `"sk-gotham"`, ptBaseURL: `"https://ai.example.com"`},
		rows: map[config.Path]string{ptBaseURL: `"https://joker.example.com"`},
	})
	got := maps.Collect(r.st.PassthroughValues())
	want := map[config.Path]config.OperatorValue{
		ptAPIKey:                       {Value: json.RawMessage(`"sk-gotham"`), Src: config.SourceEnv, By: "env WAYNE_API_KEY"},
		ptBaseURL:                      {Value: json.RawMessage(`"https://ai.example.com"`), Src: config.SourceEnv, By: "env GOTHAM_INTEGRATIONS_AI_WAYNE_BASE_URL"},
		"integrations.ai.wayne.model": {Value: json.RawMessage(`"batcomputer"`), Src: config.SourceFlag, By: "flag integrations.ai.wayne.model"},
	}
	if mustJSON(t, got) != mustJSON(t, want) {
		t.Errorf("PassthroughValues = %s\nwant %s", mustJSON(t, got), mustJSON(t, want))
	}
	got[ptAPIKey].Value[1] = 'X'
	for p, v := range r.st.PassthroughValues() {
		if p == ptAPIKey && string(v.Value) != `"sk-gotham"` {
			t.Error("a caller mutated the store's passthrough value")
		}
	}
	for p := range r.st.Keys() {
		if strings.HasPrefix(string(p), "integrations.") {
			t.Errorf("Keys lists the passthrough path %s", p)
		}
	}
	if _, ok := r.st.Lookup(ptBaseURL); ok {
		t.Error("Lookup finds a passthrough path")
	}
	before := r.state()
	if _, err := r.apply(admin(), map[config.Path]string{ptBaseURL: `"https://joker.example.com"`}); !errors.Is(err, config.ErrUnknownKey) {
		t.Errorf("Apply = %v, want ErrUnknownKey", err)
	}
	refused(t, r, before, 0)
	for _, v := range got {
		if strings.Contains(string(v.Value), "joker") {
			t.Error("the DB row reached a passthrough value")
		}
	}
}

// fakeResolver resolves every name to one value.
type fakeResolver struct{ val string }

func (f fakeResolver) Get(_ context.Context, n secrets.Name) (secrets.Value, secrets.Meta, error) {
	return secrets.NewValue(f.val), secrets.Meta{Name: n, Set: true, Source: secrets.SourceStored, Fingerprint: "0badf00d"}, nil
}

func (f fakeResolver) Meta(_ context.Context, n secrets.Name) (secrets.Meta, error) {
	return secrets.Meta{Name: n, Set: true, Source: secrets.SourceStored, Fingerprint: "0badf00d"}, nil
}

// TestKeys_NeverContainSecretValues (05-config §1.3.4): a resolved secret
// appears in no KeyState, snapshot, schema or log line; its Ref resolves
// through the bound resolver under the key's own name.
func TestKeys_NeverContainSecretValues(t *testing.T) {
	ctx := context.Background()
	r := newRig(t, rigOpts{})
	if err := r.st.Load(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := r.cave.Get().APIKey.Resolve(ctx); !errors.Is(err, secrets.ErrUnbound) {
		t.Errorf("before SetSecrets: Resolve = %v, want ErrUnbound", err)
	}
	r.st.SetSecrets(fakeResolver{val: "wayne-tech"})
	if _, err := r.st.Attach(ctx, r.mem, r.cm); err != nil {
		t.Fatal(err)
	}
	ref := r.cave.Get().APIKey
	if v, err := ref.Resolve(ctx); err != nil || v.Reveal() != "wayne-tech" || ref.Name() != secrets.Name(kAPIKey) {
		t.Fatalf("Resolve = %v (%v), name %s", v.IsZero(), err, ref.Name())
	}
	ks, ok := r.st.Lookup(kAPIKey)
	if !ok || ks.Effective != nil || ks.Secret == nil || !ks.Secret.Set || ks.Secret.Fingerprint != "0badf00d" || ks.Source != config.SourceOverride {
		t.Errorf("KeyState = %+v", ks)
	}
	schema, err := r.st.Schema(nil)
	if err != nil {
		t.Fatal(err)
	}
	logs := r.logs.all()
	for name, s := range map[string]string{"keys": keysJSON(t, r.st), "snapshot": mustJSON(t, r.cave.Get()), "pending": mustJSON(t, r.cave.Pending()), "schema": string(schema), "logs": logs} {
		if strings.Contains(s, "wayne-tech") {
			t.Errorf("%s holds the secret value", name)
		}
	}
}

// TestNoStoreWriteBeforeCheckpoint (the config half, C13): Boot and Load
// touch no storage; Attach reads once and writes nothing, whatever it finds:
// unknown, locked, quarantined and wrong-layer rows are all left in place.
func TestNoStoreWriteBeforeCheckpoint(t *testing.T) {
	ctx := context.Background()
	r := newRig(t, rigOpts{
		env:  map[config.Path]string{kLevel: `"warn"`},
		rows: map[config.Path]string{kLevel: `"debug"`, kCount: `"three"`, kMode: `"day"`, kAPIKey: `"x"`, "core.batcave.utility_belt": `1`, kMax: `20`},
	})
	rowsBefore := mustJSON(t, r.mem.Rows())
	if _, err := r.st.Boot(ctx); err != nil {
		t.Fatal(err)
	}
	noStorage(t, r, "after Boot")
	if err := r.st.Load(ctx); err != nil {
		t.Fatal(err)
	}
	noStorage(t, r, "after Load")
	if _, err := r.st.Attach(ctx, r.mem, r.cm); err != nil {
		t.Fatal(err)
	}
	if r.mem.Loads() != 1 {
		t.Errorf("Attach read %d times, want once", r.mem.Loads())
	}
	noWrite(t, r)
	if got := mustJSON(t, r.mem.Rows()); got != rowsBefore {
		t.Errorf("rows changed across Attach:\nbefore %s\nafter  %s", rowsBefore, got)
	}
	if r.record() != nil {
		t.Error("boot created a boot record")
	}
	if _, err := r.st.Attach(ctx, r.mem, r.cm); err == nil {
		t.Error("a second Attach succeeded")
	}
}

// TestSensitiveValueNeverInErrors (05-config §1.3.1): an invalid value of a
// sensitive key is refused or quarantined with an error that names the key
// and never the value, in Apply's error, the AttachReport and the logs.
func TestSensitiveValueNeverInErrors(t *testing.T) {
	bad := `{"email":"bruce@example.com"}`
	r := started(t, rigOpts{rows: map[config.Path]string{kOwner: bad}})
	var fe *config.FieldError
	_, err := r.apply(admin(), map[config.Path]string{kOwner: bad})
	if !errors.As(err, &fe) || fe.Path != kOwner {
		t.Fatalf("Apply = %v, want a FieldError on %s", err, kOwner)
	}
	r2 := newRig(t, rigOpts{rows: map[config.Path]string{kOwner: bad}})
	rep := r2.start()
	if rep.Quarantined[kOwner] == nil {
		t.Fatal("the bad row was not quarantined")
	}
	ks, _ := r2.st.Lookup(kOwner)
	for name, s := range map[string]string{"Apply": err.Error(), "report": rep.Quarantined[kOwner].Error(), "KeyState": ks.Quarantined.Error(), "logs": r.logs.all() + r2.logs.all()} {
		if strings.Contains(s, "bruce") {
			t.Errorf("%s carries the sensitive value: %s", name, s)
		}
	}
	// A key that is not sensitive keeps the validator's detail.
	if _, err := r.apply(admin(), map[config.Path]string{kLevel: `"loud"`}); err == nil || !strings.Contains(err.Error(), "loud") {
		t.Errorf("a plain key's error lost its detail: %v", err)
	}
}
