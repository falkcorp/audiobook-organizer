// file: core/kernel/secrets_test.go
// version: 1.0.3
// guid: 3f0c9a52-6e1b-4d7a-8c25-b94e1f6a0d37
// last-edited: 2026-11-10

package kernel

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/falkcorp/mediacore/health"
	"github.com/falkcorp/mediacore/metrics"
	"github.com/falkcorp/mediacore/metrics/metricstest"
	"github.com/falkcorp/mediacore/secrets"
	"github.com/falkcorp/mediacore/storage"
)

// secretsFixture is a Batman fixture value: never a real secret.
const secretsFixture = "i-am-vengeance-i-am-the-night"

// lastReport is a health.Reporter that keeps the last Result.
type lastReport struct{ r health.Result }

func (l *lastReport) Report(r health.Result) { l.r = r }

// openTestSecrets opens the secret store over db with a scratch state
// directory, closing it at cleanup.
func openTestSecrets(t *testing.T, db *storage.DB, stateDir string, mut func(*secretsInput)) *kernelSecrets {
	t.Helper()
	in := secretsInput{DB: db, StateDir: stateDir, AppID: "gotham"}
	if mut != nil {
		mut(&in)
	}
	s, err := openSecrets(context.Background(), in)
	if err != nil {
		t.Fatalf("openSecrets: %v", err)
	}
	t.Cleanup(func() { _ = s.close() })
	return s
}

// TestSecretsOwnerSpec: c/secrets/ is an enabled keyspace, Serialized as a
// whole, with no claims or migrations, and no boot lists it yet (F3-07).
func TestSecretsOwnerSpec(t *testing.T) {
	spec := secretsOwnerSpec()
	if spec.Owner != "core/secrets" || !spec.Spec.Keyspace || !spec.Enabled || !slices.Equal(spec.Spec.Serialized, []string{""}) {
		t.Fatalf("secretsOwnerSpec = %+v", spec)
	}
	if len(spec.Spec.Claims)+len(spec.Spec.Migrations) != 0 {
		t.Fatalf("secretsOwnerSpec carries claims or migrations: %+v", spec.Spec)
	}
	h := newSwapHost(t)
	k := h.mustBoot(h.config(nil))
	for _, o := range k.storageOwners() {
		if o.Owner == secretsOwnerID {
			t.Fatal("storageOwners lists core/secrets before F3-07")
		}
	}
	if _, ok := k.store.db.Keyspace(secretsOwnerID); ok {
		t.Fatal("a boot opened the c/secrets/ keyspace before F3-07")
	}
	if _, err := os.Stat(filepath.Join(h.state, "secrets.keyring")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("a boot created the keyring before F3-07: %v", err)
	}
}

