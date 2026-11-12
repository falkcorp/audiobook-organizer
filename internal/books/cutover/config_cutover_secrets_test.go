// file: internal/books/cutover/config_cutover_secrets_test.go
// version: 1.0.2
// guid: 3a8e5c71-2d90-4c6b-b5f4-7e1a9d3c4b08
// last-edited: 2026-11-11

package cutover

import (
	"bytes"
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/falkcorp/mediacore/kernel"
	"github.com/falkcorp/mediacore/secrets"
	"github.com/falkcorp/mediacore/storage"
)

// TestConfigCutover_SecretsReadable: after M2, every credential HEAD held (the
// four encrypted rows and the plaintext ones of the blob) reads from core's
// secrets store, under its canonical name, with the value it had; nothing is
// unreadable, and what is stored sealed carries no plaintext.
func TestConfigCutover_SecretsReadable(t *testing.T) {
	r := newRig(t, "stage1", seedKnown)
	keyBefore, err := os.ReadFile(filepath.Join(r.stateDir, ".encryption_key"))
	if err != nil {
		t.Fatal(err)
	}
	snap, err := r.run(t)
	if err != nil {
		t.Fatalf("M2: %v", err)
	}
	want := knownWant()
	for _, name := range slices.Sorted(maps.Keys(want)) {
		if got, ok := snap.Secrets[name]; !ok {
			t.Errorf("secret %s is not readable after M2", name)
		} else if got != want[name] {
			t.Errorf("secret %s reads back a different value", name)
		}
	}
	for name := range snap.Secrets {
		if _, ok := want[name]; !ok {
			t.Errorf("unexpected secret %s", name)
		}
	}
	if len(snap.Unreadable) != 0 {
		t.Errorf("unreadable secrets after a clean M2: %v", snap.Unreadable)
	}
	if len(snap.Quarantine) != 0 {
		t.Errorf("a clean corpus quarantined %d rows: %+v", len(snap.Quarantine), snap.Quarantine)
	}
	for k, v := range snap.RawCore {
		for _, plain := range allFixtureValues() {
			if bytes.Contains(v, []byte(plain)) {
				t.Errorf("core row %s holds a fixture credential in plaintext", k)
			}
		}
	}
	keyAfter, err := os.ReadFile(filepath.Join(r.stateDir, ".encryption_key"))
	if err != nil || !bytes.Equal(keyBefore, keyAfter) {
		t.Errorf("M2 touched the settings key file (err %v)", err)
	}
}

// legacyUnchanged fails for every key of before that is missing or different in
// after, and every new key, except those allowed(key) names.
func legacyDiff(before, after map[string][]byte) (changed, removed, added []string) {
	for k, v := range before {
		av, ok := after[k]
		switch {
		case !ok:
			removed = append(removed, k)
		case !bytes.Equal(v, av):
			changed = append(changed, k)
		}
	}
	for k := range after {
		if _, ok := before[k]; !ok {
			added = append(added, k)
		}
	}
	slices.Sort(changed)
	slices.Sort(removed)
	slices.Sort(added)
	return changed, removed, added
}

