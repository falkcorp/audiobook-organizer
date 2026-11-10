// file: core/kernel/config_test.go
// version: 1.0.0
// guid: 8b2e5f7c-3a19-4d06-b8e4-2f9c6a1d7e53
// last-edited: 2026-11-10

package kernel

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/falkcorp/mediacore/config"
	"github.com/falkcorp/mediacore/config/configtest"
	"github.com/falkcorp/mediacore/metrics"
	"github.com/falkcorp/mediacore/module"
)

// configIn is a configInput over a fresh, creatable state directory with the
// modules batman and robin compiled in.
func configIn(t *testing.T) configInput {
	t.Helper()
	return configInput{
		AppID: "gotham", StateDirDefault: filepath.Join(t.TempDir(), "state"),
		Modules: []module.Module{mod("batman", nil), mod("robin", nil)},
	}
}

// bootCfg runs bootConfig and closes the store at cleanup.
func bootCfg(t *testing.T, in configInput) *configBoot {
	t.Helper()
	cb, err := bootConfig(context.Background(), in)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := cb.store.Close(context.Background()); err != nil {
			t.Errorf("close: %v", err)
		}
	})
	return cb
}

// started is bootCfg + loadConfig + Attach over the in-memory doubles.
func startedCfg(t *testing.T, in configInput) (*configBoot, *configtest.Committer) {
	t.Helper()
	ctx := context.Background()
	cb := bootCfg(t, in)
	if _, err := cb.loadConfig(ctx); err != nil {
		t.Fatal(err)
	}
	mem := configtest.NewMemory()
	cm := configtest.NewCommitter(mem)
	if _, err := cb.store.Attach(ctx, mem, cm); err != nil {
		t.Fatal(err)
	}
	return cb, cm
}

// TestBootConfig_RegistersEveryCoreSection: every core section registers
// through the store, core.paths defaults to the resolved state directory and
// core.modules to each manifest's DefaultEnabled; Boot opens the record's
// root there and touches no storage.
func TestBootConfig_RegistersEveryCoreSection(t *testing.T) {
	in := configIn(t)
	in.Modules = append(in.Modules, testModule{man: module.Manifest{ID: "joker", Version: "1.0.0"}})
	cb := bootCfg(t, in)
	if cb.stateDir != in.StateDirDefault || cb.fellBack {
		t.Errorf("state dir %q (fell back %v), want the default %q", cb.stateDir, cb.fellBack, in.StateDirDefault)
	}
	if fi, err := os.Stat(cb.stateDir); err != nil || fi.Mode().Perm() != 0o700 {
		t.Errorf("state dir: %v, %v; want created 0700", fi, err)
	}
	if len(cb.handles) != len(coreSections()) {
		t.Errorf("%d handles, want one per core section (%d)", len(cb.handles), len(coreSections()))
	}
	if raw, src, ok := cb.view.Raw(stateDirKey); !ok || string(raw) != mustJSONK(t, cb.stateDir) || src != config.SourceDefault {
		t.Errorf("BootView %s = %s %v %v", stateDirKey, raw, src, ok)
	}
	if _, err := cb.loadConfig(context.Background()); err != nil {
		t.Fatal(err)
	}
	for id, want := range map[string]bool{"batman": true, "robin": true, "joker": false} {
		ks, ok := cb.store.Lookup(config.Path("core.modules." + id + ".enabled"))
		if !ok || string(ks.Effective) != mustJSONK(t, want) {
			t.Errorf("core.modules.%s.enabled = %s (%v), want %v", id, ks.Effective, ok, want)
		}
	}
	n := 0
	for range cb.store.Keys() {
		n++
	}
	if n < 200 {
		t.Errorf("%d keys indexed; every core key should be", n)
	}
}