// TestOpenSecrets_ScratchStateDir (the kernel helper with a scratch state
// directory, 05-config §2.4 step 1): openSecrets creates the keyring with
// the store's install marker in its header and writes only c/secrets/meta;
// a second open writes nothing; a reset boot rewrites the header; a store
// without the keyspace, or no handle, is refused by name.
func TestOpenSecrets_ScratchStateDir(t *testing.T) {
	ctx := context.Background()
	db := bootAuditDB(t, secretsOwnerSpec())
	state := t.TempDir()
	s := openTestSecrets(t, db, state, nil)
	if got := keysOf(t, db, secretsOwnerID); len(got) != 1 {
		t.Fatalf("openSecrets wrote %d c/secrets/ rows, want only meta", len(got))
	}
	if _, err := s.store.Put(ctx, "core.http.basic_gate.password", secrets.NewValue(secretsFixture), "user:alfred"); err != nil {
		t.Fatal(err)
	}
	if err := s.close(); err != nil {
		t.Fatal(err)
	}
	kr, err := os.ReadFile(filepath.Join(state, "secrets.keyring"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(kr, []byte(`"install_id": "`+db.Admin().InstallID()+`"`)) {
		t.Fatal("the keyring header does not carry the store's install marker")
	}
	again := openTestSecrets(t, db, state, nil)
	if v, _, err := again.store.Get(ctx, "core.http.basic_gate.password"); err != nil || v.Reveal() != secretsFixture {
		t.Fatalf("the reopened store does not read the value back: %v", err)
	}
	if err := again.close(); err != nil {
		t.Fatal(err)
	}
	if after, _ := os.ReadFile(filepath.Join(state, "secrets.keyring")); !bytes.Equal(after, kr) {
		t.Fatal("a second open rewrote the keyring")
	}

	// A reset boot: a fresh store's marker replaces the header's.
	fresh := bootAuditDB(t, secretsOwnerSpec())
	openTestSecrets(t, fresh, state, func(in *secretsInput) { in.ResetBoot = true }).close()
	kr2, err := os.ReadFile(filepath.Join(state, "secrets.keyring"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(kr2, []byte(`"install_id": "`+fresh.Admin().InstallID()+`"`)) {
		t.Fatal("a reset boot did not rewrite the header's install_id")
	}

	if _, err := openSecrets(ctx, secretsInput{DB: bootAuditDB(t, auditOwnerSpec()), StateDir: t.TempDir(), AppID: "gotham"}); err == nil || !strings.Contains(err.Error(), "secretsOwnerSpec") {
		t.Fatalf("openSecrets without the keyspace = %v, want a refusal naming secretsOwnerSpec", err)
	}
	if _, err := openSecrets(ctx, secretsInput{StateDir: t.TempDir(), AppID: "gotham"}); err == nil {
		t.Fatal("openSecrets without a store handle succeeded")
	}
	if _, err := openSecrets(ctx, secretsInput{DB: db, AppID: "gotham"}); err == nil {
		t.Fatal("openSecrets without a state directory succeeded")
	}
	if _, err := openSecrets(ctx, secretsInput{DB: db, StateDir: t.TempDir(), AppID: "arkham"}); !errors.Is(err, secrets.ErrAppIDMismatch) {
		t.Fatalf("openSecrets as another app = %v, want ErrAppIDMismatch", err)
	}
}

// TestExportKeyring_ClearsHealthWarning (05-config §2.9): a fresh keyring
// raises secrets_keyring_not_exported naming its keys; an export covering
// every key clears it with the store still open; after rotate-derive the next
// boot raises it again naming only the new derive key, and a second export
// clears it.
func TestExportKeyring_ClearsHealthWarning(t *testing.T) {
	ctx := context.Background()
	db := bootAuditDB(t, secretsOwnerSpec())
	state := t.TempDir()
	s := openTestSecrets(t, db, state, nil)
	export := func() {
		t.Helper()
		if _, err := secrets.ExportKeyring(s.root, "", filepath.Join(t.TempDir(), "keyring.export"), time.Now()); err != nil {
			t.Fatal(err)
		}
	}

	r := secretsHealth(ctx, s.store)
	if r.Status != health.StatusDegraded || r.Reason != reasonKeyringNotExported || !strings.Contains(r.Detail, "d1") || !strings.Contains(r.Detail, "k1") {
		t.Fatalf("fresh keyring: %+v, want degraded %s naming d1 and k1", r, reasonKeyringNotExported)
	}
	export()
	if r := secretsHealth(ctx, s.store); r.Status != health.StatusUp || r.Reason != "" {
		t.Fatalf("after the export: %+v, want Up", r)
	}

	if err := s.close(); err != nil {
		t.Fatal(err)
	}
	if _, err := secrets.RotateDerive(s.root, "", time.Hour, time.Now()); err == nil {
		t.Fatal("RotateDerive through a closed root succeeded")
	}
	root, err := os.OpenRoot(state)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = root.Close() }()
	if _, err := secrets.RotateDerive(root, "", time.Hour, time.Now()); err != nil {
		t.Fatal(err)
	}
	s = openTestSecrets(t, db, state, nil)
	r = secretsHealth(ctx, s.store)
	if r.Status != health.StatusDegraded || r.Reason != reasonKeyringNotExported || !strings.Contains(r.Detail, "d2") {
		t.Fatalf("after the rotation: %+v, want degraded %s naming d2", r, reasonKeyringNotExported)
	}
	if strings.Contains(r.Detail, "d1") || strings.Contains(r.Detail, "k1") {
		t.Fatalf("after the rotation the warning names an exported key: %q", r.Detail)
	}
	export()
	if r := secretsHealth(ctx, s.store); r.Status != health.StatusUp || r.Reason != "" {
		t.Fatalf("after the second export: %+v, want Up", r)
	}
}

// TestSecretsHealth_Tokens covers the kernel half of
// TestStageUnreadable_ReadsReenter (05-config §2.9) and the precedence of the
// core.secrets tokens: a fresh keyring raises secrets_keyring_not_exported, an
// export covering every key clears it, a failed undecryptable check raises
// secrets_undecryptable, an unreadable marker raises
// secrets_undecryptable (the most urgent) until a Put replaces it, and a
// blocked retirement raises derive_key_retire_blocked. No value reaches a
// Detail.
func TestSecretsHealth_Tokens(t *testing.T) {
	ctx := context.Background()
	db := bootAuditDB(t, secretsOwnerSpec())
	state := t.TempDir()
	s := openTestSecrets(t, db, state, nil)
	rep := &lastReport{}

	reportSecretsHealth(ctx, rep, s.store)
	if rep.r.Status != health.StatusDegraded || rep.r.Reason != reasonKeyringNotExported || !strings.Contains(rep.r.Detail, "d1") {
		t.Fatalf("fresh keyring: %+v, want degraded %s naming d1", rep.r, reasonKeyringNotExported)
	}
	if _, err := secrets.ExportKeyring(s.root, "", filepath.Join(t.TempDir(), "keyring.export"), time.Now()); err != nil {
		t.Fatal(err)
	}
	if r := secretsHealth(ctx, s.store); r.Status != health.StatusUp || r.Reason != "" {
		t.Fatalf("after the export: %+v, want Up", r)
	}
	// The undecryptable check itself fails (a cancelled context stops its
	// scan): reported as secrets_undecryptable, never taken for "all
	// readable".
	cctx, cancel := context.WithCancel(ctx)
	cancel()
	if r := secretsHealth(cctx, s.store); r.Status != health.StatusDegraded || r.Reason != reasonSecretsUndecryptable || !strings.Contains(r.Detail, "could not be checked") {
		t.Fatalf("a failed undecryptable check: %+v, want degraded %s", r, reasonSecretsUndecryptable)
	}

	ks, _ := db.Keyspace(secretsOwnerID)
	b := ks.NewBatch()
	if err := s.store.StageUnreadable(ctx, b, "core.http.basic_gate.password", "key", "cutover:m2"); err != nil {
		t.Fatal(err)
	}
	if err := b.Commit(ctx, storage.Sync); err != nil {
		t.Fatal(err)
	}
	r := secretsHealth(ctx, s.store)
	if r.Status != health.StatusDegraded || r.Reason != reasonSecretsUndecryptable || !strings.Contains(r.Detail, "core.http.basic_gate.password") {
		t.Fatalf("unreadable marker: %+v", r)
	}
	if _, err := s.store.Put(ctx, "core.http.basic_gate.password", secrets.NewValue(secretsFixture), "user:alfred"); err != nil {
		t.Fatal(err)
	}
	r = secretsHealth(ctx, s.store)
	if r.Status != health.StatusUp {
		t.Fatalf("after the Put replaced the marker: %+v, want Up", r)
	}
	if strings.Contains(r.Detail, secretsFixture) {
		t.Fatal("a Detail holds a secret value")
	}

	// Rotation: d2 is not exported; past retire_after with an archive under
	// d1, the retirement is blocked, which outranks the export warning.
	if err := s.close(); err != nil {
		t.Fatal(err)
	}
	root, err := os.OpenRoot(state)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = root.Close() }()
	if _, err := secrets.RotateDerive(root, "", time.Hour, time.Now().Add(-2*time.Hour)); err != nil {
		t.Fatal(err)
	}
	s = openTestSecrets(t, db, state, nil)
	if _, err := s.store.RetireUnused(ctx, func(context.Context, string) (bool, error) { return true, nil }); err != nil {
		t.Fatal(err)
	}
	r = secretsHealth(ctx, s.store)
	if r.Reason != reasonDeriveRetireBlocked || !strings.Contains(r.Detail, "d1") || !strings.Contains(r.Detail, "d2") {
		t.Fatalf("blocked retirement: %+v, want %s naming d1 and the unexported d2", r, reasonDeriveRetireBlocked)
	}
}

// TestSecretsMetrics_Golden: the kernel registers the store's instruments on
// the core secrets scope, whose families are secrets' golden.
func TestSecretsMetrics_Golden(t *testing.T) {
	db := bootAuditDB(t, secretsOwnerSpec())
	s := openTestSecrets(t, db, t.TempDir(), nil)
	p, err := metrics.New(context.Background(), metrics.Options{AppID: "gotham", Prometheus: true})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = p.Shutdown(context.Background()) })
	sc, err := secretsMetrics(p, s.store)
	if err != nil {
		t.Fatal(err)
	}
	metricstest.AssertSeries(t, p, sc, filepath.Join("..", "secrets", "testdata", "series.golden"))
	if _, err := secretsMetrics(p, s.store); err == nil {
		t.Fatal("a second registration of the secrets scope succeeded")
	}
}