// TestConfigCutover_RowsGone: the four encrypted rows are gone; the blob holds
// only class-i fields, none that moved and no class-i secret; and every key
// outside the listed ones is byte-identical (what changes besides is the legacy
// bookkeeping of the level step, which the runner stamps, not M2).
func TestConfigCutover_RowsGone(t *testing.T) {
	r := newRig(t, "stage1", seedKnown)
	before := r.before(t)
	snap, err := r.run(t)
	if err != nil {
		t.Fatalf("M2: %v", err)
	}
	for _, name := range headSecretRows() {
		if _, ok := snap.Legacy["setting:"+name]; ok {
			t.Errorf("the %s row is still present", name)
		}
		if _, ok := before["setting:"+name]; !ok {
			t.Errorf("the fixture has no %s row to remove", name)
		}
	}
	changed, removed, added := legacyDiff(before, snap.Legacy)
	// What may change: the five keys M2 lists, and the runner's own
	// bookkeeping for books' level step.
	allowed := map[string]bool{
		"counter:preference": true, "preference:db_version": true, "preference:storage_format": true,
		fmt.Sprintf("preference:migration_%d", r.levelVersion()): true,
	}
	for _, k := range configSourceKeys() {
		allowed[string(k)] = true
	}
	for _, k := range slices.Concat(changed, removed, added) {
		if !allowed[k] {
			t.Errorf("key %s changed, but only %v may", k, slices.Sorted(maps.Keys(allowed)))
		}
	}
	if !slices.Contains(changed, "setting:config_blob") {
		t.Error("the blob was not rewritten")
	}
	if len(removed) != 4 {
		t.Errorf("M2 removed %v, want exactly the four rows", removed)
	}

	tree := blobTree(t, snap.Legacy)
	inputs := map[string]bool{} // blob members that hold a class-i field, whole or in part
	for _, row := range Table().Rows {
		if row.Stored == StoredBlob && row.Class == ClassInput {
			inputs[row.BlobField] = true
		}
	}
	for member := range tree {
		if !inputs[member] {
			t.Errorf("the blob still holds %s, which is not a class-i field", member)
		}
	}
	for _, moved := range []string{"acoustid_api_key", "deluge_web_password", "oauth_github_client_secret", "oauth_google_client_secret", "concurrent_scans", "enable_auth", "basic_auth_enabled"} {
		if _, ok := tree[moved]; ok {
			t.Errorf("the blob still holds %s, which moved", moved)
		}
	}
	if raw, _ := tree["plugins"].(map[string]any); raw != nil {
		if _, has := raw["batsignal"]; has {
			t.Error("the blob still holds plugins.batsignal, whose settings moved")
		}
		if wh, _ := raw["webhook"].(map[string]any); wh != nil {
			if st, _ := wh["settings"].(map[string]any); st != nil {
				if _, has := st["secret"]; has {
					t.Error("the blob still holds the webhook secret")
				}
			}
		}
	}
	for _, el := range tree["metadata_sources"].([]any) {
		if m, _ := el.(map[string]any); m != nil {
			if _, has := m["credentials"]; has {
				t.Errorf("the blob still holds metadata source %v's credentials", m["id"])
			}
		}
	}
	dc, _ := tree["download_client"].(map[string]any)
	if dc != nil {
		if torrent, _ := dc["torrent"].(map[string]any); torrent != nil {
			if dl, _ := torrent["deluge"].(map[string]any); dl != nil {
				if _, has := dl["password"]; has {
					t.Error("the blob still holds the Deluge daemon password")
				}
			}
		}
	}
	raw := snap.Legacy["setting:config_blob"]
	for _, plain := range allFixtureValues() {
		if bytes.Contains(raw, []byte(plain)) {
			t.Error("the rewritten blob still holds a fixture credential")
		}
	}
	if snap.State != "done" {
		t.Errorf("M2's state is %q, want done", snap.State)
	}
}

// guardRig is a rig with the known rows sealed and no key file anywhere.
func guardRig(t *testing.T, opts ...func(*rig)) *rig {
	return newRig(t, "stage1", append([]func(*rig){seedKnown}, opts...)...)
}

func withoutKey(r *rig) { r.noKeyDir = true }

