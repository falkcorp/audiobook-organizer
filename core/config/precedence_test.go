// file: core/config/precedence_test.go
// version: 1.0.2
// guid: 0f72db87-b1b3-4bcb-9393-6594c50533ca
// last-edited: 2026-11-11

package config_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/falkcorp/mediacore/config"
	"github.com/falkcorp/mediacore/storage"
)

// The two FailClosed keys, declared with the tags and the orders their
// owners give them (05-authn §8, 05-http-api §10).
type authFix struct {
	Mode string `json:"mode" reload:"restart" protect:"owner" schema:"enum=required|disabled"`
}

type gateFix struct {
	Enabled bool `json:"enabled" protect:"admin"`
}

type httpFix struct {
	BasicGate gateFix `json:"basic_gate"`
	N         int     `json:"n" schema:"maximum=50"`
}

// Validate refuses the Basic gate with N at 13, so a test can make the
// section fail with a FailClosed value and pass without it.
func (h *httpFix) Validate() error {
	if h.BasicGate.Enabled && h.N == 13 {
		return &config.FieldError{Path: kGate, Err: errors.New("the gate cannot open on 13")}
	}
	return nil
}

const (
	kAuthMode config.Path = "core.auth.mode"
	kGate     config.Path = "core.http.basic_gate.enabled"
)

// fcRig is a rig over the real env and file layers with the two FailClosed
// sections registered.
func fcRig(t *testing.T, env []string, yml string, rows map[config.Path]string) *rig {
	t.Helper()
	r := realRig(t, env, yml, rigOpts{rows: rows})
	if _, err := config.Register(r.st.Registry(), config.Define("core.auth", authFix{Mode: "required"},
		config.FailClosed(kAuthMode, json.RawMessage(`"required"`), json.RawMessage(`"disabled"`)))); err != nil {
		t.Fatal(err)
	}
	if _, err := config.Register(r.st.Registry(), config.Define("core.http", httpFix{},
		config.FailClosed(kGate, json.RawMessage("true"), json.RawMessage("false")))); err != nil {
		t.Fatal(err)
	}
	return r
}

