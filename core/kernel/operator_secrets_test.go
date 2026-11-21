// file: core/kernel/operator_secrets_test.go
// version: 1.0.0
// guid: 91a6e3d4-2b7f-4c05-9d18-e4f0a37c2b56
// last-edited: 2026-11-19

package kernel

import (
	"errors"
	"iter"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/falkcorp/mediacore/config"
	"github.com/falkcorp/mediacore/module"
	"github.com/falkcorp/mediacore/secrets"
)

type vaultSettings struct {
	Key secrets.Ref `json:"key"`
}

var vaultSection = config.Define("modules.lucius.vault", vaultSettings{})

const vaultKey secrets.Name = "modules.lucius.vault.key"

// operatorSecretHost boots a gate-on kernel whose module lucius declares a
// secret key and an AppConfig that admits it.
func operatorSecretHost(t *testing.T, filePath string, env ...string) (*Kernel, module.Deps) {
	t.Helper()
	h := newSwapHost(t)
	var dep module.Deps
	c := h.gatedConfig(&dep, env...)
	c.ConfigOptions.FilePath = filePath
	c.Modules = append(c.Modules, mod("lucius", func(r *module.Registrar) error {
		dep = r.Deps()
		return nil
	}).with(func(m *module.Manifest) {
		m.Settings = []module.SettingsSection{module.Settings(vaultSection)}
		m.AppConfig = &module.AppConfigSpec{Secrets: func(n secrets.Name) bool { return n == vaultKey }}
	}))
	return h.mustBoot(c), dep
}

// TestOperatorSecrets_ReachTheSecretStore: a secret key supplied by the
// environment or by a <NAME>_FILE is read through Deps.Config.Secret with its
// Source, before any module registers, and the store refuses to write the name.
func TestOperatorSecrets_ReachTheSecretStore(t *testing.T) {
	file := filepath.Join(t.TempDir(), "vault-key")
	if err := os.WriteFile(file, []byte(secretsFixture+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	yamlFile := filepath.Join(t.TempDir(), "gotham.yaml")
	if err := os.WriteFile(yamlFile, []byte("modules:\n  lucius:\n    vault:\n      key: "+secretsFixture+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name     string
		filePath string
		env      []string
		src      secrets.Source
	}{
		{"env", "", []string{"GOTHAM_LUCIUS_VAULT_KEY=" + secretsFixture}, secrets.SourceEnv},
		{"env _FILE variable (an env source)", "", []string{"GOTHAM_LUCIUS_VAULT_KEY_FILE=" + file}, secrets.SourceEnv},
		{"config file", yamlFile, nil, secrets.SourceFile},
	} {
		t.Run(tc.name, func(t *testing.T) {
			k, dep := operatorSecretHost(t, tc.filePath, tc.env...)
			v, meta, err := dep.Config.Secret(t.Context(), vaultKey)
			if err != nil || !meta.Set || v.Reveal() != secretsFixture {
				t.Fatalf("Secret = %v, %+v, %v; want the operator value", v, meta, err)
			}
			if meta.Source != tc.src {
				t.Errorf("Source = %v, want %v", meta.Source, tc.src)
			}
			if _, err := k.sec.store.Put(t.Context(), vaultKey, secrets.NewValue("two-face"), "user:alfred"); err == nil {
				t.Error("Put of an operator-supplied secret was accepted; it must be locked")
			}
			if v, _, _ := dep.Config.Secret(t.Context(), vaultKey); v.Reveal() != secretsFixture {
				t.Error("the operator value changed")
			}
		})
	}
}

// TestOperatorSecrets_NothingSuppliedLeavesTheStoreAlone: with no operator
// value the name is unset and writable.
func TestOperatorSecrets_NothingSuppliedLeavesTheStoreAlone(t *testing.T) {
	k, dep := operatorSecretHost(t, "")
	if _, meta, err := dep.Config.Secret(t.Context(), vaultKey); !errors.Is(err, secrets.ErrNotSet) || meta.Set {
		t.Fatalf("Secret = %+v, %v; want ErrNotSet", meta, err)
	}
	if _, err := k.sec.store.Put(t.Context(), vaultKey, secrets.NewValue("two-face"), "user:alfred"); err != nil {
		t.Fatalf("Put: %v", err)
	}
}

type fakeOperatorSecrets map[config.Path]config.OperatorValue

func (f fakeOperatorSecrets) OperatorSecrets() iter.Seq2[config.Path, config.OperatorValue] {
	return func(yield func(config.Path, config.OperatorValue) bool) {
		for p, v := range f {
			if !yield(p, v) {
				return
			}
		}
	}
}

type fakeSink map[secrets.Name]secrets.Source

func (f fakeSink) Ephemeral(n secrets.Name, _ secrets.Value, src secrets.Source) { f[n] = src }

// TestOperatorSecrets_AFlagSecretIsRefused: the hand-off refuses a secret from a
// layer it does not know, naming where to set it instead (config itself refuses a
// secret flag at Load; this is the second wall), and a value that is not a string.
func TestOperatorSecrets_AFlagSecretIsRefused(t *testing.T) {
	sink := fakeSink{}
	err := handOperatorSecrets(fakeOperatorSecrets{"modules.lucius.vault.key": {Value: []byte(`"x"`), Src: config.SourceFlag, By: "flag --vault-key"}}, sink)
	if err == nil || !strings.Contains(err.Error(), "environment") || !strings.Contains(err.Error(), "flag --vault-key") || len(sink) != 0 {
		t.Fatalf("err = %v, registered = %v; want a refusal naming the environment", err, sink)
	}
	err = handOperatorSecrets(fakeOperatorSecrets{"modules.lucius.vault.key": {Value: []byte(`7`), Src: config.SourceEnv, By: "env X"}}, sink)
	if err == nil || len(sink) != 0 {
		t.Fatalf("a non-string value: err = %v, registered = %v", err, sink)
	}
	ok := fakeSink{}
	if err := handOperatorSecrets(fakeOperatorSecrets{"a.b": {Value: []byte(`"x"`), Src: config.SourceEnv}, "c.d": {Value: []byte(`"y"`), Src: config.SourceFile}}, ok); err != nil ||
		ok["a.b"] != secrets.SourceEnv || ok["c.d"] != secrets.SourceFile {
		t.Fatalf("env and file: err = %v, registered = %v", err, ok)
	}
}