// TestConfigCutover_KeyGuard: an encrypted row and no key file refuses the
// upgrade before anything is written, naming the file, the directories
// searched and the override; the override goes on with the rows unreadable
// (ciphertext quarantined sealed, the secrets marked), a key found under the
// root's .appdata is a key, and rows with no encrypted value need none.
func TestConfigCutover_KeyGuard(t *testing.T) {
	t.Run("no key file refuses and writes nothing", func(t *testing.T) {
		r := guardRig(t, withoutKey)
		mem := r.newMem(t)
		before := r.dump(t, mem)
		_, err := r.runOn(t, mem)
		if !errors.Is(err, storage.ErrCutoverBlocked) {
			t.Fatalf("boot error = %v, want storage.ErrCutoverBlocked", err)
		}
		if kernel.ExitCode(err) != kernel.ExitConfig {
			t.Errorf("exit code %d, want %d", kernel.ExitCode(err), kernel.ExitConfig)
		}
		report := err.Error() + r.logs.String()
		for _, want := range []string{".encryption_key", r.stateDir, "ABO_CORE_SECRETS_ALLOW_KEY_REGEN"} {
			if !strings.Contains(report, want) {
				t.Errorf("the refusal does not name %q:\n%s", want, report)
			}
		}
		if _, after := changedOnly(before, r.dump(t, mem)); len(after) != 0 {
			t.Errorf("a refused boot changed the store: %v", after)
		}
	})

	t.Run("a key that is not 32 bytes refuses", func(t *testing.T) {
		r := guardRig(t, func(r *rig) {
			r.afterSeed = func(t testing.TB, dir string) {
				if err := os.WriteFile(filepath.Join(dir, ".encryption_key"), []byte("short"), 0o600); err != nil {
					t.Fatal(err)
				}
			}
		})
		if _, err := r.run(t); !errors.Is(err, storage.ErrCutoverBlocked) {
			t.Fatalf("boot error = %v, want storage.ErrCutoverBlocked", err)
		}
		if !strings.Contains(r.logs.String(), "32-byte key") {
			t.Errorf("the refusal does not say the key is unusable:\n%s", r.logs.String())
		}
	})

	// gateAndFiles are the secrets whose ciphertext cannot be opened and which
	// config.yaml does (the three gap-fill secrets) or does not (the gate
	// password) hold a value for.
	sealedRows := func(t *testing.T, snap *snapshot) {
		t.Helper()
		sealed := 0
		for _, q := range snap.Quarantine {
			if strings.HasPrefix(q.Key, "setting:") && !strings.Contains(q.Key, "#") {
				sealed++
				if !q.Sealed {
					t.Errorf("quarantine of %s is not sealed", q.Key)
				}
			}
		}
		if sealed != 4 {
			t.Errorf("%d rows quarantined, want the four rows' ciphertext", sealed)
		}
	}
	t.Run("the override goes on, config.yaml's values stand in", func(t *testing.T) {
		r := guardRig(t, withoutKey, func(r *rig) { r.allowRegen = true })
		snap, err := r.run(t)
		if err != nil {
			t.Fatalf("M2 with the override: %v", err)
		}
		if err, ok := snap.Unreadable["core.http.basic_gate.password"]; !ok || !errors.Is(err, secrets.ErrKeyUnavailable) {
			t.Errorf("the gate password: unreadable = %v (marked %v), want ErrKeyUnavailable", err, ok)
		}
		rows := knownRows()
		for name, plain := range map[string]string{
			"integrations.ai.openai.api_key":             rows["openai_api_key"],
			"integrations.metadata.google-books.api_key": rows["google_books_api_key"],
			"integrations.metadata.hardcover.api_token":  rows["hardcover_api_token"],
		} {
			got, ok := snap.Secrets[name]
			if !ok || got == "" {
				t.Errorf("secret %s has no value from config.yaml (unreadable: %v)", name, snap.Unreadable[name])
			} else if got == plain {
				t.Errorf("secret %s holds the row's value, which cannot have been decrypted", name)
			}
		}
		sealedRows(t, snap)
		for _, name := range []string{"modules.books.acoustid.api_key", "integrations.download.deluge.password"} {
			if snap.Secrets[name] == "" {
				t.Errorf("plaintext blob secret %s was not imported", name)
			}
		}
	})
	t.Run("the override with no config.yaml leaves all four unreadable", func(t *testing.T) {
		r := guardRig(t, withoutKey, func(r *rig) { r.allowRegen = true; r.confFile = "" })
		snap, err := r.run(t)
		if err != nil {
			t.Fatalf("M2 with the override: %v", err)
		}
		for _, name := range []string{"core.http.basic_gate.password", "integrations.ai.openai.api_key", "integrations.metadata.google-books.api_key", "integrations.metadata.hardcover.api_token"} {
			err, ok := snap.Unreadable[name]
			if !ok {
				t.Errorf("secret %s is not marked unreadable", name)
			} else if !errors.Is(err, secrets.ErrKeyUnavailable) {
				t.Errorf("secret %s unreadable as %v, want ErrKeyUnavailable", name, err)
			}
			if _, readable := snap.Secrets[name]; readable {
				t.Errorf("secret %s is both unreadable and readable", name)
			}
		}
		sealedRows(t, snap)
	})

	t.Run("a key under the root's .appdata is found", func(t *testing.T) {
		r := guardRig(t)
		root := t.TempDir()
		if err := os.MkdirAll(filepath.Join(root, ".appdata"), 0o700); err != nil {
			t.Fatal(err)
		}
		r.afterSeed = func(t testing.TB, dir string) {
			if err := os.WriteFile(filepath.Join(root, ".appdata", ".encryption_key"), r.headKey, 0o600); err != nil {
				t.Fatal(err)
			}
			if err := os.Remove(filepath.Join(dir, ".encryption_key")); err != nil && !errors.Is(err, os.ErrNotExist) {
				t.Fatal(err)
			}
		}
		prev := r.mutate
		r.mutate = func(t testing.TB, lr storage.LegacyRange) {
			prev(t, lr)
			rewriteBlob(t, lr, func(tree map[string]any) { tree["root_dir"] = root })
		}
		snap, err := r.run(t)
		if err != nil {
			t.Fatalf("M2 with the key under .appdata: %v", err)
		}
		if got := snap.Secrets["core.http.basic_gate.password"]; got != fxGate {
			t.Errorf("the gate password does not read back after a key found under .appdata")
		}
	})

	t.Run("rows with no encrypted value need no key", func(t *testing.T) {
		r := newRig(t, "stage1", withoutKey, func(r *rig) {
			r.mutate = func(t testing.TB, lr storage.LegacyRange) {
				for name := range knownRows() {
					if err := lr.Set(t.Context(), settingKey(name), settingRow(name, "", "string", true)); err != nil {
						t.Fatal(err)
					}
				}
			}
		})
		snap, err := r.run(t)
		if err != nil {
			t.Fatalf("M2 with empty rows and no key: %v", err)
		}
		if len(snap.Unreadable) != 0 {
			t.Errorf("unreadable secrets: %v", snap.Unreadable)
		}
	})

	t.Run("a blob that does not decode refuses", func(t *testing.T) {
		r := newRig(t, "stage1", func(r *rig) {
			r.mutate = func(t testing.TB, lr storage.LegacyRange) {
				if err := lr.Set(t.Context(), settingKey(blobRowName), settingRow(blobRowName, `{"concurrent_scans": "many"`, "string", false)); err != nil {
					t.Fatal(err)
				}
			}
		})
		mem := r.newMem(t)
		before := r.dump(t, mem)
		if _, err := r.runOn(t, mem); !errors.Is(err, storage.ErrCutoverBlocked) {
			t.Fatalf("boot error = %v, want storage.ErrCutoverBlocked", err)
		}
		if _, after := changedOnly(before, r.dump(t, mem)); len(after) != 0 {
			t.Errorf("a refused boot changed the store: %v", after)
		}
	})
}

