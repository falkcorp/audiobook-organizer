// file: core/kernel/config_keys_test.go
// version: 1.0.0
// guid: e41b8d07-95a3-4c2f-b6e8-0a7d3c15f924
// last-edited: 2026-11-18

package kernel

import (
	"strings"
	"testing"

	"github.com/falkcorp/mediacore/config"
)

func TestCoreKeys_ReportsEverySectionKey(t *testing.T) {
	keys, err := CoreKeys()
	if err != nil {
		t.Fatalf("CoreKeys: %v", err)
	}
	for p := range allLeaves(t) {
		if _, ok := keys[config.Path(p)]; !ok && !strings.Contains(p, "<") {
			t.Errorf("section leaf %s is not a key of CoreKeys", p)
		}
	}
	for _, p := range []config.Path{"core.ai.journal_retention", "core.http.public_base_url", "core.storage.path"} {
		ks, ok := keys[p]
		if !ok {
			t.Fatalf("%s is not a key", p)
		}
		if ks.Path != p {
			t.Errorf("%s: KeyState.Path = %s", p, ks.Path)
		}
	}
	if keys["core.http.public_base_url"].Protection != config.ProtectAdmin {
		t.Errorf("core.http.public_base_url protection = %v, want admin", keys["core.http.public_base_url"].Protection)
	}
}
