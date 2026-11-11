// file: core/config/env_test.go
// version: 1.0.0
// guid: 675a9bc4-c085-4c7d-96e2-ab783574cba4
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
)

// realRig is newRig over the real env and file layers: env holds KEY=value
// entries (Options.Environ) and yml, when set, is written to the operator
// file (Options.FilePath).
func realRig(t *testing.T, env []string, yml string, ro rigOpts) *rig {
	t.Helper()
	ro.o.Environ = env
	if yml != "" {
		ro.o.FilePath = writeFile(t, "gotham.yaml", yml)
	}
	return newRig(t, ro)
}

// writeFile writes body to a fresh temp file and returns its path.
func writeFile(t *testing.T, name, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

// boots runs Boot then Load and returns the first error.
func (r *rig) boots() error {
	ctx := context.Background()
	if _, err := r.st.Boot(ctx); err != nil {
		return err
	}
	return r.st.Load(ctx)
}

// lookup is Lookup that fails t on a missing key.
func (r *rig) lookup(p config.Path) config.KeyState {
	r.t.Helper()
	ks, ok := r.st.Lookup(p)
	if !ok {
		r.t.Fatalf("%s is not a key", p)
	}
	return ks
}

// The fixture sections of the binding tests: an admin key and an
// unprotected one, each with an ecosystem name, and a duration whose binding
// keeps OTel's bare milliseconds.
type traceOTLP struct {
	Endpoint string          `json:"endpoint" protect:"admin"`
	Interval config.Duration `json:"interval"`
	Insecure bool            `json:"insecure"`
}

type traceFix struct {
	OTLP traceOTLP `json:"otlp"`
}

const (
	kEndpoint config.Path = "core.tracing.otlp.endpoint"
	kOTLPInt  config.Path = "core.tracing.otlp.interval"
	kInsecure config.Path = "core.tracing.otlp.insecure"
)

func traceSection(extra ...config.SectionOption) config.Section[traceFix] {
	opts := append([]config.SectionOption{
		config.EnvBinding(kEndpoint, "OTEL_EXPORTER_OTLP_ENDPOINT"),
		config.EnvBinding(kOTLPInt, "OTEL_METRIC_EXPORT_INTERVAL"),
		config.EnvBinding(kInsecure, "OTEL_EXPORTER_OTLP_METRICS_INSECURE"),
	}, extra...)
	return config.Define("core.tracing", traceFix{OTLP: traceOTLP{Interval: config.Duration(60e9)}}, opts...)
}

// TestEnvName_Derivation (05-config §1.3.6): <prefix>_ + upper(path), "."
// and "-" -> "_", a leading MODULES_ dropped; the env layer reads struct
// keys, module keys, map section entries and passthrough paths by those
// names, exact case only.
func TestEnvName_Derivation(t *testing.T) {
	for p, want := range map[config.Path]string{
		"core.log.level":                              "ABO_CORE_LOG_LEVEL",
		"modules.books.scan.on_startup":               "ABO_BOOKS_SCAN_ON_STARTUP",
		"integrations.metadata.google-books.api_key":  "ABO_INTEGRATIONS_METADATA_GOOGLE_BOOKS_API_KEY",
		"core.modules.books.enabled":                  "ABO_CORE_MODULES_BOOKS_ENABLED",
		"modules.gotham.watch.max":                    "ABO_GOTHAM_WATCH_MAX",
		"core.http.basic_gate.enabled":                "ABO_CORE_HTTP_BASIC_GATE_ENABLED",
		"integrations.ai.wayne-enterprises.base_url":  "ABO_INTEGRATIONS_AI_WAYNE_ENTERPRISES_BASE_URL",
		"core.auth.oidc.batcomputer.client_secret":    "ABO_CORE_AUTH_OIDC_BATCOMPUTER_CLIENT_SECRET",
		"modules.gotham.modules_like.not_dropped_mid": "ABO_GOTHAM_MODULES_LIKE_NOT_DROPPED_MID",
	} {
		if got := config.CanonicalEnvName("ABO", p); got != want {
			t.Errorf("env name of %s = %s, want %s", p, got, want)
		}
	}

	pt := func(p config.Path) bool { return p == ptAPIKey || p == "integrations.ai.wayne-tech.base_url" }
	r := realRig(t, []string{
		"GOTHAM_GOTHAM_WATCH_MAX=20",
		"GOTHAM_CORE_MODULES_BATMAN_ENABLED=false",
		"GOTHAM_INTEGRATIONS_AI_WAYNE_API_KEY=lucius",
		"GOTHAM_INTEGRATIONS_AI_WAYNE_TECH_BASE_URL=https://192.0.2.7",
		"gotham_core_batcave_level=debug", // wrong case: not the name
		"GOTHAM_CORE_BATCAVE_NOPE=1",      // unknown: one warning, at Load
	}, "", rigOpts{o: config.Options{Passthrough: pt}, modules: map[string]configtest.Module{"batman": {Enabled: true}}})
	if _, err := r.st.Boot(context.Background()); err != nil {
		t.Fatal(err)
	}
	if n := r.logs.count("names no key"); n != 0 {
		t.Errorf("Boot warned %d times about unknown names; module keys are not registered yet, so it warns about none", n)
	}
	if err := r.st.Load(context.Background()); err != nil {
		t.Fatal(err)
	}
	if n := r.logs.count("GOTHAM_CORE_BATCAVE_NOPE"); n != 1 {
		t.Errorf("unknown prefixed name warned %d times, want once", n)
	}
	if strings.Contains(r.logs.all(), "gotham_core_batcave_level") {
		t.Error("a name without the prefix warned")
	}
	if got := r.watch.Get().Max; got != 20 {
		t.Errorf("modules.gotham.watch.max = %d, want 20 from GOTHAM_GOTHAM_WATCH_MAX", got)
	}
	if ks := r.lookup(kBatman); string(ks.Effective) != "false" || ks.LockedBy != "env GOTHAM_CORE_MODULES_BATMAN_ENABLED" {
		t.Errorf("core.modules.batman.enabled = %s by %q", ks.Effective, ks.LockedBy)
	}
	if got := r.cave.Get().Level; got != "info" {
		t.Errorf("level = %s: a lower-case name was read", got)
	}
	pv := maps.Collect(r.st.PassthroughValues())
	if v := pv[ptAPIKey]; string(v.Value) != `"lucius"` || v.By != "env GOTHAM_INTEGRATIONS_AI_WAYNE_API_KEY" || v.Src != config.SourceEnv {
		t.Errorf("passthrough %s = %+v", ptAPIKey, v)
	}
	if v := pv["integrations.ai.wayne-tech.base_url"]; string(v.Value) != `"https://192.0.2.7"` {
		t.Errorf("a passthrough path with '-' was not placed: %+v", pv)
	}
}

// TestEnvName_CollisionRejected (05-config §1.3.6): two paths with one env
// name, a binding equal to a canonical name, an app binding equal to one,
// and a retired name equal to one fail with ErrDuplicate.
func TestEnvName_CollisionRejected(t *testing.T) {
	type inner struct {
		B string `json:"b"`
	}
	type collide struct {
		AB string `json:"a_b"`
		A  inner  `json:"a"`
	}
	type caveA struct {
		B string `json:"b"`
	}
	type caveB struct {
		A inner `json:"a"`
	}
	newStore := func(o config.Options) *config.Store {
		t.Helper()
		o.AppID = "gotham"
		st, err := config.NewStore(o)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = st.Close(context.Background()) })
		return st
	}

	if _, err := config.Register(newStore(config.Options{}).Registry(), config.Define("core.lair", collide{})); !errors.Is(err, config.ErrDuplicate) {
		t.Errorf("core.lair.a_b and core.lair.a.b: err = %v, want ErrDuplicate", err)
	}
	st := newStore(config.Options{})
	if _, err := config.Register(st.Registry(), config.Define("core.cave_a", caveA{})); err != nil {
		t.Fatal(err)
	}
	if _, err := config.Register(st.Registry(), config.Define("core.cave", caveB{})); !errors.Is(err, config.ErrDuplicate) || !strings.Contains(err.Error(), "GOTHAM_CORE_CAVE_A_B") {
		t.Errorf("core.cave.a.b against core.cave_a.b: err = %v, want ErrDuplicate naming GOTHAM_CORE_CAVE_A_B", err)
	}
	if _, err := config.Register(newStore(config.Options{}).Registry(),
		traceSection(config.EnvBinding(kInsecure, "GOTHAM_CORE_TRACING_OTLP_ENDPOINT"))); !errors.Is(err, config.ErrDuplicate) {
		t.Errorf("a binding equal to a canonical name: err = %v, want ErrDuplicate", err)
	}
	st = newStore(config.Options{})
	if _, err := config.Register(st.Registry(), configtest.BatcaveSection()); err != nil {
		t.Fatal(err)
	}
	if _, err := config.Register(st.Registry(), traceSection(config.EnvBinding(kInsecure, "GOTHAM_CORE_BATCAVE_LEVEL"))); !errors.Is(err, config.ErrDuplicate) {
		t.Errorf("a binding equal to an earlier section's canonical name: err = %v, want ErrDuplicate", err)
	}
	app := newStore(config.Options{EnvBindings: []config.EnvName{{Name: "GOTHAM_CORE_BATCAVE_LEVEL", Path: ptAPIKey}}})
	if _, err := config.Register(app.Registry(), configtest.BatcaveSection()); !errors.Is(err, config.ErrDuplicate) {
		t.Errorf("an app binding equal to a canonical name: err = %v, want ErrDuplicate", err)
	}
	if _, err := config.NewStore(config.Options{AppID: "gotham", EnvBindings: []config.EnvName{{Name: "WAYNE_API_KEY", Path: ptAPIKey}, {Name: "WAYNE_API_KEY", Path: ptBaseURL}}}); !errors.Is(err, config.ErrDuplicate) {
		t.Errorf("an app binding listed twice: err = %v, want ErrDuplicate", err)
	}
	r := realRig(t, []string{"GOTHAM_CORE_BATCAVE_LEVEL=debug"}, "", rigOpts{o: config.Options{
		Retired: []config.RetiredName{{Name: "GOTHAM_CORE_BATCAVE_LEVEL", Canonical: kLevel}},
	}})
	if err := r.boots(); !errors.Is(err, config.ErrDuplicate) {
		t.Errorf("a retired name equal to a canonical name: err = %v, want ErrDuplicate", err)
	}
}

