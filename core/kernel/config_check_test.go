// file: core/kernel/config_check_test.go
// version: 1.0.0
// guid: 69eef733-be91-4046-8c4b-c7d78d30e9e2
// last-edited: 2026-11-10

package kernel

import (
	"errors"
	"log/slog"
	"path/filepath"
	"testing"

	"github.com/falkcorp/mediacore/config"
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