// writeFixtureKeyring creates a keyring in state with the header install_id
// of another store, as the reset boot of a factory reset leaves it.
func writeFixtureKeyring(t *testing.T, state string) string {
	t.Helper()
	other := bootAuditDB(t, secretsOwnerSpec())
	openTestSecrets(t, other, state, nil).close()
	return other.Admin().InstallID()
}

// TestRotateDerive_RefusedWhileServed (the lock half of 05-config's
// TestRotateDerive_VerifyThenRetire and TestExportKeyring_OpensNoStore):
// rotate-derive is refused while a boot holds the store and leaves the
// keyring alone; stopped, it adds d2 with retire_after = now + the window
// read from config (168h, the remember-me TTL, by default).
func TestRotateDerive_RefusedWhileServed(t *testing.T) {
	h := newSwapHost(t)
	c := h.config(nil)
	k := h.mustBoot(c)
	writeFixtureKeyring(t, h.state)
	kr := readFile(t, filepath.Join(h.state, "secrets.keyring"))
	now := time.Date(2039, 4, 1, 22, 0, 0, 0, time.UTC)
	if _, err := RotateDeriveOffline(h.ctx, c, now); err == nil {
		t.Fatal("rotate-derive ran while the store was served")
	} else if !strings.Contains(err.Error(), "lock") {
		t.Fatalf("%s while served = %v, want the store's lock refusal", "rotate-derive", err)
	}
	if !bytes.Equal(readFile(t, filepath.Join(h.state, "secrets.keyring")), kr) {
		t.Fatal("a refused rotate-derive changed the keyring")
	}
	h.close(k)
	res, err := RotateDeriveOffline(h.ctx, c, now)
	if err != nil {
		t.Fatalf("rotate-derive with the service stopped: %v", err)
	}
	if res.Old != "d1" || res.New != "d2" || !res.RetireAfter.Equal(now.Add(168*time.Hour)) {
		t.Fatalf("rotate-derive = %+v, want d1 -> d2 retiring after 168h", res)
	}
	if w, err := rotateDeriveWindow(context.Background(), "gotham"); err != nil || w != 168*time.Hour {
		t.Fatalf("rotateDeriveWindow = %v, %v", w, err)
	}
	if _, err := os.Stat(filepath.Join(h.state, "secrets.keyring.exported")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("rotate-derive wrote an export record")
	}
}