// TestEnv_EmptyValueIsUnset (05-config §1.3.5): a variable set to "" supplies
// nothing and locks nothing, as viper treats it at HEAD; so does an empty
// <NAME>_FILE, and an empty unknown name does not warn.
func TestEnv_EmptyValueIsUnset(t *testing.T) {
	r := realRig(t, []string{
		"GOTHAM_CORE_BATCAVE_LEVEL=",
		"GOTHAM_CORE_BATCAVE_GADGET_COUNT_FILE=",
		"GOTHAM_CORE_BATCAVE_NOPE=",
	}, "", rigOpts{})
	r.start()
	for _, p := range []config.Path{kLevel, kCount} {
		if ks := r.lookup(p); ks.Source != config.SourceDefault || ks.LockedBy != "" {
			t.Errorf("%s: source %v locked by %q, want the default and no lock", p, ks.Source, ks.LockedBy)
		}
	}
	if r.logs.count("names no key") != 0 {
		t.Error("an empty unknown name warned")
	}
	if _, err := r.apply(admin(), map[config.Path]string{kLevel: `"debug"`}); err != nil {
		t.Errorf("Apply of a key whose env value is empty: %v, want it free", err)
	}
	if got := r.cave.Get().Level; got != "debug" {
		t.Errorf("level = %s, want the applied debug", got)
	}
}

