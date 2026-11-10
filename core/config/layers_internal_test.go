// file: core/config/layers_internal_test.go
// version: 1.0.1
// guid: 5e1a7c93-2d4b-4f86-b0c7-8a3d6e9f1b24
// last-edited: 2026-11-10

package config

import (
	"context"
	"encoding/json"
	"testing"
)

// TestResolve_OperatorKeyReadsNoStoredLayer (T4 review MINOR-8, surviving
// mutant M11; 05-config §1.3.5): resolve never reads a ProtectOperator key
// from the boot record or the DB layer, whatever a view holds. Attach and
// classifyBoot keep such values out of the views as well, which is why only a
// view built here can show this layer of the rule.
func TestResolve_OperatorKeyReadsNoStoredLayer(t *testing.T) {
	type gate struct {
		StateDir string `json:"state_dir" reload:"boot" protect:"operator"`
		Vault    string `json:"vault" protect:"operator"`
		Restart  string `json:"restart" reload:"restart" protect:"operator"`
	}
	st, err := NewStore(Options{AppID: "gotham"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := st.Close(context.Background()); err != nil {
			t.Error(err)
		}
	})
	if _, err := Register(st.Registry(), Define("core.gate", gate{StateDir: "/var/lib/gotham", Vault: "wayne-manor", Restart: "batcave"})); err != nil {
		t.Fatal(err)
	}
	stored := json.RawMessage(`"arkham"`)
	for p, def := range map[Path]string{"core.gate.state_dir": `"/var/lib/gotham"`, "core.gate.vault": `"wayne-manor"`, "core.gate.restart": `"batcave"`} {
		c, ok := st.keyFor(p)
		if !ok {
			t.Fatalf("%s is not a key", p)
		}
		all := map[Path]json.RawMessage{p: stored}
		kv := st.resolve(c, view{boot: all, live: all, restart: all})
		if string(kv.raw) != def || kv.src != SourceDefault {
			t.Errorf("%s = %s from %v, want the default %s: an operator-only key read a stored layer", p, kv.raw, kv.src, def)
		}
	}
}