// TestFailClosed_MoreRestrictiveWins (05-config §1.3.5, 14 E5; the row's
// gate test): for core.auth.mode and core.http.basic_gate.enabled, a stored
// value stricter than the operator value is effective and reported; a
// stricter operator value is effective and locks; Apply accepts a value at
// least as strict as the operator value (and an unset) and refuses a weaker
// one; Register refuses FailClosed on a string key or with a value missing.
func TestFailClosed_MoreRestrictiveWins(t *testing.T) {
	keys := []struct {
		path           config.Path
		env            string
		strict, weak   string // JSON
		strictT, weakT string // env text
		yml            func(v string) string
	}{
		{kAuthMode, "GOTHAM_CORE_AUTH_MODE", `"required"`, `"disabled"`, "required", "disabled",
			func(v string) string { return "core:\n  auth:\n    mode: " + v + "\n" }},
		{kGate, "GOTHAM_CORE_HTTP_BASIC_GATE_ENABLED", "true", "false", "true", "false",
			func(v string) string { return "core:\n  http:\n    basic_gate:\n      enabled: " + v + "\n" }},
	}
	for _, k := range keys {
		t.Run(string(k.path), func(t *testing.T) {
			t.Run("stored stricter than env", func(t *testing.T) {
				r := fcRig(t, []string{k.env + "=" + k.weakT}, "", map[config.Path]string{k.path: k.strict})
				rep := r.start()
				ks := r.lookup(k.path)
				if string(ks.Effective) != k.strict || ks.Source != config.SourceOverride {
					t.Errorf("effective %s from %v, want the stored %s", ks.Effective, ks.Source, k.strict)
				}
				if !ks.FailClosed || ks.LockedBy != "env "+k.env {
					t.Errorf("KeyState FailClosed %v LockedBy %q", ks.FailClosed, ks.LockedBy)
				}
				if got := rep.FailClosed[k.path]; got != "env "+k.env || len(rep.FailClosed) != 1 {
					t.Errorf("AttachReport.FailClosed = %v, want %s -> env %s", rep.FailClosed, k.path, k.env)
				}
				if _, ignored := rep.Ignored[k.path]; ignored {
					t.Errorf("the winning row is listed as ignored: %v", rep.Ignored)
				}
				if n := r.logs.count("stricter than the operator value"); n != 1 {
					t.Errorf("warned %d times, want once", n)
				}
				if !slices.Contains(r.st.HealthTokens(), "config_fail_closed_override") || config.FailClosedGauge(r.st) != 1 {
					t.Errorf("health %v gauge %d, want config_fail_closed_override and 1", r.st.HealthTokens(), config.FailClosedGauge(r.st))
				}
			})
			t.Run("stored equal to env", func(t *testing.T) {
				r := fcRig(t, []string{k.env + "=" + k.weakT}, "", map[config.Path]string{k.path: k.weak})
				rep := r.start()
				if ks := r.lookup(k.path); ks.Source != config.SourceEnv || len(rep.FailClosed) != 0 || rep.Ignored[k.path] != "env "+k.env {
					t.Errorf("source %v FailClosed %v Ignored %v; an equal stored value is ignored, not reported as stricter", ks.Source, rep.FailClosed, rep.Ignored)
				}
				if config.FailClosedGauge(r.st) != 0 || r.logs.count("stricter than the operator value") != 0 {
					t.Error("an equal stored value is reported as an override")
				}
			})
			t.Run("stored stricter than file", func(t *testing.T) {
				r := fcRig(t, nil, k.yml(k.weakT), map[config.Path]string{k.path: k.strict})
				rep := r.start()
				if ks := r.lookup(k.path); string(ks.Effective) != k.strict || !strings.HasPrefix(rep.FailClosed[k.path], "file ") {
					t.Errorf("effective %s, FailClosed %v; want the stored value over the file's", ks.Effective, rep.FailClosed)
				}
			})
			t.Run("env stricter than stored", func(t *testing.T) {
				r := fcRig(t, []string{k.env + "=" + k.strictT}, "", map[config.Path]string{k.path: k.weak})
				rep := r.start()
				if ks := r.lookup(k.path); string(ks.Effective) != k.strict || ks.Source != config.SourceEnv || ks.LockedBy != "env "+k.env {
					t.Errorf("effective %s from %v by %q, want the env's %s", ks.Effective, ks.Source, ks.LockedBy, k.strict)
				}
				if len(rep.FailClosed) != 0 || rep.Ignored[k.path] != "env "+k.env {
					t.Errorf("FailClosed %v Ignored %v; want the weaker row ignored", rep.FailClosed, rep.Ignored)
				}
				if slices.Contains(r.st.HealthTokens(), "config_fail_closed_override") || config.FailClosedGauge(r.st) != 0 {
					t.Error("health or gauge report an override that did not happen")
				}
				before := r.state()
				_, err := r.apply(admin(), map[config.Path]string{k.path: k.weak})
				if le, ok := errors.AsType[*config.LockedError](err); !ok || le.By != "env "+k.env || le.Path != k.path {
					t.Errorf("Apply of the weaker value: err = %v, want *LockedError by env %s", err, k.env)
				}
				if r.state() != before {
					t.Error("a refused Apply changed state")
				}
				if _, err := r.apply(admin(), map[config.Path]string{k.path: k.strict}); err != nil {
					t.Errorf("Apply of a value as strict as the operator's: %v", err)
				}
			})
			t.Run("apply under a weaker env", func(t *testing.T) {
				r := fcRig(t, []string{k.env + "=" + k.weakT}, "", nil)
				r.start()
				res, err := r.apply(admin(), map[config.Path]string{k.path: k.strict})
				if err != nil {
					t.Fatalf("Apply of a stricter value: %v", err)
				}
				if row, ok := r.mem.Rows()[k.path]; !ok || string(row.Value) != k.strict {
					t.Errorf("stored rows %v: the stricter value was not stored (a value equal to the default is still stored on a FailClosed key)", r.mem.Rows())
				}
				ks := r.lookup(k.path)
				if k.path == kAuthMode {
					// A restart key: stored now, effective at the next start.
					if !ks.Pending || !slices.Contains(res.RestartPending, k.path) || string(ks.Effective) != k.weak {
						t.Errorf("restart key: Pending %v RestartPending %v effective %s", ks.Pending, res.RestartPending, ks.Effective)
					}
				} else {
					if string(ks.Effective) != k.strict || ks.Source != config.SourceOverride {
						t.Errorf("live key: effective %s from %v, want the applied %s", ks.Effective, ks.Source, k.strict)
					}
					if config.FailClosedGauge(r.st) != 1 || !slices.Contains(r.st.HealthTokens(), "config_fail_closed_override") {
						t.Error("an applied stricter value is not reported as winning")
					}
				}
				if _, err := r.apply(admin(), map[config.Path]string{k.path: k.weak}); err != nil {
					t.Errorf("Apply of a value equal to the operator's: %v", err)
				}
				if _, err := r.apply(admin(), nil, k.path); err != nil {
					t.Errorf("Apply of an unset: %v", err)
				}
				if ks := r.lookup(k.path); string(ks.Effective) != k.weak || ks.Source != config.SourceEnv {
					t.Errorf("after the unset: effective %s from %v, want the operator's", ks.Effective, ks.Source)
				}
				if k.path == kGate && (config.FailClosedGauge(r.st) != 0 || slices.Contains(r.st.HealthTokens(), "config_fail_closed_override")) {
					t.Error("the override report did not clear after the unset")
				}
			})
		})
	}

	t.Run("register", func(t *testing.T) {
		type named struct {
			Name string `json:"name"`
		}
		st, err := config.NewStore(config.Options{AppID: "gotham"})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = st.Close(context.Background()) })
		if _, err := config.Register(st.Registry(), config.Define("core.lair", named{Name: "a"},
			config.FailClosed("core.lair.name", json.RawMessage(`"a"`)))); !errors.Is(err, config.ErrInvalid) {
			t.Errorf("FailClosed on a string key: err = %v, want ErrInvalid", err)
		}
		if _, err := config.Register(st.Registry(), config.Define("core.auth", authFix{Mode: "required"},
			config.FailClosed(kAuthMode, json.RawMessage(`"required"`)))); !errors.Is(err, config.ErrInvalid) {
			t.Errorf("FailClosed with a value missing: err = %v, want ErrInvalid", err)
		}
	})
}