// TestEnv_StrictDecode (05-config §1.3.6): HEAD's lenient spellings do not
// decode, from the env or the file, and the boot error names the variable
// or the file key; the strict spellings decode.
func TestEnv_StrictDecode(t *testing.T) {
	bad := []struct {
		path config.Path
		env  string // NAME=value
		yml  string
	}{
		{kAuthMode, "GOTHAM_CORE_AUTH_MODE=yes", "core:\n  auth:\n    mode: yes\n"},
		{kGate, "GOTHAM_CORE_HTTP_BASIC_GATE_ENABLED=yes", "core:\n  http:\n    basic_gate:\n      enabled: yes\n"},
		{kInterval, "GOTHAM_CORE_BATCAVE_INTERVAL=90", "core:\n  batcave:\n    interval: 90\n"},
		{kAllies, "GOTHAM_CORE_BATCAVE_ALLIES=robin batgirl", "core:\n  batcave:\n    allies: robin batgirl\n"},
		{kCount, "GOTHAM_CORE_BATCAVE_GADGET_COUNT=three", "core:\n  batcave:\n    gadget:\n      count: \"3\"\n"},
	}
	for _, c := range bad {
		name, _, _ := strings.Cut(c.env, "=")
		t.Run(name, func(t *testing.T) {
			r := fcRig(t, []string{c.env}, "", nil)
			_, err := r.st.Boot(context.Background())
			if !errors.Is(err, config.ErrInvalid) || !strings.Contains(err.Error(), "env "+name) {
				t.Errorf("env: Boot err = %v, want ErrInvalid naming env %s", err, name)
			}
			r = fcRig(t, nil, c.yml, nil)
			_, err = r.st.Boot(context.Background())
			if !errors.Is(err, config.ErrInvalid) || !strings.Contains(err.Error(), "key "+string(c.path)) {
				t.Errorf("file: Boot err = %v, want ErrInvalid naming the file key %s", err, c.path)
			}
		})
	}
	r := fcRig(t, []string{
		"GOTHAM_CORE_BATCAVE_INTERVAL=90s", `GOTHAM_CORE_BATCAVE_ALLIES=["robin","batgirl"]`,
		"GOTHAM_CORE_BATCAVE_GADGET_COUNT=5", "GOTHAM_CORE_HTTP_BASIC_GATE_ENABLED=true",
	}, "", nil)
	r.start()
	if g := r.cave.Get(); g.Interval.Std().String() != "1m30s" || !slices.Equal(g.Allies, []string{"robin", "batgirl"}) || g.Gadget.Count != 5 {
		t.Errorf("strict spellings: %+v", g)
	}
}

