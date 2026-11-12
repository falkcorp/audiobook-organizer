// file: internal/books/cutover/config_cutover_quarantine_test.go
// version: 1.0.1
// guid: 6b1d7e24-9c53-4a08-8f6e-2d4a7c91b350
// last-edited: 2026-11-11

package cutover

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"
	"testing"

	"github.com/falkcorp/mediacore/config"
	"github.com/falkcorp/mediacore/secrets"
	"github.com/falkcorp/mediacore/storage"
)

// reasonConstants are every Reason a quarantine record of M2 may carry: fixed
// sentences naming a kind of refusal, never a setting's name or value.
func reasonConstants() []string {
	return []string{reasonUnmapped, reasonRefusedUnknown, reasonRefusedStore, reasonRefusedValue, reasonNotConvertible,
		reasonUnreadableRow, reasonSecretConflict, reasonSecretName, reasonOAuthDisabled, reasonOperatorSecret,
		reasonWebhookNoURL, reasonPluginName, reasonURLUnproven, reasonOAuthNoProvider}
}

// checkQuarantined asserts what every quarantine record must satisfy: sealed,
// its reason one of the fixed sentences, its value recoverable, and neither
// the reason nor the logs carrying any credential the fixtures hold.
func checkQuarantined(t *testing.T, r *rig, snap *snapshot) {
	t.Helper()
	for _, q := range snap.Quarantine {
		if !q.Sealed {
			t.Errorf("record %d (%s) is not sealed", q.N, q.Key)
		}
		if !slices.Contains(reasonConstants(), q.Reason) {
			t.Errorf("record %d (%s) has a reason that is not one of the fixed sentences: %q", q.N, q.Key, q.Reason)
		}
		if q.Value == "" {
			t.Errorf("record %d (%s) holds no recoverable value", q.N, q.Key)
		}
		for _, plain := range allFixtureValues() {
			if strings.Contains(q.Reason, plain) || strings.Contains(q.Key, plain) || strings.Contains(q.Step, plain) {
				t.Errorf("record %d carries a credential in its reason, key or step", q.N)
			}
			if strings.Contains(r.logs.String(), plain) {
				t.Errorf("the logs carry a credential")
			}
		}
		if len(q.Value) >= 8 && (strings.Contains(q.Reason, q.Value) || strings.Contains(r.logs.String(), q.Value)) {
			t.Errorf("record %d's value appears in its reason or in the logs", q.N)
		}
	}
}

func quarantineFor(snap *snapshot, key string) (quarantinedRow, bool) {
	for _, q := range snap.Quarantine {
		if q.Key == key {
			return q, true
		}
	}
	return quarantinedRow{}, false
}