// TestPrecedence_Matrix (05-config §1.3.5, §1.11): every combination of
// file, env, flag and stored override over a live key, the boot record
// under env for a boot key, and a stored restart value under env: the
// effective value, its Source and LockedBy.
func TestPrecedence_Matrix(t *testing.T) {
	for mask := range 16 {
		file, env, flag, row := mask&1 != 0, mask&2 != 0, mask&4 != 0, mask&8 != 0
		t.Run(fmt.Sprintf("file=%v,env=%v,flag=%v,row=%v", file, env, flag, row), func(t *testing.T) {
			ro := rigOpts{}
			var envs []string
			yml := ""
			if file {
				yml = "core:\n  batcave:\n    gadget:\n      count: 10\n"
			}
			if env {
				envs = append(envs, "GOTHAM_CORE_BATCAVE_GADGET_COUNT=20")
			}
			if flag {
				ro.o.Flags = map[config.Path]string{kCount: "30"}
			}
			if row {
				ro.rows = map[config.Path]string{kCount: "40"}
			}
			r := realRig(t, envs, yml, ro)
			rep := r.start()
			want, src, by := "3", config.SourceDefault, ""
			switch {
			case flag:
				want, src, by = "30", config.SourceFlag, "flag core.batcave.gadget.count"
			case env:
				want, src, by = "20", config.SourceEnv, "env GOTHAM_CORE_BATCAVE_GADGET_COUNT"
			case file:
				want, src, by = "10", config.SourceFile, "file "+r.st.ConfigPathForTest()+" key core.batcave.gadget.count"
			case row:
				want, src = "40", config.SourceOverride
			}
			ks := r.lookup(kCount)
			if string(ks.Effective) != want || ks.Source != src || ks.LockedBy != by {
				t.Errorf("got %s from %v locked by %q; want %s from %v locked by %q", ks.Effective, ks.Source, ks.LockedBy, want, src, by)
			}
			if locked := file || env || flag; row && locked != (rep.Ignored[kCount] != "") {
				t.Errorf("Ignored = %v: a stored row is ignored exactly when an operator layer sets the key", rep.Ignored)
			}
		})
	}
	boot := `{"format":"mediacore-boot/1","gen":1,"values":{"core.batcave.mode":"day"}}`
	for _, c := range []struct {
		env, record bool
		want        string
		src         config.Source
	}{
		{false, false, `"night"`, config.SourceDefault},
		{false, true, `"day"`, config.SourceBootRecord},
		{true, true, `"night"`, config.SourceEnv},
	} {
		ro := rigOpts{}
		if c.record {
			ro.bootRecord = boot
		}
		var envs []string
		if c.env {
			envs = []string{"GOTHAM_CORE_BATCAVE_MODE=night"}
		}
		r := realRig(t, envs, "", ro)
		r.start()
		if ks := r.lookup(kMode); string(ks.Effective) != c.want || ks.Source != c.src {
			t.Errorf("boot key env=%v record=%v: %s from %v, want %s from %v", c.env, c.record, ks.Effective, ks.Source, c.want, c.src)
		}
	}
	for _, env := range []bool{false, true} {
		var envs []string
		if env {
			envs = []string{"GOTHAM_CORE_BATCAVE_WORKERS=5"}
		}
		r := realRig(t, envs, "", rigOpts{rows: map[config.Path]string{kWorkers: "8"}})
		r.start()
		want, src := "8", config.SourceOverride
		if env {
			want, src = "5", config.SourceEnv
		}
		if ks := r.lookup(kWorkers); string(ks.Effective) != want || ks.Source != src {
			t.Errorf("restart key env=%v: %s from %v, want %s from %v", env, ks.Effective, ks.Source, want, src)
		}
	}
}