// TestEnvBinding_EcosystemNames (05-config §1.3.6): a binding sets its key;
// OTEL_METRIC_EXPORT_INTERVAL keeps OTel's bare milliseconds; an equal
// canonical value is no conflict; a disagreeing canonical name wins on an
// unprotected key with config_binding_conflict and stops boot on a
// ProtectAdmin key; an app binding places a passthrough path.
func TestEnvBinding_EcosystemNames(t *testing.T) {
	pt := config.Options{
		Passthrough: func(p config.Path) bool { return strings.HasPrefix(string(p), "integrations.") },
		EnvBindings: []config.EnvName{{Name: "WAYNE_API_KEY", Path: ptAPIKey}},
	}
	r := traceRig(t, []string{
		"OTEL_EXPORTER_OTLP_ENDPOINT=http://192.0.2.10:4318",
		"OTEL_METRIC_EXPORT_INTERVAL=5000",
		"GOTHAM_CORE_TRACING_OTLP_INSECURE=true",
		"OTEL_EXPORTER_OTLP_METRICS_INSECURE=true",
		"WAYNE_API_KEY=fox",
	}, pt)
	if ks := r.lookup(kEndpoint); string(ks.Effective) != `"http://192.0.2.10:4318"` || ks.LockedBy != "env OTEL_EXPORTER_OTLP_ENDPOINT" {
		t.Errorf("endpoint = %s by %q", ks.Effective, ks.LockedBy)
	}
	if ks := r.lookup(kOTLPInt); string(ks.Effective) != `"5s"` {
		t.Errorf("OTEL_METRIC_EXPORT_INTERVAL=5000 gave %s, want 5s (bare milliseconds)", ks.Effective)
	}
	if ks := r.lookup(kInsecure); ks.LockedBy != "env GOTHAM_CORE_TRACING_OTLP_INSECURE" {
		t.Errorf("equal canonical and binding: locked by %q, want the canonical name", ks.LockedBy)
	}
	if slices.Contains(r.st.HealthTokens(), "config_binding_conflict") {
		t.Error("equal values raised config_binding_conflict")
	}
	found := false
	for p, v := range r.st.PassthroughValues() {
		found = found || (p == ptAPIKey && string(v.Value) == `"fox"` && v.By == "env WAYNE_API_KEY")
	}
	if !found {
		t.Error("the app binding WAYNE_API_KEY did not place its passthrough path")
	}
	// The canonical millisecond rule is the binding's only.
	r2 := newTraceRig(t, []string{"GOTHAM_CORE_TRACING_OTLP_INTERVAL=5000"}, config.Options{})
	if err := r2.boots(); !errors.Is(err, config.ErrInvalid) {
		t.Errorf("a bare integer through the canonical name: err = %v, want ErrInvalid", err)
	}
}