// changedOnly lists the keys that differ between two dumps.
func changedOnly(before, after map[string][]byte) (same bool, diff []string) {
	c, r, a := legacyDiff(before, after)
	diff = slices.Concat(c, r, a)
	return len(diff) == 0, diff
}

// keyOnlyAt moves the rig's key out of the state directory and into dir, once
// the store is seeded.
func keyOnlyAt(dir string, key func(r *rig) []byte) func(*rig) {
	return func(r *rig) {
		prev := r.afterSeed
		r.afterSeed = func(t testing.TB, state string) {
			if prev != nil {
				prev(t, state)
			}
			if err := os.WriteFile(filepath.Join(dir, ".encryption_key"), key(r), 0o600); err != nil {
				t.Fatal(err)
			}
			if err := os.Remove(filepath.Join(state, ".encryption_key")); err != nil && !errors.Is(err, os.ErrNotExist) {
				t.Fatal(err)
			}
		}
	}
}

// TestConfigCutover_KeySearchOrder: HEAD finds its key in the state directory,
// then in the directory of its database path (bootSettings passes both to
// FindEncryptionKey, and guardAgainstKeyRegeneration searches the same two).
// An install from before the state directory keeps the key beside the
// database, and <root_dir>/.appdata/ is an extra place M2 looks that HEAD does
// not. The first directory holding a key file wins, readable or not.
func TestConfigCutover_KeySearchOrder(t *testing.T) {
	headKey := func(r *rig) []byte { return r.headKey }
	gateReads := func(t *testing.T, r *rig) {
		t.Helper()
		snap, err := r.run(t)
		if err != nil {
			t.Fatalf("M2: %v", err)
		}
		if got := snap.Secrets["core.http.basic_gate.password"]; got != fxGate {
			t.Errorf("the gate password does not read back (readable: %v)", got != "")
		}
	}

	t.Run("a key beside the database is found", func(t *testing.T) {
		dbDir := t.TempDir()
		r := guardRig(t, keyOnlyAt(dbDir, headKey), func(r *rig) { r.databasePath = filepath.Join(dbDir, "audiobooks.pebble") })
		gateReads(t, r)
	})

	t.Run("a key in a directory that is not searched refuses", func(t *testing.T) {
		elsewhere := t.TempDir()
		r := guardRig(t, keyOnlyAt(elsewhere, headKey)) // the rig's database directory is another, empty one
		mem := r.newMem(t)
		_, err := r.runOn(t, mem)
		if !errors.Is(err, storage.ErrCutoverBlocked) {
			t.Fatalf("boot error = %v, want storage.ErrCutoverBlocked", err)
		}
	})

	t.Run("a step built without the database path panics", func(t *testing.T) {
		defer func() {
			msg, _ := recover().(string)
			if !strings.Contains(msg, "ConfigStepOptions.DatabasePath") {
				t.Errorf("panic = %q, want it to name ConfigStepOptions.DatabasePath", msg)
			}
		}()
		ConfigStep(nil, nil, ConfigStepOptions{LevelStepVersion: 1})
		t.Error("ConfigStep accepted an empty DatabasePath")
	})

	t.Run("the refusal names the database directory beside the state directory", func(t *testing.T) {
		dbDir := t.TempDir()
		r := guardRig(t, withoutKey, func(r *rig) { r.databasePath = filepath.Join(dbDir, "audiobooks.pebble") })
		_, err := r.run(t)
		if !errors.Is(err, storage.ErrCutoverBlocked) {
			t.Fatalf("boot error = %v, want storage.ErrCutoverBlocked", err)
		}
		report := err.Error() + r.logs.String()
		stateAt, dbAt := strings.Index(report, r.stateDir), strings.Index(report, dbDir)
		if stateAt < 0 || dbAt <= stateAt {
			t.Errorf("the refusal does not list the state directory and then %s:\n%s", dbDir, report)
		}
	})

	t.Run("the state directory wins over the database directory", func(t *testing.T) {
		dbDir := t.TempDir()
		wrong := []byte("0123456789abcdef0123456789abcdef")
		if err := os.WriteFile(filepath.Join(dbDir, ".encryption_key"), wrong, 0o600); err != nil {
			t.Fatal(err)
		}
		r := guardRig(t, func(r *rig) { r.databasePath = filepath.Join(dbDir, "audiobooks.pebble") })
		gateReads(t, r)
	})

	t.Run("the first directory holding a key file wins even when it is unusable", func(t *testing.T) {
		dbDir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dbDir, ".encryption_key"), []byte("short"), 0o600); err != nil {
			t.Fatal(err)
		}
		r := guardRig(t, withoutKey, func(r *rig) { r.databasePath = filepath.Join(dbDir, "audiobooks.pebble") })
		// withoutKey removed the state directory's key, so the short one beside
		// the database is the first found. HEAD would stop there too.
		if _, err := r.run(t); !errors.Is(err, storage.ErrCutoverBlocked) {
			t.Fatalf("boot error = %v, want storage.ErrCutoverBlocked", err)
		}
		if !strings.Contains(r.logs.String(), "32-byte key") {
			t.Errorf("the refusal does not say the key is unusable:\n%s", r.logs.String())
		}
	})

	t.Run("the database directory comes before the root's .appdata", func(t *testing.T) {
		got := keyDirs("/batcave/state", "/batcave/db", "/mnt/gotham")
		want := []string{"/batcave/state", "/batcave/db", "/mnt/gotham/.appdata"}
		if !slices.Equal(got, want) {
			t.Errorf("keyDirs = %v, want %v", got, want)
		}
		if got := keyDirs("/batcave/state", "", ""); !slices.Equal(got, []string{"/batcave/state"}) {
			t.Errorf("keyDirs with nothing else = %v", got)
		}
	})
}
