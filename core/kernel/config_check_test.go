// file: core/kernel/config_check_test.go
// version: 1.1.0
// guid: 69eef733-be91-4046-8c4b-c7d78d30e9e2
// last-edited: 2026-11-19

package kernel

import (
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"testing"

	"github.com/falkcorp/mediacore/config"
	"github.com/falkcorp/mediacore/module"
	"github.com/falkcorp/mediacore/secrets"
)

// TestCoreSections_FailClosedKeys (05-config §1.3.5, 05-authn §8,
// 05-http-api §10): core.auth.mode and core.http.basic_gate.enabled are
// declared FailClosed and other keys are not; config's own tests cover the
// rule over these two keys' tags and orders.
func TestCoreSections_FailClosedKeys(t *testing.T) {
	st, err := config.NewStore(config.Options{
		AppID:   "gotham",
		Environ: []string{"GOTHAM_CORE_AUTH_MODE=disabled", "GOTHAM_CORE_HTTP_BASIC_GATE_ENABLED=false"},
		Logger:  slog.New(slog.DiscardHandler),
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close(t.Context()) })
	if _, err := config.RegisterSections(st.Registry(), coreSections()...); err != nil {
		t.Fatal(err)
	}
	if err := st.Load(t.Context()); err != nil {
		t.Fatal(err)
	}
	for _, p := range []config.Path{"core.auth.mode", "core.http.basic_gate.enabled"} {
		ks, ok := st.Lookup(p)
		if !ok || !ks.FailClosed {
			t.Errorf("%s: FailClosed = %v (key %v)", p, ks.FailClosed, ok)
		}
	}
	for _, p := range []config.Path{"core.log.journal.retention_days", "core.storage.path"} {
		if ks, ok := st.Lookup(p); !ok || ks.FailClosed {
			t.Errorf("%s: FailClosed = %v (key %v)", p, ks.FailClosed, ok)
		}
	}
}

// TestCheckConfig_RetiredNames (05-config §1.3.6, SEC10-9): CheckConfig
// judges retired names against the real core sections without a boot
// record root: a retired name of an operator key refuses, one of an
// unprotected key is listed and ignored.
func TestCheckConfig_RetiredNames(t *testing.T) {
	c := ConfigCheck{
		Config: config.Options{
			AppID:   "gotham",
			Environ: []string{"GOTHAM_DB=/srv/gotham/main.pebble", "GOTHAM_DAYS=30"},
			Retired: []config.RetiredName{
				{Name: "GOTHAM_DB", Canonical: "core.storage.path"},
				{Name: "GOTHAM_DAYS", Canonical: "core.log.journal.retention_days"},
			},
			Logger: slog.New(slog.DiscardHandler),
		},
		StateDirDefault: filepath.Join(t.TempDir(), "state"),
	}
	uses, err := CheckConfig(t.Context(), c)
	re, ok := errors.AsType[*config.RetiredNameError](err)
	if !ok || len(re.Uses) != 1 || re.Uses[0].Name != "GOTHAM_DB" || re.Uses[0].CanonicalEnv != "GOTHAM_CORE_STORAGE_PATH" {
		t.Fatalf("CheckConfig err = %v, want *RetiredNameError naming GOTHAM_DB", err)
	}
	if len(uses) != 2 || !uses[0].Refused || uses[1].Refused || uses[1].Name != "GOTHAM_DAYS" {
		t.Errorf("uses = %+v", uses)
	}

	c.Config.Environ = []string{"GOTHAM_DAYS=30"}
	if uses, err := CheckConfig(t.Context(), c); err != nil || len(uses) != 1 || uses[0].Refused {
		t.Errorf("warn-only CheckConfig = %+v, %v", uses, err)
	}
}

