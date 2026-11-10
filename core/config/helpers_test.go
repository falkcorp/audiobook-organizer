// file: core/config/helpers_test.go
// version: 1.0.2
// guid: b5d04556-252d-45d5-96bd-ee0c3a1729aa
// last-edited: 2026-11-10

package config_test

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/falkcorp/mediacore/config"
	"github.com/falkcorp/mediacore/config/configtest"
)

// Fixture keys.
const (
	kLevel       config.Path = configtest.BatcavePath + ".level"
	kInterval    config.Path = configtest.BatcavePath + ".interval"
	kWorkers     config.Path = configtest.BatcavePath + ".workers"
	kMode        config.Path = configtest.BatcavePath + ".mode"
	kPath        config.Path = configtest.BatcavePath + ".path"
	kSignal      config.Path = configtest.BatcavePath + ".signal"
	kOwner       config.Path = configtest.BatcavePath + ".owner_email"
	kVault       config.Path = configtest.BatcavePath + ".vault"
	kCount       config.Path = configtest.BatcavePath + ".gadget.count"
	kAllies      config.Path = configtest.BatcavePath + ".allies"
	kAPIKey      config.Path = configtest.BatcavePath + ".api_key"
	kMin         config.Path = configtest.WatchPath + ".min"
	kMax         config.Path = configtest.WatchPath + ".max"
	kVillains    config.Path = configtest.WatchPath + ".villains"
	kBatman      config.Path = configtest.ModulesPath + ".batman.enabled"
	kRobin       config.Path = configtest.ModulesPath + ".robin.enabled"
	pathsSection config.Path = "core.paths"
	stateDir     config.Path = "core.paths.state_dir"
	ptBaseURL    config.Path = "integrations.ai.wayne.base_url"
	ptAPIKey     config.Path = "integrations.ai.wayne.api_key"
)

// logRecorder is a slog handler that keeps every record's message and
// attributes, one line per record.
type logRecorder struct {
	mu   sync.Mutex
	msgs []string
}

func (l *logRecorder) Enabled(context.Context, slog.Level) bool { return true }
func (l *logRecorder) Handle(_ context.Context, r slog.Record) error {
	line := r.Message
	r.Attrs(func(a slog.Attr) bool {
		line += " " + a.String()
		return true
	})
	l.mu.Lock()
	defer l.mu.Unlock()
	l.msgs = append(l.msgs, line)
	return nil
}

// all is every recorded line, attributes included.
func (l *logRecorder) all() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return strings.Join(l.msgs, "\n")
}
func (l *logRecorder) WithAttrs([]slog.Attr) slog.Handler { return l }
func (l *logRecorder) WithGroup(string) slog.Handler      { return l }

// count is how many messages contain sub.
func (l *logRecorder) count(sub string) int {
	l.mu.Lock()
	defer l.mu.Unlock()
	n := 0
	for _, m := range l.msgs {
		if strings.Contains(m, sub) {
			n++
		}
	}
	return n
}

// rigOpts configures a rig. Operator values are JSON.
type rigOpts struct {
	o          config.Options
	file, env  map[config.Path]string
	rows       map[config.Path]string
	bootRecord string // the record's bytes before boot; "" = absent
	noRecord   bool   // no BootRecordRoot: an app without a state directory
	modules    map[string]configtest.Module
	dir        string // the state directory; "" = a fresh temp dir
	stateDir   string // when set, core.paths registers with this default
}

// pathsFix mirrors the kernel's core.paths section: the pre-boot key.
type pathsFix struct {
	StateDir string `json:"state_dir" reload:"boot" protect:"operator"`
}

// rig is a store over the fixture sections with in-memory doubles.
type rig struct {
	t     *testing.T
	st    *config.Store
	dir   string
	roots []string // every directory BootRecordRoot was called with
	mem   *configtest.Memory
	cm    *configtest.Committer
	logs  *logRecorder
	cave  *config.Handle[configtest.Batcave]
	watch *config.Handle[configtest.Watch]
	mods  *config.Handle[map[string]configtest.Module]
}

func source(kind string, vals map[config.Path]string) *config.TestSource {
	if vals == nil {
		return nil
	}
	src := &config.TestSource{Vals: map[config.Path]json.RawMessage{}, By: func(p config.Path) string {
		if kind == "file" {
			return "file gotham.yaml"
		}
		if p == ptAPIKey {
			return "env WAYNE_API_KEY"
		}
		return "env GOTHAM_" + strings.ToUpper(strings.ReplaceAll(string(p), ".", "_"))
	}}
	for p, v := range vals {
		src.Vals[p] = json.RawMessage(v)
	}
	return src
}