// TestConfigCutover_QuarantineNotDrop: what M2 cannot place it quarantines,
// sealed, with a fixed reason and a recoverable value; it never drops it and
// never fails the upgrade for it.
func TestConfigCutover_QuarantineNotDrop(t *testing.T) {
	t.Run("the overlay corpus quarantines exactly its two rows", func(t *testing.T) {
		r := newRig(t, "overlay")
		snap, err := r.run(t)
		if err != nil {
			t.Fatalf("M2: %v", err)
		}
		keys := make([]string, 0, len(snap.Quarantine))
		for _, q := range snap.Quarantine {
			keys = append(keys, q.Key)
		}
		slices.Sort(keys)
		want := []string{"setting:basic_auth_password", "setting:config_blob#batcomputer_uplink_mode"}
		if !slices.Equal(keys, want) {
			t.Fatalf("quarantined %v, want %v", keys, want)
		}
		if q, _ := quarantineFor(snap, "setting:basic_auth_password"); q.Reason != reasonUnreadableRow {
			t.Errorf("the sealed row's reason is %q, want %q", q.Reason, reasonUnreadableRow)
		}
		if q, _ := quarantineFor(snap, "setting:config_blob#batcomputer_uplink_mode"); q.Reason != reasonUnmapped {
			t.Errorf("the unmapped member's reason is %q, want %q", q.Reason, reasonUnmapped)
		}
		if err, ok := snap.Unreadable["core.http.basic_gate.password"]; !ok || !errors.Is(err, secrets.ErrCorrupt) {
			t.Errorf("the gate password: unreadable = %v (marked %v), want ErrCorrupt", err, ok)
		}
		if _, still := snap.Legacy["setting:basic_auth_password"]; still {
			t.Error("the unreadable row is still in the legacy range after its ciphertext was quarantined")
		}
		checkQuarantined(t, r, snap)
	})

	// A path the clean run stages in the table-backed double: refused, its
	// member is quarantined instead of dropped.
	cleanRig := func(t *testing.T, opts ...func(*rig)) (*rig, string) {
		t.Helper()
		base := newRig(t, "stage1", seedKnown)
		snap, err := base.run(t)
		if err != nil {
			t.Fatalf("clean M2: %v", err)
		}
		var paths []string
		for _, c := range snap.Calls {
			if !c.Real && strings.HasPrefix(c.Path, "modules.books.") {
				paths = append(paths, c.Path)
			}
		}
		if len(paths) == 0 {
			t.Fatal("the clean run staged no modules.books.* key to refuse")
		}
		slices.Sort(paths)
		return newRig(t, "stage1", append([]func(*rig){seedKnown}, opts...)...), paths[0]
	}

	for _, tc := range []struct {
		name   string
		err    func(path string) error
		reason string
	}{
		{"an unregistered key", func(p string) error { return fmt.Errorf("%w: %s", config.ErrUnknownKey, p) }, reasonRefusedUnknown},
		{"a key that cannot be stored", func(p string) error { return fmt.Errorf("%w: %s is operator-only", config.ErrInvalid, p) }, reasonRefusedStore},
		{"a value the key refuses", func(p string) error {
			return &config.FieldError{Path: config.Path(p), Err: fmt.Errorf("%w: out of range", config.ErrInvalid)}
		}, reasonRefusedValue},
	} {
		t.Run("refused as "+tc.name, func(t *testing.T) {
			probe, path := cleanRig(t)
			probe.refuse = map[string]error{path: tc.err(path)}
			snap, err := probe.run(t)
			if err != nil {
				t.Fatalf("M2 failed on a refused value: %v", err)
			}
			if _, staged := snap.Config[path]; staged {
				t.Errorf("%s was stored although the registry refused it", path)
			}
			var got []quarantinedRow
			for _, q := range snap.Quarantine {
				if q.Reason == tc.reason {
					got = append(got, q)
				}
			}
			if len(got) != 1 {
				t.Fatalf("%d records carry %q, want 1: %+v", len(got), tc.reason, snap.Quarantine)
			}
			if !strings.HasPrefix(got[0].Key, "setting:config_blob#") {
				t.Errorf("the record's key is %q, want a blob member", got[0].Key)
			}
			member := strings.TrimPrefix(got[0].Key, "setting:config_blob#")
			if tree := blobTree(t, snap.Legacy); tree[strings.SplitN(member, ".", 2)[0]] != nil && !strings.Contains(member, ".") {
				t.Errorf("the blob still holds %s, which was quarantined", member)
			}
			checkQuarantined(t, probe, snap)
		})
	}

	t.Run("any other staging error fails the chunk and commits nothing", func(t *testing.T) {
		// The baseline is the same store migrated without the failure.
		clean := newRig(t, "stage1", seedKnown)
		want, err := clean.run(t)
		if err != nil {
			t.Fatalf("clean M2: %v", err)
		}
		probe, path := cleanRig(t)
		probe.refuse = map[string]error{path: errors.New("the disk is on fire")}
		mem := probe.newMem(t)
		if _, err := probe.runOn(t, mem); err == nil {
			t.Fatal("M2 succeeded although staging failed with an unclassified error")
		}
		// Had the failed chunk committed anything (a secret, a quarantine
		// record, the blob), the rerun would meet its own leftovers: a create-
		// only secret conflicts, the blob is already stripped.
		probe.refuse = nil
		got, err := probe.runOn(t, mem)
		if err != nil {
			t.Fatalf("rerun after the failure: %v", err)
		}
		if !maps.EqualFunc(got.Secrets, want.Secrets, func(a, b string) bool { return a == b }) {
			t.Errorf("secrets after a failed chunk and a rerun differ from a clean run: %v vs %v", slices.Sorted(maps.Keys(got.Secrets)), slices.Sorted(maps.Keys(want.Secrets)))
		}
		if len(got.Config) != len(want.Config) {
			t.Errorf("%d config rows after a failed chunk and a rerun, a clean run left %d", len(got.Config), len(want.Config))
		}
		for k, v := range want.Config {
			if !jsonSame(got.Config[k], v) {
				t.Errorf("config row %s differs after a failed chunk and a rerun", k)
			}
		}
		if len(got.Quarantine) != len(want.Quarantine) {
			t.Errorf("%d quarantine records after a failed chunk and a rerun, a clean run left %d: %+v", len(got.Quarantine), len(want.Quarantine), got.Quarantine)
		}
		if !bytes.Equal(got.Legacy["setting:config_blob"], want.Legacy["setting:config_blob"]) {
			t.Error("the blob differs after a failed chunk and a rerun")
		}
	})

	t.Run("a secret name that already holds content is quarantined, not overwritten", func(t *testing.T) {
		r := newRig(t, "stage1", seedKnown)
		r.preMigrate = func(ctx context.Context, e *bootEnv) error {
			for _, name := range []secrets.Name{"integrations.ai.openai.api_key", "modules.books.acoustid.api_key"} {
				if _, err := e.sec.Put(ctx, name, secrets.NewValue("existing-owner-value"), "owner"); err != nil {
					return err
				}
			}
			return nil
		}
		snap, err := r.run(t)
		if err != nil {
			t.Fatalf("M2: %v", err)
		}
		for _, name := range []string{"integrations.ai.openai.api_key", "modules.books.acoustid.api_key"} {
			if got := snap.Secrets[name]; got != "existing-owner-value" {
				t.Errorf("the existing secret %s was overwritten", name)
			}
		}
		// A value that came from a setting row is quarantined under the row's
		// key; one from a blob member under the member's.
		for key, plain := range map[string]string{"setting:openai_api_key": fxOpenAI, "setting:config_blob#acoustid_api_key": fxAcoustid} {
			q, ok := quarantineFor(snap, key)
			if !ok {
				t.Errorf("the conflicting value was not quarantined under %s: %+v", key, snap.Quarantine)
				continue
			}
			if q.Reason != reasonSecretConflict || q.Value != plain {
				t.Errorf("%s: reason %q, value recovered %v", key, q.Reason, q.Value == plain)
			}
		}
		if _, still := snap.Legacy["setting:openai_api_key"]; still {
			t.Error("the openai row is still in the legacy range after its value was quarantined")
		}
		checkQuarantined(t, r, snap)
	})

	t.Run("an unknown nested member is quarantined under its dotted name", func(t *testing.T) {
		r := newRig(t, "stage1", func(r *rig) {
			r.mutate = func(t testing.TB, lr storage.LegacyRange) {
				rewriteBlob(t, lr, func(tree map[string]any) { setAt(tree, "itunes.batcomputer_nested_flag", "robin") })
			}
		})
		snap, err := r.run(t)
		if err != nil {
			t.Fatalf("M2: %v", err)
		}
		q, ok := quarantineFor(snap, "setting:config_blob#itunes.batcomputer_nested_flag")
		if !ok {
			t.Fatalf("the nested member was not quarantined: %+v", snap.Quarantine)
		}
		if q.Reason != reasonUnmapped || q.Value != `"robin"` {
			t.Errorf("record = reason %q, value %q", q.Reason, q.Value)
		}
		if itunes, _ := blobTree(t, snap.Legacy)["itunes"].(map[string]any); itunes != nil {
			if _, still := itunes["batcomputer_nested_flag"]; still {
				t.Error("the blob still holds the quarantined member")
			}
		}
		checkQuarantined(t, r, snap)
	})
}

// TestConfigCutover_QuarantineReasonsCarryNoValue: across the scenarios that
// quarantine, no record's reason, key or step, and no log line, carries a
// credential or the text of an error that could (errClass names a class).
func TestConfigCutover_QuarantineReasonsCarryNoValue(t *testing.T) {
	for _, name := range []string{"stage1", "overlay"} {
		r := newRig(t, name, seedKnown)
		r.allowRegen = true
		r.noKeyDir = true
		snap, err := r.run(t)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if len(snap.Quarantine) == 0 {
			t.Fatalf("%s: nothing quarantined, the check proves nothing", name)
		}
		checkQuarantined(t, r, snap)
	}
	for _, err := range []error{
		fmt.Errorf("pebble: key setting:openai_api_key value %s", fxOpenAI),
		fmt.Errorf("%w: value %s", storage.ErrConflict, fxGate),
		fmt.Errorf("%w: %s", context.Canceled, fxWebhook),
		fmt.Errorf("%w: %s", storage.ErrClosed, fxGitHub),
	} {
		for _, plain := range allFixtureValues() {
			if strings.Contains(errClass(err), plain) {
				t.Errorf("errClass(%T) carries a credential", err)
			}
		}
	}
}
