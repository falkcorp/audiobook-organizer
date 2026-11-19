// file: core/kernel/dryrun_wire_test.go
// version: 1.0.0
// guid: 9e3a6d14-7b25-4c80-a1f3-5d8b2c6e0a41
// last-edited: 2026-11-19

package kernel

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/falkcorp/mediacore/config"
	"github.com/falkcorp/mediacore/internal/configstore"
	"github.com/falkcorp/mediacore/module"
	"github.com/falkcorp/mediacore/secrets"
	"github.com/falkcorp/mediacore/storage"
	"github.com/falkcorp/mediacore/storage/datamigrate"
	"github.com/falkcorp/mediacore/tasks"
)

// WireDryRun (F3-07 e1): what `storage migrate --dry-run` hands kernel.DryRun
// so the manifest's cutover list runs on the scratch copy the way a live
// boot's P4b runs it. The fixtures are the GCPD's: gcpd's cutover step is a
// stand-in for books' M2 (it reads its legacy range, stages a config key,
// imports a secret, reads the key-regen switch and quarantines a sensitive
// row), and the manifest's Rewrite is M8.

const (
	wireProbeID   = "gcpd.case-files"
	wireRewriteID = "gcpd.rewrite"
	wireProbeKey  = "case:file/001"
	wireProbeRow  = "The Long Halloween, part 1"
)

// wireProbeStep is the M2 stand-in over imp. Every importer it reaches is a
// late-bound one, so each of them refuses with ErrImporterNotBound until the
// wiring binds it; the outcomes it counts say which seams it saw.
func wireProbeStep(imp module.Importers) datamigrate.DataMigration {
	return datamigrate.DataMigration{
		ID: wireProbeID, Owner: "gcpd", Description: "the GCPD's case files, as M2 reads its inputs",
		Source:      []storage.Range{{Start: []byte("case:"), End: []byte("case;")}},
		Parallelism: tasks.Fixed(1, "test: one shard"), Mode: datamigrate.BootBlocking,
		Compat: storage.Breaking, Cutover: true, Sensitive: []string{"case:vault/"},
		Step: func(ctx context.Context, kv storage.KV, w *datamigrate.StepWriter) error {
			k := string(kv.Key)
			if strings.HasPrefix(k, "case:vault/") {
				return w.Quarantine(kv.Key, kv.Value, "a credential the importer does not carry")
			}
			if k != wireProbeKey {
				w.Count("seen")
				return nil
			}
			r := imp.Legacy("gcpd")
			if r == nil {
				return errors.New("the legacy range is not bound")
			}
			got, err := r.Get(ctx, []byte(wireProbeKey))
			if err != nil || string(got) != wireProbeRow {
				return fmt.Errorf("the legacy read = %q, %v; want %q", got, err, wireProbeRow)
			}
			w.Count("legacy-read")
			b := w.Batch()
			if err := imp.Config.Stage(b, "core.backup.retention.max_count", []byte("7"), "gcpd.probe"); err != nil {
				return fmt.Errorf("stage a config key: %w", err)
			}
			w.Count("key-staged")
			if err := imp.Secrets.ImportSecret(ctx, b, secrets.Write{
				Name: "core.gcpd.batphone", Value: secrets.NewValue("Gotham Gazette hotline"), By: "gcpd.probe"}); err != nil {
				return fmt.Errorf("import a secret: %w", err)
			}
			w.Count("secret-imported")
			if imp.AllowKeyRegen() {
				w.Count("regen-true")
			} else {
				w.Count("regen-false")
			}
			return nil
		},
	}
}

// wireMod is the cutover build of gcpd: its legacy claim retired, the probe
// step in Manifest.Cutover and M8 in Manifest.Rewrite.
func wireMod() testModule {
	rw := storage.NewRewriteStep("gcpd", wireRewriteID)
	return gcpdMod(storage.ClaimRetired).with(func(m *module.Manifest) {
		m.Cutover = func(imp module.Importers) []datamigrate.DataMigration {
			return []datamigrate.DataMigration{wireProbeStep(imp)}
		}
		m.Rewrite = &rw
	})
}

