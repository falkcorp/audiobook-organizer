// file: core/module/settings_test.go
// version: 1.0.0
// guid: 0d6a8f2c-71e4-4b39-a5c8-3f9e12b47d60
// last-edited: 2026-11-18

package module

import (
	"context"
	"errors"
	"testing"

	"github.com/falkcorp/mediacore/config"
	"github.com/falkcorp/mediacore/httpapi"
	"github.com/falkcorp/mediacore/secrets"
)

type cowlSettings struct {
	Signal string `json:"signal"`
	Beams  int    `json:"beams"`
}

type gadgetSettings struct {
	Grapple bool `json:"grapple"`
}

var (
	cowlSection   = config.Define("modules.batcave", cowlSettings{Signal: "bat", Beams: 2})
	gadgetSection = config.Define("modules.batcave.gadget", gadgetSettings{Grapple: true})
	rogueSection  = config.Define("modules.arkham", gadgetSettings{})
)

func settingsRegistrar(t *testing.T, live map[config.Path]any) *Registrar {
	t.Helper()
	router := httpapi.NewRouter(httpapi.Options{}, httpapi.Deps{})
	return NewRegistrar(RegistrarConfig{Context: t.Context(), Deps: Deps{Module: "batcave"},
		HTTP: router.ModuleGroup("batcave"), Settings: live})
}

func problemKinds(err error) []ProblemKind {
	var ve *ValidationError
	if !errors.As(err, &ve) {
		return nil
	}
	var out []ProblemKind
	for _, p := range ve.Problems {
		out = append(out, p.Kind)
	}
	return out
}

func TestSettingsProblems(t *testing.T) {
	good := Manifest{ID: "batcave", Settings: []SettingsSection{Settings(cowlSection), Settings(gadgetSection)}}
	if probs := SettingsProblems(good); len(probs) != 0 {
		t.Fatalf("a module's own section and one below it are valid: %v", probs)
	}

	cases := map[string]Manifest{
		"another module's path": {ID: "batcave", Settings: []SettingsSection{Settings(rogueSection)}},
		"a prefix lookalike":    {ID: "bat", Settings: []SettingsSection{Settings(cowlSection)}},
		"declared twice":        {ID: "batcave", Settings: []SettingsSection{Settings(cowlSection), Settings(cowlSection)}},
		"zero section":          {ID: "batcave", Settings: []SettingsSection{{}}},
	}
	for name, m := range cases {
		probs := SettingsProblems(m)
		if len(probs) == 0 {
			t.Errorf("%s: no problem", name)
			continue
		}
		for _, p := range probs {
			if p.Kind != ProblemSettingsNamespace || p.Module != m.ID {
				t.Errorf("%s: problem %+v, want ProblemSettingsNamespace of %s", name, p, m.ID)
			}
		}
	}
}

func TestSettingsSection_Register(t *testing.T) {
	if _, err := (SettingsSection{}).Register(nil); err == nil {
		t.Fatal("the zero SettingsSection registered")
	}
	st, err := config.NewStore(config.Options{AppID: "batcave"})
	if err != nil {
		t.Fatal(err)
	}
	h, err := Settings(cowlSection).Register(st.Registry())
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := h.(*config.Handle[cowlSettings]); !ok {
		t.Fatalf("Register returned %T, want *config.Handle[cowlSettings]", h)
	}
	if got := Settings(cowlSection).Path(); got != "modules.batcave" {
		t.Fatalf("Path = %q", got)
	}
}

func TestSetting_DeclaredLiveHandle(t *testing.T) {
	st, err := config.NewStore(config.Options{AppID: "batcave"})
	if err != nil {
		t.Fatal(err)
	}
	h, err := config.Register(st.Registry(), cowlSection)
	if err != nil {
		t.Fatal(err)
	}
	r := settingsRegistrar(t, map[config.Path]any{"modules.batcave": h})
	if got := Setting(r, cowlSection); got != h {
		t.Fatal("Setting did not return the kernel's live handle")
	}
	if err := r.Err(); err != nil {
		t.Fatalf("Err = %v", err)
	}
}

