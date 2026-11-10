// file: core/config/config_test.go
// version: 1.0.0
// guid: b8632661-ffc1-4067-a649-c9e47142f9e8
// last-edited: 2026-11-10

package config

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
)

// TestPath_Validate is a test name coined by F2-09. It covers the grammar of
// 05-config §1.3.1: first segment core or modules, each segment
// [a-z][a-z0-9_]{0,47}.
func TestPath_Validate(t *testing.T) {
	long48 := "a" + strings.Repeat("b", 47)
	long49 := "a" + strings.Repeat("b", 48)
	valid := []Path{
		"core", "modules", "core.log.level", "modules.batman.scan.on_startup",
		"core.a1_b2", Path("core." + long48),
	}
	for _, p := range valid {
		if err := p.Validate(); err != nil {
			t.Errorf("Validate(%q) = %v, want nil", p, err)
		}
	}
	invalid := []Path{
		"", "log.level", "integrations.x", "Core.log", "core.", ".core", "core..log",
		"core.1log", "core._log", "core.log-level", "core.log level", "core.Log",
		Path("core." + long49), "core.tasks.kinds.books.dedup.full-scan",
	}
	for _, p := range invalid {
		if err := p.Validate(); err == nil {
			t.Errorf("Validate(%q) = nil, want an error", p)
		}
	}
}

func TestPath_ParentJoin(t *testing.T) {
	if got := Path("core.log.level").Parent(); got != "core.log" {
		t.Errorf("Parent = %q", got)
	}
	if got := Path("core").Parent(); got != "" {
		t.Errorf("Parent of one segment = %q, want empty", got)
	}
	if got := Path("core.log").Join("level"); got != "core.log.level" {
		t.Errorf("Join = %q", got)
	}
	if got := Path("").Join("core"); got != "core" {
		t.Errorf("Join onto empty = %q", got)
	}
}

// TestDuration_JSONRoundTrip is a test name coined by F2-09.
func TestDuration_JSONRoundTrip(t *testing.T) {
	cases := []struct {
		d    time.Duration
		want string
	}{
		{90 * time.Second, `"1m30s"`},
		{24 * time.Hour, `"24h"`},
		{time.Hour, `"1h"`},
		{time.Minute, `"1m"`},
		{0, `"0s"`},
		{200 * time.Millisecond, `"200ms"`},
		{25*time.Hour + 30*time.Minute + 5*time.Second, `"25h30m5s"`},
		{168 * time.Hour, `"168h"`},
	}
	for _, c := range cases {
		b, err := json.Marshal(Duration(c.d))
		if err != nil {
			t.Fatalf("Marshal(%v): %v", c.d, err)
		}
		if string(b) != c.want {
			t.Errorf("Marshal(%v) = %s, want %s", c.d, b, c.want)
		}
		var back Duration
		if err := json.Unmarshal(b, &back); err != nil {
			t.Fatalf("Unmarshal(%s): %v", b, err)
		}
		if back.Std() != c.d {
			t.Errorf("round trip %v = %v", c.d, back.Std())
		}
	}
	for in, want := range map[string]time.Duration{`"90s"`: 90 * time.Second, `"24h"`: 24 * time.Hour, `"1h30m"`: 90 * time.Minute} {
		var d Duration
		if err := json.Unmarshal([]byte(in), &d); err != nil || d.Std() != want {
			t.Errorf("Unmarshal(%s) = %v, %v; want %v", in, d.Std(), err, want)
		}
	}
	// Strict decoding: no bare number, no empty string, no unit-less text.
	for _, in := range []string{`90`, `1.5`, `""`, `"90"`, `"soon"`, `null0`, `true`, `{}`} {
		var d Duration
		if err := json.Unmarshal([]byte(in), &d); err == nil {
			t.Errorf("Unmarshal(%s) = %v, want an error", in, d.Std())
		}
	}
}

func TestDuration_InStruct(t *testing.T) {
	type s struct {
		TTL Duration `json:"ttl"`
	}
	b, err := json.Marshal(s{TTL: Duration(15 * time.Minute)})
	if err != nil || string(b) != `{"ttl":"15m"}` {
		t.Fatalf("Marshal = %s, %v", b, err)
	}
	var back s
	if err := json.Unmarshal([]byte(`{"ttl":"5s"}`), &back); err != nil || back.TTL.Std() != 5*time.Second {
		t.Fatalf("Unmarshal = %v, %v", back, err)
	}
}

