// file: core/app/operator_config_test.go
// version: 0.1.0
// guid: 0f3b8d61-9c24-4e7a-a5d2-6e1c7b9f4a08
// last-edited: 2026-11-20

package app

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/falkcorp/mediacore/config"
	"github.com/falkcorp/mediacore/kernel"
)

// TestOperatorConfig_OptionsReachTheLayers (F3-07 slice 3): the app's
// environment, flags and core defaults are the layers OperatorConfig loads,
// a retired name refuses as a boot would, and nothing is created.
func TestOperatorConfig_OptionsReachTheLayers(t *testing.T) {
	state := filepath.Join(t.TempDir(), "state")
	opts := Options{
		AppID:        "gotham",
		StateDir:     state,
		Env:          []string{"GOTHAM_CORE_LOG_LEVEL=debug"},
		ConfigFlags:  map[config.Path]string{"core.storage.path": filepath.Join(state, "arkham.pebble")},
		CoreDefaults: CoreDefaults{HTTPListen: "localhost:8484"},
	}
	acc, err := OperatorConfig(t.Context(), opts, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = acc.Close(t.Context()) }()
	for p, want := range map[config.Path]string{
		"core.log.level":    `"debug"`,
		"core.http.listen":  `"localhost:8484"`,
		"core.storage.path": `"` + filepath.Join(state, "arkham.pebble") + `"`,
	} {
		if ks, ok := acc.Lookup(p); !ok || string(ks.Effective) != want {
			t.Errorf("%s = %s (key %v), want %s", p, ks.Effective, ok, want)
		}
	}
	if _, err := acc.Apply(t.Context(), config.Patch{}, config.Actor{}); !errors.Is(err, kernel.ErrOperatorConfigReadOnly) {
		t.Errorf("Apply err = %v, want ErrOperatorConfigReadOnly", err)
	}
	if _, err := os.Stat(state); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("state dir: stat err = %v, want it never created", err)
	}

	opts.RetiredConfigNames = []config.RetiredName{{Name: "GOTHAM_DB", Canonical: "core.storage.path"}}
	opts.Env = []string{"GOTHAM_DB=/srv/gotham/main.pebble"}
	if _, err := OperatorConfig(t.Context(), opts, nil); err == nil {
		t.Fatal("a retired operator name loaded")
	} else if _, ok := errors.AsType[*config.RetiredNameError](err); !ok {
		t.Fatalf("err = %v, want *config.RetiredNameError", err)
	}
}
