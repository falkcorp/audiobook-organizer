// file: core/kernel/operator_config_test.go
// version: 0.1.0
// guid: 8d41c7e2-5a93-4f0b-b6e8-2c9a1f7d3e54
// last-edited: 2026-11-20

package kernel

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"testing"

	"github.com/falkcorp/mediacore/config"
	"github.com/falkcorp/mediacore/module"
	"github.com/falkcorp/mediacore/secrets"
)

// operatorCheck is a ConfigCheck over the gadgets module (one plain key, one
// admitted secret) with env as the whole environment.
func operatorCheck(t *testing.T, env []string, admit func(secrets.Name) bool) (ConfigCheck, string) {
	t.Helper()
	gadgets := config.Define("modules.gadgets.vault", gadgetSettings{Grapple: 2})
	m := mod("gadgets", nil).with(func(m *module.Manifest) {
		m.Settings = []module.SettingsSection{module.Settings(gadgets)}
		if admit != nil {
			m.AppConfig = &module.AppConfigSpec{Secrets: admit}
		}
	})
	state := filepath.Join(t.TempDir(), "state")
	return ConfigCheck{
		Config: config.Options{
			AppID:   "gotham",
			Environ: env,
			Retired: []config.RetiredName{{Name: "GOTHAM_DB", Canonical: "core.storage.path"}},
			Logger:  slog.New(slog.DiscardHandler),
		},
		StateDirDefault: state,
		Modules:         []module.Module{m},
		Defaults:        CoreDefaults{HTTPListen: "localhost:8484"},
	}, state
}

// TestOperatorConfig_ReadsTheOperatorLayersAlone (F3-07 slice 3): the
// effective values are the operator layers over the defaults (the app's core
// defaults included); no directory is created; every write is refused; an
// admitted operator secret is readable with its source and nothing else is.
func TestOperatorConfig_ReadsTheOperatorLayersAlone(t *testing.T) {
	admit := func(n secrets.Name) bool { return n == "modules.gadgets.vault.cipher" }
	c, state := operatorCheck(t, []string{
		"GOTHAM_GADGETS_VAULT_GRAPPLE=7",
		"GOTHAM_GADGETS_VAULT_CIPHER=riddle",
	}, admit)
	acc, err := OperatorConfig(t.Context(), c)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = acc.Close(context.Background()) })

	if ks, ok := acc.Lookup("modules.gadgets.vault.grapple"); !ok || string(ks.Effective) != "7" || ks.Source != config.SourceEnv {
		t.Errorf("grapple = %s from %v (key %v), want 7 from the environment", ks.Effective, ks.Source, ok)
	}
	if ks, ok := acc.Lookup("core.http.listen"); !ok || string(ks.Effective) != `"localhost:8484"` {
		t.Errorf("core.http.listen = %s (key %v), want the app's core default", ks.Effective, ok)
	}
	if _, err := os.Stat(state); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("state dir: stat err = %v, want it never created", err)
	}

	if _, err := acc.Apply(t.Context(), config.Patch{}, config.Actor{}); !errors.Is(err, ErrOperatorConfigReadOnly) {
		t.Errorf("Apply err = %v, want ErrOperatorConfigReadOnly", err)
	}
	if _, err := acc.PutSecret(t.Context(), "modules.gadgets.vault.cipher", secrets.NewValue("x"), "t"); !errors.Is(err, ErrOperatorConfigReadOnly) {
		t.Errorf("PutSecret err = %v, want ErrOperatorConfigReadOnly", err)
	}
	if err := acc.DeleteSecret(t.Context(), "modules.gadgets.vault.cipher", "t"); !errors.Is(err, ErrOperatorConfigReadOnly) {
		t.Errorf("DeleteSecret err = %v, want ErrOperatorConfigReadOnly", err)
	}

	v, meta, err := acc.Secret(t.Context(), "modules.gadgets.vault.cipher")
	if err != nil || v.Reveal() != "riddle" || !meta.Set || meta.Source != secrets.SourceEnv {
		t.Errorf("Secret = %q, %+v, %v; want riddle from the environment", v.Reveal(), meta, err)
	}
	if _, _, err := acc.Secret(t.Context(), "modules.gadgets.other"); !errors.Is(err, secrets.ErrOutOfScope) {
		t.Errorf("unadmitted Secret err = %v, want ErrOutOfScope", err)
	}
	list, err := acc.ListSecrets(t.Context(), "modules.gadgets.")
	if err != nil || len(list) != 1 || list[0].Name != "modules.gadgets.vault.cipher" || list[0].Source != secrets.SourceEnv {
		t.Errorf("ListSecrets = %+v, %v", list, err)
	}
	if err := acc.Close(t.Context()); err != nil {
		t.Errorf("Close = %v", err)
	}
}