func TestSetting_DeclaredNoLiveHandleHoldsDefaults(t *testing.T) {
	// app.Describe and a boot without core config pass the declared path
	// with no live handle.
	r := settingsRegistrar(t, map[config.Path]any{"modules.batcave": nil})
	h := Setting(r, cowlSection)
	if h == nil {
		t.Fatal("Setting returned nil")
	}
	if got := *h.Get(); got != (cowlSettings{Signal: "bat", Beams: 2}) {
		t.Fatalf("defaults handle holds %+v", got)
	}
	if err := r.Err(); err != nil {
		t.Fatalf("a declared section without a live handle is not a problem: %v", err)
	}
}

func TestSetting_UndeclaredRecordsProblemAndHoldsDefaults(t *testing.T) {
	r := settingsRegistrar(t, nil)
	h := Setting(r, cowlSection)
	if h == nil || *h.Get() != (cowlSettings{Signal: "bat", Beams: 2}) {
		t.Fatalf("an undeclared section must return a defaults handle, got %v", h)
	}
	if kinds := problemKinds(r.Err()); len(kinds) != 1 || kinds[0] != ProblemSettingsUndeclared {
		t.Fatalf("problems = %v, want one ProblemSettingsUndeclared", kinds)
	}
}

func TestSetting_WrongTypeIsAProblem(t *testing.T) {
	st, err := config.NewStore(config.Options{AppID: "batcave"})
	if err != nil {
		t.Fatal(err)
	}
	other, err := config.Register(st.Registry(), gadgetSection)
	if err != nil {
		t.Fatal(err)
	}
	// The live handle at modules.batcave is a gadget handle; the module asks
	// for the cowl type.
	r := settingsRegistrar(t, map[config.Path]any{"modules.batcave": other})
	h := Setting(r, cowlSection)
	if h == nil || h.Get().Beams != 2 {
		t.Fatalf("wrong-type lookup must fall back to defaults, got %v", h)
	}
	if kinds := problemKinds(r.Err()); len(kinds) != 1 || kinds[0] != ProblemSettingsUndeclared {
		t.Fatalf("problems = %v, want one ProblemSettingsUndeclared", kinds)
	}
}

func TestSetting_AfterCloseRecordsClosed(t *testing.T) {
	r := settingsRegistrar(t, map[config.Path]any{"modules.batcave": nil})
	r.Close()
	if h := Setting(r, cowlSection); h == nil {
		t.Fatal("Setting returned nil after Close")
	}
	kinds := problemKinds(r.Err())
	if len(kinds) != 1 || kinds[0] != ProblemRegistrarClosed {
		t.Fatalf("problems = %v, want one ProblemRegistrarClosed", kinds)
	}
}

func TestDeps_ZeroSecretsScopeRefusesNotPanics(t *testing.T) {
	var d Deps
	ctx := context.Background()
	name := secrets.Name("modules.batcave.signal_key")
	if _, _, err := d.Secrets.Get(ctx, name); !errors.Is(err, secrets.ErrOutOfScope) {
		t.Errorf("Get on the zero Scope = %v, want ErrOutOfScope", err)
	}
	if _, err := d.Secrets.Put(ctx, name, secrets.Value{}, "test"); !errors.Is(err, secrets.ErrOutOfScope) {
		t.Errorf("Put on the zero Scope = %v, want ErrOutOfScope", err)
	}
	if err := d.Secrets.Delete(ctx, name, "test"); !errors.Is(err, secrets.ErrOutOfScope) {
		t.Errorf("Delete on the zero Scope = %v, want ErrOutOfScope", err)
	}
	if _, err := d.Secrets.DeriveKey("batcave.signal"); !errors.Is(err, secrets.ErrOutOfScope) {
		t.Errorf("DeriveKey on the zero Scope = %v, want ErrOutOfScope", err)
	}
}