func mustJSONK(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// TestStateDirFallbackMatchesHEAD_Kernel: the kernel's P0 applies the one
// resolver: an explicit directory never falls back; an uncreatable default
// falls back to the directory of an absolute core.storage.path with a
// warning, and the boot record opens there; a relative or empty path fails
// boot naming <APP>_CORE_PATHS_STATE_DIR.
func TestStateDirFallbackMatchesHEAD_Kernel(t *testing.T) {
	root := t.TempDir()
	blocker := filepath.Join(root, "file")
	if err := os.WriteFile(blocker, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	uncreatable := filepath.Join(blocker, "state")
	dbDir := filepath.Join(root, "db")

	in := configIn(t)
	in.StateDirDefault = uncreatable
	in.Flags = map[config.Path]string{storagePathKey: filepath.Join(dbDir, "main.pebble")}
	cb := bootCfg(t, in)
	if cb.stateDir != dbDir || !cb.fellBack {
		t.Errorf("fallback: state dir %q (fell back %v), want %q", cb.stateDir, cb.fellBack, dbDir)
	}

	in = configIn(t)
	explicit := filepath.Join(blocker, "explicit")
	in.Flags = map[config.Path]string{stateDirKey: explicit, storagePathKey: filepath.Join(dbDir, "main.pebble")}
	if _, err := bootConfig(context.Background(), in); err == nil || !strings.Contains(err.Error(), "GOTHAM_CORE_PATHS_STATE_DIR") || !strings.Contains(err.Error(), explicit) {
		t.Errorf("explicit uncreatable: %v, want a refusal naming the directory and GOTHAM_CORE_PATHS_STATE_DIR", err)
	}

	for _, db := range []string{"db/main.pebble", ""} {
		in = configIn(t)
		in.StateDirDefault = uncreatable
		if db != "" {
			in.Flags = map[config.Path]string{storagePathKey: db}
		}
		_, err := bootConfig(context.Background(), in)
		if err == nil || !strings.Contains(err.Error(), "GOTHAM_CORE_PATHS_STATE_DIR") {
			t.Errorf("db %q: %v, want a refusal naming GOTHAM_CORE_PATHS_STATE_DIR", db, err)
		}
	}

	in = configIn(t)
	in.EnvPrefix = "ARKHAM"
	in.StateDirDefault = uncreatable
	if _, err := bootConfig(context.Background(), in); err == nil || !strings.Contains(err.Error(), "ARKHAM_CORE_PATHS_STATE_DIR") {
		t.Errorf("custom prefix: %v", err)
	}
}

// TestUnknownModuleInConfig_OperatorFails (06 §6.10, COR-14):
// core.modules.<id>.enabled naming an unknown module in an operator layer
// fails boot with ProblemUnknownModuleInConfig listing the known IDs.
func TestUnknownModuleInConfig_OperatorFails(t *testing.T) {
	in := configIn(t)
	in.Flags = map[config.Path]string{"core.modules.joker.enabled": "true"}
	_, err := bootConfig(context.Background(), in)
	if !errors.Is(err, module.ErrUnknownModuleInConfig) {
		t.Fatalf("err = %v, want ProblemUnknownModuleInConfig", err)
	}
	for _, want := range []string{"joker", "batman, robin", "flag core.modules.joker.enabled"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("err = %v, want it to name %q", err, want)
		}
	}
	in = configIn(t)
	in.Flags = map[config.Path]string{"core.modules.robin.enabled": "false"}
	cb := bootCfg(t, in)
	if v, src, ok := cb.view.Bool("core.modules.robin.enabled"); !ok || v || src != config.SourceFlag {
		t.Errorf("a known module's flag: %v %v %v", v, src, ok)
	}
}

// TestUnknownModuleInConfig_BootRecordWarns (06 §6.10, COR-14): the same in
// the UI-written boot record is retained and reported, core.config's health
// degrades, and boot succeeds.
func TestUnknownModuleInConfig_BootRecordWarns(t *testing.T) {
	in := configIn(t)
	if err := os.MkdirAll(in.StateDirDefault, 0o700); err != nil {
		t.Fatal(err)
	}
	rec := `{"format":"mediacore-boot/1","gen":2,"values":{"core.modules.joker.enabled":true,"core.modules.robin.enabled":false}}`
	if err := os.WriteFile(filepath.Join(in.StateDirDefault, "config.boot.json"), []byte(rec), 0o600); err != nil {
		t.Fatal(err)
	}
	cb := bootCfg(t, in)
	if !slices.Equal(cb.unknown, []module.ID{"joker"}) {
		t.Errorf("unknown = %v, want [joker]", cb.unknown)
	}
	if !slices.Contains(cb.HealthTokens(), healthUnknownModule) {
		t.Errorf("health tokens %v", cb.HealthTokens())
	}
	if v, src, ok := cb.view.Bool("core.modules.robin.enabled"); !ok || v || src != config.SourceBootRecord {
		t.Errorf("a known module's record entry: %v %v %v", v, src, ok)
	}
	if _, err := cb.loadConfig(context.Background()); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(filepath.Join(in.StateDirDefault, "config.boot.json"))
	if err != nil || string(b) != rec {
		t.Errorf("the record changed: %s (%v)", b, err)
	}
}

// interactiveAdmin, manager and authOff are 05-config §1.3.7's actors.
func interactiveAdmin() config.Actor {
	return config.Actor{Subject: "alfred", Interactive: true, Can: func(string) bool { return true }}
}

func settingsManager() config.Actor {
	return config.Actor{Subject: "robin", Interactive: true, Can: func(p string) bool { return p != "core:admin" }}
}

func authOffActor() config.Actor {
	return config.Actor{Can: func(p string) bool { return p != "core:admin" }}
}

// applyOne applies one key and reports the error.
func applyOne(cb *configBoot, a config.Actor, p config.Path, v string) error {
	_, err := cb.store.Apply(context.Background(), config.Patch{Set: map[config.Path]json.RawMessage{p: json.RawMessage(v)}}, a)
	return err
}

// TestApply_SettingsManageCannotChangeAdminKeys (05-config §1.11, the
// kernel half over the real keys): core:settings.manage without core:admin
// cannot change sign-in, proxy or updater keys.
func TestApply_SettingsManageCannotChangeAdminKeys(t *testing.T) {
	cb, cm := startedCfg(t, configIn(t))
	for p, v := range map[config.Path]string{
		"core.auth.disabled_allow_remote": `true`,
		"core.http.trusted_proxies":       `["192.0.2.1"]`,
		"core.updater.mode":               `"check"`,
	} {
		var pe *config.ProtectedError
		if err := applyOne(cb, settingsManager(), p, v); !errors.As(err, &pe) || pe.Need < config.ProtectAdmin {
			t.Errorf("%s: %v, want *ProtectedError at admin or above", p, err)
		}
		if err := applyOne(cb, interactiveAdmin(), p, v); err != nil {
			t.Errorf("%s by an interactive admin: %v", p, err)
		}
	}
	if cm.Calls() != 3 {
		t.Errorf("%d commits, want only the admin's 3", cm.Calls())
	}
}

// TestAuthOff_ProtectedKeysRefused (SEC4-8, the kernel half): with auth
// off, protected keys are refused. core.http.basic_gate.enabled joins with
// F3-02, which declares it.
func TestAuthOff_ProtectedKeysRefused(t *testing.T) {
	cb, cm := startedCfg(t, configIn(t))
	for p, v := range map[config.Path]string{
		"core.auth.mode":                     `"disabled"`,
		"core.integrations.egress.lan_allow": `["192.0.2.0/24"]`,
		"core.http.trusted_proxies":          `["192.0.2.1"]`,
	} {
		if err := applyOne(cb, authOffActor(), p, v); !errors.Is(err, config.ErrProtected) {
			t.Errorf("auth off, %s: %v, want ErrProtected", p, err)
		}
	}
	if cm.Calls() != 0 {
		t.Errorf("%d commits", cm.Calls())
	}
}

// TestOwnerTrustRootKeysAreOwnerProtected (SEC3-6, the kernel half): the
// trust-root keys are ProtectOwner; with an owner set, an interactive admin
// without owner proof is refused and one with it succeeds.
func TestOwnerTrustRootKeysAreOwnerProtected(t *testing.T) {
	in := configIn(t)
	in.OwnerSet = func() bool { return true }
	cb, _ := startedCfg(t, in)
	keys := map[config.Path]string{
		"core.auth.mode":           `"disabled"`,
		"core.auth.oidc.providers": `[{"id":"wayne","kind":"oidc","issuer":"https://id.example.com","client_id":"bat","scopes":["openid"],"allowed_emails":["bruce@example.com"],"allowed_domains":[],"default_role":"viewer","link_existing_by_email":false}]`,
		"core.auth.owner.required": `true`,
	}
	proof := interactiveAdmin()
	proof.Owner = true
	for p, v := range keys {
		ks, ok := cb.store.Lookup(p)
		if !ok || ks.Protection != config.ProtectOwner {
			t.Errorf("%s: protection %v (%v), want owner", p, ks.Protection, ok)
			continue
		}
		var pe *config.ProtectedError
		if err := applyOne(cb, interactiveAdmin(), p, v); !errors.As(err, &pe) || pe.Need != config.ProtectOwner {
			t.Errorf("%s without owner proof: %v", p, err)
		}
		if err := applyOne(cb, proof, p, v); err != nil {
			t.Errorf("%s with owner proof: %v", p, err)
		}
	}
}

// TestAttachConfig_NativeStore: P4b-P4d over a real store: Attach writes
// nothing; an Apply commits one c/config/ row and its core.settings.update
// record in one batch; the metrics scope registers once.
func TestAttachConfig_NativeStore(t *testing.T) {
	ctx := context.Background()
	db := bootAuditDB(t, configOwnerSpec(), auditOwnerSpec())
	auditStore, err := buildAudit(ctx, db, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	cb := bootCfg(t, configIn(t))
	if _, err := cb.attachConfig(ctx, auditStore, nil, nil); err == nil {
		t.Error("attachConfig before buildConfigStore succeeded")
	}
	if _, err := cb.loadConfig(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := cb.buildConfigStore(nil); err == nil {
		t.Error("buildConfigStore(nil) succeeded")
	}
	if _, err := cb.buildConfigStore(db); err != nil {
		t.Fatal(err)
	}
	if _, err := cb.attachConfig(ctx, auditStore, nil, nil); err != nil {
		t.Fatal(err)
	}
	if got := keysOf(t, db, "core/config"); len(got) != 0 {
		t.Fatalf("Attach wrote %d rows", len(got))
	}
	if err := applyOne(cb, interactiveAdmin(), "core.updater.channel", `"prerelease"`); err != nil {
		t.Fatal(err)
	}
	if got := keysOf(t, db, "core/config"); len(got) != 1 {
		t.Errorf("%d rows after one Apply, want 1", len(got))
	}
	if got := keysOf(t, db, auditOwnerID); len(got) == 0 {
		t.Error("no audit record")
	}
	p, err := metrics.New(ctx, metrics.Options{AppID: "gotham", Prometheus: true})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := p.Shutdown(context.Background()); err != nil {
			t.Error(err)
		}
	})
	if _, err := cb.configMetrics(p); err != nil {
		t.Fatal(err)
	}
	if _, err := cb.configMetrics(p); err == nil {
		t.Error("configMetrics twice succeeded")
	}
}