// TestOverride_IgnoredWhenOperatorLocked_EnvAndFile (05-config §1.11, the
// env and file cells): a row of a key the real env or the real file sets is
// kept, listed in AttachReport.Ignored and logged once.
func TestOverride_IgnoredWhenOperatorLocked_EnvAndFile(t *testing.T) {
	r := realRig(t, []string{"GOTHAM_CORE_BATCAVE_LEVEL=warn"}, "modules:\n  gotham:\n    watch:\n      max: 20\n",
		rigOpts{rows: map[config.Path]string{kLevel: `"debug"`, kMax: "30", kCount: "7"}})
	rep := r.start()
	want := map[config.Path]string{kLevel: "env GOTHAM_CORE_BATCAVE_LEVEL", kMax: "file " + r.st.ConfigPathForTest() + " key modules.gotham.watch.max"}
	if !maps.Equal(rep.Ignored, want) {
		t.Errorf("Ignored = %v, want %v", rep.Ignored, want)
	}
	if rep.Applied != 1 {
		t.Errorf("Applied = %d, want only gadget.count", rep.Applied)
	}
	if rows := r.mem.Rows(); string(rows[kLevel].Value) != `"debug"` || string(rows[kMax].Value) != "30" {
		t.Errorf("an ignored row was not kept: %v", rows)
	}
	if n := r.logs.count("ignored because an operator layer"); n != 2 {
		t.Errorf("logged %d times, want once per ignored row", n)
	}
	if r.cave.Get().Level != "warn" || r.watch.Get().Max != 20 {
		t.Errorf("effective level %s max %d, want the operator values", r.cave.Get().Level, r.watch.Get().Max)
	}
}