// TestOperatorConfig_UnsetAndUndeclared: an admitted secret no layer sets is
// ErrNotSet; a module set with no AppConfig declaration reaches no secret.
func TestOperatorConfig_UnsetAndUndeclared(t *testing.T) {
	admit := func(n secrets.Name) bool { return n == "modules.gadgets.vault.cipher" }
	c, _ := operatorCheck(t, nil, admit)
	acc, err := OperatorConfig(t.Context(), c)
	if err != nil {
		t.Fatal(err)
	}
	if _, meta, err := acc.Secret(t.Context(), "modules.gadgets.vault.cipher"); !errors.Is(err, secrets.ErrNotSet) || meta.Set {
		t.Errorf("unset Secret = %+v, %v; want ErrNotSet", meta, err)
	}
	if ks, ok := acc.Lookup("modules.gadgets.vault.grapple"); !ok || string(ks.Effective) != "2" {
		t.Errorf("grapple = %s, want the section default 2", ks.Effective)
	}
	_ = acc.Close(t.Context())

	c, _ = operatorCheck(t, []string{"GOTHAM_GADGETS_VAULT_CIPHER=riddle"}, nil)
	acc, err = OperatorConfig(t.Context(), c)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = acc.Close(t.Context()) }()
	if _, _, err := acc.Secret(t.Context(), "modules.gadgets.vault.cipher"); !errors.Is(err, secrets.ErrOutOfScope) {
		t.Errorf("Secret with no AppConfig = %v, want ErrOutOfScope", err)
	}
	if _, err := acc.ListSecrets(t.Context(), ""); !errors.Is(err, secrets.ErrOutOfScope) {
		t.Errorf("ListSecrets with no AppConfig = %v, want ErrOutOfScope", err)
	}
}

// TestOperatorConfig_RefusesAsBootDoes: a retired name that would refuse a
// boot refuses here too, as the *config.RetiredNameError, and a value that
// does not decode is Load's error.
func TestOperatorConfig_RefusesAsBootDoes(t *testing.T) {
	c, state := operatorCheck(t, []string{"GOTHAM_DB=/srv/gotham/main.pebble"}, nil)
	if _, err := OperatorConfig(t.Context(), c); !errorsAsRetired(err) {
		t.Fatalf("err = %v, want *config.RetiredNameError", err)
	}
	if _, err := os.Stat(state); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("state dir: stat err = %v, want it never created", err)
	}
	c, _ = operatorCheck(t, []string{"GOTHAM_GADGETS_VAULT_GRAPPLE=batarang"}, nil)
	if _, err := OperatorConfig(t.Context(), c); err == nil {
		t.Fatal("a value that does not decode loaded")
	}
}

func errorsAsRetired(err error) bool {
	_, ok := errors.AsType[*config.RetiredNameError](err)
	return ok
}

// TestOperatorSecret_SourceRule: a flag cannot carry a secret, and a
// non-string value is refused without echoing it.
func TestOperatorSecret_SourceRule(t *testing.T) {
	str, _ := json.Marshal("riddle")
	if _, src, err := operatorSecret("n", config.OperatorValue{Value: str, Src: config.SourceFile, By: "file"}); err != nil || src != secrets.SourceFile {
		t.Errorf("file secret = %v, %v", src, err)
	}
	if _, _, err := operatorSecret("n", config.OperatorValue{Value: str, Src: config.SourceFlag, By: "--cipher"}); err == nil {
		t.Error("a flag secret was accepted")
	}
	if _, _, err := operatorSecret("n", config.OperatorValue{Value: json.RawMessage(`42`), Src: config.SourceEnv, By: "GOTHAM_X"}); err == nil {
		t.Error("a non-string secret was accepted")
	}
}