func TestFieldError(t *testing.T) {
	base := errors.New("must be positive")
	var err error = &FieldError{Path: "core.http.rate_limit.per_minute", Err: base}
	if got := err.Error(); got != "core.http.rate_limit.per_minute: must be positive" {
		t.Errorf("Error = %q", got)
	}
	if !errors.Is(err, base) {
		t.Error("errors.Is does not reach the wrapped error")
	}
	var fe *FieldError
	if !errors.As(errors.Join(err), &fe) || fe.Path != "core.http.rate_limit.per_minute" {
		t.Error("errors.As does not find the FieldError through Join")
	}
	if got := (&FieldError{Path: "core.x"}).Error(); got != "core.x" {
		t.Errorf("Error with nil Err = %q", got)
	}
}

type sampleSettings struct {
	Level string `json:"level"`
}

// TestDefine_RecordsPathDefaultsOptions is a test name coined by F2-09. Define
// never fails, even for a path that Validate refuses, and the unexported spec
// reflects every option.
func TestDefine_RecordsPathDefaultsOptions(t *testing.T) {
	s := Define("core.sample", sampleSettings{Level: "info"},
		Permissions("core:sample.view", "core:sample.manage"),
		Docs(map[Path]string{"core.sample.level": "the level"}),
		ProtectionWaivers(map[Path]string{"core.sample.level": "not a path"}),
		EnvBinding("core.sample.level", "SAMPLE_LEVEL"),
		FailClosed("core.sample.level", json.RawMessage(`"strict"`), json.RawMessage(`"lax"`)),
	)
	if s.Path() != "core.sample" {
		t.Fatalf("Path = %q", s.Path())
	}
	sp := s.spec()
	if sp.path != "core.sample" || sp.defaults.(sampleSettings).Level != "info" {
		t.Errorf("spec path/defaults = %q, %v", sp.path, sp.defaults)
	}
	if sp.permRead != "core:sample.view" || sp.permWrite != "core:sample.manage" {
		t.Errorf("permissions = %q, %q", sp.permRead, sp.permWrite)
	}
	if sp.docs["core.sample.level"] != "the level" || sp.waivers["core.sample.level"] != "not a path" {
		t.Errorf("docs/waivers = %v, %v", sp.docs, sp.waivers)
	}
	if len(sp.envBindings) != 1 || sp.envBindings[0].name != "SAMPLE_LEVEL" {
		t.Errorf("envBindings = %v", sp.envBindings)
	}
	if len(sp.failClosed) != 1 || len(sp.failClosed[0].order) != 2 || string(sp.failClosed[0].order[0]) != `"strict"` {
		t.Errorf("failClosed = %v", sp.failClosed)
	}

	bad := Define("Bad Path", sampleSettings{})
	if bad.Path() != "Bad Path" || bad.Path().Validate() == nil {
		t.Error("Define must record an invalid path unchanged and leave refusal to Validate")
	}
}

// TestDefine_OptionsAreCopied checks that a caller's later edits to the maps
// and slices it passed do not reach the section, and that two spec() calls do
// not alias each other.
func TestDefine_OptionsAreCopied(t *testing.T) {
	docs := map[Path]string{"core.sample.level": "a"}
	order := json.RawMessage(`"strict"`)
	s := Define("core.sample", sampleSettings{}, Docs(docs), FailClosed("core.sample.level", order))
	docs["core.sample.level"] = "changed"
	order[1] = 'X'
	one, two := s.spec(), s.spec()
	if one.docs["core.sample.level"] != "a" {
		t.Errorf("docs aliased the caller's map: %v", one.docs)
	}
	if string(one.failClosed[0].order[0]) != `"strict"` {
		t.Errorf("order aliased the caller's bytes: %s", one.failClosed[0].order[0])
	}
	one.docs["core.sample.level"] = "mutated"
	if two.docs["core.sample.level"] != "a" {
		t.Error("two spec() results share a map")
	}
}

func TestVocabularyValues(t *testing.T) {
	// 05-config §1.3.1 orders: each Protection includes the ones before it.
	prot := []Protection{ProtectNone, ProtectInteractive, ProtectAdmin, ProtectOwner, ProtectOperator}
	for i, p := range prot {
		if int(p) != i+1 {
			t.Errorf("Protection #%d = %d", i, p)
		}
	}
	rel := []Reload{ReloadLive, ReloadRestart, ReloadBoot}
	for i, r := range rel {
		if int(r) != i+1 {
			t.Errorf("Reload #%d = %d", i, r)
		}
	}
}

type validating struct{}

func (validating) Validate() error { return &FieldError{Path: "core.x", Err: errors.New("bad")} }

func TestValidatorInterface(t *testing.T) {
	var v Validator = validating{}
	var fe *FieldError
	if !errors.As(v.Validate(), &fe) {
		t.Error("Validator result is not a FieldError")
	}
}