// TestFile_CanonicalForm (05-config §1.3.6): the file is nested by path
// segments; it sets struct keys, module keys, map section entries, lists
// and passthrough paths; a YAML null supplies nothing; an unknown top-level
// or nested key warns once, at Load only.
func TestFile_CanonicalForm(t *testing.T) {
	yml := `# the operator file
core:
  batcave:
    level: debug
    signal: ~
    allies: [robin, batgirl]
    gadget:
      count: 9
      grapple: true
  modules:
    batman:
      enabled: false
modules:
  gotham:
    watch:
      max: 20
integrations:
  ai:
    wayne:
      base_url: https://192.0.2.9
robin: {nightwing: true}
`
	r := realRig(t, nil, yml, rigOpts{o: config.Options{Passthrough: func(p config.Path) bool { return strings.HasPrefix(string(p), "integrations.ai.wayne.") }}})
	if _, err := r.st.Boot(context.Background()); err != nil {
		t.Fatal(err)
	}
	if n := r.logs.count("no registered section owns"); n != 0 {
		t.Errorf("Boot warned %d times", n)
	}
	if err := r.st.Load(context.Background()); err != nil {
		t.Fatal(err)
	}
	g := r.cave.Get()
	if g.Level != "debug" || g.Gadget.Count != 9 || !slices.Equal(g.Allies, []string{"robin", "batgirl"}) || g.Signal != "bat-signal" {
		t.Errorf("batcave = %+v", g)
	}
	if ks := r.lookup(kSignal); ks.Source != config.SourceDefault {
		t.Errorf("a null value locked the key: %v %q", ks.Source, ks.LockedBy)
	}
	if r.watch.Get().Max != 20 {
		t.Errorf("max = %d", r.watch.Get().Max)
	}
	if ks := r.lookup(kBatman); string(ks.Effective) != "false" {
		t.Errorf("core.modules.batman.enabled = %s", ks.Effective)
	}
	found := false
	for p, v := range r.st.PassthroughValues() {
		found = found || (p == ptBaseURL && string(v.Value) == `"https://192.0.2.9"` && v.Src == config.SourceFile)
	}
	if !found {
		t.Error("the passthrough value was not placed")
	}
	for _, key := range []string{"key=robin", "key=core.batcave.gadget.grapple"} {
		if n := r.logs.count(key); n != 1 {
			t.Errorf("%s warned %d times, want once", key, n)
		}
	}
}

// TestFile_RefusesWhatItCannotRead (05-config §1.5, §1.7): an unreadable or
// unparsable file, a second document, a top level that is not a mapping, a
// key set twice, a section given a value, and a file that sets
// core.paths.state_dir fail boot.
func TestFile_RefusesWhatItCannotRead(t *testing.T) {
	for name, yml := range map[string]string{
		"unparsable":      "core: [batcave\n",
		"two documents":   "core: {}\n---\nmodules: {}\n",
		"not a mapping":   "- batcave\n",
		"key twice":       "core:\n  batcave:\n    level: debug\n    level: warn\n",
		"top-level twice": "core: {}\ncore: {}\n",
		"section scalar":  "core:\n  batcave: debug\n",
		"state dir":       "core:\n  paths:\n    state_dir: /var/lib/gotham\n",
	} {
		t.Run(name, func(t *testing.T) {
			r := realRig(t, nil, yml, rigOpts{stateDir: "/var/lib/gotham"})
			if _, err := r.st.Boot(context.Background()); err == nil {
				t.Error("Boot succeeded")
			}
			if len(r.roots) != 0 && name != "state dir" {
				t.Errorf("the boot record's directory was opened: %v", r.roots)
			}
		})
	}
	r := newRig(t, rigOpts{o: config.Options{FilePath: filepath.Join(t.TempDir(), "absent.yaml")}})
	if _, err := r.st.Boot(context.Background()); err == nil || !strings.Contains(err.Error(), "absent.yaml") {
		t.Errorf("unreadable file: err = %v", err)
	}
	r = realRig(t, nil, "# nothing set\n", rigOpts{})
	r.start()
	if ks := r.lookup(kLevel); ks.Source != config.SourceDefault {
		t.Error("an empty file set a key")
	}
}