// TestSetInstallID_RestoresLineageOffline (05-config §2.9, DATA8-12): after
// a reset and a manual undo (the header names the fresh store, the old store
// is back), set-install-id with the old store's marker rewrites only the
// header (keys and export record unchanged); it refuses while the store is
// served, an ID that differs from the store's marker, and a missing keyring.
func TestSetInstallID_RestoresLineageOffline(t *testing.T) {
	h := newSwapHost(t)
	c := h.config(nil)
	k := h.mustBoot(c)
	marker := k.store.db.Admin().InstallID()
	freshID := writeFixtureKeyring(t, h.state)
	root, err := os.OpenRoot(h.state)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = root.Close() }()
	if _, err := secrets.ExportKeyring(root, "", filepath.Join(t.TempDir(), "keyring.export"), time.Now()); err != nil {
		t.Fatal(err)
	}
	record := readFile(t, filepath.Join(h.state, "secrets.keyring.exported"))
	keysBefore := keyringKeys(t, readFile(t, filepath.Join(h.state, "secrets.keyring")))

	if _, err := SetInstallIDOffline(h.ctx, c, marker); err == nil {
		t.Fatal("set-install-id ran while the store was served")
	} else if !strings.Contains(err.Error(), "lock") {
		t.Fatalf("%s while served = %v, want the store's lock refusal", "set-install-id", err)
	}
	h.close(k)
	if _, err := SetInstallIDOffline(h.ctx, c, "batcave-9999"); !errors.Is(err, errInstallIDNotStore) {
		t.Fatalf("set-install-id with another ID = %v, want errInstallIDNotStore", err)
	}
	old, err := SetInstallIDOffline(h.ctx, c, marker)
	if err != nil {
		t.Fatal(err)
	}
	if old != freshID {
		t.Fatalf("old install ID %q, want the fresh store's %q", old, freshID)
	}
	after := readFile(t, filepath.Join(h.state, "secrets.keyring"))
	if !bytes.Contains(after, []byte(`"install_id": "`+marker+`"`)) {
		t.Fatal("the header does not carry the old store's marker")
	}
	if !slices.Equal(keyringKeys(t, after), keysBefore) {
		t.Fatal("set-install-id changed the keys")
	}
	if !bytes.Equal(readFile(t, filepath.Join(h.state, "secrets.keyring.exported")), record) {
		t.Fatal("set-install-id changed the export record")
	}
	if err := os.Remove(filepath.Join(h.state, "secrets.keyring")); err != nil {
		t.Fatal(err)
	}
	if _, err := SetInstallIDOffline(h.ctx, c, marker); err == nil {
		t.Fatal("set-install-id succeeded with no keyring")
	}
	if _, err := os.Stat(filepath.Join(h.state, "secrets.keyring")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("set-install-id created a keyring")
	}
}