// TestEnvBindingConflictOnProtectedKeyRefusesBoot (05-config §1.3.5, §1.5,
// OPS2-12): the canonical name and the binding of a ProtectAdmin key set to
// different values stop boot naming both; on an unprotected key the
// canonical name wins, with one warning and config_binding_conflict.
func TestEnvBindingConflictOnProtectedKeyRefusesBoot(t *testing.T) {
	r := newTraceRig(t, []string{
		"GOTHAM_CORE_TRACING_OTLP_ENDPOINT=http://192.0.2.10:4318",
		"OTEL_EXPORTER_OTLP_ENDPOINT=http://192.0.2.11:4318",
	}, config.Options{})
	_, err := r.st.Boot(context.Background())
	if err == nil || !strings.Contains(err.Error(), "GOTHAM_CORE_TRACING_OTLP_ENDPOINT") || !strings.Contains(err.Error(), "OTEL_EXPORTER_OTLP_ENDPOINT") {
		t.Fatalf("Boot err = %v, want a refusal naming both variables", err)
	}
	if len(r.roots) != 0 {
		t.Errorf("the refused boot opened the boot record's directory %v", r.roots)
	}

	r = traceRig(t, []string{
		"GOTHAM_CORE_TRACING_OTLP_INSECURE=false",
		"OTEL_EXPORTER_OTLP_METRICS_INSECURE=true",
	}, config.Options{})
	if ks := r.lookup(kInsecure); string(ks.Effective) != "false" || ks.LockedBy != "env GOTHAM_CORE_TRACING_OTLP_INSECURE" {
		t.Errorf("insecure = %s by %q, want the canonical false", ks.Effective, ks.LockedBy)
	}
	if !slices.Contains(r.st.HealthTokens(), "config_binding_conflict") {
		t.Errorf("health %v lacks config_binding_conflict", r.st.HealthTokens())
	}
	if n := r.logs.count("canonical name wins"); n != 1 {
		t.Errorf("warned %d times, want once per boot", n)
	}
}

// TestEnvFileSuffix (05-config §1.3.6): <NAME>_FILE is read once, one
// trailing newline trimmed; it hands a secret in; an unreadable file, or the
// name and its _FILE form both set, stops boot. A key whose own name ends in
// _FILE is read as that key.
func TestEnvFileSuffix(t *testing.T) {
	type lair struct {
		File string `json:"file"`
	}
	level := writeFile(t, "level", "warn\n")
	name := writeFile(t, "name", "batmobile\n\n")
	secret := writeFile(t, "key", "s3cret-alfred\r\n")
	r := realRig(t, []string{
		"GOTHAM_CORE_BATCAVE_LEVEL_FILE=" + level,
		"GOTHAM_CORE_BATCAVE_GADGET_NAME_FILE=" + name,
		"GOTHAM_CORE_BATCAVE_API_KEY_FILE=" + secret,
		"GOTHAM_CORE_LAIR_FILE=/batcave/lair",
	}, "", rigOpts{})
	reads := map[string]int{}
	config.SetEnvFileReader(r.st, func(p string) ([]byte, error) {
		reads[p]++
		return os.ReadFile(p)
	})
	if _, err := config.Register(r.st.Registry(), config.Define("core.lair", lair{})); err != nil {
		t.Fatal(err)
	}
	r.start()
	if ks := r.lookup(kLevel); string(ks.Effective) != `"warn"` || ks.LockedBy != "env GOTHAM_CORE_BATCAVE_LEVEL_FILE" {
		t.Errorf("level = %s by %q", ks.Effective, ks.LockedBy)
	}
	if got := r.cave.Get().Gadget.Name; got != "batmobile\n" {
		t.Errorf("gadget.name = %q, want one newline trimmed", got)
	}
	for p, n := range reads {
		if n != 1 {
			t.Errorf("%s read %d times, want once", p, n)
		}
	}
	var sv config.OperatorValue
	for p, v := range r.st.OperatorSecrets() {
		if p == kAPIKey {
			sv = v
		}
	}
	if string(sv.Value) != `"s3cret-alfred"` || sv.Src != config.SourceEnv || sv.By != "env GOTHAM_CORE_BATCAVE_API_KEY_FILE" {
		t.Errorf("OperatorSecrets[%s] = %+v", kAPIKey, sv)
	}
	if ks := r.lookup(kAPIKey); ks.Effective != nil || strings.Contains(r.logs.all(), "s3cret") {
		t.Error("the secret's value leaked into KeyState or the log")
	}
	if ks := r.lookup("core.lair.file"); string(ks.Effective) != `"/batcave/lair"` {
		t.Errorf("core.lair.file = %s, want its own name read as the key", ks.Effective)
	}

	r = realRig(t, []string{"GOTHAM_CORE_BATCAVE_LEVEL_FILE=" + filepath.Join(t.TempDir(), "absent")}, "", rigOpts{})
	if _, err := r.st.Boot(context.Background()); err == nil || !strings.Contains(err.Error(), "GOTHAM_CORE_BATCAVE_LEVEL_FILE") {
		t.Errorf("unreadable _FILE: Boot err = %v, want an error naming the variable", err)
	}
	r = realRig(t, []string{"GOTHAM_CORE_BATCAVE_LEVEL=debug", "GOTHAM_CORE_BATCAVE_LEVEL_FILE=" + level}, "", rigOpts{})
	if _, err := r.st.Boot(context.Background()); err == nil || !strings.Contains(err.Error(), "both set") {
		t.Errorf("a name and its _FILE form: Boot err = %v, want a refusal", err)
	}
}