// TestBootConfig_RetiredRefusalCreatesNothing (05-config §1.3.6): a retired
// name that refuses boot is judged before step 0 may create the state
// directory, so a refused boot creates nothing.
func TestBootConfig_RetiredRefusalCreatesNothing(t *testing.T) {
	var made []string
	in := configInput{
		AppID:           "gotham",
		Environ:         []string{"GOTHAM_DB=/srv/gotham/main.pebble"},
		StateDirDefault: filepath.Join(t.TempDir(), "state"),
		Retired:         []config.RetiredName{{Name: "GOTHAM_DB", Canonical: "core.storage.path"}},
		Logger:          slog.New(slog.DiscardHandler),
		Mkdir: func(d string) error {
			made = append(made, d)
			return os.MkdirAll(d, 0o700)
		},
	}
	_, err := bootConfig(t.Context(), in)
	if !errors.Is(err, config.ErrRetiredName) {
		t.Fatalf("bootConfig err = %v, want ErrRetiredName", err)
	}
	if len(made) != 0 {
		t.Errorf("a refused boot created %v", made)
	}
	in.Environ = nil
	cb, err := bootConfig(t.Context(), in)
	if err != nil {
		t.Fatalf("bootConfig without the retired name: %v", err)
	}
	_ = cb.store.Close(t.Context())
	if len(made) == 0 {
		t.Error("the control boot called no Mkdir, so its silence above proves nothing")
	}
}

// gadgetSettings is a module section with a protected credential, so a
// retired name that lands on it is refused.
type gadgetSettings struct {
	Grapple int         `json:"grapple"`
	Cipher  secrets.Ref `json:"cipher" protect:"admin"`
}

// TestCheckConfig_ModuleSectionsComeFromEveryCompiledModule (05-config 1.3.6;
// coordinator decision 2026-11-19): config check judges a retired name of a
// module key from the sections of ALL compiled modules, enabled or not, so a
// disabled module's retired secret still STOPS THE SERVICE FROM STARTING
// and a module that declares no section stays Unchecked. Without the
// sections (the old seam) the same name is only Unchecked.
func TestCheckConfig_ModuleSectionsComeFromEveryCompiledModule(t *testing.T) {
	gadgets := config.Define("modules.gadgets.vault", gadgetSettings{Grapple: 2})
	mk := func(id string, enabled bool, secs ...module.SettingsSection) testModule {
		return mod(id, nil).with(func(m *module.Manifest) { m.DefaultEnabled = enabled; m.Settings = secs })
	}
	cfg := func(mods ...module.Module) ConfigCheck {
		return ConfigCheck{
			Config: config.Options{
				AppID:   "gotham",
				Environ: []string{"GOTHAM_GADGETS_CIPHER=riddle", "GOTHAM_PLAIN_GRAPPLE=4"},
				Retired: []config.RetiredName{
					{Name: "GOTHAM_GADGETS_CIPHER", Canonical: "modules.gadgets.vault.cipher"},
					{Name: "GOTHAM_PLAIN_GRAPPLE", Canonical: "modules.plain.belt.grapple"},
				},
				Logger: slog.New(slog.DiscardHandler),
			},
			StateDirDefault: filepath.Join(t.TempDir(), "state"),
			Modules:         mods,
		}
	}
	byName := func(uses []config.RetiredUse, name string) config.RetiredUse {
		t.Helper()
		for _, u := range uses {
			if u.Name == name {
				return u
			}
		}
		t.Fatalf("no use named %s in %+v", name, uses)
		return config.RetiredUse{}
	}
	for _, enabled := range []bool{true, false} {
		uses, err := CheckConfig(t.Context(), cfg(mk("gadgets", enabled, module.Settings(gadgets)), mk("plain", true)))
		re, ok := errors.AsType[*config.RetiredNameError](err)
		if !ok || len(re.Uses) != 1 || re.Uses[0].Name != "GOTHAM_GADGETS_CIPHER" {
			t.Fatalf("enabled=%v: err = %v, want *RetiredNameError naming only the gadgets secret", enabled, err)
		}
		if u := byName(uses, "GOTHAM_GADGETS_CIPHER"); !u.Refused || u.Unchecked {
			t.Errorf("enabled=%v: the gadgets secret = %+v, want refused and judged", enabled, u)
		}
		if u := byName(uses, "GOTHAM_PLAIN_GRAPPLE"); u.Refused || !u.Unchecked {
			t.Errorf("enabled=%v: a module with no section = %+v, want unchecked", enabled, u)
		}
	}
	// A module whose sections the caller never declared is the old seam: unchecked.
	uses, err := CheckConfig(t.Context(), cfg(mk("gadgets", true), mk("plain", true)))
	if err != nil {
		t.Fatalf("a module with no section refused: %v", err)
	}
	if u := byName(uses, "GOTHAM_GADGETS_CIPHER"); u.Refused || !u.Unchecked {
		t.Errorf("a section-less gadgets = %+v, want unchecked", u)
	}
}