// readFile is path's content, fatal on error.
func readFile(t *testing.T, path string) []byte {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// keyringKeys is the "keys" array of a keyring file, as raw lines, so two
// files compare by their keys alone.
func keyringKeys(t *testing.T, kr []byte) []string {
	t.Helper()
	_, keys, ok := bytes.Cut(kr, []byte(`"keys"`))
	if !ok {
		t.Fatal("a keyring without keys")
	}
	return strings.Split(string(keys), "\n")
}

// TestKernelSecrets_NeverFormatsSecrets (a test name coined by the F3-03
// follow-up): the kernel's kernelSecrets, which holds the *secrets.Store in
// an unexported field, prints no stored value, operator value or key
// material, alone or inside a struct of its own, under any verb or slog's
// text and JSON handlers. kernelSecrets has no printing methods of its own:
// the Store keeps every secret-bearing field behind a pointer, so fmt's walk
// through the unexported field stops at addresses.
func TestKernelSecrets_NeverFormatsSecrets(t *testing.T) {
	const operator = "why-so-serious-hahaha"
	db := bootAuditDB(t, secretsOwnerSpec())
	state := t.TempDir()
	ks := openTestSecrets(t, db, state, nil)
	if _, err := ks.store.Put(context.Background(), "core.http.basic_gate.password", secrets.NewValue(secretsFixture), "user:alfred"); err != nil {
		t.Fatal(err)
	}
	ks.store.Ephemeral("modules.batcomputer.api_key", secrets.NewValue(operator), secrets.SourceEnv)

	var kr struct {
		Keys []struct {
			Material []byte `json:"material"`
		} `json:"keys"`
	}
	if err := json.Unmarshal(readFile(t, filepath.Join(state, "secrets.keyring")), &kr); err != nil || len(kr.Keys) == 0 {
		t.Fatalf("read the keyring's keys: %v (%d keys)", err, len(kr.Keys))
	}
	var leaks []string
	add := func(b []byte) {
		leaks = append(leaks, string(b), hex.EncodeToString(b), strings.Trim(fmt.Sprint(b), "[]"),
			base64.StdEncoding.EncodeToString(b), strings.Trim(fmt.Sprintf("%#v", b), "[]uint8{}")[:20])
	}
	add([]byte(secretsFixture))
	add([]byte(operator))
	for _, k := range kr.Keys {
		add(k.Material[:8])
	}

	type kernelish struct {
		name    string
		secrets *kernelSecrets
	}
	k := kernelish{name: "wayne-manor", secrets: ks}
	for name, v := range map[string]any{
		"*kernelSecrets": ks, "kernelSecrets": *ks, "wrapper": k, "*wrapper": &k, "nested": struct{ k kernelish }{k},
	} {
		outs := map[string]string{}
		for _, verb := range []string{"%v", "%+v", "%#v", "%s", "%q", "%x", "%d"} {
			outs[verb] = fmt.Sprintf(verb, v)
		}
		var text, js bytes.Buffer
		slog.New(slog.NewTextHandler(&text, nil)).Info("wayne manor", "kernel", v)
		slog.New(slog.NewJSONHandler(&js, nil)).Info("wayne manor", "kernel", v)
		outs["slog text"], outs["slog json"] = text.String(), js.String()
		for path, out := range outs {
			for _, l := range leaks {
				if strings.Contains(out, l) {
					t.Errorf("%s formatted with %s leaks a value or key material: %s", name, path, out)
					break
				}
			}
		}
	}
}