// newTraceRig is a rig with the binding fixture section registered.
func newTraceRig(t *testing.T, env []string, o config.Options) *rig {
	t.Helper()
	r := realRig(t, env, "", rigOpts{o: o})
	if _, err := config.Register(r.st.Registry(), traceSection()); err != nil {
		t.Fatal(err)
	}
	return r
}

// traceRig is newTraceRig, started.
func traceRig(t *testing.T, env []string, o config.Options) *rig {
	t.Helper()
	r := newTraceRig(t, env, o)
	r.start()
	return r
}

// FuzzEnvDecode (05-config §1.11): no env text panics the strict decoder or
// the env layer, and every failure is typed.
func FuzzEnvDecode(f *testing.F) {
	for _, s := range []string{"", "true", "yes", "90", "90s", "-1", "1e9", `["robin"]`, "robin batgirl", "{", "\x00", "18446744073709551616", "night"} {
		f.Add(uint8(0), s)
	}
	keys := []config.Path{kLevel, kInterval, kWorkers, kMode, kPath, kOwner, kCount, kAllies, kAPIKey, kMax, kVillains, ptAPIKey}
	f.Fuzz(func(t *testing.T, which uint8, text string) {
		p := keys[int(which)%len(keys)]
		st, err := config.NewStore(config.Options{AppID: "gotham", Passthrough: func(q config.Path) bool { return q == ptAPIKey }})
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = st.Close(context.Background()) }()
		for _, sec := range []any{configtest.BatcaveSection(), configtest.WatchSection()} {
			if _, err := config.RegisterSections(st.Registry(), sec); err != nil {
				t.Fatal(err)
			}
		}
		raw, err := config.TextJSON(st, p, text)
		if err != nil {
			if _, ok := errors.AsType[*config.FieldError](err); !ok {
				t.Fatalf("%s %q: untyped error %T %v", p, text, err, err)
			}
			return
		}
		if !json.Valid(raw) {
			t.Fatalf("%s %q: decoded to invalid JSON %q", p, text, raw)
		}
		if strings.ContainsRune(text, 0) {
			return // not representable in an environment
		}
		env := []string{config.CanonicalEnvName("GOTHAM", p) + "=" + text}
		st2, err := config.NewStore(config.Options{AppID: "gotham", Environ: env})
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = st2.Close(context.Background()) }()
		for _, sec := range []any{configtest.BatcaveSection(), configtest.WatchSection()} {
			if _, err := config.RegisterSections(st2.Registry(), sec); err != nil {
				t.Fatal(err)
			}
		}
		if err := st2.Load(context.Background()); err != nil && !errors.Is(err, config.ErrInvalid) {
			t.Fatalf("%s %q: Load error is not ErrInvalid: %v", p, text, err)
		}
	})
}