// TestConfigFileHasSecrets_RealFile (05-config §1.3.3, DATA2-17): a file
// with a secret key, a passthrough value or a key config cannot place holds
// secrets; the redacted render keeps the plain keys and drops those and
// every comment.
func TestConfigFileHasSecrets_RealFile(t *testing.T) {
	yml := `core:
  batcave:
    level: debug # keep me
    api_key: alfred-pennyworth
integrations:
  ai:
    wayne:
      api_key: lucius-fox
# old_password: hunter
`
	r := realRig(t, nil, yml, rigOpts{o: config.Options{Passthrough: func(p config.Path) bool { return strings.HasPrefix(string(p), "integrations.") }}})
	r.start()
	if !r.st.ConfigFileHasSecrets() {
		t.Error("ConfigFileHasSecrets = false for a file with a secret and a passthrough value")
	}
	out, err := r.st.RedactedConfigFile()
	if err != nil {
		t.Fatal(err)
	}
	for _, gone := range []string{"alfred-pennyworth", "lucius-fox", "hunter", "keep me", "api_key"} {
		if strings.Contains(string(out), gone) {
			t.Errorf("redacted render holds %q:\n%s", gone, out)
		}
	}
	if !strings.Contains(string(out), "level: debug") {
		t.Errorf("redacted render lost the plain key:\n%s", out)
	}

	plain := realRig(t, nil, "core:\n  batcave:\n    level: debug\n", rigOpts{})
	plain.start()
	if plain.st.ConfigFileHasSecrets() {
		t.Error("ConfigFileHasSecrets = true for a file with only a plain key")
	}
	stale := realRig(t, nil, "core:\n  batcave:\n    level: debug\nbasic_auth_password: joker\n", rigOpts{})
	stale.start()
	if !stale.st.ConfigFileHasSecrets() {
		t.Error("ConfigFileHasSecrets = false for a file with a key config cannot place")
	}
	if out, _ := stale.st.RedactedConfigFile(); strings.Contains(string(out), "joker") {
		t.Errorf("an unknown key survived redaction:\n%s", out)
	}
	if err := os.WriteFile(stale.st.ConfigPathForTest(), []byte("core: [\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if !stale.st.ConfigFileHasSecrets() {
		t.Error("an unparsable file does not count as holding secrets")
	}
}

// TestOperator_RealLayersBeforeRegistration (05-config §1.7): the kernel's
// step 0 reads core.paths.state_dir from the env (and its _FILE form) and
// core.storage.path from the file before either section registers.
func TestOperator_RealLayersBeforeRegistration(t *testing.T) {
	st, err := config.NewStore(config.Options{
		AppID:    "gotham",
		Environ:  []string{"GOTHAM_CORE_PATHS_STATE_DIR=/var/lib/gotham"},
		FilePath: writeFile(t, "gotham.yaml", "core:\n  storage:\n    path: /srv/gotham/main.pebble\n"),
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close(context.Background()) })
	v, ok, err := st.Operator("core.paths.state_dir")
	if err != nil || !ok || string(v.Value) != `"/var/lib/gotham"` || v.Src != config.SourceEnv || v.By != "env GOTHAM_CORE_PATHS_STATE_DIR" {
		t.Errorf("state_dir = %+v %v %v", v, ok, err)
	}
	v, ok, err = st.Operator("core.storage.path")
	if err != nil || !ok || string(v.Value) != `"/srv/gotham/main.pebble"` || v.Src != config.SourceFile {
		t.Errorf("storage.path = %+v %v %v", v, ok, err)
	}

	fileSt, err := config.NewStore(config.Options{
		AppID:   "gotham",
		Environ: []string{"GOTHAM_CORE_PATHS_STATE_DIR_FILE=" + writeFile(t, "state_dir", "/var/lib/arkham\n")},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = fileSt.Close(context.Background()) })
	v, ok, err = fileSt.Operator("core.paths.state_dir")
	if err != nil || !ok || string(v.Value) != `"/var/lib/arkham"` || v.Src != config.SourceEnv || v.By != "env GOTHAM_CORE_PATHS_STATE_DIR_FILE" {
		t.Errorf("state_dir from _FILE = %+v %v %v", v, ok, err)
	}
}

// TestFile_ScalarTags (05-config §1.3.6): a file scalar becomes JSON by its
// resolved YAML tag, losslessly: a float keeps its spelling, an integer
// past int64 is kept as an unsigned one, a timestamp stays a string; an
// infinite float, NaN and an unsupported tag fail boot. (YAML resolves an
// integer past uint64 as a float, so it is a JSON number that a key of an
// integer type then refuses.)
func TestFile_ScalarTags(t *testing.T) {
	pt := config.Options{Passthrough: func(p config.Path) bool { return strings.HasPrefix(string(p), "integrations.ai.wayne.") }}
	r := realRig(t, nil, `integrations:
  ai:
    wayne:
      ratio: 1.10
      big: 18446744073709551615
      when: 2026-11-10
      quoted: "007"
      plain: 7
      exp: 1e3
      flag: true
`, rigOpts{o: pt})
	r.start()
	got := map[config.Path]string{}
	for p, v := range r.st.PassthroughValues() {
		got[p] = string(v.Value)
	}
	want := map[config.Path]string{
		"integrations.ai.wayne.ratio": "1.10", "integrations.ai.wayne.big": "18446744073709551615",
		"integrations.ai.wayne.when": `"2026-11-10"`, "integrations.ai.wayne.quoted": `"007"`,
		"integrations.ai.wayne.plain": "7", "integrations.ai.wayne.exp": "1e3", "integrations.ai.wayne.flag": "true",
	}
	if !maps.Equal(got, want) {
		t.Errorf("passthrough values = %v\nwant %v", got, want)
	}
	for name, v := range map[string]string{
		"infinite": ".inf", "not a number": ".nan", "binary": "!!binary aGVsbG8=",
	} {
		r := realRig(t, nil, "integrations:\n  ai:\n    wayne:\n      x: "+v+"\n", rigOpts{o: pt})
		if _, err := r.st.Boot(context.Background()); err == nil {
			t.Errorf("%s: Boot accepted %s", name, v)
		}
	}
}

// TestImporter_StagesFailClosedDefault (05-config §1.3.5, §1.12): the
// Importer stages a FailClosed key's value even when it equals the default,
// so R0's enable_auth=true (core.auth.mode "required", the default) still
// beats a stale ABO_CORE_AUTH_MODE=disabled after the cutover; a plain key's
// default value stages nothing (C4).
func TestImporter_StagesFailClosedDefault(t *testing.T) {
	r := fcRig(t, []string{"GOTHAM_CORE_AUTH_MODE=disabled"}, "", nil)
	ctx := context.Background()
	if _, err := r.st.Boot(ctx); err != nil {
		t.Fatal(err)
	}
	if err := r.st.Load(ctx); err != nil {
		t.Fatal(err)
	}
	r.st.SetImportTarget(r.mem)
	imp := r.st.Importer()
	if err := imp.Stage(new(storage.Batch), kAuthMode, json.RawMessage(`"required"`), "cutover:m1"); err != nil {
		t.Fatal(err)
	}
	if err := imp.Stage(new(storage.Batch), kCount, json.RawMessage("3"), "cutover:m1"); err != nil {
		t.Fatal(err)
	}
	rows := r.mem.Rows()
	if string(rows[kAuthMode].Value) != `"required"` {
		t.Errorf("rows %v: the FailClosed default was not staged", rows)
	}
	if _, ok := rows[kCount]; ok {
		t.Error("a plain key's default value was staged")
	}
	rep, err := r.st.Attach(ctx, r.mem, r.cm)
	if err != nil {
		t.Fatal(err)
	}
	if ks := r.lookup(kAuthMode); string(ks.Effective) != `"required"` || rep.FailClosed[kAuthMode] != "env GOTHAM_CORE_AUTH_MODE" {
		t.Errorf("after the cutover: core.auth.mode %s, FailClosed %v; want the staged required over the stale env", ks.Effective, rep.FailClosed)
	}
}

// TestFailClosed_SurvivesSectionQuarantine (05-config §1.3.5, §1.4 step 6,
// owner-delegated decision of 2026-11-11): a FailClosed key's own stored row
// keeps counting toward the more restrictive choice when its section's rows
// are quarantined, whenever it decodes on its own; one that does not decode
// stops boot, naming the key and never its value.
func TestFailClosed_SurvivesSectionQuarantine(t *testing.T) {
	const kN config.Path = "core.http.n"
	for name, sibling := range map[string]string{"sibling does not decode": `"robin"`, "sibling fails validation": "99"} {
		t.Run(name, func(t *testing.T) {
			// The reviewer's probe: stored gate on, env off, a bad sibling.
			r := fcRig(t, []string{"GOTHAM_CORE_HTTP_BASIC_GATE_ENABLED=false"}, "", map[config.Path]string{kGate: "true", kN: sibling})
			rep := r.start()
			if ks := r.lookup(kGate); string(ks.Effective) != "true" || ks.Source != config.SourceOverride {
				t.Errorf("gate %s from %v: the bad sibling opened the gate", ks.Effective, ks.Source)
			}
			if rep.FailClosed[kGate] != "env GOTHAM_CORE_HTTP_BASIC_GATE_ENABLED" {
				t.Errorf("AttachReport.FailClosed = %v", rep.FailClosed)
			}
			if _, q := rep.Quarantined[kN]; !q {
				t.Errorf("the sibling was not quarantined: %v", rep.Quarantined)
			}
			if _, q := rep.Quarantined[kGate]; q {
				t.Errorf("the FailClosed row was quarantined with its section: %v", rep.Quarantined)
			}
		})
	}
	t.Run("a row no stricter is quarantined with its section", func(t *testing.T) {
		r := fcRig(t, nil, "", map[config.Path]string{kGate: "false", kN: `"robin"`})
		rep := r.start()
		if _, q := rep.Quarantined[kGate]; !q {
			t.Errorf("Quarantined = %v: a row equal to the default stays in play", rep.Quarantined)
		}
	})
	t.Run("the stricter row is kept when no operator layer sets the key", func(t *testing.T) {
		r := fcRig(t, nil, "", map[config.Path]string{kGate: "true", kN: `"robin"`})
		r.start()
		if ks := r.lookup(kGate); string(ks.Effective) != "true" {
			t.Errorf("gate %s: the quarantine loosened a FailClosed key past its stored value", ks.Effective)
		}
	})
	for name, row := range map[config.Path]string{kGate: `"joker-maybe"`, kAuthMode: `"joker-sometimes"`} {
		t.Run(string(name)+" does not decode", func(t *testing.T) {
			r := fcRig(t, nil, "", map[config.Path]string{name: row})
			ctx := context.Background()
			if _, err := r.st.Boot(ctx); err != nil {
				t.Fatal(err)
			}
			if err := r.st.Load(ctx); err != nil {
				t.Fatal(err)
			}
			_, err := r.st.Attach(ctx, r.mem, r.cm)
			if !errors.Is(err, config.ErrInvalid) || !strings.Contains(err.Error(), string(name)) || strings.Contains(err.Error(), "joker") {
				t.Errorf("Attach err = %v, want ErrInvalid naming %s and not its value", err, name)
			}
			if strings.Contains(r.logs.all(), "joker") {
				t.Error("the value reached the log")
			}
		})
	}
	t.Run("the stricter row fails its section's validation", func(t *testing.T) {
		r := fcRig(t, []string{"GOTHAM_CORE_HTTP_BASIC_GATE_ENABLED=false", "GOTHAM_CORE_HTTP_N=13"}, "", map[config.Path]string{kGate: "true"})
		ctx := context.Background()
		if _, err := r.st.Boot(ctx); err != nil {
			t.Fatal(err)
		}
		if err := r.st.Load(ctx); err != nil {
			t.Fatal(err)
		}
		if _, err := r.st.Attach(ctx, r.mem, r.cm); !errors.Is(err, config.ErrInvalid) || !strings.Contains(err.Error(), string(kGate)) {
			t.Errorf("Attach err = %v, want ErrInvalid naming %s", err, kGate)
		}
	})
}