// newRig registers the fixture sections; it does not boot.
func newRig(t *testing.T, ro rigOpts) *rig {
	t.Helper()
	r := &rig{t: t, dir: ro.dir, mem: configtest.NewMemory(), logs: &logRecorder{}}
	r.cm = configtest.NewCommitter(r.mem)
	if r.dir == "" {
		r.dir = t.TempDir()
	}
	if ro.bootRecord != "" {
		if err := os.WriteFile(filepath.Join(r.dir, "config.boot.json"), []byte(ro.bootRecord), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	o := ro.o
	if o.AppID == "" {
		o.AppID = "gotham"
	}
	if o.Logger == nil {
		o.Logger = slog.New(r.logs)
	}
	if !ro.noRecord {
		o.BootRecordRoot = func(d string) (*os.Root, error) {
			r.roots = append(r.roots, d)
			return os.OpenRoot(r.dir)
		}
	}
	st, err := config.NewStore(o)
	if err != nil {
		t.Fatal(err)
	}
	r.st = st
	t.Cleanup(func() {
		if err := st.Close(context.Background()); err != nil {
			t.Errorf("Close: %v", err)
		}
	})
	config.SetOperatorSources(st, source("file", ro.file), source("env", ro.env))
	if ro.stateDir != "" {
		if _, err := config.Register(st.Registry(), config.Define(pathsSection, pathsFix{StateDir: ro.stateDir})); err != nil {
			t.Fatal(err)
		}
	}
	if r.cave, err = config.Register(st.Registry(), configtest.BatcaveSection()); err != nil {
		t.Fatal(err)
	}
	if r.watch, err = config.Register(st.Registry(), configtest.WatchSection()); err != nil {
		t.Fatal(err)
	}
	if r.mods, err = config.Register(st.Registry(), configtest.ModulesSection(ro.modules)); err != nil {
		t.Fatal(err)
	}
	for p, v := range ro.rows {
		r.mem.Put(config.Override{Path: p, Value: json.RawMessage(v), By: "alfred"})
	}
	return r
}

// start boots, loads and attaches, failing t on any error.
func (r *rig) start() config.AttachReport {
	r.t.Helper()
	ctx := context.Background()
	if _, err := r.st.Boot(ctx); err != nil {
		r.t.Fatalf("Boot: %v", err)
	}
	if err := r.st.Load(ctx); err != nil {
		r.t.Fatalf("Load: %v", err)
	}
	rep, err := r.st.Attach(ctx, r.mem, r.cm)
	if err != nil {
		r.t.Fatalf("Attach: %v", err)
	}
	return rep
}

// started is newRig + start.
func started(t *testing.T, ro rigOpts) *rig {
	t.Helper()
	r := newRig(t, ro)
	r.start()
	return r
}

// apply is Apply with a background context.
func (r *rig) apply(a config.Actor, set map[config.Path]string, unset ...config.Path) (config.Result, error) {
	p := config.Patch{Set: map[config.Path]json.RawMessage{}, Unset: unset}
	for k, v := range set {
		p.Set[k] = json.RawMessage(v)
	}
	return r.st.Apply(context.Background(), p, a)
}

// record returns the boot record's bytes, nil when absent.
func (r *rig) record() []byte {
	r.t.Helper()
	b, err := os.ReadFile(filepath.Join(r.dir, "config.boot.json"))
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		r.t.Fatal(err)
	}
	return b
}

// state is a fingerprint of everything Apply may change: both snapshots of
// every section, the generation, every stored row and the boot record.
func (r *rig) state() string {
	r.t.Helper()
	var buf bytes.Buffer
	for _, v := range []any{r.cave.Get(), r.cave.Pending(), r.watch.Get(), r.watch.Pending(), r.mods.Get(), r.mods.Pending(), r.mem.Rows()} {
		b, err := json.Marshal(v)
		if err != nil {
			r.t.Fatal(err)
		}
		buf.Write(b)
		buf.WriteByte('\n')
	}
	b, err := json.Marshal([]any{r.st.Generation(), r.cave.Generation(), r.watch.Generation(), string(r.record())})
	if err != nil {
		r.t.Fatal(err)
	}
	buf.Write(b)
	return buf.String()
}

// Actors (05-config §1.3.7): an interactive admin, an interactive
// settings.manage holder without core:admin, and the auth-disabled
// principal, which never holds core:admin and is not interactive.
func admin() config.Actor {
	return config.Actor{Subject: "alfred", Interactive: true, Can: func(string) bool { return true }}
}

func manager() config.Actor {
	return config.Actor{Subject: "robin", Interactive: true, Can: func(p string) bool { return p != "core:admin" }}
}

func authOff() config.Actor {
	return config.Actor{Can: func(p string) bool { return p != "core:admin" }}
}

func mustJSON(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}