// wireHost serves a store with the legacy rows under the pre-cutover build,
// gate off, so the state directory holds no keyring, and returns the cutover
// build's Config with the gate on over the operator environment env.
func wireHost(t *testing.T, env ...string) (*swapHost, Config) {
	t.Helper()
	h, _ := dryRunHost(t, map[string]string{
		wireProbeKey:     wireProbeRow,
		"case:file/002":  "The Long Halloween, part 2",
		"case:vault/001": "Wayne Manor safe combination 1",
	})
	if _, err := os.Stat(filepath.Join(h.state, "secrets.keyring")); !os.IsNotExist(err) {
		t.Fatalf("fixture: the pre-cutover serve left a keyring in the state directory: %v", err)
	}
	return h, wireConfig(t, h, env...)
}

// wireConfig is the cutover build's Config over h with the gate on and the
// operator environment env.
func wireConfig(t *testing.T, h *swapHost, env ...string) Config {
	t.Helper()
	c := testConfig(t, wireMod())
	c.StoragePath, c.StateDir = h.db, h.state
	c.CoreConfigBoot = true
	c.ConfigOptions = config.Options{Environ: append([]string{}, env...)}
	c.StateDirDefault = h.state
	return c
}

// hashTree is every regular file under dir with its content's SHA-256: names
// and sizes would miss a rewrite in place.
func hashTree(t *testing.T, dir string) map[string]string {
	t.Helper()
	out := map[string]string{}
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		sum := sha256.Sum256(b)
		rel, _ := filepath.Rel(dir, p)
		out[rel] = hex.EncodeToString(sum[:])
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// sameTree fails the test when after differs from before.
func sameTree(t *testing.T, what string, before, after map[string]string) {
	t.Helper()
	for name, sum := range before {
		if got, ok := after[name]; !ok {
			t.Errorf("%s: %s was removed", what, name)
		} else if got != sum {
			t.Errorf("%s: %s changed", what, name)
		}
	}
	for name := range after {
		if _, ok := before[name]; !ok {
			t.Errorf("%s: %s was created", what, name)
		}
	}
}

// wireRun wires and runs a dry run of c, returning the report and the number
// of times Release ran.
func wireRun(t *testing.T, h *swapHost, c Config, opt DryRunOptions) (*storage.DryRunReport, int, error) {
	t.Helper()
	opt, err := WireDryRun(h.ctx, c, opt)
	if err != nil {
		t.Fatalf("WireDryRun: %v", err)
	}
	var released atomic.Int64
	if inner := opt.Release; inner != nil {
		opt.Release = func() error { released.Add(1); return inner() }
	}
	rep, err := DryRun(h.ctx, c, opt)
	return rep, int(released.Load()), err
}

func stepOf(rep *storage.DryRunReport, id string) *storage.DryRunStep {
	for i := range rep.Steps {
		if rep.Steps[i].ID == id {
			return &rep.Steps[i]
		}
	}
	return nil
}

// TestWireDryRun_PlansM2AndM8AndWritesNothing is F3-07 e1's acceptance: a dry
// run of a fixture data directory, wired from the manifest, plans the cutover
// step and M8 (it reports M2's counts through the bound importers and M8's
// copy), reads M2's legacy row through the late-bound Legacy, and no byte of
// the store directory or of the state directory (the keyring above all)
// changes: the throw-away keyring and everything the step wrote are in the
// scratch directory, deleted at the end.
func TestWireDryRun_PlansM2AndM8AndWritesNothing(t *testing.T) {
	h, c := wireHost(t)
	storeBefore, stateBefore := hashTree(t, h.db), hashTree(t, h.state)

	rep, released, err := wireRun(t, h, c, DryRunOptions{})
	if err != nil {
		t.Fatalf("dry run: %v (report %+v)", err, rep)
	}
	m2 := stepOf(rep, wireProbeID)
	if m2 == nil {
		t.Fatalf("the report plans no %s: %+v", wireProbeID, rep.Steps)
	}
	if rep.Rewrite == nil {
		t.Errorf("the report plans no M8 (%s): %+v", wireRewriteID, rep)
	}
	if rep.Err != "" || len(rep.Blockers) != 0 {
		t.Errorf("report err %q, blockers %v; want a clean dry run", rep.Err, rep.Blockers)
	}
	for outcome, want := range map[string]int64{"legacy-read": 1, "key-staged": 1, "secret-imported": 1, "regen-false": 1, "seen": 1} {
		if got := m2.Items[outcome]; got != want {
			t.Errorf("%s: %s = %d, want %d (items %v)", wireProbeID, outcome, got, want, m2.Items)
		}
	}
	if m2.Items["regen-true"] != 0 {
		t.Errorf("Importers.AllowKeyRegen was true with the default config (items %v)", m2.Items)
	}
	if len(m2.Quarantine) != 1 || !m2.Quarantine[0].Sensitive {
		t.Errorf("quarantine = %+v, want the one sensitive vault row sealed by the scratch keyring", m2.Quarantine)
	}
	if released != 1 {
		t.Errorf("Release ran %d times, want 1", released)
	}

	sameTree(t, "the store directory", storeBefore, hashTree(t, h.db))
	sameTree(t, "the state directory", stateBefore, hashTree(t, h.state))
	if _, err := os.Stat(filepath.Join(h.state, "secrets.keyring")); !os.IsNotExist(err) {
		t.Errorf("the dry run created the state directory's keyring: %v", err)
	}
	var leftovers []string
	_ = filepath.WalkDir(filepath.Dir(h.state), func(p string, d fs.DirEntry, err error) error {
		if err == nil && strings.HasPrefix(d.Name(), "dryrun-") {
			leftovers = append(leftovers, p)
		}
		return nil
	})
	if len(leftovers) > 0 {
		t.Errorf("the dry run's scratch (and its throw-away keyring) survived: %v", leftovers)
	}
}

// Importers.AllowKeyRegen is core.secrets.allow_key_regen as the operator
// layers resolve it, not a constant: true from the environment, false by
// default (the default is the assertion above).
func TestWireDryRun_AllowKeyRegenIsTheConfiguredValue(t *testing.T) {
	h, c := wireHost(t, "GOTHAM_CORE_SECRETS_ALLOW_KEY_REGEN=true")
	rep, _, err := wireRun(t, h, c, DryRunOptions{})
	if err != nil {
		t.Fatalf("dry run: %v (report %+v)", err, rep)
	}
	m2 := stepOf(rep, wireProbeID)
	if m2 == nil || m2.Items["regen-true"] != 1 || m2.Items["regen-false"] != 0 {
		t.Fatalf("with allow_key_regen=true the step saw %+v, want regen-true", m2)
	}
}

// --id names M2 alone (and what it runs after): a build whose manifest
// declares M2 and M8 accepts both IDs, where an unwired dry run refused every
// one of them as "no data migration of this build".
func TestWireDryRun_IDSelectsTheManifestsSteps(t *testing.T) {
	h, c := wireHost(t)
	rep, _, err := wireRun(t, h, c, DryRunOptions{ID: wireProbeID})
	if err != nil || stepOf(rep, wireProbeID) == nil || rep.Rewrite != nil {
		t.Fatalf("--id %s = %+v, %v; want M2 only", wireProbeID, rep, err)
	}
	rep, _, err = wireRun(t, h, c, DryRunOptions{ID: wireRewriteID})
	if err != nil || rep.Rewrite == nil || len(rep.Steps) != 0 {
		t.Fatalf("--id %s = %+v, %v; want M8 only", wireRewriteID, rep, err)
	}
}

// --estimate gets the manifest's list too: the planned steps appear, the live
// store is read-only and is not changed. (M2's own estimate needs a batch for
// its importers and a legacy handle the estimate does not bind, so the run
// does not assert M2's counts: see the F3-07 e1 notes.)
func TestWireDryRun_EstimateListsTheStepsAndWritesNothing(t *testing.T) {
	h, c := wireHost(t)
	storeBefore := hashTree(t, h.db)
	rep, _, err := wireRun(t, h, c, DryRunOptions{Estimate: true})
	if err != nil {
		t.Fatalf("estimate: %v (report %+v)", err, rep)
	}
	if stepOf(rep, wireProbeID) == nil {
		t.Errorf("the estimate plans no %s: %+v", wireProbeID, rep.Steps)
	}
	sameTree(t, "the store directory", storeBefore, hashTree(t, h.db))
}

// A Bind that cannot open its stores fails the dry run with the cause, after
// the scratch is deleted, and still calls Release once: here an operator
// value that does not decode refuses the scratch config store.
func TestWireDryRun_BindFailureReleasesAndLeavesNothing(t *testing.T) {
	h, c := wireHost(t, "GOTHAM_CORE_BACKUP_RETENTION_MAX_COUNT=Penguin")
	storeBefore, stateBefore := hashTree(t, h.db), hashTree(t, h.state)
	rep, released, err := wireRun(t, h, c, DryRunOptions{})
	if err == nil || !strings.Contains(err.Error(), "bind the scratch store") {
		t.Fatalf("dry run = %v, want a refusal naming the bind", err)
	}
	if rep == nil || rep.Err == "" {
		t.Errorf("the report carries no error: %+v", rep)
	}
	if released != 1 {
		t.Errorf("Release ran %d times, want 1", released)
	}
	sameTree(t, "the store directory", storeBefore, hashTree(t, h.db))
	sameTree(t, "the state directory", stateBefore, hashTree(t, h.state))
}

// With the gate off the live boot has no config or secret store: only the
// legacy ranges bind (migrateStorage), so the dry run mirrors it. A step that
// reads its legacy range runs; one that reaches the config importer fails
// with ErrImporterNotBound; no keyring is created anywhere.
func TestWireDryRun_GateOffBindsOnlyTheLegacyRange(t *testing.T) {
	h, _ := dryRunHost(t, map[string]string{wireProbeKey: wireProbeRow})
	var sawLegacy atomic.Bool
	var stageErr atomic.Value
	m := gcpdMod(storage.ClaimRetired).with(func(m *module.Manifest) {
		m.Cutover = func(imp module.Importers) []datamigrate.DataMigration {
			step := wireProbeStep(imp)
			inner := step.Step
			step.Step = func(ctx context.Context, kv storage.KV, w *datamigrate.StepWriter) error {
				if string(kv.Key) == wireProbeKey {
					if r := imp.Legacy("gcpd"); r != nil {
						sawLegacy.Store(true)
					}
					_, err := imp.Config.Default("core.backup.retention.max_count")
					stageErr.Store(fmt.Sprint(errors.Is(err, module.ErrImporterNotBound)))
					return nil
				}
				return inner(ctx, kv, w)
			}
			return []datamigrate.DataMigration{step}
		}
	})
	c := testConfig(t, m)
	c.StoragePath, c.StateDir = h.db, h.state
	rep, _, err := wireRun(t, h, c, DryRunOptions{})
	if err != nil {
		t.Fatalf("gate-off dry run: %v (report %+v)", err, rep)
	}
	if !sawLegacy.Load() {
		t.Error("the legacy range was not bound with the gate off")
	}
	if got, _ := stageErr.Load().(string); got != "true" {
		t.Errorf("the config importer with the gate off: ErrImporterNotBound = %q, want true", got)
	}
	if _, err := os.Stat(filepath.Join(h.state, "secrets.keyring")); !os.IsNotExist(err) {
		t.Errorf("a gate-off dry run created a keyring: %v", err)
	}
}

// A copy that already holds sealed secrets (a store served with the gate on,
// past the point where the keyring was created) does not refuse the dry run:
// the throw-away keyring is new and the copy's sealed records are not its
// concern, so secrets.Open must not answer ErrKeyMissing for want of the live
// keyring, which the dry run neither reads nor needs. The live keyring and the
// rest of the state directory are byte-identical afterwards.
func TestWireDryRun_ASealedCopyNeedsNoKeyring(t *testing.T) {
	h := newSwapHost(t)
	seed := testConfig(t, gcpdMod(storage.ClaimReadWrite))
	seed.StoragePath, seed.StateDir = h.db, h.state
	seed.CoreConfigBoot = true
	seed.ConfigOptions = config.Options{Environ: []string{}}
	seed.StateDirDefault = h.state
	k := h.mustBoot(seed)
	lr, _ := k.store.db.Legacy("gcpd")
	b := lr.NewBatch()
	if err := b.Set([]byte(wireProbeKey), []byte(wireProbeRow)); err != nil {
		t.Fatal(err)
	}
	if err := b.Commit(h.ctx, storage.Sync); err != nil {
		t.Fatal(err)
	}
	if _, err := k.sec.store.Put(h.ctx, "core.gcpd.hotline", secrets.NewValue("Gotham Gazette hotline"), "test"); err != nil {
		t.Fatal(err)
	}
	h.close(k)
	if _, err := os.Stat(filepath.Join(h.state, "secrets.keyring")); err != nil {
		t.Fatalf("fixture: the gate-on serve left no keyring: %v", err)
	}
	storeBefore, stateBefore := hashTree(t, h.db), hashTree(t, h.state)

	rep, _, err := wireRun(t, h, wireConfig(t, h), DryRunOptions{})
	if err != nil {
		t.Fatalf("dry run over a store holding a sealed secret: %v (report %+v)", err, rep)
	}
	if m2 := stepOf(rep, wireProbeID); m2 == nil || m2.Items["secret-imported"] != 1 {
		t.Errorf("the step did not run against the sealed copy: %+v", m2)
	}
	sameTree(t, "the store directory", storeBefore, hashTree(t, h.db))
	sameTree(t, "the state directory", stateBefore, hashTree(t, h.state))
}

// M2's config importer reaches the throw-away secret store: a step that
// replaces a stored provider list, dropping an entry whose secret the secret
// store holds, stages that secret's delete in its own batch (Store.
// SetImportSecrets, as P4b does). Without the stager config refuses the Stage,
// the step fails and the report says so.
func TestWireDryRun_ImporterDeletesEntrySecretsThroughTheStager(t *testing.T) {
	const list = "core.auth.oidc.providers"
	provider := func(id string) string {
		return `{"id":"` + id + `","kind":"oidc","issuer":"https://id.example.com","client_id":"asylum","scopes":[],"allowed_emails":[],"allowed_domains":[],"default_role":"viewer","link_existing_by_email":false}`
	}
	h := newSwapHost(t)
	seed := testConfig(t, gcpdMod(storage.ClaimReadWrite))
	seed.StoragePath, seed.StateDir = h.db, h.state
	seed.CoreConfigBoot = true
	seed.ConfigOptions = config.Options{Environ: []string{}}
	seed.StateDirDefault = h.state
	k := h.mustBoot(seed)
	lr, _ := k.store.db.Legacy("gcpd")
	cks, ok := k.store.db.Keyspace(configstore.Owner)
	if !ok {
		t.Fatal("no config keyspace")
	}
	b := lr.NewBatch()
	if err := b.Set([]byte(wireProbeKey), []byte(wireProbeRow)); err != nil {
		t.Fatal(err)
	}
	if err := k.cfgBoot.native.Stage(h.ctx, b.Bind(cks), []config.Override{{Path: list, By: "alfred", At: time.Now(),
		Value: json.RawMessage("[" + provider("arkham") + "]")}}, nil); err != nil {
		t.Fatal(err)
	}
	if err := b.Commit(h.ctx, storage.Sync); err != nil {
		t.Fatal(err)
	}
	h.close(k)

	c := testConfig(t, gcpdMod(storage.ClaimRetired).with(func(m *module.Manifest) {
		m.Cutover = func(imp module.Importers) []datamigrate.DataMigration {
			return []datamigrate.DataMigration{{
				ID: "gcpd.providers", Owner: "gcpd", Description: "replace the stored provider list",
				Source:      []storage.Range{{Start: []byte("case:"), End: []byte("case;")}},
				Parallelism: tasks.Fixed(1, "test: one shard"), Mode: datamigrate.BootBlocking,
				Compat: storage.Breaking, Cutover: true,
				Step: func(_ context.Context, kv storage.KV, w *datamigrate.StepWriter) error {
					if err := imp.Config.Stage(w.Batch(), list, json.RawMessage("["+provider("batcave")+"]"), "cutover:m2"); err != nil {
						return err
					}
					w.Count("replaced")
					return nil
				},
			}}
		}
	}))
	c.StoragePath, c.StateDir = h.db, h.state
	c.CoreConfigBoot = true
	c.ConfigOptions = config.Options{Environ: []string{}}
	c.StateDirDefault = h.state
	rep, _, err := wireRun(t, h, c, DryRunOptions{})
	if err != nil {
		t.Fatalf("dry run of a step that drops a stored entry: %v (report %+v)", err, rep)
	}
	if st := stepOf(rep, "gcpd.providers"); st == nil || st.Items["replaced"] != 1 {
		t.Errorf("the step did not run to the end: %+v", st)
	}
}
