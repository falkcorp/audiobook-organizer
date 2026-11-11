// file: internal/books/cutover/config_cutover_failclosed_test.go
// version: 1.0.0
// guid: 7d3f8b51-0c2e-4a96-b4d8-e15a9c6027f3
// last-edited: 2026-11-11

package cutover

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/falkcorp/mediacore/config"
	"github.com/falkcorp/mediacore/storage"
	"github.com/falkcorp/mediacore/storage/datamigrate"
)

const (
	kAuthMode = "core.auth.mode"
	kGate     = "core.http.basic_gate.enabled"
)

// failClosedBlob sets the two HEAD fields the FailClosed keys come from.
func failClosedBlob(enableAuth, basicAuth bool) func(*rig) {
	return func(r *rig) {
		r.mutate = func(t testing.TB, lr storage.LegacyRange) {
			rewriteBlob(t, lr, func(tree map[string]any) {
				tree["enable_auth"] = enableAuth
				tree["basic_auth_enabled"] = basicAuth
			})
		}
	}
}

// TestConfigCutover_FailClosedKeysAreStaged: core.auth.mode and
// core.http.basic_gate.enabled are stored by M2 whatever their value, the
// default included (the registry skips every other default-equal value), so a
// stored value that is stricter than an operator's later one defends it.
func TestConfigCutover_FailClosedKeysAreStaged(t *testing.T) {
	for _, c := range []struct {
		name               string
		enableAuth, basic  bool
		wantMode, wantGate string
	}{
		{"both at their defaults", true, false, `"required"`, `false`},
		{"both weakened", false, true, `"disabled"`, `true`},
		{"both strict", true, true, `"required"`, `true`},
	} {
		t.Run(c.name, func(t *testing.T) {
			r := newRig(t, "stage1", failClosedBlob(c.enableAuth, c.basic))
			snap, err := r.run(t)
			if err != nil {
				t.Fatalf("M2: %v", err)
			}
			for path, want := range map[string]string{kAuthMode: c.wantMode, kGate: c.wantGate} {
				staged := false
				for _, call := range snap.Calls {
					if call.Path == path {
						staged = true
						if !call.Real {
							t.Errorf("%s was not staged through the real registry", path)
						}
						if strings.TrimSpace(call.Value) != want {
							t.Errorf("%s staged as %s, want %s", path, call.Value, want)
						}
					}
				}
				if !staged {
					t.Errorf("%s was never staged", path)
				}
				got, ok := snap.Config[path]
				if !ok || !jsonSame(got, json.RawMessage(want)) {
					t.Errorf("%s: stored row %s (present %v), want %s", path, got, ok, want)
				}
			}
		})
	}
}

// TestConfigCutover_FailClosedStoredBeatsWeakerOperator: with both keys
// stored by M2 and an operator layer that weakens them, a real config store
// keeps the stored, stricter value, including the one M2 stored at the
// default, and reports it.
func TestConfigCutover_FailClosedStoredBeatsWeakerOperator(t *testing.T) {
	r := newRig(t, "stage1", failClosedBlob(true, true))
	weaker := []string{"ABO_CORE_AUTH_MODE=disabled", "ABO_CORE_HTTP_BASIC_GATE_ENABLED=false"}
	var rep config.AttachReport
	effective := map[string]string{}
	err := r.boot(context.Background(), r.newMem(t), []datamigrate.DataMigration{r.migration()}, func(ctx context.Context, e *bootEnv) error {
		st, err := newFailClosedStore(config.Options{AppID: rigAppID, Environ: weaker})
		if err != nil {
			return err
		}
		if _, err := st.Boot(ctx); err != nil {
			return err
		}
		if err := st.Load(ctx); err != nil {
			return err
		}
		if rep, err = st.Attach(ctx, rowStore{ks: e.ks}, noWrites{}); err != nil {
			return err
		}
		for _, p := range []string{kAuthMode, kGate} {
			ks, ok := st.Lookup(config.Path(p))
			if !ok {
				t.Errorf("%s is not a key", p)
				continue
			}
			effective[p] = string(ks.Effective)
		}
		return st.Close(ctx)
	})
	if err != nil {
		t.Fatalf("M2 and the attach: %v", err)
	}
	if effective[kAuthMode] != `"required"` || effective[kGate] != `true` {
		t.Errorf("effective values %v: a weaker operator value beat M2's stored one", effective)
	}
	for _, p := range []config.Path{kAuthMode, kGate} {
		if _, ok := rep.FailClosed[p]; !ok {
			t.Errorf("%s: the stricter stored value was not reported (AttachReport.FailClosed = %v)", p, rep.FailClosed)
		}
	}
}

// noWrites is the Committer of a store that only reads: nothing here calls Apply.
type noWrites struct{}

func (noWrites) Commit(context.Context, config.Commit) error {
	return errors.New("the drill's config store does not write")
}
